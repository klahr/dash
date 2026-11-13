// Package tracker puts each project's task board behind a pluggable board
// backend: Dash's own board ("dash", the default) or an external tracker
// such as Trello or Jira.
//
// # The per-project model
//
// Every project has exactly one backend (model.TrackerConfig, stored per
// project): its name, its own settings as JSON (board or collection ids…),
// the mapping of Dash statuses to the tracker's columns, lists or workflow
// states, its credentials and a webhook secret. Projects on one Dash may use
// different backends, or the same provider with different boards and
// accounts, at the same time. Nothing about a backend is global.
//
// # Intent vs execution
//
// The tracker owns a todo's intent: its title, prompt (the card's
// description), priority, dependencies, status column and comments. Dash
// keeps everything about executing it (worktree, session, checks, feedback,
// PR, cost…) in its store, and never sends that to a tracker. The queue,
// dispatcher and runners keep working on the store as before; only the sync
// engine (Manager) talks to trackers.
//
// # Sync
//
// A Manager runs one worker per project whose backend isn't native. It
// pushes the project's intent changes to its tracker as the store announces
// them (outbound), and polls the tracker, sooner when a webhook says so, for
// changes to apply to the todos (inbound). Each link (model.TrackerLink)
// remembers each side's fields at the last sync, so a change is told from
// an echo of one Dash made itself, and when both sides changed a field the
// later writer wins. An external move a todo's state machine forbids is
// undone on the tracker, with a comment saying why. Native projects run no
// worker: their board is the store itself.
//
// # Writing a backend
//
// Implement Backend (and any of the optional Webhooker, Tester and
// ColumnLister) and Register it with a factory that builds an instance for
// one project from its ProjectConfig and Credentials. Status mapping is the
// project's: use ProjectConfig.ColumnFor in SetStatus and Create, and
// report each card's raw column in Item.Column; the engine maps it back.
package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/klahr/dash/internal/model"
)

// Ref identifies a card in a tracker.
type Ref = model.ExternalRef

// ProjectConfig is one project's backend configuration.
type ProjectConfig = model.TrackerConfig

// Credentials are a project's secrets for its backend, by field name.
type Credentials = map[string]string

// Native is the name of Dash's own board backend.
const Native = model.NativeTracker

// Item is a card's intent fields, as a tracker has them.
type Item struct {
	Ref         Ref
	Title       string
	Description string // the todo's prompt, in markdown
	Priority    *int   // nil: unprioritised
	// DependsOn are the tracker ids of the cards this one waits for.
	DependsOn []string
	// Column is the raw column, list or workflow state the card is in; the
	// project's status mapping turns it into a Dash status.
	Column string
	// Comments on the card, oldest first (optional).
	Comments []Comment
	// UpdatedAt and Version are when and in which version the tracker last
	// changed the card, for conflicts and the change feed. Version may be
	// empty when the tracker has none.
	UpdatedAt time.Time
	Version   string
	// Deleted: the card was deleted or archived (change feeds that report it).
	Deleted bool
}

// Comment is one comment on a card.
type Comment struct {
	ID     string
	Author string
	Body   string
	At     time.Time
}

// Backend is one project's board in one tracker. Every project gets its own
// instance (see Register), so an instance only ever sees its project's
// board and credentials. Methods may be called from one goroutine at a time.
type Backend interface {
	// Name is the backend's registered name, e.g. "trello".
	Name() string
	// List returns the cards in the project's board changed since then
	// (zero: all of them). Returning more than asked is fine; the engine
	// skips what didn't change.
	List(ctx context.Context, since time.Time) ([]Item, error)
	// Create makes a card for it (it.Ref is empty) in it.Column and
	// returns its ref.
	Create(ctx context.Context, it Item) (Ref, error)
	// Update sets a card's title, description, priority and dependencies.
	Update(ctx context.Context, ref Ref, it Item) error
	// SetStatus moves a card to the column the project maps status to.
	SetStatus(ctx context.Context, ref Ref, status model.TodoStatus) error
	// Comment adds a markdown comment to a card.
	Comment(ctx context.Context, ref Ref, markdown string) error
	// Delete deletes (or archives) a card.
	Delete(ctx context.Context, ref Ref) error
}

// Webhooker is a Backend that receives the tracker's webhooks at
// /api/projects/{id}/tracker/webhook.
type Webhooker interface {
	// VerifyWebhook checks a webhook call with the project's secret and
	// returns the ids of the cards it says changed (none: poll them all).
	// An error refuses the call.
	VerifyWebhook(r *http.Request, body []byte, secret string) ([]string, error)
}

// Tester is a Backend that can check its settings and credentials without
// changing anything (the settings' Test connection button). Backends
// without it are tested by listing their cards.
type Tester interface {
	Test(ctx context.Context) error
}

// Column is one of a tracker's columns, lists or workflow states.
type Column struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ColumnLister is a Backend that can list its board's columns, for the
// status mapping editor.
type ColumnLister interface {
	Columns(ctx context.Context) ([]Column, error)
}

// Factory builds a backend for one project.
type Factory func(cfg ProjectConfig, creds Credentials) (Backend, error)

// Field is one setting a backend's form asks for.
type Field struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Hint        string `json:"hint,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	// Secret fields are credentials: stored apart from the settings, never
	// sent back to the browser.
	Secret   bool `json:"secret,omitempty"`
	Required bool `json:"required,omitempty"`
	// Type is "select" for a dropdown (default: a text input).
	Type string `json:"type,omitempty"`
	// Options are a select's fixed choices.
	Options []Option `json:"options,omitempty"`
	// Dynamic: a select whose choices the tracker supplies (see
	// Registration.Options), loaded again whenever a field it DependsOn
	// changes.
	Dynamic   bool     `json:"dynamic,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
	// Columns: the field picks the board, so the settings load its
	// columns (Test) for the status mapping when it changes.
	Columns bool `json:"columns,omitempty"`
}

// Option is one choice of a select field.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// OptionsFunc lists a dynamic field's choices for a config that may be
// incomplete (the fields it depends on are set, those after it maybe not).
type OptionsFunc func(ctx context.Context, cfg ProjectConfig, creds Credentials, field string) ([]Option, error)

// WebhookRegistrar is a Backend that registers its webhook with the
// tracker itself. When Dash has a public URL the engine calls Register
// as the project's worker starts (with cfg.WebhookURL and the project's
// secret; it should replace any webhook of its own already there), and
// Unregister when the project leaves the board or is deleted.
type WebhookRegistrar interface {
	RegisterWebhook(ctx context.Context, url, secret string) error
	UnregisterWebhook(ctx context.Context, url string) error
}

// FieldPriority names the priority field (see FieldSkipper).
const FieldPriority = "priority"

// FieldSkipper is a Backend whose project's board can't hold some intent
// fields (as set up): the engine leaves them to Dash, neither pushing them
// nor reading them, rather than reading their absence as a clear.
type FieldSkipper interface {
	Skips() []string
}

// Comment feedback policies (see CommentFeedbacker).
const (
	// FeedbackChat: comments by people only go to the todo's chat.
	FeedbackChat = ""
	// FeedbackNote: comments on a todo in review are also kept as rework
	// notes, sent to Claude when the todo is reworked.
	FeedbackNote = "note"
	// FeedbackRework: a comment on a todo in review sends it back to
	// Claude with the comment as its rework feedback.
	FeedbackRework = "rework"
)

// CommentFeedbacker is a Backend whose project chose what the tracker's
// comments on a todo in review do (a Feedback* policy).
type CommentFeedbacker interface {
	CommentFeedback() string
}

// Registration is a backend's entry in the registry.
type Registration struct {
	Name  string  `json:"name"`
	Label string  `json:"label"`
	New   Factory `json:"-"`
	// Fields are the settings and credentials its form asks for.
	Fields []Field `json:"fields,omitempty"`
	// DefaultStatusMap is a new project's mapping (optional).
	DefaultStatusMap map[model.TodoStatus]string `json:"default_status_map,omitempty"`
	// Board names the board a config points at, so moving a project to
	// another board is told from editing its settings (default: the
	// config's canonical JSON).
	Board func(config json.RawMessage) string `json:"-"`
	// Options lists the choices of the Dynamic fields (optional).
	Options OptionsFunc `json:"-"`
	// AutoWebhook: the backend registers its webhook with the tracker
	// itself (WebhookRegistrar) when Dash has a public URL.
	AutoWebhook bool `json:"auto_webhook,omitempty"`
	// Native is Dash's own board: built by the Manager on its store, never
	// synced.
	Native bool `json:"native,omitempty"`
}

var (
	regMu    sync.RWMutex
	registry = map[string]Registration{}
)

func init() {
	Register(Registration{Name: Native, Label: "Dash", Native: true})
}

// Register adds a backend; registering a name again replaces it.
func Register(r Registration) {
	if r.Name == "" {
		panic("tracker: Register without a name")
	}
	if r.New == nil && !r.Native {
		panic("tracker: Register " + r.Name + " without a factory")
	}
	regMu.Lock()
	defer regMu.Unlock()
	registry[r.Name] = r
}

// Unregister removes a backend (tests).
func Unregister(name string) {
	regMu.Lock()
	defer regMu.Unlock()
	if name != Native {
		delete(registry, name)
	}
}

// Lookup finds a registered backend.
func Lookup(name string) (Registration, bool) {
	if name == "" {
		name = Native
	}
	regMu.RLock()
	defer regMu.RUnlock()
	r, ok := registry[name]
	return r, ok
}

// Backends are every registered backend, Dash's own first.
func Backends() []Registration {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Registration, 0, len(registry))
	for _, r := range registry {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Native != out[j].Native {
			return out[i].Native
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// ErrUnknownBackend is returned for a backend name nobody registered.
var ErrUnknownBackend = errors.New("unknown board backend")

// Resolve is cfg as this Dash can use it. A backend nobody registered (one
// a project was set to before Dash dropped it, say) reads as Dash's own
// board, and known is false: the project runs no sync until it is set up
// again.
func Resolve(cfg ProjectConfig) (_ ProjectConfig, known bool) {
	if cfg.Native() {
		return cfg, true
	}
	if _, ok := Lookup(cfg.Backend); ok {
		return cfg, true
	}
	return ProjectConfig{ProjectID: cfg.ProjectID, Backend: Native}, false
}

// New builds the backend cfg names for its project. Native projects have
// no backend: their board is the store.
func New(cfg ProjectConfig) (Backend, error) {
	r, ok := Lookup(cfg.Backend)
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownBackend, cfg.Backend)
	}
	if r.Native {
		return nil, errors.New("tracker: a native project has no backend; its board is the store")
	}
	return r.New(cfg, cfg.Credentials)
}

// ErrNoOptions is returned for a field whose backend lists no choices.
var ErrNoOptions = errors.New("this field has no choices to load")

// Options lists a dynamic field's choices for cfg (see Registration.Options).
func Options(ctx context.Context, cfg ProjectConfig, field string) ([]Option, error) {
	r, ok := Lookup(cfg.Backend)
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownBackend, cfg.Backend)
	}
	if r.Options == nil || !slices.ContainsFunc(r.Fields, func(f Field) bool { return f.Name == field && f.Dynamic }) {
		return nil, ErrNoOptions
	}
	return r.Options(ctx, cfg, cfg.Credentials, field)
}

// BoardOf names the board cfg points at (see Registration.Board).
func BoardOf(cfg ProjectConfig) string {
	if cfg.Native() {
		return Native
	}
	if r, ok := Lookup(cfg.Backend); ok && r.Board != nil {
		return cfg.Backend + ":" + r.Board(cfg.Config)
	}
	return cfg.Backend + ":" + canonicalJSON(cfg.Config)
}

// canonicalJSON re-encodes raw with sorted keys, so equal settings compare equal.
func canonicalJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v) // maps marshal with sorted keys
	if string(b) == "null" {
		return "{}"
	}
	return string(b)
}

// SameBoard reports whether a and b point a project at the same board, so
// switching between them keeps the links.
func SameBoard(a, b ProjectConfig) bool { return BoardOf(a) == BoardOf(b) }

// inboundStatuses are the statuses a tracker may move a todo to: the ones
// a person picks by hand on the board (back to draft, or into the queue).
// Every other status is Dash's to reach (running, review, merging, merged,
// failed), and a card moved there is moved back.
var inboundStatuses = []model.TodoStatus{model.StatusDraft, model.StatusQueued}

// InboundAllowed reports whether a tracker may move a todo from one status
// to another: the state machine allows it and it is a person's move.
func InboundAllowed(from, to model.TodoStatus) bool {
	return slices.Contains(inboundStatuses, to) && model.CanTransition(from, to)
}
