package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ---- GitHub webhooks (internal/ci, hooks.go) ----
//
// One row per repository whose webhook the hub registered (or tried to),
// in the database only: the hook manager and the webhook endpoint read
// them. The secret is kept here and nowhere else.

const githubHookCols = `repo, hook_id, secret, url, state, reason, registered, last_delivery, last_ping, checked`

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func scanGitHubHook(row scanner) (model.GitHubHook, error) {
	var h model.GitHubHook
	var reg, del, ping, checked int64
	if err := row.Scan(&h.Repo, &h.HookID, &h.Secret, &h.URL, &h.State, &h.Reason, &reg, &del, &ping, &checked); err != nil {
		return h, err
	}
	h.Registered, h.LastDelivery, h.LastPing, h.Checked = fromMillis(reg), fromMillis(del), fromMillis(ping), fromMillis(checked)
	return h, nil
}

// GitHubHook is the webhook saved for repo (OWNER/REPO, any case), or
// ErrNotFound.
func (s *Store) GitHubHook(repo string) (model.GitHubHook, error) {
	h, err := scanGitHubHook(s.db.QueryRow("SELECT "+githubHookCols+" FROM github_hooks WHERE repo = ?", strings.ToLower(repo)))
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrNotFound
	}
	return h, err
}

// GitHubHooks are all the saved webhooks.
func (s *Store) GitHubHooks() ([]model.GitHubHook, error) {
	rows, err := s.db.Query("SELECT " + githubHookCols + " FROM github_hooks ORDER BY repo")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.GitHubHook
	for rows.Next() {
		h, err := scanGitHubHook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// PutGitHubHook saves h, replacing what was saved for its repository.
func (s *Store) PutGitHubHook(h model.GitHubHook) error {
	if h.Repo == "" {
		return errors.New("a GitHub hook needs a repository")
	}
	if len(h.Reason) > 500 {
		h.Reason = h.Reason[:500]
	}
	_, err := s.db.Exec(`INSERT INTO github_hooks (`+githubHookCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (repo) DO UPDATE SET hook_id = excluded.hook_id, secret = excluded.secret, url = excluded.url,
		state = excluded.state, reason = excluded.reason, registered = excluded.registered,
		last_delivery = excluded.last_delivery, last_ping = excluded.last_ping, checked = excluded.checked`,
		strings.ToLower(h.Repo), h.HookID, h.Secret, h.URL, h.State, h.Reason,
		millis(h.Registered), millis(h.LastDelivery), millis(h.LastPing), millis(h.Checked))
	return err
}

// GitHubHookDelivered notes a signed delivery for repo's webhook at at.
func (s *Store) GitHubHookDelivered(repo string, at time.Time) error {
	_, err := s.db.Exec("UPDATE github_hooks SET last_delivery = ? WHERE repo = ?", millis(at), strings.ToLower(repo))
	return err
}

// DeleteGitHubHook forgets repo's webhook.
func (s *Store) DeleteGitHubHook(repo string) error {
	_, err := s.db.Exec("DELETE FROM github_hooks WHERE repo = ?", strings.ToLower(repo))
	return err
}
