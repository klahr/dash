package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/klahr/dash/internal/model"
)

// ---- Past chats ----
//
// New chat puts a user's general chat, or their chat about a project, aside
// (past_chats) and starts an empty one. A past chat can be opened again,
// which puts the current one aside in its place, or deleted, which keeps it
// a day (pastChatUndo) so the delete can be undone.

// pastChatUndo is how long a deleted past chat can still be restored.
const pastChatUndo = 24 * time.Hour

// chatPlace is where a user's own conversation is kept: their general
// chat in org (project "") or their chat about a project.
type chatPlace struct {
	kind, owner, project string
	org, user            int64
	p                    *model.Project // the project, for a project chat
}

// placeLocked is user's general chat in org, or (project set) their chat
// about it. The caller holds s.mu.
func (s *Store) placeLocked(org, user int64, project string) (chatPlace, error) {
	if project == "" {
		return chatPlace{kind: chatMain, owner: mainChatOwner(org, user), org: org, user: user}, nil
	}
	p, ok := s.data.Projects[project]
	if !ok {
		return chatPlace{}, ErrNotFound
	}
	return chatPlace{kind: chatProject, owner: projectChatOwner(project, user), project: project, org: p.OrgID, user: user, p: p}, nil
}

// chatsLocked is the map that keeps pl's current conversation.
func (s *Store) chatsLocked(pl chatPlace) *map[string][]model.ChatMessage {
	if pl.kind == chatMain {
		return &s.data.MainChats
	}
	return &s.data.ProjectChats
}

// setCurrentLocked makes msgs pl's current conversation in memory (the
// database is written by the caller) and tells the user's dashboards of a
// project chat that it changed.
func (s *Store) setCurrentLocked(pl chatPlace, msgs []model.ChatMessage) {
	m := s.chatsLocked(pl)
	if *m == nil {
		*m = map[string][]model.ChatMessage{}
	}
	if len(msgs) == 0 {
		delete(*m, pl.owner)
	} else {
		(*m)[pl.owner] = msgs
	}
	if pl.p != nil {
		s.projectChatChangedLocked(pl.p, pl.user)
	}
}

// pastChatTitle names a conversation by its first question: its first
// line, cut short.
func pastChatTitle(msgs []model.ChatMessage) string {
	pick := ""
	for _, m := range msgs {
		if m.Role == "user" && m.Action == nil && strings.TrimSpace(m.Content) != "" {
			pick = m.Content
			break
		}
	}
	if pick == "" && len(msgs) > 0 {
		pick = msgs[0].Content
	}
	line, _, _ := strings.Cut(strings.TrimSpace(pick), "\n")
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) > 80 {
		r := []rune(line)
		line = strings.TrimSpace(string(r[:79])) + "…"
	}
	if line == "" {
		return "Chat"
	}
	return line
}

// putAside writes msgs, pl's conversation, as a past chat.
func putAside(tx *sql.Tx, pl chatPlace, msgs []model.ChatMessage, at time.Time) error {
	pc := model.PastChat{ID: NewID(), Title: pastChatTitle(msgs), At: at.UTC(), Count: len(msgs), Messages: msgs}
	return put(tx, "INSERT INTO past_chats (id, kind, owner_id, project_id, user_id, org_id, at, data) VALUES (?, ?, ?, ?, ?, COALESCE(?, "+defaultOrg+"), ?, ?)",
		pc, pc.ID, pl.kind, pl.owner, pl.project, pl.user, nullOrg(pl.org), at.UnixMilli())
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// PutChatAside starts a new conversation in user's general chat in org
// (project "") or their chat about project: the current one, if it has
// messages, becomes a past chat.
func (s *Store) PutChatAside(org, user int64, project string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pl, err := s.placeLocked(org, user, project)
	if err != nil {
		return err
	}
	cur := (*s.chatsLocked(pl))[pl.owner]
	if len(cur) == 0 {
		return nil
	}
	if err := s.writeLocked(func(tx *sql.Tx) error {
		if err := putAside(tx, pl, cur, s.now()); err != nil {
			return err
		}
		return setChat(tx, pl.kind, pl.owner, pl.org, nil)
	}); err != nil {
		return err
	}
	s.setCurrentLocked(pl, nil)
	return nil
}

// PastChats are the past chats of user's general chat in org (project "")
// or of their chat about project, newest first, without their messages.
func (s *Store) PastChats(org, user int64, project string) ([]model.PastChat, error) {
	s.mu.RLock()
	pl, err := s.placeLocked(org, user, project)
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	return queryAll(s.db, func(sc scanner) (model.PastChat, error) {
		var pc model.PastChat
		var at int64
		err := sc.Scan(&pc.ID, &at, &pc.Title, &pc.Count)
		pc.At = time.UnixMilli(at).UTC()
		return pc, err
	}, `SELECT id, at, COALESCE(json_extract(data, '$.title'), ''), COALESCE(json_extract(data, '$.count'), 0)
		FROM past_chats WHERE kind = ? AND owner_id = ? AND deleted_at IS NULL ORDER BY at DESC, rowid DESC`, pl.kind, pl.owner)
}

// OpenPastChat makes past chat id the current conversation of user's
// general chat in org (project "") or their chat about project, and
// returns its messages. The current one, if it has messages, becomes a
// past chat in its place.
func (s *Store) OpenPastChat(org, user int64, project, id string) ([]model.ChatMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pl, err := s.placeLocked(org, user, project)
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = s.db.QueryRow("SELECT data FROM past_chats WHERE id = ? AND kind = ? AND owner_id = ? AND deleted_at IS NULL", id, pl.kind, pl.owner).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var pc model.PastChat
	if err := json.Unmarshal(raw, &pc); err != nil {
		return nil, err
	}
	cur := (*s.chatsLocked(pl))[pl.owner]
	if err := s.writeLocked(func(tx *sql.Tx) error {
		if len(cur) > 0 {
			if err := putAside(tx, pl, cur, s.now()); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("DELETE FROM past_chats WHERE id = ?", id); err != nil {
			return err
		}
		return setChat(tx, pl.kind, pl.owner, pl.org, pc.Messages)
	}); err != nil {
		return nil, err
	}
	s.setCurrentLocked(pl, pc.Messages)
	return append([]model.ChatMessage{}, pc.Messages...), nil
}

// DeletePastChat deletes past chat id of user's general chat in org
// (project "") or of their chat about project. RestorePastChat brings it
// back for a day; past chats deleted before that are gone for good.
func (s *Store) DeletePastChat(org, user int64, project, id string) error {
	return s.markPastChat(org, user, project, id, true)
}

// RestorePastChat undoes DeletePastChat.
func (s *Store) RestorePastChat(org, user int64, project, id string) error {
	return s.markPastChat(org, user, project, id, false)
}

func (s *Store) markPastChat(org, user int64, project, id string, del bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pl, err := s.placeLocked(org, user, project)
	if err != nil {
		return err
	}
	now := s.now()
	return inTx(s.db, func(tx *sql.Tx) error {
		q := "UPDATE past_chats SET deleted_at = ? WHERE id = ? AND kind = ? AND owner_id = ? AND deleted_at IS NULL"
		args := []any{now.UnixMilli(), id, pl.kind, pl.owner}
		if !del {
			q = "UPDATE past_chats SET deleted_at = NULL WHERE id = ? AND kind = ? AND owner_id = ? AND deleted_at >= ?"
			args = []any{id, pl.kind, pl.owner, now.Add(-pastChatUndo).UnixMilli()}
		}
		res, err := tx.Exec(q, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec("DELETE FROM past_chats WHERE deleted_at < ?", now.Add(-pastChatUndo).UnixMilli())
		return err
	})
}

// PastChatUploads are the ids of the uploads attached to past chats'
// messages (deleted ones too, while they can be restored), for cleanup.
func (s *Store) PastChatUploads() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT json_extract(a.value, '$.id') FROM past_chats p,
		json_each(p.data, '$.messages') m, json_each(m.value, '$.attachments') a`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id.Valid && id.String != "" {
			out = append(out, id.String)
		}
	}
	return out, rows.Err()
}
