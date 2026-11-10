package tracker_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tracker"
	"github.com/klahr/dash/internal/tracker/trackertest"
)

// fwd hands the store's events to the manager made after the store,
// unless they are held back (then a sync's full pass finds the changes).
type fwd struct {
	m    *tracker.Manager
	held atomic.Bool
}

func (f *fwd) Publish(ev model.Event) {
	if f.m != nil && !f.held.Load() {
		f.m.Observe(ev)
	}
}

func (f *fwd) hold(on bool) { f.held.Store(on) }

type env struct {
	t     *testing.T
	f     *fwd
	st    *store.Store
	m     *tracker.Manager
	kicks int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	f := &fwd{}
	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), f)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, f: f, st: st}
	e.m = &tracker.Manager{Store: st, PollEvery: time.Hour, Kick: func() { e.kicks++ }}
	f.m = e.m
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		for _, p := range st.ListProjects() {
			e.m.Stop(p.ID)
		}
		st.Close()
	})
	return e
}

func (e *env) project(name string) model.Project {
	e.t.Helper()
	p, err := e.st.CreateProject(model.Project{Name: name, Path: "/tmp/" + name, DefaultBranch: "main"})
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) todo(pid, title string) model.Todo {
	e.t.Helper()
	td, err := e.st.CreateTodo(model.Todo{ProjectID: pid, Title: title, Prompt: title + " prompt"})
	if err != nil {
		e.t.Fatal(err)
	}
	return td
}

func (e *env) get(id string) model.Todo {
	e.t.Helper()
	td, err := e.st.GetTodo(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return td
}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

func boardCfg(backend, board string) tracker.ProjectConfig {
	raw, _ := json.Marshal(trackertest.Config{Board: board})
	return tracker.ProjectConfig{Backend: backend, Config: raw}
}

// connect moves pid's board to board on backend (nothing exported).
func (e *env) connect(pid, backend, board string, creds map[string]string) {
	e.t.Helper()
	cfg := boardCfg(backend, board)
	cfg.Credentials = creds
	if _, err := e.m.Apply(context.Background(), pid, cfg, tracker.SwitchOptions{Export: no(), Confirm: true}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) sync(pid string) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.m.SyncNow(ctx, pid); err != nil {
		e.t.Fatalf("sync %s: %v", pid, err)
	}
}

func (e *env) setStatus(id string, s model.TodoStatus) model.Todo {
	e.t.Helper()
	td, err := e.st.SetStatus(id, s, "")
	if err != nil {
		e.t.Fatal(err)
	}
	return td
}

func todoByRef(st *store.Store, pid, cardID string) (model.Todo, bool) {
	for _, td := range st.ListTodos(pid) {
		if td.ExternalRef != nil && td.ExternalRef.ID == cardID {
			return td, true
		}
	}
	return model.Todo{}, false
}

func TestInboundCreateAndUpdate(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	e := newEnv(t)
	p := e.project("web")
	e.connect(p.ID, "fake", "b1", nil)

	prio := 2
	draft := fake.AddCard("b1", tracker.Item{Title: "Fix login", Description: "The login form breaks", Column: "Backlog", Priority: &prio})
	queued := fake.AddCard("b1", tracker.Item{Title: "Ship it", Description: "Now", Column: "Todo"})
	fake.AddCard("b1", tracker.Item{Title: "Somebody's work", Column: "Review"})      // not a column Dash starts work from
	fake.AddCard("b1", tracker.Item{Title: "Elsewhere", Column: "Some other column"}) // unmapped
	e.sync(p.ID)

	todos := e.st.ListTodos(p.ID)
	if len(todos) != 2 {
		t.Fatalf("%d todos, want the 2 cards in draft and queued columns: %+v", len(todos), todos)
	}
	d, ok := todoByRef(e.st, p.ID, draft)
	if !ok || d.Title != "Fix login" || d.Prompt != "The login form breaks" || d.Status != model.StatusDraft || d.Priority == nil || *d.Priority != 2 {
		t.Fatalf("draft card's todo: %+v", d)
	}
	if d.ExternalRef.Provider != "fake" || d.ExternalRef.URL == "" {
		t.Fatalf("ref %+v", d.ExternalRef)
	}
	q, ok := todoByRef(e.st, p.ID, queued)
	if !ok || q.Status != model.StatusQueued || e.kicks == 0 {
		t.Fatalf("queued card's todo: %+v (kicks %d)", q, e.kicks)
	}

	fake.ResetCalls()
	fake.Edit("b1", draft, func(it *tracker.Item) { it.Title = "Fix the login form"; it.Description = "Details" })
	e.sync(p.ID)
	d = e.get(d.ID)
	if d.Title != "Fix the login form" || d.Prompt != "Details" {
		t.Fatalf("after the card's edit: %+v", d)
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Fatalf("applying the card's edit was echoed back: %v", calls)
	}
}

func TestOutboundCreateStatusAndComments(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	e := newEnv(t)
	p := e.project("web")
	e.connect(p.ID, "fake", "b1", nil)

	td := e.todo(p.ID, "Add search")
	e.sync(p.ID)
	td = e.get(td.ID)
	if td.ExternalRef == nil {
		t.Fatal("a todo made after the switch got no card")
	}
	card, _ := fake.Card("b1", td.ExternalRef.ID)
	if card.Title != "Add search" || card.Description != "Add search prompt" || card.Column != "Backlog" {
		t.Fatalf("card %+v", card)
	}

	e.setStatus(td.ID, model.StatusQueued)
	e.setStatus(td.ID, model.StatusInProgress)
	if _, err := e.st.UpdateTodo(td.ID, func(x *model.Todo) error {
		x.Status, x.Summary = model.StatusReview, "Added a search box"
		x.PR = &model.PullRequest{Number: 7, URL: "https://github.com/x/web/pull/7"}
		x.CostUSD, x.SessionID = 1.5, "sess" // execution state: never sent
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.sync(p.ID)
	card, _ = fake.Card("b1", td.ExternalRef.ID)
	if card.Column != "Review" {
		t.Fatalf("card in %q, want Review", card.Column)
	}
	if len(card.Comments) != 1 || !strings.Contains(card.Comments[0].Body, "Added a search box") || !strings.Contains(card.Comments[0].Body, "/pull/7") {
		t.Fatalf("comments %+v", card.Comments)
	}

	e.setStatus(td.ID, model.StatusQueued)
	e.setStatus(td.ID, model.StatusInProgress)
	if _, err := e.st.UpdateTodo(td.ID, func(x *model.Todo) error {
		x.Status, x.Error = model.StatusFailed, "tests failed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.sync(p.ID)
	card, _ = fake.Card("b1", td.ExternalRef.ID)
	if card.Column != "Failed" || !strings.Contains(card.Comments[len(card.Comments)-1].Body, "tests failed") {
		t.Fatalf("failed card %+v", card)
	}

	// Its own comments aren't read back into the todo's chat.
	if chat := e.st.TodoChat(td.ID); len(chat) != 0 {
		t.Fatalf("Dash's own comments came back: %+v", chat)
	}
	// A person's comment is.
	fake.AddComment("b1", td.ExternalRef.ID, "Ada", "Try the other index")
	e.sync(p.ID)
	if chat := e.st.TodoChat(td.ID); len(chat) != 1 || !strings.Contains(chat[0].Content, "Try the other index") || !strings.Contains(chat[0].Content, "Ada") {
		t.Fatalf("chat %+v", chat)
	}

	// Deleting the todo deletes its card.
	if _, err := e.st.DeleteTodo(td.ID); err != nil {
		t.Fatal(err)
	}
	e.sync(p.ID)
	if _, ok := fake.Card("b1", td.ExternalRef.ID); !ok || len(fake.Cards("b1")) != 0 {
		t.Fatalf("card not deleted: %+v", fake.Cards("b1"))
	}
}

func TestLoopPrevention(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	e := newEnv(t)
	p := e.project("web")
	e.connect(p.ID, "fake", "b1", nil)
	td := e.todo(p.ID, "One")
	e.sync(p.ID)
	if _, err := e.st.UpdateTodo(td.ID, func(x *model.Todo) error { x.Title = "One, renamed"; return nil }); err != nil {
		t.Fatal(err)
	}
	e.sync(p.ID)
	fake.ResetCalls()
	for range 3 {
		e.sync(p.ID)
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Fatalf("idle syncs made calls: %v", calls)
	}
	// Execution state changes push nothing.
	if _, err := e.st.UpdateTodo(td.ID, func(x *model.Todo) error {
		x.CostUSD, x.DiffAdd, x.Feedback = 2, 10, []string{"note"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.sync(p.ID)
	if calls := fake.Calls(); len(calls) != 0 {
		t.Fatalf("execution state was pushed: %v", calls)
	}
}

func TestConflictLastWriterWins(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	e := newEnv(t)
	p := e.project("web")
	e.connect(p.ID, "fake", "b1", nil)
	td := e.todo(p.ID, "Base")
	e.sync(p.ID)
	card := e.get(td.ID).ExternalRef.ID

	// Dash first, the tracker later: the tracker's title wins. (The
	// store's events are held back, so the engine sees both edits at once.)
	e.f.hold(true)
	if _, err := e.st.UpdateTodo(td.ID, func(x *model.Todo) error { x.Title = "Dash title"; return nil }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	fake.Edit("b1", card, func(it *tracker.Item) { it.Title = "Tracker title" })
	e.f.hold(false)
	e.sync(p.ID)
	if got := e.get(td.ID).Title; got != "Tracker title" {
		t.Fatalf("todo title %q, want the later tracker edit", got)
	}
	if c, _ := fake.Card("b1", card); c.Title != "Tracker title" {
		t.Fatalf("card title %q", c.Title)
	}

	// The tracker first, Dash later: Dash's wins, and only for that field.
	e.f.hold(true)
	fake.Edit("b1", card, func(it *tracker.Item) { it.Title = "Tracker again"; it.Description = "New body" })
	time.Sleep(2 * time.Millisecond)
	if _, err := e.st.UpdateTodo(td.ID, func(x *model.Todo) error { x.Title = "Dash again"; return nil }); err != nil {
		t.Fatal(err)
	}
	e.f.hold(false)
	e.sync(p.ID)
	got := e.get(td.ID)
	if got.Title != "Dash again" || got.Prompt != "New body" {
		t.Fatalf("todo %q / %q, want Dash's later title and the tracker's body", got.Title, got.Prompt)
	}
	if c, _ := fake.Card("b1", card); c.Title != "Dash again" || c.Description != "New body" {
		t.Fatalf("card %q / %q", c.Title, c.Description)
	}
}

func TestForbiddenMoveIsReverted(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	e := newEnv(t)
	p := e.project("web")
	e.connect(p.ID, "fake", "b1", nil)
	td := e.todo(p.ID, "Run me")
	e.setStatus(td.ID, model.StatusQueued)
	e.setStatus(td.ID, model.StatusInProgress)
	e.sync(p.ID)
	card := e.get(td.ID).ExternalRef.ID
	if c, _ := fake.Card("b1", card); c.Column != "Doing" {
		t.Fatalf("card in %q", c.Column)
	}

	// Done means merged: only Dash gets a task there.
	fake.Move("b1", card, "Done")
	e.sync(p.ID)
	if s := e.get(td.ID).Status; s != model.StatusInProgress {
		t.Fatalf("todo %s after the forbidden move", s)
	}
	c, _ := fake.Card("b1", card)
	if c.Column != "Doing" {
		t.Fatalf("card left in %q, want moved back to Doing", c.Column)
	}
	if n := len(c.Comments); n == 0 || !strings.Contains(c.Comments[n-1].Body, "Moved back") {
		t.Fatalf("no comment on why: %+v", c.Comments)
	}

	// Back to draft isn't allowed while running either.
	fake.Move("b1", card, "Backlog")
	e.sync(p.ID)
	if s := e.get(td.ID).Status; s != model.StatusInProgress {
		t.Fatalf("todo %s", s)
	}

	// From review, back into the queue is a rework: allowed.
	e.setStatus(td.ID, model.StatusReview)
	e.sync(p.ID)
	fake.Move("b1", card, "Todo")
	e.sync(p.ID)
	if s := e.get(td.ID).Status; s != model.StatusQueued {
		t.Fatalf("todo %s, want queued", s)
	}
	if c, _ := fake.Card("b1", card); c.Column != "Todo" {
		t.Fatalf("card in %q", c.Column)
	}
}

func TestPromptWaitsWhileRunning(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	e := newEnv(t)
	p := e.project("web")
	e.connect(p.ID, "fake", "b1", nil)
	td := e.todo(p.ID, "Task")
	e.setStatus(td.ID, model.StatusQueued)
	e.setStatus(td.ID, model.StatusInProgress)
	e.sync(p.ID)
	card := e.get(td.ID).ExternalRef.ID

	fake.Edit("b1", card, func(it *tracker.Item) { it.Description = "Better instructions" })
	e.sync(p.ID)
	if got := e.get(td.ID).Prompt; got != "Task prompt" {
		t.Fatalf("a running todo's prompt changed to %q", got)
	}
	if c, _ := fake.Card("b1", card); c.Description != "Better instructions" {
		t.Fatalf("the card's edit was overwritten: %q", c.Description)
	}
	fake.ResetCalls()
	e.sync(p.ID)
	if calls := fake.Calls(); len(calls) != 0 {
		t.Fatalf("a waiting edit made calls: %v", calls)
	}

	// Once it can change again, it does (and a resumed session hears of it).
	if _, err := e.st.UpdateTodo(td.ID, func(x *model.Todo) error {
		x.Status, x.SessionID = model.StatusFailed, "sess"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.sync(p.ID)
	got := e.get(td.ID)
	if got.Prompt != "Better instructions" || len(got.Feedback) != 1 || !strings.Contains(got.Feedback[0], "Better instructions") {
		t.Fatalf("after failing: prompt %q, feedback %q", got.Prompt, got.Feedback)
	}
	if c, _ := fake.Card("b1", card); c.Description != "Better instructions" {
		t.Fatalf("card %q", c.Description)
	}
}

func TestDependencies(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	e := newEnv(t)
	p := e.project("web")
	e.connect(p.ID, "fake", "b1", nil)
	a := e.todo(p.ID, "A")
	b := e.todo(p.ID, "B")
	e.sync(p.ID)
	ca, cb := e.get(a.ID).ExternalRef.ID, e.get(b.ID).ExternalRef.ID
	if _, err := e.st.UpdateTodo(b.ID, func(x *model.Todo) error { x.DependsOn = []string{a.ID}; return nil }); err != nil {
		t.Fatal(err)
	}
	e.sync(p.ID)
	if c, _ := fake.Card("b1", cb); !slices.Equal(c.DependsOn, []string{ca}) {
		t.Fatalf("card deps %v, want [%s]", c.DependsOn, ca)
	}
	fake.Edit("b1", cb, func(it *tracker.Item) { it.DependsOn = nil })
	e.sync(p.ID)
	if deps := e.get(b.ID).DependsOn; len(deps) != 0 {
		t.Fatalf("todo deps %v", deps)
	}
}

func TestSwitchExport(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	e := newEnv(t)
	p := e.project("web")
	draft := e.todo(p.ID, "draft")
	queued := e.todo(p.ID, "queued")
	e.setStatus(queued.ID, model.StatusQueued)
	running := e.todo(p.ID, "running")
	e.setStatus(running.ID, model.StatusQueued)
	e.setStatus(running.ID, model.StatusInProgress)
	merged := e.todo(p.ID, "merged")
	e.setStatus(merged.ID, model.StatusQueued)
	e.setStatus(merged.ID, model.StatusInProgress)
	e.setStatus(merged.ID, model.StatusReview)
	e.setStatus(merged.ID, model.StatusMerged)

	ctx := context.Background()
	_, err := e.m.Apply(ctx, p.ID, boardCfg("fake", "b1"), tracker.SwitchOptions{})
	var need tracker.ErrNeedsDecision
	if !errors.As(err, &need) || need.Need != "export" || need.OpenTodos != 3 {
		t.Fatalf("switch without a choice: %v", err)
	}
	if e.m.Running(p.ID) {
		t.Fatal("switched silently")
	}
	if _, err := e.m.Apply(ctx, p.ID, boardCfg("fake", "b1"), tracker.SwitchOptions{Export: yes(), UserID: 7}); err != nil {
		t.Fatal(err)
	}
	e.sync(p.ID)
	for _, td := range []model.Todo{draft, queued, running} {
		got := e.get(td.ID)
		if got.ExternalRef == nil {
			t.Fatalf("%s not exported", td.Title)
		}
		c, _ := fake.Card("b1", got.ExternalRef.ID)
		if c.Title != td.Title {
			t.Fatalf("card %+v for %s", c, td.Title)
		}
	}
	if e.get(running.ID).Status != model.StatusInProgress {
		t.Fatal("exporting stopped a running todo")
	}
	if e.get(merged.ID).ExternalRef != nil || len(fake.Cards("b1")) != 3 {
		t.Fatalf("merged todo exported: %d cards", len(fake.Cards("b1")))
	}
	evs, _ := e.st.ProjectEvents(p.ID, 10)
	if len(evs) != 1 || evs[0].Kind != "board.switched" || !strings.Contains(evs[0].Text, "3 open tasks exported") || evs[0].UserID != 7 {
		t.Fatalf("events %+v", evs)
	}
}

func TestSwitchWithoutExport(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	e := newEnv(t)
	p := e.project("web")
	old := e.todo(p.ID, "old")
	if _, err := e.m.Apply(context.Background(), p.ID, boardCfg("fake", "b1"), tracker.SwitchOptions{Export: no()}); err != nil {
		t.Fatal(err)
	}
	fresh := e.todo(p.ID, "new")
	e.sync(p.ID)
	if e.get(old.ID).ExternalRef != nil {
		t.Fatal("a todo left Dash-only was exported")
	}
	if e.get(fresh.ID).ExternalRef == nil || len(fake.Cards("b1")) != 1 {
		t.Fatalf("new todo: %+v, cards %+v", e.get(fresh.ID), fake.Cards("b1"))
	}
	// The Dash-only todo keeps working as a native one.
	e.setStatus(old.ID, model.StatusQueued)
	e.sync(p.ID)
	if len(fake.Cards("b1")) != 1 {
		t.Fatal("a Dash-only todo got a card")
	}
}

func TestSwitchBackUnlinks(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	e := newEnv(t)
	p := e.project("web")
	e.connect(p.ID, "fake", "b1", nil)
	td := e.todo(p.ID, "linked")
	e.sync(p.ID)
	if e.get(td.ID).ExternalRef == nil {
		t.Fatal("not linked")
	}

	ctx := context.Background()
	var need tracker.ErrNeedsDecision
	if _, err := e.m.Apply(ctx, p.ID, tracker.ProjectConfig{Backend: tracker.Native}, tracker.SwitchOptions{}); !errors.As(err, &need) || need.Need != "confirm" {
		t.Fatalf("leaving the tracker unconfirmed: %v", err)
	}
	if _, err := e.m.Apply(ctx, p.ID, boardCfg("fake", "b2"), tracker.SwitchOptions{Export: no()}); !errors.As(err, &need) || need.Need != "confirm" {
		t.Fatalf("moving to another board unconfirmed: %v", err)
	}
	if !e.m.Running(p.ID) || e.get(td.ID).ExternalRef == nil {
		t.Fatal("an unconfirmed switch changed something")
	}

	// Another board: links dropped, the todo stays.
	if _, err := e.m.Apply(ctx, p.ID, boardCfg("fake", "b2"), tracker.SwitchOptions{Confirm: true, Export: no()}); err != nil {
		t.Fatal(err)
	}
	e.sync(p.ID)
	if got := e.get(td.ID); got.ExternalRef != nil {
		t.Fatalf("still linked: %+v", got.ExternalRef)
	}
	if len(fake.Cards("b2")) != 0 || len(fake.Cards("b1")) != 1 {
		t.Fatalf("cards b1 %d b2 %d", len(fake.Cards("b1")), len(fake.Cards("b2")))
	}
	links, _ := e.st.TrackerLinks(p.ID)
	if len(links) != 0 {
		t.Fatalf("links %+v", links)
	}

	// Back to Dash: no worker, the todo is native.
	if _, err := e.m.Apply(ctx, p.ID, tracker.ProjectConfig{Backend: tracker.Native}, tracker.SwitchOptions{Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if e.m.Running(p.ID) {
		t.Fatal("a native project has a worker")
	}
	cfg, _ := e.st.TrackerConfig(p.ID)
	if !cfg.Native() || cfg.HasCredentials {
		t.Fatalf("config %+v", cfg)
	}
	if _, err := e.st.GetTodo(td.ID); err != nil {
		t.Fatal(err)
	}
	evs, _ := e.st.ProjectEvents(p.ID, 10)
	if len(evs) != 3 || !strings.Contains(evs[0].Text, "to Dash") {
		t.Fatalf("events %+v", evs)
	}
}

func TestProjectsAreIsolated(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	other := trackertest.Register(t, "other")
	e := newEnv(t)
	a, b, c := e.project("a"), e.project("b"), e.project("c")
	fake.RequireToken("ba", "token-a")
	fake.RequireToken("bb", "token-b")
	e.connect(a.ID, "fake", "ba", map[string]string{"token": "token-a"})
	e.connect(b.ID, "fake", "bb", map[string]string{"token": "token-b"})
	e.connect(c.ID, "other", "bc", nil)
	native := e.project("native")

	ta := e.todo(a.ID, "in a")
	tb := e.todo(b.ID, "in b")
	tc := e.todo(c.ID, "in c")
	tn := e.todo(native.ID, "native")
	fake.AddCard("ba", tracker.Item{Title: "card for a", Column: "Backlog"})
	for _, p := range []model.Project{a, b, c} {
		e.sync(p.ID)
	}
	if n := len(fake.Cards("ba")); n != 2 {
		t.Fatalf("board a has %d cards", n)
	}
	if cards := fake.Cards("bb"); len(cards) != 1 || cards[0].Title != "in b" {
		t.Fatalf("board b %+v", cards)
	}
	if cards := other.Cards("bc"); len(cards) != 1 || cards[0].Title != "in c" {
		t.Fatalf("board c %+v", cards)
	}
	if n := len(e.st.ListTodos(a.ID)); n != 2 {
		t.Fatalf("project a has %d todos", n)
	}
	if n := len(e.st.ListTodos(b.ID)); n != 1 {
		t.Fatalf("project b has %d todos: a's card leaked", n)
	}
	if e.get(tn.ID).ExternalRef != nil || e.m.Running(native.ID) {
		t.Fatal("the native project was synced")
	}
	if e.get(tb.ID).ExternalRef.ID == e.get(ta.ID).ExternalRef.ID || e.get(tc.ID).ExternalRef.Provider != "other" {
		t.Fatal("refs mixed up")
	}

	// A change in a reaches a's board only.
	fake.ResetCalls()
	other.ResetCalls()
	e.setStatus(ta.ID, model.StatusQueued)
	for _, p := range []model.Project{a, b, c} {
		e.sync(p.ID)
	}
	for _, call := range fake.Calls() {
		if !strings.Contains(call, "ba/") {
			t.Fatalf("a's change made call %q", call)
		}
	}
	if len(fake.Calls()) == 0 || len(other.Calls()) != 0 {
		t.Fatalf("calls %v / %v", fake.Calls(), other.Calls())
	}

	// Breaking a's credentials fails a's sync only.
	cfgA, _ := e.st.TrackerConfig(a.ID)
	cfgA.Credentials = map[string]string{"token": "wrong"}
	if _, err := e.m.Apply(context.Background(), a.ID, cfgA, tracker.SwitchOptions{}); err != nil {
		t.Fatalf("reconfigure a: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.m.SyncNow(ctx, a.ID); err == nil {
		t.Fatal("a synced with the wrong token")
	}
	e.sync(b.ID)
	if cfg, _ := e.st.TrackerConfig(b.ID); cfg.LastError != "" {
		t.Fatalf("b has a's error: %q", cfg.LastError)
	}
	if cfg, _ := e.st.TrackerConfig(a.ID); cfg.LastError == "" || cfg.LastSyncAt.IsZero() {
		t.Fatalf("a's error not recorded: %+v", cfg)
	}
	// Reconfiguring a kept its links (same board).
	links, _ := e.st.TrackerLinks(a.ID)
	if len(links) != 2 {
		t.Fatalf("a has %d links after a settings change", len(links))
	}
}

func TestReconfigureRestartsOnlyItsWorker(t *testing.T) {
	trackertest.Register(t, "fake")
	e := newEnv(t)
	a, b := e.project("a"), e.project("b")
	e.connect(a.ID, "fake", "ba", nil)
	e.connect(b.ID, "fake", "bb", nil)
	e.sync(a.ID)
	e.sync(b.ID)
	before, _ := e.st.TrackerConfig(b.ID)

	cfgA, _ := e.st.TrackerConfig(a.ID)
	cfgA.StatusMap = map[model.TodoStatus]string{model.StatusDraft: "Ideas"}
	if _, err := e.m.Apply(context.Background(), a.ID, cfgA, tracker.SwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	if !e.m.Running(a.ID) || !e.m.Running(b.ID) {
		t.Fatal("a worker stopped")
	}
	after, _ := e.st.TrackerConfig(b.ID)
	if !after.LastSyncAt.Equal(before.LastSyncAt) {
		t.Fatal("b's worker ran again when a was reconfigured")
	}
	if got, _ := e.st.TrackerConfig(a.ID); got.StatusMap[model.StatusDraft] != "Ideas" {
		t.Fatalf("a's mapping %+v", got.StatusMap)
	}
}

func TestWorkersStartOnBoot(t *testing.T) {
	trackertest.Register(t, "fake")
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.CreateProject(model.Project{Name: "web", Path: "/tmp/web"})
	n, _ := st.CreateProject(model.Project{Name: "native", Path: "/tmp/native"})
	cfg := boardCfg("fake", "b1")
	cfg.ProjectID = p.ID
	if err := st.PutTrackerConfig(cfg); err != nil {
		t.Fatal(err)
	}
	m := &tracker.Manager{Store: st, PollEvery: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(p.ID)
	if !m.Running(p.ID) || m.Running(n.ID) {
		t.Fatalf("running: synced %v, native %v", m.Running(p.ID), m.Running(n.ID))
	}
	defer st.Close()
}

func TestWebhookUsesProjectSecret(t *testing.T) {
	trackertest.Register(t, "fake")
	e := newEnv(t)
	a, b := e.project("a"), e.project("b")
	e.connect(a.ID, "fake", "ba", nil)
	e.connect(b.ID, "fake", "bb", nil)
	ca, _ := e.st.TrackerConfig(a.ID)
	cb, _ := e.st.TrackerConfig(b.ID)
	if ca.WebhookSecret == "" || ca.WebhookSecret == cb.WebhookSecret {
		t.Fatal("projects need secrets of their own")
	}
	req := func(secret string) error {
		r := httptest.NewRequest("POST", "/api/projects/"+a.ID+"/tracker/webhook", nil)
		r.Header.Set("X-Dash-Tracker-Secret", secret)
		return e.m.Webhook(a.ID, r, nil)
	}
	if err := req(cb.WebhookSecret); !errors.Is(err, tracker.ErrWebhookRefused) {
		t.Fatalf("b's secret on a: %v", err)
	}
	if err := req(""); !errors.Is(err, tracker.ErrWebhookRefused) {
		t.Fatalf("no secret: %v", err)
	}
	if err := req(ca.WebhookSecret); err != nil {
		t.Fatal(err)
	}
	n := e.project("native")
	if err := e.m.Webhook(n.ID, httptest.NewRequest("POST", "/", nil), nil); !errors.Is(err, tracker.ErrNoSync) {
		t.Fatalf("native project: %v", err)
	}
}

func TestDeletedCardUnlinks(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	e := newEnv(t)
	p := e.project("web")
	e.connect(p.ID, "fake", "b1", nil)
	id := fake.AddCard("b1", tracker.Item{Title: "x", Column: "Backlog"})
	e.sync(p.ID)
	td, ok := todoByRef(e.st, p.ID, id)
	if !ok {
		t.Fatal("not imported")
	}
	fake.Edit("b1", id, func(it *tracker.Item) { it.Deleted = true })
	e.sync(p.ID)
	if got := e.get(td.ID); got.ExternalRef != nil {
		t.Fatal("still linked to a deleted card")
	}
}

func TestBackendErrorsRetry(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	e := newEnv(t)
	p := e.project("web")
	e.connect(p.ID, "fake", "b1", nil)
	e.sync(p.ID)
	fake.FailNext(errors.New("tracker down"))
	td := e.todo(p.ID, "x")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = e.m.SyncNow(ctx, p.ID) // may have failed the list or the create
	e.sync(p.ID)
	if e.get(td.ID).ExternalRef == nil || len(fake.Cards("b1")) != 1 {
		t.Fatalf("not retried: %d cards", len(fake.Cards("b1")))
	}
	if cfg, _ := e.st.TrackerConfig(p.ID); cfg.LastError != "" {
		t.Fatalf("error kept after a good sync: %q", cfg.LastError)
	}
}

func TestTestConnection(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	e := newEnv(t)
	fake.RequireToken("b1", "tok")
	cfg := boardCfg("fake", "b1")
	cfg.StatusMap = map[model.TodoStatus]string{model.StatusDraft: "Backlog", model.StatusQueued: "Todo"}
	if _, err := e.m.Test(context.Background(), cfg); err == nil {
		t.Fatal("wrong credentials passed")
	}
	cfg.Credentials = map[string]string{"token": "tok"}
	cols, err := e.m.Test(context.Background(), cfg)
	if err != nil || len(cols) != 2 {
		t.Fatalf("cols %v err %v", cols, err)
	}
	if _, err := e.m.Test(context.Background(), boardCfg("nope", "b")); !errors.Is(err, tracker.ErrUnknownBackend) {
		t.Fatalf("unknown backend: %v", err)
	}
}
