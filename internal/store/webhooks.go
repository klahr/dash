package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

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

const webhookCols = `id, project_id, name, url, format, events, enabled, secret, token, created_at, last_at, last_event, last_error`

func scanWebhook(row interface{ Scan(...any) error }) (model.Webhook, error) {
	var w model.Webhook
	var events string
	var enabled int
	var created int64
	var last sql.NullInt64
	err := row.Scan(&w.ID, &w.ProjectID, &w.Name, &w.URL, &w.Format, &events, &enabled, &w.Secret, &w.Token, &created, &last, &w.LastEvent, &w.LastError)
	if err != nil {
		return w, err
	}
	_ = json.Unmarshal([]byte(events), &w.Events)
	if w.Events == nil {
		w.Events = []model.NotifyEvent{}
	}
	w.Enabled = enabled != 0
	w.HasToken = w.Token != ""
	w.CreatedAt = time.Unix(created, 0).UTC()
	if last.Valid {
		w.LastAt = time.Unix(last.Int64, 0).UTC()
	}
	return w, nil
}

// ListWebhooks returns a project's webhooks ("" = every project's), oldest
// first, with their secrets: callers showing them must leave those out
// (the JSON encoding does).
func (s *Store) ListWebhooks(projectID string) ([]model.Webhook, error) {
	q := "SELECT " + webhookCols + " FROM webhooks"
	var args []any
	if projectID != "" {
		q += " WHERE project_id = ?"
		args = append(args, projectID)
	}
	rows, err := s.db.Query(q+" ORDER BY created_at, id", args...)
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

// CreateWebhook adds a webhook to its project, with a new id and signing
// secret; the result carries the secret, to show once.
func (s *Store) CreateWebhook(w model.Webhook) (model.Webhook, error) {
	if _, err := s.GetProject(w.ProjectID); err != nil {
		return w, err
	}
	if err := w.Normalize(); err != nil {
		return w, err
	}
	w.ID = NewID()
	w.Secret = newWebhookSecret()
	w.CreatedAt = time.Now().UTC().Truncate(time.Second)
	w.LastAt, w.LastEvent, w.LastError = time.Time{}, "", ""
	ev, _ := json.Marshal(w.Events)
	_, err := s.db.Exec(`INSERT INTO webhooks (id, project_id, name, url, format, events, enabled, secret, token, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.ProjectID, w.Name, w.URL, w.Format, string(ev), boolInt(w.Enabled), w.Secret, w.Token, w.CreatedAt.Unix())
	return w, err
}

// UpdateWebhook applies fn to a copy of the webhook and saves its settings
// (not its id, project, secret or delivery status).
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
		cp.ID, cp.ProjectID, cp.Secret = w.ID, w.ProjectID, w.Secret
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

// RecordWebhookDelivery notes how the webhook's latest delivery went
// (errMsg "" = it went through). A deleted webhook is no error.
func (s *Store) RecordWebhookDelivery(id string, ev model.NotifyEvent, at time.Time, errMsg string) error {
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	_, err := s.db.Exec("UPDATE webhooks SET last_at = ?, last_event = ?, last_error = ? WHERE id = ?", at.Unix(), string(ev), errMsg, id)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- the org's view ----

func (o *OrgStore) ownsWebhook(id string) (model.Webhook, error) {
	w, err := o.s.GetWebhook(id)
	if err != nil {
		return w, err
	}
	if !o.ownsProject(w.ProjectID) {
		return model.Webhook{}, ErrNotFound
	}
	return w, nil
}

// ListWebhooks returns one of the org's projects' webhooks.
func (o *OrgStore) ListWebhooks(projectID string) ([]model.Webhook, error) {
	if !o.ownsProject(projectID) {
		return nil, ErrNotFound
	}
	return o.s.ListWebhooks(projectID)
}

func (o *OrgStore) GetWebhook(id string) (model.Webhook, error) { return o.ownsWebhook(id) }

func (o *OrgStore) CreateWebhook(w model.Webhook) (model.Webhook, error) {
	if !o.ownsProject(w.ProjectID) {
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
