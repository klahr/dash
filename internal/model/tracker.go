package model

import (
	"encoding/json"
	"time"
)

// NativeTracker is the board backend of a project whose board is Dash's
// own (the default): no external tracker, no sync.
const NativeTracker = "dash"

// ExternalRef links a todo to the card, issue or ticket that stands for it
// in its project's external tracker (see internal/tracker).
type ExternalRef struct {
	Provider string `json:"provider"`      // the backend's name, e.g. "trello"
	ID       string `json:"id"`            // the tracker's id for the card
	URL      string `json:"url,omitempty"` // a link to it, for people
}

// TrackerConfig is a project's board backend: which tracker its task board
// lives in, how to reach it and how Dash's statuses map to its columns.
// Every project has exactly one; without a stored one it is NativeTracker.
type TrackerConfig struct {
	ProjectID string `json:"project_id"`
	Backend   string `json:"backend"`
	// Config is the backend's own settings (board or collection ids, a
	// base URL, …) as JSON; its shape is the backend's business.
	Config json.RawMessage `json:"config,omitempty"`
	// StatusMap maps each Dash status to the backend's column, list or
	// workflow state. Several statuses may share one; an unmapped status
	// leaves the card where it is.
	StatusMap map[TodoStatus]string `json:"status_map,omitempty"`
	// Credentials are the backend's secrets (API tokens, user names). They
	// are stored like webhook secrets and never leave Dash: HasCredentials
	// tells whether there are any.
	Credentials    map[string]string `json:"-"`
	HasCredentials bool              `json:"has_credentials"`
	// WebhookSecret verifies the tracker's webhook calls for this project
	// (shown once, when made or rotated).
	WebhookSecret    string `json:"-"`
	HasWebhookSecret bool   `json:"has_webhook_secret"`
	// WebhookURL is the public address of the project's webhook endpoint,
	// when Dash has a public URL (hub setups): set by the sync engine before
	// it builds the backend, never stored. Backends that register their
	// webhooks with the tracker (and sign with the URL) use it.
	WebhookURL string `json:"-"`
	// SwitchedAt is when the project moved to this backend: todos created
	// before it stay Dash-only unless they were exported then.
	SwitchedAt time.Time `json:"switched_at,omitzero"`
	// Cursor is the external update time the sync engine has read up to.
	Cursor     time.Time `json:"-"`
	LastSyncAt time.Time `json:"last_sync_at,omitzero"`
	LastError  string    `json:"last_error,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitzero"`
}

// Native reports whether the project's board is Dash's own.
func (c TrackerConfig) Native() bool { return c.Backend == "" || c.Backend == NativeTracker }

// ColumnFor is the external column status maps to ("" = unmapped).
func (c TrackerConfig) ColumnFor(s TodoStatus) string { return c.StatusMap[s] }

// StatusFor is the Dash status a card in column stands for: the first, in
// lifecycle order, of the statuses mapped to it ("" = none).
func (c TrackerConfig) StatusFor(column string) TodoStatus {
	if column == "" {
		return ""
	}
	for _, s := range Statuses {
		if c.StatusMap[s] == column {
			return s
		}
	}
	return ""
}

// Statuses are every TodoStatus in lifecycle order.
var Statuses = []TodoStatus{StatusDraft, StatusQueued, StatusInProgress, StatusReview, StatusMerging, StatusMerged, StatusFailed}

// TrackerLink is the sync engine's record of one todo linked to (or
// waiting to be exported to) its project's tracker.
type TrackerLink struct {
	TodoID    string      `json:"todo_id"`
	ProjectID string      `json:"project_id"`
	Ref       ExternalRef `json:"ref"`
	// Pending: chosen for export when the project switched; no card yet.
	Pending bool `json:"pending,omitempty"`
	// DashHash and ExtHash fingerprint the intent fields as each side had
	// them at the last sync; ExtVersion is the tracker's version or update
	// time of the card then. State is the engine's own JSON (the fields
	// themselves, comments seen).
	DashHash   string          `json:"dash_hash,omitempty"`
	ExtHash    string          `json:"ext_hash,omitempty"`
	ExtVersion string          `json:"ext_version,omitempty"`
	State      json.RawMessage `json:"state,omitempty"`
	SyncedAt   time.Time       `json:"synced_at,omitzero"`
}

// ProjectEvent is a notable change to a project's setup, kept for its
// history (e.g. switching its board backend).
type ProjectEvent struct {
	ID        int64     `json:"id"`
	ProjectID string    `json:"project_id"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	UserID    int64     `json:"user_id,omitempty"`
	At        time.Time `json:"at"`
}
