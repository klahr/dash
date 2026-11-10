package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/klahr/dash/internal/model"
)

// ---- Webhooks ----
//
// Webhooks live in the database only (not in memory with the other
// records): they're read when an event goes out and when settings open.

// WebhookSecretPrefix starts every webhook signing secret, so a leaked one
// is recognisable.
const WebhookSecretPrefix = "whsec_"

func newWebhookSecret() string { return WebhookSecretPrefix + rand.Text() }

const webhookCols = `id, project_id, user_id, org_id, name, url, format, events, enabled, secret, token, created_at, last_at, last_event, last_error`

func scanWebhook(row interface{ Scan(...any) error }) (model.Webhook, error) {
	var w model.Webhook
	var events string
	var enabled int
	var created int64
	var last, user, org sql.NullInt64
	err := row.Scan(&w.ID, &w.ProjectID, &user, &org, &w.Name, &w.URL, &w.Format, &events, &enabled, &w.Secret, &w.Token, &created, &last, &w.LastEvent, &w.LastError)
	if err != nil {
		return w, err
	}
	_ = json.Unmarshal([]byte(events), &w.Events)
	if w.Events == nil {
		w.Events = []model.NotifyEvent{}
	}
	w.Enabled = enabled != 0
	w.UserID, w.OrgID = user.Int64, org.Int64
	w.HasToken = w.Token != ""
	w.CreatedAt = time.Unix(created, 0).UTC()
	if last.Valid {
		w.LastAt = time.Unix(last.Int64, 0).UTC()
	}
	return w, nil
}

// ListWebhooks returns a project's webhooks ("" = every project's, not
// the personal ones), oldest first, with their secrets: callers showing
// them must leave those out (the JSON encoding does).
func (s *Store) ListWebhooks(projectID string) ([]model.Webhook, error) {
	if projectID != "" {
		return s.queryWebhooks("SELECT "+webhookCols+" FROM webhooks WHERE project_id = ? ORDER BY created_at, id", projectID)
	}
	return s.queryWebhooks("SELECT " + webhookCols + " FROM webhooks WHERE project_id != '' ORDER BY created_at, id")
}

// ListPersonalWebhooks returns user's personal webhooks made in org,
// oldest first, secrets included.
func (s *Store) ListPersonalWebhooks(user, org int64) ([]model.Webhook, error) {
	return s.queryWebhooks("SELECT "+webhookCols+" FROM webhooks WHERE project_id = '' AND user_id = ? AND org_id = ? ORDER BY created_at, id", user, org)
}

// WebhooksFor returns the webhooks an event of a project goes to: the
// project's own, then the personal webhooks made in its org by those still
// members of it.
func (s *Store) WebhooksFor(projectID string) ([]model.Webhook, error) {
	out, err := s.ListWebhooks(projectID)
	if err != nil || projectID == "" {
		return out, err
	}
	org, ok := s.ProjectOrg(projectID)
	if !ok || org == 0 {
		return out, nil
	}
	mine, err := s.queryWebhooks(`SELECT `+webhookCols+` FROM webhooks w
		WHERE w.project_id = '' AND w.org_id = ?
			AND EXISTS (SELECT 1 FROM org_memberships m WHERE m.user_id = w.user_id AND m.org_id = w.org_id)
		ORDER BY w.created_at, w.id`, org)
	return append(out, mine...), err
}

func (s *Store) queryWebhooks(q string, args ...any) ([]model.Webhook, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Webhook{}
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetWebhook returns a webhook, secret included.
func (s *Store) GetWebhook(id string) (model.Webhook, error) {
	w, err := scanWebhook(s.db.QueryRow("SELECT "+webhookCols+" FROM webhooks WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	return w, err
}

// CreateWebhook adds a webhook to its project, or a personal one (no
// project; UserID and OrgID set), with a new id and signing secret; the
// result carries the secret, to show once.
func (s *Store) CreateWebhook(w model.Webhook) (model.Webhook, error) {
	var user, org any
	if w.Personal() {
		if w.UserID == 0 || w.OrgID == 0 {
			return w, errors.New("a personal webhook needs its user and org")
		}
		user, org = w.UserID, w.OrgID
	} else {
		if _, err := s.GetProject(w.ProjectID); err != nil {
			return w, err
		}
		w.UserID, w.OrgID = 0, 0
	}
	if err := w.Normalize(); err != nil {
		return w, err
	}
	w.ID = NewID()
	w.Secret = newWebhookSecret()
	w.CreatedAt = time.Now().UTC().Truncate(time.Second)
	w.LastAt, w.LastEvent, w.LastError = time.Time{}, "", ""
	ev, _ := json.Marshal(w.Events)
	_, err := s.db.Exec(`INSERT INTO webhooks (id, project_id, user_id, org_id, name, url, format, events, enabled, secret, token, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.ProjectID, user, org, w.Name, w.URL, w.Format, string(ev), boolInt(w.Enabled), w.Secret, w.Token, w.CreatedAt.Unix())
	return w, err
}

// UpdateWebhook applies fn to a copy of the webhook and saves its settings
// (not its id, project or owner, secret or delivery status).
func (s *Store) UpdateWebhook(id string, fn func(*model.Webhook) error) (model.Webhook, error) {
	var out model.Webhook
	err := inTx(s.db, func(tx *sql.Tx) error {
		w, err := scanWebhook(tx.QueryRow("SELECT "+webhookCols+" FROM webhooks WHERE id = ?", id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		cp := w
		cp.Events = append([]model.NotifyEvent(nil), w.Events...)
		if err := fn(&cp); err != nil {
			return err
		}
		if err := cp.Normalize(); err != nil {
			return err
		}
		ev, _ := json.Marshal(cp.Events)
		if _, err := tx.Exec(`UPDATE webhooks SET name = ?, url = ?, format = ?, events = ?, enabled = ?, token = ? WHERE id = ?`,
			cp.Name, cp.URL, cp.Format, string(ev), boolInt(cp.Enabled), cp.Token, id); err != nil {
			return err
		}
		cp.ID, cp.ProjectID, cp.UserID, cp.OrgID, cp.Secret = w.ID, w.ProjectID, w.UserID, w.OrgID, w.Secret
		cp.CreatedAt, cp.LastAt, cp.LastEvent, cp.LastError = w.CreatedAt, w.LastAt, w.LastEvent, w.LastError
		out = cp
		return nil
	})
	return out, err
}

// RotateWebhookSecret gives the webhook a new signing secret and returns
// it; the old one stops working at once.
func (s *Store) RotateWebhookSecret(id string) (string, error) {
	secret := newWebhookSecret()
	res, err := s.db.Exec("UPDATE webhooks SET secret = ? WHERE id = ?", secret, id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return secret, nil
}

// DeleteWebhook removes a webhook.
func (s *Store) DeleteWebhook(id string) error {
	res, err := s.db.Exec("DELETE FROM webhooks WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// WebhookHistory is how many deliveries each webhook's history keeps.
const WebhookHistory = 50

// RecordWebhookDelivery adds d to its webhook's history (dropping the
// oldest beyond WebhookHistory) and makes it the webhook's last delivery.
// A deleted webhook is no error.
func (s *Store) RecordWebhookDelivery(d model.WebhookDelivery) error {
	d.Error = clip(d.Error, 500)
	d.Project, d.Subject = clip(d.Project, 200), clip(d.Subject, 200)
	if d.Attempts < 1 {
		d.Attempts = 1
	}
	return inTx(s.db, func(tx *sql.Tx) error {
		res, err := tx.Exec("UPDATE webhooks SET last_at = ?, last_event = ?, last_error = ? WHERE id = ?",
			d.At.Unix(), string(d.Event), d.Error, d.WebhookID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // deleted meanwhile
		}
		if _, err := tx.Exec(`INSERT INTO webhook_deliveries
			(webhook_id, at, event, delivery, project_id, project, subject, attempts, status, duration_ms, error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.WebhookID, d.At.Unix(), string(d.Event), d.Delivery, d.ProjectID, d.Project, d.Subject,
			d.Attempts, d.Status, d.DurationMS, d.Error); err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM webhook_deliveries WHERE webhook_id = ? AND id <=
			(SELECT id FROM webhook_deliveries WHERE webhook_id = ? ORDER BY id DESC LIMIT 1 OFFSET ?)`,
			d.WebhookID, d.WebhookID, WebhookHistory)
		return err
	})
}

// ListWebhookDeliveries returns a webhook's history, newest first.
func (s *Store) ListWebhookDeliveries(id string) ([]model.WebhookDelivery, error) {
	rows, err := s.db.Query(`SELECT id, webhook_id, at, event, delivery, project_id, project, subject, attempts, status, duration_ms, error
		FROM webhook_deliveries WHERE webhook_id = ? ORDER BY id DESC LIMIT ?`, id, WebhookHistory)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.WebhookDelivery{}
	for rows.Next() {
		var d model.WebhookDelivery
		var at int64
		if err := rows.Scan(&d.ID, &d.WebhookID, &at, &d.Event, &d.Delivery, &d.ProjectID, &d.Project, &d.Subject,
			&d.Attempts, &d.Status, &d.DurationMS, &d.Error); err != nil {
			return nil, err
		}
		d.At = time.Unix(at, 0).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- the org's view ----
//
// A project's webhooks are its org's; a personal webhook is only its
// owner's, in the org it was made in (OrgStore.AsUser).

func (o *OrgStore) ownsWebhook(id string) (model.Webhook, error) {
	w, err := o.s.GetWebhook(id)
	if err != nil {
		return w, err
	}
	if w.Personal() {
		if o.user == 0 || w.UserID != o.user || w.OrgID != o.org {
			return model.Webhook{}, ErrNotFound
		}
	} else if !o.ownsProject(w.ProjectID) {
		return model.Webhook{}, ErrNotFound
	}
	return w, nil
}

// ListPersonalWebhooks returns the user's personal webhooks in the org
// (none without a user).
func (o *OrgStore) ListPersonalWebhooks() ([]model.Webhook, error) {
	if o.user == 0 || o.org == 0 {
		return []model.Webhook{}, nil
	}
	return o.s.ListPersonalWebhooks(o.user, o.org)
}

// ListWebhookDeliveries returns one of the org's (or the user's)
// webhooks' history, newest first.
func (o *OrgStore) ListWebhookDeliveries(id string) ([]model.WebhookDelivery, error) {
	if _, err := o.ownsWebhook(id); err != nil {
		return nil, err
	}
	return o.s.ListWebhookDeliveries(id)
}

// ListWebhooks returns one of the org's projects' webhooks.
func (o *OrgStore) ListWebhooks(projectID string) ([]model.Webhook, error) {
	if !o.ownsProject(projectID) {
		return nil, ErrNotFound
	}
	return o.s.ListWebhooks(projectID)
}

func (o *OrgStore) GetWebhook(id string) (model.Webhook, error) { return o.ownsWebhook(id) }

// CreateWebhook adds a webhook to one of the org's projects, or (no
// ProjectID) a personal one of the user's in the org.
func (o *OrgStore) CreateWebhook(w model.Webhook) (model.Webhook, error) {
	if w.Personal() {
		if o.user == 0 || o.org == 0 {
			return w, ErrNotFound
		}
		w.UserID, w.OrgID = o.user, o.org
	} else if !o.ownsProject(w.ProjectID) {
		return w, ErrNotFound
	}
	return o.s.CreateWebhook(w)
}

func (o *OrgStore) UpdateWebhook(id string, fn func(*model.Webhook) error) (model.Webhook, error) {
	if _, err := o.ownsWebhook(id); err != nil {
		return model.Webhook{}, err
	}
	return o.s.UpdateWebhook(id, fn)
}

func (o *OrgStore) RotateWebhookSecret(id string) (string, error) {
	if _, err := o.ownsWebhook(id); err != nil {
		return "", err
	}
	return o.s.RotateWebhookSecret(id)
}

func (o *OrgStore) DeleteWebhook(id string) error {
	if _, err := o.ownsWebhook(id); err != nil {
		return err
	}
	return o.s.DeleteWebhook(id)
}
