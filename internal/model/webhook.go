package model

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// NotifyEvent is something a project's outgoing webhooks can be told about:
// the same moments the web app notifies the user of.
type NotifyEvent string

const (
	NotifyReview     NotifyEvent = "task.review"       // a task is ready for review
	NotifyFailed     NotifyEvent = "task.failed"       // a task failed
	NotifyMerged     NotifyEvent = "task.merged"       // a task's work landed
	NotifyPermission NotifyEvent = "permission.needed" // Claude waits for a tool approval
	NotifyBuild      NotifyEvent = "build.failing"     // GitHub Actions started failing on the default branch
	NotifyBehind     NotifyEvent = "checkout.behind"   // a runner's checkout was left behind origin (local changes or commits)
	NotifyTest       NotifyEvent = "webhook.test"      // the settings' "Send test" button
)

// NotifyEvents are the events a webhook can subscribe to, in display order.
// NotifyBehind is never on unless chosen (see DefaultNotifyEvents).
var NotifyEvents = []NotifyEvent{NotifyReview, NotifyFailed, NotifyMerged, NotifyPermission, NotifyBuild, NotifyBehind}

// DefaultNotifyEvents are a new webhook's events: all but NotifyBehind,
// which only matters to whoever sits at a runner, so it is opt-in.
var DefaultNotifyEvents = []NotifyEvent{NotifyReview, NotifyFailed, NotifyMerged, NotifyPermission, NotifyBuild}

// Webhook formats: how a delivery is shaped for the receiving service.
const (
	WebhookJSON  = "json"  // generic JSON, signed with the webhook's secret
	WebhookSlack = "slack" // Slack incoming webhook
	WebhookNtfy  = "ntfy"  // ntfy topic URL
)

// WebhookFormats are the supported formats.
var WebhookFormats = []string{WebhookJSON, WebhookSlack, WebhookNtfy}

// Webhook is an outgoing notification target of a project.
type Webhook struct {
	ID        string        `json:"id"`
	ProjectID string        `json:"project_id"`
	Name      string        `json:"name,omitempty"`
	URL       string        `json:"url"`
	Format    string        `json:"format"`
	Events    []NotifyEvent `json:"events"`
	Enabled   bool          `json:"enabled"`
	// Secret signs generic JSON deliveries. It is shown once, when the
	// webhook is made or the secret rotated; never in lists.
	Secret string `json:"-"`
	// Token is an optional access token sent as "Authorization: Bearer"
	// (ntfy servers that need one). Write-only: HasToken tells it is set.
	Token     string    `json:"-"`
	HasToken  bool      `json:"has_token"`
	CreatedAt time.Time `json:"created_at"`

	// The last delivery: when, of what event, and its error ("" = it
	// went through).
	LastAt    time.Time   `json:"last_at,omitzero"`
	LastEvent NotifyEvent `json:"last_event,omitempty"`
	LastError string      `json:"last_error,omitempty"`
}

// Wants reports whether w is on and subscribed to ev. Tests always go out.
func (w Webhook) Wants(ev NotifyEvent) bool {
	return w.Enabled && (ev == NotifyTest || slices.Contains(w.Events, ev))
}

// Normalize trims w's fields and checks them.
func (w *Webhook) Normalize() error {
	w.Name = strings.TrimSpace(w.Name)
	w.URL = strings.TrimSpace(w.URL)
	w.Token = strings.TrimSpace(w.Token)
	if len(w.Name) > 100 {
		return errors.New("webhook name: up to 100 characters")
	}
	if !slices.Contains(WebhookFormats, w.Format) {
		return fmt.Errorf("webhook format must be one of %s", strings.Join(WebhookFormats, ", "))
	}
	if len(w.URL) > 2000 {
		return errors.New("webhook URL is too long")
	}
	u, err := url.Parse(w.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("webhook URL must be an http(s) URL")
	}
	if u.User != nil {
		return errors.New("webhook URL must not contain a user name or password")
	}
	var evs []NotifyEvent
	for _, e := range w.Events {
		if !slices.Contains(NotifyEvents, e) {
			return fmt.Errorf("unknown webhook event %q", e)
		}
		if !slices.Contains(evs, e) {
			evs = append(evs, e)
		}
	}
	w.Events = evs
	if w.Events == nil {
		w.Events = []NotifyEvent{}
	}
	w.HasToken = w.Token != ""
	return nil
}
