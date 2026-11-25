package model

import "time"

// GitHubHook is the webhook a hub registered on a GitHub repository so
// GitHub tells it of workflow runs (internal/ci, hooks.go). There is one
// per repository, whatever number of projects use it.
type GitHubHook struct {
	Repo   string `json:"repo"`              // OWNER/REPO, lower case
	HookID int64  `json:"hook_id,omitempty"` // GitHub's id; 0 while none is registered
	// Secret signs GitHub's deliveries. It never leaves the hub but to the
	// runner whose gh registers it: never in JSON.
	Secret string `json:"-"`
	URL    string `json:"url,omitempty"` // where GitHub delivers
	// State is the last registration's outcome (ci.Hook*), Reason why it
	// failed.
	State        string    `json:"state"`
	Reason       string    `json:"reason,omitempty"`
	Registered   time.Time `json:"registered,omitzero"`    // when it was registered (or updated) last
	LastDelivery time.Time `json:"last_delivery,omitzero"` // the last signed delivery, a ping included
	LastPing     time.Time `json:"last_ping,omitzero"`     // when Dash last asked GitHub to ping it
	Checked      time.Time `json:"checked,omitzero"`       // the last registration attempt
}
