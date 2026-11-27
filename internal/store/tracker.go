package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ---- Board backends (internal/tracker) ----
//
// A project's board backend, its links to external cards and its project
// events live in the database only, like webhooks: the sync engine and the
// settings read them, the queue never does. A project without a row is on
// Dash's own board.

// TrackerSecretPrefix starts every tracker webhook secret.
const TrackerSecretPrefix = "trsec_"

// NewTrackerSecret makes a webhook secret for a project's tracker.
func NewTrackerSecret() string { return TrackerSecretPrefix + rand.Text() }

const trackerCols = `project_id, backend, config, status_map, credentials, webhook_secret, switched_at, cursor, last_sync_at, last_error, updated_at`

// scanTrackerRow reads a row into c, the credentials (as stored) into creds.
func scanTrackerRow(row scanner, c *model.TrackerConfig, creds *string) error {
	var cfg, smap string
	var switched, lastSync sql.NullInt64
	var cursor, updated int64
	if err := row.Scan(&c.ProjectID, &c.Backend, &cfg, &smap, creds, &c.WebhookSecret, &switched, &cursor, &lastSync, &c.LastError, &updated); err != nil {
		return err
	}
	if cfg != "" && cfg != "{}" {
		c.Config = json.RawMessage(cfg)
	}
	_ = json.Unmarshal([]byte(smap), &c.StatusMap)
	if switched.Valid {
		c.SwitchedAt = time.Unix(0, switched.Int64).UTC()
	}
	if cursor != 0 {
		c.Cursor = time.Unix(0, cursor).UTC()
	}
	c.LastSyncAt = nullUnix(lastSync)
	c.UpdatedAt = time.Unix(updated, 0).UTC()
	return nil
}

// scanTracker reads a board backend, its credentials and webhook secret
// opened. If they don't open (the secret key changed) it comes back without
// them, its last error saying why: syncing fails and its webhook is refused
// until they're entered again.
func (s *Store) scanTracker(row scanner) (model.TrackerConfig, error) {
	var c model.TrackerConfig
	var creds string
	if err := scanTrackerRow(row, &c, &creds); err != nil {
		return c, err
	}
	creds, err := s.open(creds, trackerCredsContext(c.ProjectID))
	secret, serr := s.open(c.WebhookSecret, trackerSecretContext(c.ProjectID))
	if err = errors.Join(err, serr); err != nil {
		creds, secret = "", ""
		c.LastError = "Dash can't read this board's credentials or webhook secret: enter them again (" + err.Error() + ")"
	}
	c.WebhookSecret = secret
	_ = json.Unmarshal([]byte(creds), &c.Credentials)
	c.HasCredentials = len(c.Credentials) > 0
	c.HasWebhookSecret = c.WebhookSecret != ""
	return c, nil
}

// TrackerConfig is a project's board backend, credentials included (callers
// showing it must leave them out; the JSON encoding does). A project
// without one is on Dash's own board.
func (s *Store) TrackerConfig(projectID string) (model.TrackerConfig, error) {
	if _, err := s.GetProject(projectID); err != nil {
		return model.TrackerConfig{}, err
	}
	c, err := s.scanTracker(s.db.QueryRow("SELECT "+trackerCols+" FROM tracker_projects WHERE project_id = ?", projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.TrackerConfig{ProjectID: projectID, Backend: model.NativeTracker}, nil
	}
	return c, err
}

// TrackerConfigs are the board backends of every project not on Dash's own
// board, for the sync engine to start on boot.
func (s *Store) TrackerConfigs() ([]model.TrackerConfig, error) {
	rows, err := s.db.Query("SELECT " + trackerCols + " FROM tracker_projects WHERE backend NOT IN ('', 'dash') ORDER BY project_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TrackerConfig
	for rows.Next() {
		c, err := s.scanTracker(rows)
		if err != nil {
			return nil, err
		}
		if _, err := s.GetProject(c.ProjectID); err == nil {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// PutTrackerConfig saves a project's board backend as given, credentials
// and webhook secret included. Sync progress (cursor, last sync and error)
// is kept unless the backend changes. A native backend with nothing else
// set removes the row.
func (s *Store) PutTrackerConfig(c model.TrackerConfig) error {
	if _, err := s.GetProject(c.ProjectID); err != nil {
		return err
	}
	if c.Backend == "" {
		c.Backend = model.NativeTracker
	}
	cfg := "{}"
	if len(c.Config) > 0 {
		cfg = string(c.Config)
	}
	smap, _ := json.Marshal(nonNil(c.StatusMap))
	creds := noCreds
	if len(c.Credentials) > 0 {
		b, _ := json.Marshal(c.Credentials)
		var err error
		if creds, err = s.seal(string(b), trackerCredsContext(c.ProjectID)); err != nil {
			return err
		}
	}
	secret, err := s.seal(c.WebhookSecret, trackerSecretContext(c.ProjectID))
	if err != nil {
		return err
	}
	var switched any
	if !c.SwitchedAt.IsZero() {
		switched = c.SwitchedAt.UnixNano()
	}
	_, err = s.db.Exec(`INSERT INTO tracker_projects (project_id, backend, config, status_map, credentials, webhook_secret, switched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (project_id) DO UPDATE SET
			cursor = CASE WHEN backend = excluded.backend AND config = excluded.config THEN cursor ELSE 0 END,
			last_sync_at = CASE WHEN backend = excluded.backend THEN last_sync_at ELSE NULL END,
			last_error = CASE WHEN backend = excluded.backend THEN last_error ELSE '' END,
			backend = excluded.backend, config = excluded.config, status_map = excluded.status_map,
			credentials = excluded.credentials, webhook_secret = excluded.webhook_secret,
			switched_at = excluded.switched_at, updated_at = excluded.updated_at`,
		c.ProjectID, c.Backend, cfg, string(smap), creds, secret, switched, time.Now().Unix())
	return err
}

func nonNil[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return m
}

// SetTrackerSync records how a project's latest sync went (errMsg "" = it
// went through) and how far it read the tracker's changes (cursor; zero
// keeps the stored one).
func (s *Store) SetTrackerSync(projectID string, at time.Time, cursor time.Time, errMsg string) error {
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	var cur any
	if !cursor.IsZero() {
		cur = cursor.UnixNano()
	}
	_, err := s.db.Exec("UPDATE tracker_projects SET last_sync_at = ?, last_error = ?, cursor = COALESCE(?, cursor) WHERE project_id = ?",
		at.Unix(), errMsg, cur, projectID)
	return err
}

// ---- links ----

const linkCols = `todo_id, project_id, provider, external_id, url, pending, dash_hash, ext_hash, ext_version, state, synced_at`

func scanLink(row scanner) (model.TrackerLink, error) {
	var l model.TrackerLink
	var pending int
	var state string
	var synced sql.NullInt64
	if err := row.Scan(&l.TodoID, &l.ProjectID, &l.Ref.Provider, &l.Ref.ID, &l.Ref.URL, &pending, &l.DashHash, &l.ExtHash, &l.ExtVersion, &state, &synced); err != nil {
		return l, err
	}
	l.Pending = pending != 0
	if state != "" && state != "{}" {
		l.State = json.RawMessage(state)
	}
	l.SyncedAt = nullUnix(synced)
	return l, nil
}

// TrackerLinks are a project's todos linked to (or waiting for export to)
// its tracker.
func (s *Store) TrackerLinks(projectID string) ([]model.TrackerLink, error) {
	return queryAll(s.db, scanLink, "SELECT "+linkCols+" FROM tracker_links WHERE project_id = ? ORDER BY todo_id", projectID)
}

// PutTrackerLink saves a todo's link.
func (s *Store) PutTrackerLink(l model.TrackerLink) error {
	state := "{}"
	if len(l.State) > 0 {
		state = string(l.State)
	}
	var synced any
	if !l.SyncedAt.IsZero() {
		synced = l.SyncedAt.Unix()
	}
	_, err := s.db.Exec(`INSERT INTO tracker_links (`+linkCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (todo_id) DO UPDATE SET project_id = excluded.project_id, provider = excluded.provider,
			external_id = excluded.external_id, url = excluded.url, pending = excluded.pending,
			dash_hash = excluded.dash_hash, ext_hash = excluded.ext_hash, ext_version = excluded.ext_version,
			state = excluded.state, synced_at = excluded.synced_at`,
		l.TodoID, l.ProjectID, l.Ref.Provider, l.Ref.ID, l.Ref.URL, boolInt(l.Pending), l.DashHash, l.ExtHash, l.ExtVersion, state, synced)
	return err
}

// DeleteTrackerLink forgets a todo's link.
func (s *Store) DeleteTrackerLink(todoID string) error {
	_, err := s.db.Exec("DELETE FROM tracker_links WHERE todo_id = ?", todoID)
	return err
}

// UnlinkTracker drops every link of a project to its tracker, and the
// todos' ExternalRef: they stay in Dash as native todos.
func (s *Store) UnlinkTracker(projectID string) error {
	if _, err := s.db.Exec("DELETE FROM tracker_links WHERE project_id = ?", projectID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var changed []*model.Todo
	for id, t := range s.data.Todos {
		if t.ProjectID == projectID && t.ExternalRef != nil {
			cp := clone(*t)
			cp.ExternalRef = nil
			s.data.Todos[id] = &cp
			changed = append(changed, &cp)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	err := s.writeLocked(func(tx *sql.Tx) error {
		for _, t := range changed {
			if err := putTodo(tx, t); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, t := range changed {
		s.emit(model.EvTodoUpdated, t.ProjectID, t.ID, *t)
	}
	return nil
}

// SetExternalRef links a todo to its card (nil unlinks it) without
// touching anything else, UpdatedAt included: a link isn't an edit.
func (s *Store) SetExternalRef(todoID string, ref *model.ExternalRef) (model.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.data.Todos[todoID]
	if !ok {
		return model.Todo{}, ErrNotFound
	}
	cp := clone(*t)
	cp.ExternalRef = nil
	if ref != nil {
		r := *ref
		cp.ExternalRef = &r
	}
	s.data.Todos[todoID] = &cp
	if err := s.writeLocked(func(tx *sql.Tx) error { return putTodo(tx, &cp) }); err != nil {
		return cp, err
	}
	s.emit(model.EvTodoUpdated, cp.ProjectID, cp.ID, cp)
	return cp, nil
}

// ---- project events ----

// AddProjectEvent records a notable change to a project's setup.
func (s *Store) AddProjectEvent(ev model.ProjectEvent) (model.ProjectEvent, error) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	ev.At = ev.At.UTC().Truncate(time.Second)
	res, err := s.db.Exec("INSERT INTO project_events (project_id, kind, text, user_id, at) VALUES (?, ?, ?, ?, ?)",
		ev.ProjectID, ev.Kind, ev.Text, ev.UserID, ev.At.Unix())
	if err != nil {
		return ev, err
	}
	ev.ID, _ = res.LastInsertId()
	return ev, nil
}

// ProjectEvents are a project's latest events (at most limit), newest first.
func (s *Store) ProjectEvents(projectID string, limit int) ([]model.ProjectEvent, error) {
	return queryAll(s.db, func(sc scanner) (ev model.ProjectEvent, err error) {
		var at int64
		err = sc.Scan(&ev.ID, &ev.ProjectID, &ev.Kind, &ev.Text, &ev.UserID, &at)
		ev.At = time.Unix(at, 0).UTC()
		return ev, err
	}, "SELECT id, project_id, kind, text, user_id, at FROM project_events WHERE project_id = ? ORDER BY id DESC LIMIT ?", projectID, limit)
}

// ---- the org's view ----

// TrackerConfig is one of the org's projects' board backend.
func (o *OrgStore) TrackerConfig(projectID string) (model.TrackerConfig, error) {
	if !o.ownsProject(projectID) {
		return model.TrackerConfig{}, ErrNotFound
	}
	return o.s.TrackerConfig(projectID)
}

// ProjectEvents are one of the org's projects' latest events.
func (o *OrgStore) ProjectEvents(projectID string, limit int) ([]model.ProjectEvent, error) {
	if !o.ownsProject(projectID) {
		return nil, ErrNotFound
	}
	return o.s.ProjectEvents(projectID, limit)
}
