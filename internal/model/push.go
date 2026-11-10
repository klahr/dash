package model

import "time"

// PushSubscription is a browser that gets Dash's notifications by Web Push
// (internal/notify/webpush.go): one user's, in the org they turned
// notifications on in. Endpoint is the push service's URL for that browser
// and, with the keys, a secret: whoever has it can push to it.
type PushSubscription struct {
	ID        string    `json:"id"`
	UserID    int64     `json:"user_id"`
	OrgID     int64     `json:"org_id"`
	Endpoint  string    `json:"-"`
	P256dh    string    `json:"-"` // the browser's public key, base64url
	Auth      string    `json:"-"` // its 16-byte auth secret, base64url
	UserAgent string    `json:"user_agent,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	LastAt    time.Time `json:"last_at,omitzero"` // zero: never delivered
	LastError string    `json:"last_error,omitempty"`
}
