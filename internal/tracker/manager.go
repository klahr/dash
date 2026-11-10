package tracker

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// DefaultPollEvery is how often a worker polls its tracker for changes
// when no webhook tells it sooner.
const DefaultPollEvery = time.Minute

// Manager is the sync engine: one worker per project whose board is an
// external tracker, each with its own backend instance, credentials and
// state. Starting, stopping or reconfiguring one project's worker never
// touches another's. Set its fields, then Start it.
type Manager struct {
	Store *store.Store
	// Kick nudges the queue after the tracker queued a todo (optional).
	Kick func()
	// PollEvery is how often workers poll their trackers (DefaultPollEvery).
	PollEvery time.Duration
	Now       func() time.Time

	// mu guards the fields below. It is only held for map operations, never
	// while calling the store: Observe is called with the store's lock held.
	mu      sync.Mutex
	ctx     context.Context
	workers map[string]*worker
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) pollEvery() time.Duration {
	if m.PollEvery > 0 {
		return m.PollEvery
	}
	return DefaultPollEvery
}

// Start starts a worker for every project with an external backend; they
// stop when ctx ends.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	cfgs, err := m.Store.TrackerConfigs()
	if err != nil {
		return err
	}
	for _, cfg := range cfgs {
		if err := m.start(cfg); err != nil {
			slog.Warn("board backend", "project", cfg.ProjectID, "backend", cfg.Backend, "err", err)
			_ = m.Store.SetTrackerSync(cfg.ProjectID, m.now(), time.Time{}, err.Error())
		}
	}
	return nil
}

// start runs a worker for cfg's project (not native).
func (m *Manager) start(cfg ProjectConfig) error {
	be, err := New(cfg)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.workers == nil {
		m.workers = map[string]*worker{}
	}
	if _, ok := m.workers[cfg.ProjectID]; ok {
		return fmt.Errorf("project %s already has a sync worker", cfg.ProjectID)
	}
	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	w := newWorker(m, cfg, be, cancel)
	m.workers[cfg.ProjectID] = w
	go w.run(ctx)
	return nil
}

// Stop stops a project's worker, if it has one, and waits for it.
func (m *Manager) Stop(projectID string) {
	m.mu.Lock()
	w := m.workers[projectID]
	delete(m.workers, projectID)
	m.mu.Unlock()
	if w != nil {
		w.cancel()
		<-w.done
	}
}

// StopAll stops every worker and waits for them.
func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.workers))
	for id := range m.workers {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Stop(id)
	}
}

// Reconfigure restarts a project's worker with its stored config (or stops
// it, for a native board). Other projects' workers carry on.
func (m *Manager) Reconfigure(projectID string) error {
	m.Stop(projectID)
	cfg, err := m.Store.TrackerConfig(projectID)
	if err != nil {
		return err
	}
	if cfg.Native() {
		return nil
	}
	if err := m.start(cfg); err != nil {
		_ = m.Store.SetTrackerSync(projectID, m.now(), time.Time{}, err.Error())
		return err
	}
	return nil
}

func (m *Manager) worker(projectID string) *worker {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workers[projectID]
}

// Running reports whether a project has a sync worker.
func (m *Manager) Running(projectID string) bool { return m.worker(projectID) != nil }

// Observe is fed the store's events. It never blocks (the store publishes
// holding its lock): it marks the todo for its project's worker, if any.
// Native projects cost a map lookup.
func (m *Manager) Observe(ev model.Event) {
	switch ev.Type {
	case model.EvTodoUpdated, model.EvTodoDeleted:
		if w := m.worker(ev.ProjectID); w != nil && ev.TodoID != "" {
			w.markDirty(ev.TodoID)
		}
	case model.EvProjectDeleted:
		if m.worker(ev.ProjectID) != nil {
			go m.Stop(ev.ProjectID)
		}
	}
}

// ErrNoSync is returned for a project that has no sync worker (its board
// is Dash's own, or its backend failed to start).
var ErrNoSync = errors.New("this project's board isn't synced with a tracker")

// SyncNow runs a full sync of a project (poll, then push) and waits for it.
func (m *Manager) SyncNow(ctx context.Context, projectID string) error {
	w := m.worker(projectID)
	if w == nil {
		return ErrNoSync
	}
	reply := make(chan error, 1)
	select {
	case w.reqs <- reply:
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return ErrNoSync
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return ErrNoSync
	}
}

// ErrWebhookRefused is returned for a webhook call that failed verification.
var ErrWebhookRefused = errors.New("webhook refused")

// Webhook handles a tracker's webhook call for a project: verified with the
// project's own secret (by the backend, or else as the X-Dash-Tracker-Secret
// header or ?secret= parameter), it makes the worker poll at once.
func (m *Manager) Webhook(projectID string, r *http.Request, body []byte) error {
	w := m.worker(projectID)
	if w == nil {
		return ErrNoSync
	}
	secret := w.cfg.WebhookSecret
	if secret == "" {
		return ErrWebhookRefused
	}
	if wh, ok := w.be.(Webhooker); ok {
		if _, err := wh.VerifyWebhook(r, body, secret); err != nil {
			return fmt.Errorf("%w: %v", ErrWebhookRefused, err)
		}
	} else {
		given := r.Header.Get("X-Dash-Tracker-Secret")
		if given == "" {
			given = r.URL.Query().Get("secret")
		}
		if subtle.ConstantTimeCompare([]byte(given), []byte(secret)) != 1 {
			return ErrWebhookRefused
		}
	}
	w.pollNow()
	return nil
}

// Test checks a config without saving it: it builds the backend and tries
// it (Tester, or a List), and returns its columns when it can list them.
func (m *Manager) Test(ctx context.Context, cfg ProjectConfig) ([]Column, error) {
	if cfg.Native() {
		return nil, nil
	}
	be, err := New(cfg)
	if err != nil {
		return nil, err
	}
	if t, ok := be.(Tester); ok {
		err = t.Test(ctx)
	} else {
		_, err = be.List(ctx, m.now())
	}
	if err != nil {
		return nil, err
	}
	if cl, ok := be.(ColumnLister); ok {
		return cl.Columns(ctx)
	}
	return nil, nil
}

// ---- switching backends ----

// SwitchOptions are a person's decisions on moving a project's board.
type SwitchOptions struct {
	// Export: when the project moves to an external tracker, whether its
	// open todos (draft, queued, running, review, failed) become new cards
	// (true) or stay Dash-only (false). Leaving Dash's own board needs it.
	Export *bool
	// Confirm drops the links to the current external tracker (to go back
	// to Dash's board, or to another board): its cards stay as they are,
	// the todos stay in Dash as native todos.
	Confirm bool
	UserID  int64
}

// ErrNeedsDecision is returned when a switch needs a choice the caller
// didn't make (see SwitchOptions): Dash never switches silently.
type ErrNeedsDecision struct {
	Need      string // "export" or "confirm"
	OpenTodos int    // what export would export
	From, To  string // the backends
}

func (e ErrNeedsDecision) Error() string {
	if e.Need == "export" {
		return fmt.Sprintf("moving the board to %s: choose whether to export the %d open tasks", e.To, e.OpenTodos)
	}
	return fmt.Sprintf("moving the board off %s drops its links to the tracker's cards: confirm to go on", e.From)
}

// exportable are the statuses of todos a switch exports.
var exportable = []model.TodoStatus{model.StatusDraft, model.StatusQueued, model.StatusInProgress, model.StatusReview, model.StatusFailed}

// Apply saves a project's board backend and (re)starts its worker. A switch
// to another backend or board needs opts' decisions; it then drops the old
// links, marks the open todos for export if asked, and is logged as a
// project event. Other changes (mapping, credentials) only restart the
// project's worker. next.Credentials are saved as given: merge in the stored
// ones first to keep them.
func (m *Manager) Apply(ctx context.Context, projectID string, next ProjectConfig, opts SwitchOptions) (ProjectConfig, error) {
	p, err := m.Store.GetProject(projectID)
	if err != nil {
		return next, err
	}
	old, err := m.Store.TrackerConfig(projectID)
	if err != nil {
		return next, err
	}
	next.ProjectID = projectID
	if next.Backend == "" {
		next.Backend = Native
	}
	reg, ok := Lookup(next.Backend)
	if !ok {
		return next, fmt.Errorf("%w %q", ErrUnknownBackend, next.Backend)
	}
	if !next.Native() && p.HubProjectID != "" {
		return next, errors.New("this project's board is its hub's: set its backend on the hub")
	}
	switching := !SameBoard(old, next)
	open := len(m.Store.ListTodos(projectID, exportable...))
	if switching && !old.Native() && !opts.Confirm {
		return next, ErrNeedsDecision{Need: "confirm", OpenTodos: open, From: old.Backend, To: next.Backend}
	}
	if switching && old.Native() && !next.Native() && opts.Export == nil {
		return next, ErrNeedsDecision{Need: "export", OpenTodos: open, From: old.Backend, To: next.Backend}
	}
	export := switching && !next.Native() && opts.Export != nil && *opts.Export
	if next.Native() {
		next = ProjectConfig{ProjectID: projectID, Backend: Native}
	} else {
		if len(next.StatusMap) == 0 && reg.DefaultStatusMap != nil {
			next.StatusMap = reg.DefaultStatusMap
		}
		if next.WebhookSecret == "" {
			next.WebhookSecret = old.WebhookSecret
		}
		if next.WebhookSecret == "" {
			next.WebhookSecret = store.NewTrackerSecret()
		}
		next.SwitchedAt = old.SwitchedAt
		if switching {
			next.SwitchedAt = m.now()
		}
		if _, err := New(next); err != nil { // its settings must at least build
			return next, err
		}
	}
	m.Stop(projectID)
	if switching && !old.Native() {
		if err := m.Store.UnlinkTracker(projectID); err != nil {
			return next, err
		}
	}
	if err := m.Store.PutTrackerConfig(next); err != nil {
		return next, err
	}
	exported := 0
	if export {
		for _, t := range m.Store.ListTodos(projectID, exportable...) {
			if err := m.Store.PutTrackerLink(model.TrackerLink{TodoID: t.ID, ProjectID: projectID, Ref: Ref{Provider: next.Backend}, Pending: true}); err != nil {
				return next, err
			}
			exported++
		}
	}
	if switching {
		text := fmt.Sprintf("Board moved from %s to %s", label(old.Backend), label(next.Backend))
		switch {
		case !old.Native() && !next.Native():
			text += ": links to the old board's cards dropped"
		case !old.Native():
			text += ": links to its cards dropped, the tasks stay in Dash"
		}
		if !next.Native() {
			if export {
				text += fmt.Sprintf("; %d open tasks exported as new cards", exported)
			} else if open > 0 {
				text += fmt.Sprintf("; %d existing tasks left Dash-only", open)
			}
		}
		if _, err := m.Store.AddProjectEvent(model.ProjectEvent{ProjectID: projectID, Kind: "board.switched", Text: text, UserID: opts.UserID}); err != nil {
			slog.Warn("project event", "project", projectID, "err", err)
		}
		slog.Info("board backend switched", "project", projectID, "from", old.Backend, "to", next.Backend, "exported", exported)
	}
	if !next.Native() {
		if err := m.start(next); err != nil {
			return next, err
		}
	}
	return m.Store.TrackerConfig(projectID)
}

func label(name string) string {
	if r, ok := Lookup(name); ok && r.Label != "" {
		return r.Label
	}
	return name
}

// ---- the worker ----

type worker struct {
	m      *Manager
	cfg    ProjectConfig
	be     Backend
	cancel context.CancelFunc
	done   chan struct{}
	reqs   chan chan error
	wake   chan struct{} // dirty todos to push
	poll   chan struct{} // poll the tracker now (webhook)

	mu    sync.Mutex
	dirty map[string]bool

	// Owned by the run goroutine.
	links  map[string]*link  // todo id →
	byExt  map[string]string // card id → todo id
	loaded bool
	// fresh: the tracker was read in this flush or cycle already.
	fresh bool
}

func newWorker(m *Manager, cfg ProjectConfig, be Backend, cancel context.CancelFunc) *worker {
	return &worker{
		m: m, cfg: cfg, be: be, cancel: cancel,
		done: make(chan struct{}), reqs: make(chan chan error),
		wake: make(chan struct{}, 1), poll: make(chan struct{}, 1),
		dirty: map[string]bool{},
	}
}

func (w *worker) markDirty(id string) {
	w.mu.Lock()
	w.dirty[id] = true
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *worker) pollNow() {
	select {
	case w.poll <- struct{}{}:
	default:
	}
}

func (w *worker) takeDirty() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	ids := make([]string, 0, len(w.dirty))
	for id := range w.dirty {
		ids = append(ids, id)
	}
	clear(w.dirty)
	slices.Sort(ids)
	return ids
}

func (w *worker) run(ctx context.Context) {
	defer close(w.done)
	_ = w.cycle(ctx)
	tick := time.NewTicker(w.m.pollEvery())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
			w.flush(ctx)
		case <-w.poll:
			_ = w.cycle(ctx)
		case <-tick.C:
			_ = w.cycle(ctx)
		case reply := <-w.reqs:
			reply <- w.cycle(ctx)
		}
	}
}

// flush pushes the todos the store announced changes to.
func (w *worker) flush(ctx context.Context) {
	if err := w.load(); err != nil {
		return
	}
	w.fresh = false
	var errs error
	for _, id := range w.takeDirty() {
		if err := w.outbound(ctx, id); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("board sync: push", "project", w.cfg.ProjectID, "todo", id, "err", err)
			w.markLater(id) // retried on the next cycle
			errs = errors.Join(errs, err)
		}
	}
	if errs != nil || w.fresh { // a failure, or it read the tracker: note it
		w.record(errs)
	}
}

// markLater keeps a todo dirty for the next cycle without waking the loop.
func (w *worker) markLater(id string) {
	w.mu.Lock()
	w.dirty[id] = true
	w.mu.Unlock()
}

// cycle polls the tracker and applies its changes, then pushes every todo
// of the project that differs from what was last synced (catching any
// event dropped or failed before), and records how it went.
func (w *worker) cycle(ctx context.Context) error {
	err := w.load()
	if err == nil {
		var cursor time.Time
		cursor, err = w.pull(ctx)
		w.advance(cursor)
		w.fresh = true
	}
	if err == nil || !errors.Is(err, errLoad) {
		w.takeDirty()
		ids := map[string]bool{}
		for _, t := range w.m.Store.ListTodos(w.cfg.ProjectID) {
			ids[t.ID] = true
		}
		for id := range w.links {
			ids[id] = true // deleted todos: their cards go too
		}
		sorted := make([]string, 0, len(ids))
		for id := range ids {
			sorted = append(sorted, id)
		}
		slices.Sort(sorted)
		for _, id := range sorted {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if perr := w.outbound(ctx, id); perr != nil {
				w.markLater(id)
				err = errors.Join(err, perr)
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	w.record(err)
	if err != nil {
		slog.Warn("board sync", "project", w.cfg.ProjectID, "backend", w.cfg.Backend, "err", err)
	}
	return err
}

// advance moves the cursor forward to where the tracker was read up to.
func (w *worker) advance(cursor time.Time) {
	if cursor.After(w.cfg.Cursor) {
		w.cfg.Cursor = cursor
	}
}

// record notes how the latest sync went, and the cursor.
func (w *worker) record(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	_ = w.m.Store.SetTrackerSync(w.cfg.ProjectID, w.m.now(), w.cfg.Cursor, msg)
}

var errLoad = errors.New("load the board's links")

// load reads the project's links once.
func (w *worker) load() error {
	if w.loaded {
		return nil
	}
	ls, err := w.m.Store.TrackerLinks(w.cfg.ProjectID)
	if err != nil {
		return fmt.Errorf("%w: %v", errLoad, err)
	}
	w.links, w.byExt = map[string]*link{}, map[string]string{}
	for _, l := range ls {
		w.put(newLink(l))
	}
	w.loaded = true
	return nil
}

func (w *worker) put(l *link) {
	w.links[l.TodoID] = l
	if l.Ref.ID != "" {
		w.byExt[l.Ref.ID] = l.TodoID
	}
}

func (w *worker) drop(todoID string) error {
	if l, ok := w.links[todoID]; ok {
		delete(w.byExt, l.Ref.ID)
		delete(w.links, todoID)
	}
	return w.m.Store.DeleteTrackerLink(todoID)
}

func (w *worker) save(l *link) error {
	l.SyncedAt = w.m.now()
	if err := w.m.Store.PutTrackerLink(l.persist()); err != nil {
		return err
	}
	w.put(l)
	return nil
}
