// Package notify sends Dash's events to outgoing webhooks: generic signed
// JSON, Slack incoming webhooks and ntfy topics. It watches the event hub
// (hub.Hub.Observe) for the moments the web app notifies the user of: a
// task ready for review, failed or merged, Claude waiting for an approval,
// and the default branch's build starting to fail. Deliveries run in the
// background with a timeout and retries, so they never hold up the queue.
package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/ci"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
)

// Store is what the notifier reads: webhooks, and the records events name.
type Store interface {
	ListWebhooks(projectID string) ([]model.Webhook, error)
	GetProject(id string) (model.Project, error)
	GetTodo(id string) (model.Todo, error)
	ListTodos(projectID string, statuses ...model.TodoStatus) []model.Todo
	RecordWebhookDelivery(id string, ev model.NotifyEvent, at time.Time, errMsg string) error
}

// Defaults.
const (
	DefaultTimeout  = 10 * time.Second
	DefaultAttempts = 4
	DefaultBackoff  = 2 * time.Second
	// DefaultBehindEvery limits checkout.behind: a checkout that keeps
	// getting stuck and unstuck notifies at most this often.
	DefaultBehindEvery = 6 * time.Hour
	queueSize          = 1024
)

// Notifier turns hub events into webhook deliveries. Set its fields, then
// Start it; Observe may be called before.
type Notifier struct {
	Store Store
	// Origin is the URL Dash is reached at for a project's org, for links
	// back to tasks (e.g. "https://dash.example.com"); "" = no links.
	Origin func(projectID string) string
	// AllowPrivate lets webhooks reach private, loopback and link-local
	// addresses (a self-hosted Dash on its own network, or development).
	AllowPrivate bool
	Timeout      time.Duration // per attempt (DefaultTimeout)
	Attempts     int           // tries per delivery (DefaultAttempts)
	Backoff      time.Duration // wait before the 2nd try; doubles after (DefaultBackoff)
	// Actions, if set, adds Approve/Deny buttons to ntfy notifications of
	// permission requests (needs Origin).
	Actions *Actions
	Now     func() time.Time

	once   sync.Once
	queue  chan model.Event
	client *http.Client

	// BehindEvery is how often, at most, one runner's checkout of a
	// project being left behind origin notifies (DefaultBehindEvery).
	BehindEvery time.Duration

	// Seen by the run loop only.
	status map[string]model.TodoStatus // todo id → last status
	ci     map[string]string           // project id → last build state
	stuck  map[[2]string]bool          // {project, runner} → its checkout is left behind
	told   map[[2]string]time.Time     // {project, runner} → when that last notified

	wg sync.WaitGroup
}

func (n *Notifier) init() {
	n.once.Do(func() {
		n.queue = make(chan model.Event, queueSize)
		n.status = map[string]model.TodoStatus{}
		n.ci = map[string]string{}
		n.stuck, n.told = map[[2]string]bool{}, map[[2]string]time.Time{}
		n.client = newClient(n.timeout(), n.AllowPrivate)
	})
}

func (n *Notifier) timeout() time.Duration {
	if n.Timeout > 0 {
		return n.Timeout
	}
	return DefaultTimeout
}

func (n *Notifier) attempts() int {
	if n.Attempts > 0 {
		return n.Attempts
	}
	return DefaultAttempts
}

func (n *Notifier) backoff() time.Duration {
	if n.Backoff > 0 {
		return n.Backoff
	}
	return DefaultBackoff
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// Observe is fed every hub event. It never blocks (the store publishes
// holding its lock): the events that may notify are queued for the run
// loop, and dropped with a warning if it falls far behind.
func (n *Notifier) Observe(ev model.Event) {
	switch ev.Type {
	case model.EvTodoUpdated, model.EvTodoDeleted, model.EvPermission, model.EvCI, model.EvCheckouts:
	default:
		return
	}
	n.init()
	select {
	case n.queue <- ev:
	default:
		slog.Warn("notifications: queue full, event dropped", "type", ev.Type, "project", ev.ProjectID)
	}
}

// Start remembers every task's status, so only changes from now on notify,
// and handles events until ctx ends. Deliveries in flight are given until
// Wait.
func (n *Notifier) Start(ctx context.Context) {
	n.init()
	for _, t := range n.Store.ListTodos("") {
		n.status[t.ID] = t.Status
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-n.queue:
				if ev.Type == evFlush {
					close(ev.Data.(chan struct{}))
					continue
				}
				n.handle(ctx, ev)
			}
		}
	}()
}

// evFlush marks the place in the queue Flush waits for.
const evFlush model.EventType = "notify.flush"

// Flush waits until the events observed so far are handled and their
// deliveries done (tests, and a graceful stop).
func (n *Notifier) Flush(ctx context.Context) {
	n.init()
	done := make(chan struct{})
	select {
	case n.queue <- model.Event{Type: evFlush, Data: done}:
	case <-ctx.Done():
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
		return
	}
	n.wg.Wait()
}

// handle turns one hub event into a notification, if it is one, and sends
// it to the webhooks of its project that want it.
func (n *Notifier) handle(ctx context.Context, ev model.Event) {
	note, ok := n.classify(ev)
	if !ok {
		return
	}
	hooks, err := n.Store.ListWebhooks(note.Project.ID)
	if err != nil {
		slog.Error("notifications: list webhooks", "project", note.Project.ID, "err", err)
		return
	}
	for _, w := range hooks {
		if !w.Wants(note.Type) {
			continue
		}
		n.wg.Add(1)
		go func(w model.Webhook) {
			defer n.wg.Done()
			_ = n.deliver(ctx, w, note, n.attempts())
		}(w)
	}
}

// classify reports whether ev is a moment to notify of, as the web app
// decides it (web/static/app.js onEvent): a task's status changing to
// review, failed or merged; a new permission request; a build turning
// failing.
func (n *Notifier) classify(ev model.Event) (Notification, bool) {
	var kind model.NotifyEvent
	var todoID, message string
	var permRef *PermissionRef
	var build *BuildRef
	switch ev.Type {
	case model.EvTodoDeleted:
		delete(n.status, ev.TodoID)
		return Notification{}, false
	case model.EvTodoUpdated:
		t, ok := ev.Data.(model.Todo)
		if !ok {
			return Notification{}, false
		}
		prev, known := n.status[t.ID]
		n.status[t.ID] = t.Status
		if !known || prev == t.Status {
			return Notification{}, false
		}
		switch t.Status {
		case model.StatusReview:
			kind = model.NotifyReview
		case model.StatusFailed:
			kind, message = model.NotifyFailed, t.Error
		case model.StatusMerged:
			kind = model.NotifyMerged
		default:
			return Notification{}, false
		}
		todoID = t.ID
	case model.EvPermission:
		r, ok := ev.Data.(perm.Request)
		if !ok || r.Decision != "" {
			return Notification{}, false
		}
		kind, todoID = model.NotifyPermission, r.TodoID
		permRef = &PermissionRef{ID: r.ID, Tool: r.Tool}
		message = "Claude wants to use " + r.Tool
	case model.EvCI:
		st, ok := ev.Data.(ci.Status)
		if !ok {
			return Notification{}, false
		}
		prev, known := n.ci[st.ProjectID]
		n.ci[st.ProjectID] = st.State
		if st.State != "failing" || !known || prev == "failing" {
			return Notification{}, false
		}
		kind, todoID = model.NotifyBuild, st.TodoID
		build = &BuildRef{Branch: st.Branch, SHA: st.SHA}
		for _, r := range st.Runs {
			if r.Failed() {
				build.URL = r.URL
				break
			}
		}
		if todoID != "" {
			message = "Claude is investigating"
		}
	case model.EvCheckouts:
		cs, ok := ev.Data.([]model.Checkout)
		if !ok {
			return Notification{}, false
		}
		if message = n.newlyStuck(ev.ProjectID, cs); message == "" {
			return Notification{}, false
		}
		kind = model.NotifyBehind
	default:
		return Notification{}, false
	}
	note, ok := n.build(kind, ev.ProjectID, todoID)
	if !ok {
		return note, false
	}
	note.Message, note.Permission, note.Build = message, permRef, build
	return note, true
}

// newlyStuck notes which runners' checkouts of project pid are left behind
// origin now, and describes those that weren't before and haven't notified
// within BehindEvery ("" if none).
func (n *Notifier) newlyStuck(pid string, cs []model.Checkout) string {
	every := n.BehindEvery
	if every <= 0 {
		every = DefaultBehindEvery
	}
	now := n.now()
	stuckNow := map[[2]string]bool{}
	var lines []string
	for _, c := range cs {
		if c.Stuck == "" {
			continue
		}
		k := [2]string{pid, c.Runner}
		stuckNow[k] = true
		if n.stuck[k] {
			continue
		}
		n.stuck[k] = true
		if at, ok := n.told[k]; ok && now.Sub(at) < every {
			continue
		}
		n.told[k] = now
		who := c.RunnerName
		if who == "" {
			who = "The checkout"
		}
		s := "s"
		if c.Behind == 1 {
			s = ""
		}
		lines = append(lines, fmt.Sprintf("%s: %s is %d commit%s behind origin (%s)", who, c.Branch, c.Behind, s, c.Stuck))
	}
	for k := range n.stuck {
		if k[0] == pid && !stuckNow[k] {
			delete(n.stuck, k)
		}
	}
	return strings.Join(lines, "; ")
}

// build fills in a notification of kind about a project and (optionally)
// one of its tasks.
func (n *Notifier) build(kind model.NotifyEvent, projectID, todoID string) (Notification, bool) {
	note := Notification{ID: newEventID(), Type: kind, Time: n.now().UTC().Truncate(time.Second)}
	if todoID != "" {
		if t, err := n.Store.GetTodo(todoID); err == nil {
			projectID = t.ProjectID
			note.Task = &TaskRef{ID: t.ID, Title: TaskTitle(t), Status: t.Status, URL: n.taskURL(t.ProjectID, t.ID)}
		}
	}
	if projectID == "" {
		return note, false
	}
	p, err := n.Store.GetProject(projectID)
	if err != nil {
		return note, false
	}
	note.Project = ProjectRef{ID: p.ID, Name: p.Name}
	return note, true
}

func (n *Notifier) origin(projectID string) string {
	if n.Origin == nil {
		return ""
	}
	return strings.TrimRight(n.Origin(projectID), "/")
}

// taskURL opens the task in Dash's web app.
func (n *Notifier) taskURL(projectID, todoID string) string {
	o := n.origin(projectID)
	if o == "" {
		return ""
	}
	return o + "/?" + url.Values{"project": {projectID}, "task": {todoID}}.Encode()
}

// TaskTitle is how a task is named: its title, else its prompt's first line.
func TaskTitle(t model.Todo) string {
	if t.Title != "" {
		return t.Title
	}
	return oneLine(t.Prompt, 120)
}

func newEventID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "evt_" + hex.EncodeToString(b)
}

// Test sends a test notification to w once, now, and reports how it went.
func (n *Notifier) Test(ctx context.Context, w model.Webhook) error {
	n.init()
	note, ok := n.build(model.NotifyTest, w.ProjectID, "")
	if !ok {
		return errors.New("the webhook's project is gone")
	}
	note.Message = "This is a test from Dash: this webhook works."
	note.Task = &TaskRef{ID: "test", Title: "Test notification", Status: model.StatusReview, URL: n.origin(w.ProjectID)}
	if note.Task.URL != "" {
		note.Task.URL += "/?" + url.Values{"project": {w.ProjectID}}.Encode()
	}
	return n.deliver(ctx, w, note, 1)
}

// errPermanent marks a failure that trying again won't fix (4xx, a blocked
// address, a bad URL).
type errPermanent struct{ error }

func (e errPermanent) Unwrap() error { return e.error }

// deliver sends note to w, trying up to attempts times on network errors,
// 5xx and 429, with exponential backoff, and records the outcome on w.
// Errors never carry the URL (Slack's and ntfy's URLs are secrets).
func (n *Notifier) deliver(ctx context.Context, w model.Webhook, note Notification, attempts int) error {
	var err error
retry:
	for i := 0; i < attempts; i++ {
		if i > 0 {
			t := time.NewTimer(n.backoff() << (i - 1))
			select {
			case <-ctx.Done():
				t.Stop()
				err = ctx.Err()
				break retry
			case <-t.C:
			}
		}
		err = n.send(ctx, w, note)
		var pe errPermanent
		if err == nil || errors.As(err, &pe) {
			break
		}
	}
	msg := ""
	if err != nil {
		msg = err.Error()
		host := ""
		if u, perr := url.Parse(w.URL); perr == nil {
			host = u.Hostname()
		}
		slog.Warn("webhook delivery failed", "webhook", w.ID, "project", w.ProjectID, "event", note.Type, "host", host, "err", msg)
	}
	if rerr := n.Store.RecordWebhookDelivery(w.ID, note.Type, n.now(), msg); rerr != nil {
		slog.Error("notifications: record delivery", "webhook", w.ID, "err", rerr)
	}
	return err
}

// send makes one attempt.
func (n *Notifier) send(ctx context.Context, w model.Webhook, note Notification) error {
	var actions []ntfyAction
	if w.Format == model.WebhookNtfy && note.Permission != nil && n.Actions != nil {
		if o := n.origin(note.Project.ID); o != "" {
			actions = n.Actions.buttons(o, note.Permission.ID, n.now())
		}
	}
	req, err := request(ctx, w, note, n.now(), actions)
	if err != nil {
		return errPermanent{err}
	}
	resp, err := n.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the URL
		}
		if errors.Is(err, ErrPrivateAddress) {
			return errPermanent{err}
		}
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode >= 500 || resp.StatusCode == 429:
		return fmt.Errorf("the server answered %s", resp.Status)
	default:
		return errPermanent{fmt.Errorf("the server answered %s", resp.Status)}
	}
}
