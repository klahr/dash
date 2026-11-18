package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ---- Planning ----
//
// The org's plan: cards in fixed columns, their comments, and their links
// to the tasks and goals that carry them out (docs/planning.md). They live
// in the database only, like plans; reads go to it directly. Writes hold
// the store's lock, so a move's renumbering doesn't interleave with
// another. Every change is announced to the org (model.EvPlanningCard).
//
// The links are indexed in memory too (planLinks), so that a change to a
// linked task or goal can be announced to Planning without a query while
// the store's lock is held (emitLinked).

// planRef is a card that links to a task or goal, and the card's org.
type planRef struct {
	card string
	org  int64
}

func linkKey(kind, id string) string { return kind + ":" + id }

// loadPlanLinks builds the link index from the database.
func (s *Store) loadPlanLinks() error {
	rows, err := s.db.Query("SELECT l.kind, l.work_id, l.card_id, c.org_id FROM planning_links l JOIN planning_cards c ON c.id = l.card_id")
	if err != nil {
		return err
	}
	defer rows.Close()
	idx := map[string][]planRef{}
	for rows.Next() {
		var kind, work string
		var ref planRef
		if err := rows.Scan(&kind, &work, &ref.card, &ref.org); err != nil {
			return err
		}
		idx[linkKey(kind, work)] = append(idx[linkKey(kind, work)], ref)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.planMu.Lock()
	s.planLinks = idx
	s.planMu.Unlock()
	return nil
}

func (s *Store) indexLink(kind, work, card string, org int64, add bool) {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	if s.planLinks == nil {
		s.planLinks = map[string][]planRef{}
	}
	k := linkKey(kind, work)
	refs := slices.DeleteFunc(s.planLinks[k], func(r planRef) bool { return r.card == card })
	if add {
		refs = append(refs, planRef{card, org})
	}
	if len(refs) == 0 {
		delete(s.planLinks, k)
		return
	}
	s.planLinks[k] = refs
}

// unindexCard drops a deleted card's links from the index.
func (s *Store) unindexCard(card string) {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	for k, refs := range s.planLinks {
		refs = slices.DeleteFunc(refs, func(r planRef) bool { return r.card == card })
		if len(refs) == 0 {
			delete(s.planLinks, k)
		} else {
			s.planLinks[k] = refs
		}
	}
}

// emitLinked announces a change to a task or goal that planning cards link
// to, as model.EvPlanningWork to each card's org, so Planning (whose
// planners see no task events) shows the cards' new status.
func (s *Store) emitLinked(t model.EventType, todoID string, data any) {
	var key string
	switch t {
	case model.EvTodoUpdated, model.EvTodoDeleted:
		key = linkKey(model.LinkTask, todoID)
	case model.EvGoalUpdated:
		g, ok := data.(model.Goal)
		if !ok {
			return
		}
		key = linkKey(model.LinkGoal, g.ID)
	default:
		return
	}
	s.planMu.Lock()
	refs := slices.Clone(s.planLinks[key])
	s.planMu.Unlock()
	byOrg := map[int64][]string{}
	for _, r := range refs {
		byOrg[r.org] = append(byOrg[r.org], r.card)
	}
	for org, cards := range byOrg {
		s.pub.Publish(model.Event{Type: model.EvPlanningWork, Data: map[string]any{"cards": cards}, Time: time.Now(), OrgID: org})
	}
}

// emitCard announces a change to one of the org's cards.
func (o *OrgStore) emitCard(id string, deleted bool) {
	if o.s.pub != nil {
		o.s.pub.Publish(model.Event{Type: model.EvPlanningCard, Data: map[string]any{"card": id, "deleted": deleted}, Time: time.Now(), OrgID: o.org})
	}
}

const cardColumns = "c.id, c.org_id, c.project_id, c.title, c.description, c.col, c.position, c.priority, c.target, COALESCE(c.created_by, 0), c.created_at, c.updated_at"

func scanCard(sc interface{ Scan(...any) error }) (model.Card, error) {
	c := model.Card{Members: []int64{}, Files: []model.CardFile{}}
	var created, updated int64
	err := sc.Scan(&c.ID, &c.OrgID, &c.ProjectID, &c.Title, &c.Description, &c.Column, &c.Position, &c.Priority, &c.Target, &c.CreatedBy, &created, &updated)
	c.CreatedAt, c.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return c, err
}

type rowsQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
}

// cardMembers reads the members of the cards the query (with args) picks,
// by card, in the order they were added.
func cardMembers(q rowsQuerier, where string, args ...any) (map[string][]int64, error) {
	rows, err := q.Query("SELECT m.card_id, m.user_id FROM planning_card_members m JOIN planning_cards c ON c.id = m.card_id WHERE "+where+
		" ORDER BY m.added_at, m.rowid", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]int64{}
	for rows.Next() {
		var id string
		var uid int64
		if err := rows.Scan(&id, &uid); err != nil {
			return nil, err
		}
		out[id] = append(out[id], uid)
	}
	return out, rows.Err()
}

// cardFiles reads the files of the cards the query (with args) picks, by
// card, in the order they were added.
func cardFiles(q rowsQuerier, where string, args ...any) (map[string][]model.CardFile, error) {
	rows, err := q.Query("SELECT f.card_id, f.upload_id, f.name, f.type, f.size, COALESCE(f.added_by, 0), f.added_at FROM planning_card_files f JOIN planning_cards c ON c.id = f.card_id WHERE "+where+
		" ORDER BY f.added_at, f.rowid", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]model.CardFile{}
	for rows.Next() {
		var id string
		var f model.CardFile
		var at int64
		if err := rows.Scan(&id, &f.ID, &f.Name, &f.Type, &f.Size, &f.AddedBy, &at); err != nil {
			return nil, err
		}
		f.AddedAt = time.Unix(at, 0).UTC()
		out[id] = append(out[id], f)
	}
	return out, rows.Err()
}

// errNoOrg: planning is an org's, and this view has none.
var errNoOrg = errors.New("planning belongs to an organization")

// ListCards returns the org's cards, of one project (projectID) or of all
// ("" ), by column and position.
func (o *OrgStore) ListCards(projectID string) ([]model.Card, error) {
	if o.org == 0 {
		return nil, errNoOrg
	}
	q := "SELECT " + cardColumns + " FROM planning_cards c WHERE c.org_id = ?"
	args := []any{o.org}
	if projectID != "" {
		q += " AND c.project_id = ?"
		args = append(args, projectID)
	}
	rows, err := o.s.db.Query(q+" ORDER BY c.col, c.position, c.created_at, c.id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Card{}
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	members, err := cardMembers(o.s.db, "c.org_id = ?", o.org)
	if err != nil {
		return nil, err
	}
	files, err := cardFiles(o.s.db, "c.org_id = ?", o.org)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if m := members[out[i].ID]; m != nil {
			out[i].Members = m
		}
		if f := files[out[i].ID]; f != nil {
			out[i].Files = f
		}
	}
	return out, nil
}

// GetCard returns one of the org's cards.
func (o *OrgStore) GetCard(id string) (model.Card, error) {
	return getCard(o.s.db, o.org, id)
}

func getCard(q interface {
	rowsQuerier
	QueryRow(string, ...any) *sql.Row
}, org int64, id string) (model.Card, error) {
	c, err := scanCard(q.QueryRow("SELECT "+cardColumns+" FROM planning_cards c WHERE c.id = ? AND c.org_id = ?", id, org))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Card{}, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	members, err := cardMembers(q, "c.id = ?", id)
	if err != nil {
		return c, err
	}
	if m := members[id]; m != nil {
		c.Members = m
	}
	files, err := cardFiles(q, "c.id = ?", id)
	if f := files[id]; f != nil {
		c.Files = f
	}
	return c, err
}

// cardProject checks that a card's project, if it names one, is the org's.
func (o *OrgStore) cardProject(projectID string) error {
	if projectID != "" && !o.ownsProject(projectID) {
		return ErrNotFound
	}
	return nil
}

// ErrBadMember: a card's members must be members of its org.
var ErrBadMember = errors.New("a card's members must be in this organization")

// checkMembers checks that each of uids is a member of the org.
func (o *OrgStore) checkMembers(q querier, uids []int64) error {
	for _, uid := range uids {
		var one int
		err := q.QueryRow("SELECT 1 FROM org_memberships WHERE user_id = ? AND org_id = ?", uid, o.org).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrBadMember
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// setMembers makes the card's members uids: those no longer in it go,
// new ones are added after the others (the ones staying keep their place).
func setMembers(tx *sql.Tx, cardID string, uids []int64, now time.Time) error {
	keep := map[int64]bool{}
	for _, uid := range uids {
		keep[uid] = true
	}
	had, err := cardMembers(tx, "c.id = ?", cardID)
	if err != nil {
		return err
	}
	for _, uid := range had[cardID] {
		if !keep[uid] {
			if _, err := tx.Exec("DELETE FROM planning_card_members WHERE card_id = ? AND user_id = ?", cardID, uid); err != nil {
				return err
			}
		}
	}
	for _, uid := range added(had[cardID], uids) {
		if _, err := tx.Exec("INSERT INTO planning_card_members (card_id, user_id, added_at) VALUES (?, ?, ?)", cardID, uid, now.Unix()); err != nil {
			return err
		}
	}
	return nil
}

// added is who of now isn't in was.
func added(was, now []int64) []int64 {
	in := map[int64]bool{}
	for _, id := range was {
		in[id] = true
	}
	var out []int64
	for _, id := range now {
		if !in[id] {
			out = append(out, id)
		}
	}
	return out
}

// CreateCard adds a card at the end of its column. c must be cleaned
// (model.CleanCard) by the caller; the creator is the view's user.
func (o *OrgStore) CreateCard(c model.Card) (model.Card, error) {
	if o.org == 0 {
		return c, errNoOrg
	}
	if err := o.cardProject(c.ProjectID); err != nil {
		return c, err
	}
	if c.Members == nil {
		c.Members = []int64{}
	}
	if err := o.checkMembers(o.s.db, c.Members); err != nil {
		return c, err
	}
	c.ID, c.OrgID, c.CreatedBy = NewID(), o.org, o.user
	c.CreatedAt = time.Now().UTC().Truncate(time.Second)
	c.UpdatedAt = c.CreatedAt
	o.s.mu.Lock()
	err := inTx(o.s.db, func(tx *sql.Tx) error {
		if err := tx.QueryRow("SELECT COALESCE(MAX(position) + 1, 0) FROM planning_cards WHERE org_id = ? AND col = ?", o.org, c.Column).Scan(&c.Position); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO planning_cards (id, org_id, project_id, title, description, col, position, priority, target, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, 0), ?, ?)`,
			c.ID, c.OrgID, c.ProjectID, c.Title, c.Description, c.Column, c.Position, c.Priority, c.Target, c.CreatedBy, c.CreatedAt.Unix(), c.UpdatedAt.Unix())
		if err != nil {
			return err
		}
		return setMembers(tx, c.ID, c.Members, c.CreatedAt)
	})
	o.s.mu.Unlock()
	if err != nil {
		return c, err
	}
	o.emitCard(c.ID, false)
	return c, nil
}

// UpdateCard applies fn to the card and stores its title, description,
// project, priority, target and members (fn must leave it clean). Its column and
// position change with MoveCard only.
func (o *OrgStore) UpdateCard(id string, fn func(*model.Card) error) (model.Card, error) {
	var c model.Card
	o.s.mu.Lock()
	err := inTx(o.s.db, func(tx *sql.Tx) error {
		var err error
		if c, err = getCard(tx, o.org, id); err != nil {
			return err
		}
		col, pos, members := c.Column, c.Position, c.Members
		if err := fn(&c); err != nil {
			return err
		}
		c.Column, c.Position = col, pos
		if err := o.cardProject(c.ProjectID); err != nil {
			return err
		}
		// Only the members added are checked: one who has left the org since
		// doesn't stop other changes to the card.
		if err := o.checkMembers(tx, added(members, c.Members)); err != nil {
			return err
		}
		c.UpdatedAt = time.Now().UTC().Truncate(time.Second)
		_, err = tx.Exec("UPDATE planning_cards SET project_id = ?, title = ?, description = ?, priority = ?, target = ?, updated_at = ? WHERE id = ?",
			c.ProjectID, c.Title, c.Description, c.Priority, c.Target, c.UpdatedAt.Unix(), id)
		if err != nil {
			return err
		}
		return setMembers(tx, id, c.Members, c.UpdatedAt)
	})
	o.s.mu.Unlock()
	if err != nil {
		return c, err
	}
	o.emitCard(id, false)
	return c, nil
}

// MoveCard puts the card in column col at position (0 is the top; past the
// end is the end), and renumbers the cards of the columns it left and
// joined.
func (o *OrgStore) MoveCard(id string, col model.CardColumn, position int) (model.Card, error) {
	var c model.Card
	if !col.Valid() {
		return c, errors.New("unknown column")
	}
	o.s.mu.Lock()
	err := inTx(o.s.db, func(tx *sql.Tx) error {
		var err error
		if c, err = getCard(tx, o.org, id); err != nil {
			return err
		}
		from := c.Column
		ids := func(col model.CardColumn) ([]string, error) {
			rows, err := tx.Query("SELECT id FROM planning_cards WHERE org_id = ? AND col = ? AND id != ? ORDER BY position, created_at, id", o.org, col, id)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			var out []string
			for rows.Next() {
				var x string
				if err := rows.Scan(&x); err != nil {
					return nil, err
				}
				out = append(out, x)
			}
			return out, rows.Err()
		}
		dest, err := ids(col)
		if err != nil {
			return err
		}
		position = max(0, min(position, len(dest)))
		dest = slices.Insert(dest, position, id)
		now := time.Now().UTC().Truncate(time.Second)
		for i, x := range dest {
			if _, err := tx.Exec("UPDATE planning_cards SET col = ?, position = ? WHERE id = ?", col, i, x); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("UPDATE planning_cards SET updated_at = ? WHERE id = ?", now.Unix(), id); err != nil {
			return err
		}
		if from != col {
			rest, err := ids(from)
			if err != nil {
				return err
			}
			for i, x := range rest {
				if _, err := tx.Exec("UPDATE planning_cards SET position = ? WHERE id = ?", i, x); err != nil {
					return err
				}
			}
		}
		c.Column, c.Position, c.UpdatedAt = col, position, now
		return nil
	})
	o.s.mu.Unlock()
	if err != nil {
		return c, err
	}
	o.emitCard(id, false)
	return c, nil
}

// DeleteCard deletes a card with its comments and links (not the tasks
// and goals it links to).
func (o *OrgStore) DeleteCard(id string) error {
	o.s.mu.Lock()
	err := inTx(o.s.db, func(tx *sql.Tx) error {
		c, err := getCard(tx, o.org, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM planning_cards WHERE id = ?", id); err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE planning_cards SET position = position - 1 WHERE org_id = ? AND col = ? AND position > ?", o.org, c.Column, c.Position)
		return err
	})
	o.s.mu.Unlock()
	if err != nil {
		return err
	}
	o.s.unindexCard(id)
	o.emitCard(id, true)
	return nil
}

// ErrTooManyFiles: a card has at most model.MaxCardFiles files.
var ErrTooManyFiles = fmt.Errorf("a card has at most %d files", model.MaxCardFiles)

// AddCardFile attaches an upload of the org to a card, as the view's user's,
// after its other files. Attaching the same upload again is no change.
func (o *OrgStore) AddCardFile(cardID string, f model.CardFile) (model.CardFile, error) {
	f.AddedBy, f.AddedAt = o.user, time.Now().UTC().Truncate(time.Second)
	o.s.mu.Lock()
	err := inTx(o.s.db, func(tx *sql.Tx) error {
		c, err := getCard(tx, o.org, cardID)
		if err != nil {
			return err
		}
		if len(c.Files) >= model.MaxCardFiles {
			return ErrTooManyFiles
		}
		_, err = tx.Exec(`INSERT INTO planning_card_files (card_id, upload_id, name, type, size, added_by, added_at) VALUES (?, ?, ?, ?, ?, NULLIF(?, 0), ?)
			ON CONFLICT (card_id, upload_id) DO NOTHING`, cardID, f.ID, f.Name, f.Type, f.Size, f.AddedBy, f.AddedAt.Unix())
		return err
	})
	o.s.mu.Unlock()
	if err != nil {
		return f, err
	}
	o.emitCard(cardID, false)
	return f, nil
}

// RemoveCardFile takes a file off a card. The upload stays until cleanup
// finds nothing else uses it.
func (o *OrgStore) RemoveCardFile(cardID, uploadID string) error {
	o.s.mu.Lock()
	err := inTx(o.s.db, func(tx *sql.Tx) error {
		if _, err := getCard(tx, o.org, cardID); err != nil {
			return err
		}
		res, err := tx.Exec("DELETE FROM planning_card_files WHERE card_id = ? AND upload_id = ?", cardID, uploadID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	o.s.mu.Unlock()
	if err != nil {
		return err
	}
	o.emitCard(cardID, false)
	return nil
}

// CardUploads are the ids of the uploads attached to any org's cards, for
// cleanup to keep.
func (s *Store) CardUploads() ([]string, error) {
	rows, err := s.db.Query("SELECT DISTINCT upload_id FROM planning_card_files")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CardComments are a card's comments, oldest first.
func (o *OrgStore) CardComments(cardID string) ([]model.CardComment, error) {
	if _, err := o.GetCard(cardID); err != nil {
		return nil, err
	}
	rows, err := o.s.db.Query(`SELECT m.id, m.card_id, COALESCE(m.author, 0), COALESCE(u.username, ''), m.text, m.created_at
		FROM planning_comments m LEFT JOIN users u ON u.id = m.author WHERE m.card_id = ? ORDER BY m.id`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.CardComment{}
	for rows.Next() {
		var m model.CardComment
		var at int64
		if err := rows.Scan(&m.ID, &m.CardID, &m.Author, &m.AuthorName, &m.Text, &at); err != nil {
			return nil, err
		}
		m.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

// CommentCounts is how many comments each of the org's cards has.
func (o *OrgStore) CommentCounts() (map[string]int, error) {
	rows, err := o.s.db.Query(`SELECT m.card_id, count(*) FROM planning_comments m JOIN planning_cards c ON c.id = m.card_id
		WHERE c.org_id = ? GROUP BY m.card_id`, o.org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// AddCardComment adds the view's user's comment to a card. text must be
// cleaned (model.CleanComment) by the caller.
func (o *OrgStore) AddCardComment(cardID, text string) (model.CardComment, error) {
	m := model.CardComment{CardID: cardID, Author: o.user, Text: text, CreatedAt: time.Now().UTC().Truncate(time.Second)}
	o.s.mu.Lock()
	err := inTx(o.s.db, func(tx *sql.Tx) error {
		if _, err := getCard(tx, o.org, cardID); err != nil {
			return err
		}
		res, err := tx.Exec("INSERT INTO planning_comments (card_id, author, text, created_at) VALUES (?, NULLIF(?, 0), ?, ?)",
			cardID, o.user, text, m.CreatedAt.Unix())
		if err != nil {
			return err
		}
		m.ID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		_ = tx.QueryRow("SELECT username FROM users WHERE id = ?", o.user).Scan(&m.AuthorName)
		return nil
	})
	o.s.mu.Unlock()
	if err != nil {
		return m, err
	}
	o.emitCard(cardID, false)
	return m, nil
}

// DeleteCardComment deletes a comment of a card.
func (o *OrgStore) DeleteCardComment(cardID string, id int64) error {
	o.s.mu.Lock()
	err := inTx(o.s.db, func(tx *sql.Tx) error {
		if _, err := getCard(tx, o.org, cardID); err != nil {
			return err
		}
		res, err := tx.Exec("DELETE FROM planning_comments WHERE id = ? AND card_id = ?", id, cardID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	o.s.mu.Unlock()
	if err != nil {
		return err
	}
	o.emitCard(cardID, false)
	return nil
}

// CardComment is one comment of a card.
func (o *OrgStore) CardComment(cardID string, id int64) (model.CardComment, error) {
	ms, err := o.CardComments(cardID)
	if err != nil {
		return model.CardComment{}, err
	}
	for _, m := range ms {
		if m.ID == id {
			return m, nil
		}
	}
	return model.CardComment{}, ErrNotFound
}

// CardLinks are the links of the org's cards, by card id, oldest first.
// With ids, only those cards'.
func (o *OrgStore) CardLinks(ids ...string) (map[string][]model.CardLink, error) {
	q := `SELECT l.card_id, l.kind, l.work_id, l.created_at FROM planning_links l JOIN planning_cards c ON c.id = l.card_id WHERE c.org_id = ?`
	args := []any{o.org}
	if len(ids) > 0 {
		q += " AND l.card_id IN (?" + strings.Repeat(", ?", len(ids)-1) + ")"
		for _, id := range ids {
			args = append(args, id)
		}
	}
	rows, err := o.s.db.Query(q+" ORDER BY l.created_at, l.rowid", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]model.CardLink{}
	for rows.Next() {
		var l model.CardLink
		var at int64
		if err := rows.Scan(&l.CardID, &l.Kind, &l.WorkID, &at); err != nil {
			return nil, err
		}
		l.CreatedAt = time.Unix(at, 0).UTC()
		out[l.CardID] = append(out[l.CardID], l)
	}
	return out, rows.Err()
}

// ownsWork reports whether the task or goal is the org's.
func (o *OrgStore) ownsWork(kind, id string) bool {
	switch kind {
	case model.LinkTask:
		return o.ownsTodo(id)
	case model.LinkGoal:
		return o.ownsGoal(id)
	}
	return false
}

// LinkCard links a card to one of the org's tasks or goals. Linking again
// is no change.
func (o *OrgStore) LinkCard(cardID, kind, workID string) error {
	if !o.ownsWork(kind, workID) {
		return ErrNotFound
	}
	o.s.mu.Lock()
	err := inTx(o.s.db, func(tx *sql.Tx) error {
		if _, err := getCard(tx, o.org, cardID); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO planning_links (card_id, kind, work_id, created_by, created_at) VALUES (?, ?, ?, NULLIF(?, 0), ?)
			ON CONFLICT (card_id, kind, work_id) DO NOTHING`, cardID, kind, workID, o.user, time.Now().Unix())
		return err
	})
	o.s.mu.Unlock()
	if err != nil {
		return err
	}
	o.s.indexLink(kind, workID, cardID, o.org, true)
	o.emitCard(cardID, false)
	return nil
}

// UnlinkCard removes a card's link to a task or goal.
func (o *OrgStore) UnlinkCard(cardID, kind, workID string) error {
	o.s.mu.Lock()
	err := inTx(o.s.db, func(tx *sql.Tx) error {
		if _, err := getCard(tx, o.org, cardID); err != nil {
			return err
		}
		res, err := tx.Exec("DELETE FROM planning_links WHERE card_id = ? AND kind = ? AND work_id = ?", cardID, kind, workID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	o.s.mu.Unlock()
	if err != nil {
		return err
	}
	o.s.indexLink(kind, workID, cardID, o.org, false)
	o.emitCard(cardID, false)
	return nil
}

// CardsLinkedTo are the org's cards that link to a task or goal.
func (o *OrgStore) CardsLinkedTo(kind, workID string) ([]model.Card, error) {
	rows, err := o.s.db.Query("SELECT "+cardColumns+` FROM planning_cards c JOIN planning_links l ON l.card_id = c.id
		WHERE c.org_id = ? AND l.kind = ? AND l.work_id = ? ORDER BY c.title, c.id`, o.org, kind, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Card{}
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
