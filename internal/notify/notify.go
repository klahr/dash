// Package notify sends Dash's events to outgoing webhooks (generic signed
// JSON, Slack incoming webhooks and ntfy topics) and, by Web Push, to the
// browsers that turned notifications on. It watches the event hub
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

	"github.com/klahr/dash/internal/applink"
	"github.com/klahr/dash/internal/ci"
	"github.com/klahr/dash/internal/metrics"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/textutil"
)

// Store is what the notifier reads: webhooks, and the records events name.
type Store interface {
	// WebhooksFor lists the webhooks a project's events go to: its own,
	// and the personal webhooks of its org's members.
	WebhooksFor(projectID string) ([]model.Webhook, error)
	GetProject(id string) (model.Project, error)
	GetTodo(id string) (model.Todo, error)
	ListTodos(projectID string, statuses ...model.TodoStatus) []model.Todo
	RecordWebhookDelivery(d model.WebhookDelivery) error
}

// goalLister is a Store that has goals, whose statuses Start remembers.
type goalLister interface {
	ListGoals(projectID string) []model.Goal
}

// deployLister is a Store that has deploys: Start remembers those not
// done yet, so one that fails after a restart still notifies.
type deployLister interface {
	ListDeploys(projectID, env string) []model.Deploy
}

// activityRecorder is a Store that keeps what the summaries tell about.
type activityRecorder interface {
	RecordActivity(a model.Activity) error
}

// Defaults.
const (
	DefaultTimeout  = 10 * time.Second
	DefaultAttempts = 4
	DefaultBackoff  = 2 * time.Second
	// DefaultBehindEvery limits checkout.behind: a checkout that keeps
	// getting stuck and unstuck notifies at most this often.
	DefaultBehindEvery = 6 * time.Hour
	// DefaultDiskEvery limits runner.disk the same way, per runner.
	DefaultDiskEvery = 6 * time.Hour
	queueSize        = 1024
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
	// permission requests (needs Origin), and to pushed ones.
	Actions *Actions
	// Push and VAPID, both set, push PushEvents to the browsers of the
	// project's org's members that turned notifications on (webpush.go).
	// Set VAPID before Start; SetVAPID replaces it later.
	Push    PushStore
	VAPID   *VAPIDKey
	vapidMu sync.RWMutex
	Now     func() time.Time

	once   sync.Once
	queue  chan model.Event
	client *http.Client

	// BehindEvery is how often, at most, one runner's checkout of a
	// project being left behind origin notifies (DefaultBehindEvery).
	BehindEvery time.Duration
	// DiskEvery is how often, at most, one runner's disk running low
	// notifies (DefaultDiskEvery).
	DiskEvery time.Duration

	// Seen by the run loop only.
	status map[string]model.TodoStatus // todo id → last status
	goals  map[string]model.GoalStatus // goal id → last status
	ci     map[string]string           // project id → last build state
	stuck  map[[2]string]bool          // {project, runner} → its checkout is left behind
	told   map[[2]string]time.Time     // {project, runner} → when that last notified
	// diskLow: runner id → its disk is low; diskTold: when that last notified.
	diskLow  map[string]bool
	diskTold map[string]time.Time
	// deploys: the deploys not done yet, by id, with their status.
	deploys map[string]model.DeployStatus

	wg sync.WaitGroup
}

func (n *Notifier) init() {
	n.once.Do(func() {
		n.queue = make(chan model.Event, queueSize)
		n.status = map[string]model.TodoStatus{}
		n.goals = map[string]model.GoalStatus{}
		n.ci = map[string]string{}
		n.stuck, n.told = map[[2]string]bool{}, map[[2]string]time.Time{}
		n.diskLow, n.diskTold = map[string]bool{}, map[string]time.Time{}
		n.deploys = map[string]model.DeployStatus{}
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
	case model.EvTodoUpdated, model.EvTodoDeleted, model.EvPermission, model.EvCI, model.EvCheckouts, model.EvRunnerDisk, model.EvGoalUpdated,
		model.EvGoalDeleted, model.EvDeployUpdated:
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
	if gs, ok := n.Store.(goalLister); ok {
		for _, g := range gs.ListGoals("") {
			n.goals[g.ID] = goalSeen(g)
		}
	}
	if ds, ok := n.Store.(deployLister); ok {
		for _, d := range ds.ListDeploys("", "") {
			if !d.Status.Done() {
				n.deploys[d.ID] = d.Status
			}
		}
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

// evFlush marks a place in the queue: the run loop closes the event's
// channel when it reaches it. Tests use it to wait for queued events.
const evFlush model.EventType = "notify.flush"

// handle turns one hub event into a notification, if it is one, and sends
// it to the webhooks that want it: its project's, and its org's members'
// personal ones. A URL gets it once, though a project's and someone's
// personal webhook both go there.
func (n *Notifier) handle(ctx context.Context, ev model.Event) {
	if ev.Type == model.EvRunnerDisk {
		n.runnerDisk(ctx, ev)
		return
	}
	if ev.Type == model.EvGoalUpdated {
		n.goal(ctx, ev)
		return
	}
	if ev.Type == model.EvGoalDeleted {
		if g, ok := ev.Data.(model.Goal); ok {
			delete(n.goals, g.ID)
		}
		return
	}
	if t, ok := ev.Data.(model.Todo); ok && ev.Type == model.EvTodoUpdated {
		if _, known := n.status[t.ID]; !known && t.Finding != nil && t.Status == model.StatusDraft {
			n.issue(ctx, t) // classify, below, remembers the task
		}
	}
	if ev.Type == model.EvDeployUpdated {
		n.deploy(ctx, ev)
		return
	}
	note, ok := n.classify(ev)
	if !ok {
		return
	}
	news := n.taskPlanning(&note)
	n.record(note, 0)
	n.push(ctx, note)
	n.toWebhooks(ctx, note, []string{note.Project.ID})
	for _, cn := range news {
		n.pushTo(ctx, cn.note, cn.org, cn.users)
	}
}

// toWebhooks sends note to the webhooks of the projects pids, each URL once.
func (n *Notifier) toWebhooks(ctx context.Context, note Notification, pids []string) {
	var hooks []model.Webhook
	for _, pid := range pids {
		hs, err := n.Store.WebhooksFor(pid)
		if err != nil {
			slog.Error("notifications: list webhooks", "project", pid, "err", err)
			continue
		}
		hooks = append(hooks, hs...)
	}
	sent := map[string]bool{}
	for _, w := range hooks {
		if !w.Wants(note.Type) || sent[w.URL] {
			continue
		}
		sent[w.URL] = true
		n.wg.Add(1)
		go func(w model.Webhook) {
			defer n.wg.Done()
			_ = n.deliver(ctx, w, note, n.attempts())
		}(w)
	}
}

// runnerDisk notifies, once per crossing, that a runner's free disk space
// fell below its threshold: the webhooks of the projects linked to it and
// the browsers of its org's members. A runner that keeps crossing it
// notifies at most every DiskEvery (and once after the hub restarts).
func (n *Notifier) runnerDisk(ctx context.Context, ev model.Event) {
	rd, ok := ev.Data.(model.RunnerDisk)
	if !ok {
		return
	}
	if !rd.Low {
		delete(n.diskLow, rd.RunnerID)
		return
	}
	if n.diskLow[rd.RunnerID] {
		return // told already
	}
	n.diskLow[rd.RunnerID] = true
	every := n.DiskEvery
	if every <= 0 {
		every = DefaultDiskEvery
	}
	now := n.now()
	if at, ok := n.diskTold[rd.RunnerID]; ok && now.Sub(at) < every {
		return
	}
	n.diskTold[rd.RunnerID] = now
	note := Notification{ID: newEventID(), Type: model.NotifyDisk, Time: now.UTC().Truncate(time.Second),
		Runner: &RunnerRef{ID: rd.RunnerID, Name: rd.RunnerName}, Message: rd.Message}
	if len(rd.Projects) > 0 {
		if p, err := n.Store.GetProject(rd.Projects[0]); err == nil {
			note.Project = projectRef(p)
		}
	}
	n.pushOrg(ctx, note, rd.OrgID)
	n.toWebhooks(ctx, note, rd.Projects)
}

// goalBlocked stands for a blocked goal (model.Goal.IsBlocked) among the
// statuses the Notifier remembers.
const goalBlocked model.GoalStatus = "blocked"

// goalSeen is g's status as the Notifier remembers it: goalBlocked for a
// goal whose coordinator can't run.
func goalSeen(g model.Goal) model.GoalStatus {
	if g.IsBlocked() {
		return goalBlocked
	}
	return g.Status
}

// goal notifies that a goal started waiting for its user, is done, ran
// out of budget or is blocked (its coordinator can't run): each time its
// status changes to one of those.
func (n *Notifier) goal(ctx context.Context, ev model.Event) {
	g, ok := ev.Data.(model.Goal)
	if !ok {
		return
	}
	seen := goalSeen(g)
	prev, known := n.goals[g.ID]
	n.goals[g.ID] = seen
	if prev == seen || (!known && seen != goalBlocked) {
		return // a new goal is first seen planning
	}
	switch seen {
	case model.GoalWaiting, model.GoalDone, model.GoalOutOfBudget, goalBlocked:
	default:
		return
	}
	note, ok := n.build(model.NotifyGoal, g.ProjectID, "")
	if !ok {
		return
	}
	note.Goal = &GoalRef{ID: g.ID, Title: g.Title, Status: g.Status, URL: n.goalURL(g.ProjectID, g.ID)}
	note.Message = g.Note
	if seen == goalBlocked {
		note.Goal.Blocked, note.Message = g.Blocked, g.Blocked
	}
	n.record(note, g.Owner)
	n.push(ctx, note)
	n.toWebhooks(ctx, note, []string{g.ProjectID})
}

// issue notifies of a draft a scan or Claude (report_issue) added for an
// issue it reported: a task it hadn't seen, so a report that bumps a task
// that has the issue already doesn't notify, nor does a draft for an issue
// added by hand.
func (n *Notifier) issue(ctx context.Context, t model.Todo) {
	var is *model.Issue
	for i := range t.Finding.Issues {
		if src := t.Finding.Issues[i].Source; src != "" && src != "manual" {
			is = &t.Finding.Issues[i]
			break
		}
	}
	if is == nil {
		return
	}
	note, ok := n.build(model.NotifyIssue, t.ProjectID, "")
	if !ok {
		return
	}
	note.Issue = &IssueRef{ID: t.ID, Title: TaskTitle(t), Severity: t.Finding.Severity(), URL: n.taskURL(t.ProjectID, t.ID)}
	if loc := is.Location(); loc != "" {
		note.Message = loc
	}
	n.record(note, 0)
	n.push(ctx, note)
	n.toWebhooks(ctx, note, []string{t.ProjectID})
}

// deploy notifies of a deploy that failed: one seen on its way (queued or
// running) that is failed now, unless someone cancelled it.
func (n *Notifier) deploy(ctx context.Context, ev model.Event) {
	d, ok := ev.Data.(model.Deploy)
	if !ok || d.ID == "" {
		return
	}
	_, known := n.deploys[d.ID]
	if !d.Status.Done() {
		n.deploys[d.ID] = d.Status
		return
	}
	delete(n.deploys, d.ID)
	if !known || d.Status != model.DeployFailed || d.Cancelled {
		return
	}
	note, ok := n.build(model.NotifyDeploy, d.ProjectID, "")
	if !ok {
		return
	}
	note.Deploy = &DeployRef{ID: d.ID, Environment: d.Environment, Commit: d.Commit, Rollback: d.Rollback, URL: n.deployURL(d.ProjectID, d.ID)}
	note.Message = d.Error
	n.record(note, 0)
	n.push(ctx, note)
	n.toWebhooks(ctx, note, []string{d.ProjectID})
}

// record keeps note for the summaries, if it is something they tell
// about. user is who it is for (a goal's owner; 0: everyone).
func (n *Notifier) record(note Notification, user int64) {
	rec, ok := n.Store.(activityRecorder)
	if !ok {
		return
	}
	a := model.Activity{ProjectID: note.Project.ID, At: note.Time, User: user}
	switch note.Type {
	case model.NotifyIssue:
		a.Kind, a.Ref, a.Title, a.URL, a.Severity = model.SummaryIssue, note.Issue.ID, note.Issue.Title, note.Issue.URL, note.Issue.Severity
	case model.NotifyReview, model.NotifyFailed, model.NotifyMerged:
		if note.Task == nil {
			return
		}
		a.Kind = map[model.NotifyEvent]string{model.NotifyReview: model.SummaryReview, model.NotifyFailed: model.SummaryFailed, model.NotifyMerged: model.SummaryMerged}[note.Type]
		a.Ref, a.Title, a.URL, a.Creator = note.Task.ID, note.Task.Title, note.Task.URL, max(note.creator, 0)
		if note.byPlanner {
			// A planner's task is its owners' (one item each; the summary
			// shows it once), or everyone's if it has none.
			a.Creator = 0
			for _, o := range note.owners {
				a.Creator = o
				if err := rec.RecordActivity(a); err != nil {
					slog.Error("notifications: record activity", "project", a.ProjectID, "kind", a.Kind, "err", err)
				}
			}
			if len(note.owners) > 0 {
				return
			}
		}
	case model.NotifyGoal:
		if note.Goal == nil || (note.Goal.Status != model.GoalWaiting && note.Goal.Blocked == "") {
			return
		}
		a.Kind, a.Ref, a.Title, a.URL = model.SummaryGoal, note.Goal.ID, note.Goal.Title, note.Goal.URL
	case model.NotifyBuild:
		a.Kind, a.Title = model.SummaryBuild, "Build failing"
		if note.Build != nil {
			a.Ref, a.URL = note.Build.SHA, note.Build.URL
			if note.Build.Branch != "" {
				a.Title += " on " + note.Build.Branch
			}
		}
	default:
		return
	}
	if a.ProjectID == "" {
		return
	}
	if err := rec.RecordActivity(a); err != nil {
		slog.Error("notifications: record activity", "project", a.ProjectID, "kind", a.Kind, "err", err)
	}
}

// classify reports whether ev is a moment to notify of, as the web app
// decides it (web/static/app/events.js onEvent): a task's status changing to
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
			if a := t.AutoApproval; a != nil && !a.Approved && a.Failed != "" {
				message = "Not approved automatically: " + a.Failed
			}
		case model.StatusFailed:
			kind, message = model.NotifyFailed, t.Error
		case model.StatusMerged:
			kind = model.NotifyMerged
			if t.NoChanges && t.MergedCommit == "" {
				message = ClosedNoChanges
			} else if a := t.AutoApproval; a != nil && a.Approved {
				message = AutoApproved
			}
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
		if r.Tool == perm.AskTool {
			message = "Claude asks you a question"
			if qs := perm.Questions(r.Input); len(qs) > 0 && qs[0].Question != "" {
				message = "Claude asks: " + textutil.OneLine(qs[0].Question, 200)
			}
		}
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
		lines = append(lines, fmt.Sprintf("%s: %s is %s behind origin (%s)", who, c.Branch, textutil.Count(c.Behind, "commit", "commits"), c.Stuck))
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
			note.creator = t.CreatedBy
		}
	}
	if projectID == "" {
		return note, false
	}
	p, err := n.Store.GetProject(projectID)
	if err != nil {
		return note, false
	}
	note.Project = projectRef(p)
	return note, true
}

func (n *Notifier) origin(projectID string) string {
	if n.Origin == nil {
		return ""
	}
	return strings.TrimRight(n.Origin(projectID), "/")
}

// goalURL opens the goal in Dash's web app.
func (n *Notifier) goalURL(projectID, goalID string) string {
	o := n.origin(projectID)
	if o == "" {
		return ""
	}
	return o + applink.Goal(projectID, goalID)
}

// deployURL opens the deploy on the project's Deploys page.
func (n *Notifier) deployURL(projectID, deployID string) string {
	o := n.origin(projectID)
	if o == "" {
		return ""
	}
	return o + applink.Deploy(projectID, deployID)
}

// taskURL opens the task in Dash's web app.
func (n *Notifier) taskURL(projectID, todoID string) string {
	o := n.origin(projectID)
	if o == "" {
		return ""
	}
	return o + applink.Task(projectID, todoID)
}

// TaskTitle is how a task is named: its title, else its prompt's first line.
func TaskTitle(t model.Todo) string { return t.Name(120) }

func newEventID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "evt_" + hex.EncodeToString(b)
}

// Test sends a test notification to w once, now, and reports how it went.
// A personal webhook's test is about no project.
func (n *Notifier) Test(ctx context.Context, w model.Webhook) error {
	n.init()
	note, ok := n.build(model.NotifyTest, w.ProjectID, "")
	if w.Personal() {
		note.Project, ok = ProjectRef{Name: "Personal webhook"}, true
	}
	if !ok {
		return errors.New("the webhook's project is gone")
	}
	note.Message = "This is a test from Dash: this webhook works."
	note.Task = &TaskRef{ID: "test", Title: "Test notification", Status: model.StatusReview, URL: n.origin(w.ProjectID)}
	if note.Task.URL != "" {
		if w.ProjectID != "" {
			note.Task.URL += applink.Project(w.ProjectID)
		} else {
			note.Task.URL += "/"
		}
	}
	return n.deliver(ctx, w, note, 1)
}

// errPermanent marks a failure that trying again won't fix (4xx, a blocked
// address, a bad URL).
type errPermanent struct{ error }

func (e errPermanent) Unwrap() error { return e.error }

// deliver sends note to w, trying up to attempts times on network errors,
// 5xx and 429, with exponential backoff, and records the outcome in w's
// history. Errors never carry the URL (Slack's and ntfy's URLs are secrets).
// errUnreadableSecret is sending to a webhook whose signing secret the
// store couldn't open: never unsigned.
var errUnreadableSecret = errors.New("this webhook's signing secret can't be read (was the hub's secret key changed?): rotate the secret, or delete the webhook and add it again")

func (n *Notifier) deliver(ctx context.Context, w model.Webhook, note Notification, attempts int) error {
	d := model.WebhookDelivery{WebhookID: w.ID, Event: note.Type, Delivery: note.ID,
		ProjectID: note.Project.ID, Project: note.Project.Name}
	if note.Task != nil && note.Type != model.NotifyTest {
		d.Subject = note.Task.Title
	}
	if note.Issue != nil {
		d.Subject = note.Issue.Title
	}
	if note.Deploy != nil {
		d.Subject = note.subject()
	}
	if note.Summary != nil {
		d.Subject = note.Message
	}
	start := time.Now()
	var err error
	if w.Unreadable { // it didn't open with the hub's secret key (store.scanWebhook)
		err = errUnreadableSecret
	} else {
		err = n.retry(ctx, attempts, func() error {
			d.Attempts++
			var err error
			d.Status, err = n.send(ctx, w, note)
			return err
		})
	}
	d.DurationMS = time.Since(start).Milliseconds()
	msg := ""
	if err == nil {
		metrics.WebhookDeliveries.Inc("ok")
	} else {
		metrics.WebhookDeliveries.Inc("failed")
		msg = err.Error()
		host := ""
		if u, perr := url.Parse(w.URL); perr == nil {
			host = u.Hostname()
		}
		slog.Warn("webhook delivery failed", "webhook", w.ID, "project", note.Project.ID, "event", note.Type, "host", host, "err", msg)
	}
	d.At, d.Error = n.now(), msg
	if rerr := n.Store.RecordWebhookDelivery(d); rerr != nil {
		slog.Error("notifications: record delivery", "webhook", w.ID, "err", rerr)
	}
	return err
}

// retry calls try up to attempts times until it succeeds or fails for
// good (errPermanent), waiting n.backoff() before the 2nd call and twice
// as long before each next one.
func (n *Notifier) retry(ctx context.Context, attempts int, try func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			t := time.NewTimer(n.backoff() << (i - 1))
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		err = try()
		var pe errPermanent
		if err == nil || errors.As(err, &pe) {
			break
		}
	}
	return err
}

// send makes one attempt, and answers the HTTP status (0: none).
func (n *Notifier) send(ctx context.Context, w model.Webhook, note Notification) (int, error) {
	var actions []ntfyAction
	if w.Format == model.WebhookNtfy && note.Permission != nil && !note.Permission.Question() && n.Actions != nil {
		if o := n.origin(note.Project.ID); o != "" {
			actions = n.Actions.buttons(o, note.Permission.ID, n.now())
		}
	}
	req, err := request(ctx, w, note, n.now(), actions)
	if err != nil {
		return 0, errPermanent{err}
	}
	resp, err := n.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the URL
		}
		if errors.Is(err, ErrPrivateAddress) {
			return 0, errPermanent{err}
		}
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	code := resp.StatusCode
	switch {
	case code >= 200 && code < 300:
		return code, nil
	case code >= 500 || code == 429:
		return code, fmt.Errorf("the server answered %s", resp.Status)
	default:
		return code, errPermanent{fmt.Errorf("the server answered %s", resp.Status)}
	}
}
