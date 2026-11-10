package favro_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tracker"
	"github.com/klahr/dash/internal/tracker/favro"
	"github.com/klahr/dash/internal/tracker/favro/favrotest"
)

// The sync engine with Favro projects: a store, a Manager and a public
// webhook endpoint (like the API's) at Dash's "public URL".

type fwd struct {
	m    atomic.Pointer[tracker.Manager]
	held atomic.Bool
}

func (f *fwd) Publish(ev model.Event) {
	if m := f.m.Load(); m != nil && !f.held.Load() {
		m.Observe(ev)
	}
}

type env struct {
	t  *testing.T
	f  *fwd
	st *store.Store
	m  *tracker.Manager
	fv *favrotest.Server

	mu    sync.Mutex
	hooks map[string][]int // project → webhook answers
}

func newEnv(t *testing.T) *env {
	t.Helper()
	t.Parallel()
	f := &fwd{}
	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), f)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, f: f, st: st, fv: favrotest.New(t), hooks: map[string][]int{}}
	favro.AllowBaseURL(e.fv.APIURL())
	dash := httptest.NewServer(http.HandlerFunc(e.webhook))
	e.m = &tracker.Manager{Store: st, PollEvery: time.Hour, PublicURL: dash.URL}
	f.m.Store(e.m)
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.fv.WaitWebhooks()
		cancel()
		e.m.StopAll()
		dash.Close()
		st.Close()
	})
	return e
}

// webhook is the API's /api/projects/{pid}/tracker/webhook.
func (e *env) webhook(w http.ResponseWriter, r *http.Request) {
	pid, ok := strings.CutPrefix(r.URL.Path, "/api/projects/")
	pid, ok2 := strings.CutSuffix(pid, "/tracker/webhook")
	if !ok || !ok2 {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	code := http.StatusOK
	if err := e.m.Webhook(pid, r, body); err != nil {
		code = http.StatusUnauthorized
	}
	e.mu.Lock()
	e.hooks[pid] = append(e.hooks[pid], code)
	e.mu.Unlock()
	w.WriteHeader(code)
}

func (e *env) hookAnswers(pid string) []int {
	e.fv.WaitWebhooks()
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.hooks[pid])
}

// account is one Favro account with a board.
type account struct {
	email, token string
	org, widget  string
	cols         []string // Backlog, To do, Doing, Review, Done
}

func (e *env) account(email, token string) account {
	e.fv.AddAccount(email, token)
	a := account{email: email, token: token}
	a.org = e.fv.AddOrg(email+"'s org", email)
	coll := e.fv.AddCollection(a.org, "Main")
	a.widget, a.cols = e.fv.AddBoard(a.org, coll, "Board", "Backlog", "To do", "Doing", "Review", "Done")
	return a
}

func (e *env) project(name string) model.Project {
	e.t.Helper()
	p, err := e.st.CreateProject(model.Project{Name: name, Path: "/tmp/" + name, DefaultBranch: "main"})
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) cfg(a account, extra map[string]any) tracker.ProjectConfig {
	c := map[string]any{"email": a.email, "organization_id": a.org, "widget_common_id": a.widget, "base_url": e.fv.APIURL()}
	for k, v := range extra {
		c[k] = v
	}
	raw, _ := json.Marshal(c)
	return tracker.ProjectConfig{Backend: favro.Name, Config: raw, Credentials: map[string]string{"token": a.token}}
}

func (e *env) connect(pid string, a account, extra map[string]any, export bool) {
	e.t.Helper()
	if _, err := e.m.Apply(context.Background(), pid, e.cfg(a, extra), tracker.SwitchOptions{Export: &export, Confirm: true}); err != nil {
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

func (e *env) status(id string, ss ...model.TodoStatus) model.Todo {
	e.t.Helper()
	var td model.Todo
	var err error
	for _, s := range ss {
		if td, err = e.st.SetStatus(id, s, ""); err != nil {
			e.t.Fatal(err)
		}
	}
	return td
}

func (e *env) eventually(what string, ok func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func cardOf(e *env, id string) string {
	td := e.get(id)
	if td.ExternalRef == nil {
		return ""
	}
	return td.ExternalRef.ID
}

func todoForCard(st *store.Store, pid, card string) (model.Todo, bool) {
	for _, td := range st.ListTodos(pid) {
		if td.ExternalRef != nil && td.ExternalRef.ID == card {
			return td, true
		}
	}
	return model.Todo{}, false
}

// ---- tests ----

// Two projects on different Favro accounts and boards: each syncs its own
// board with its own token, and webhooks reach the right project only.
func TestTwoProjectsSyncIndependently(t *testing.T) {
	e := newEnv(t)
	a := e.account("ada@example.com", "tok-ADA")
	b := e.account("bob@example.com", "tok-BOB")
	pa, pb := e.project("alpha"), e.project("beta")
	e.connect(pa.ID, a, map[string]any{"tag": "Claude"}, false)
	e.connect(pb.ID, b, nil, false)

	// Each project registered its own webhook, at its own address, on its board.
	e.eventually("webhooks", func() bool { return len(e.fv.Webhooks(a.org)) == 1 && len(e.fv.Webhooks(b.org)) == 1 })
	ha, hb := e.fv.Webhooks(a.org)[0], e.fv.Webhooks(b.org)[0]
	if !strings.HasSuffix(ha.PostToURL, "/api/projects/"+pa.ID+"/tracker/webhook") || !strings.HasSuffix(hb.PostToURL, "/api/projects/"+pb.ID+"/tracker/webhook") ||
		ha.WidgetCommonID != a.widget || hb.WidgetCommonID != b.widget || ha.Secret == hb.Secret {
		t.Fatalf("webhooks: %+v %+v", ha, hb)
	}
	// Their pings were verified with each project's secret.
	e.eventually("pings", func() bool { return len(e.hookAnswers(pa.ID)) > 0 && len(e.hookAnswers(pb.ID)) > 0 })
	if !slices.Equal(e.hookAnswers(pa.ID), []int{200}) || !slices.Equal(e.hookAnswers(pb.ID), []int{200}) {
		t.Fatalf("pings: %v %v", e.hookAnswers(pa.ID), e.hookAnswers(pb.ID))
	}

	// Outbound: each project's todo becomes a card on its own board.
	ta, tb := e.todo(pa.ID, "alpha task"), e.todo(pb.ID, "beta task")
	e.sync(pa.ID)
	e.sync(pb.ID)
	ca, cb := cardOf(e, ta.ID), cardOf(e, tb.ID)
	if ca == "" || cb == "" {
		t.Fatal("no cards made")
	}
	if c := e.fv.Card(a.org, ca); c.WidgetCommonID != a.widget || c.Name != "alpha task" || c.ColumnID != a.cols[0] || len(c.Tags) != 1 {
		t.Fatalf("alpha's card: %+v", c)
	}
	if c := e.fv.Card(b.org, cb); c.WidgetCommonID != b.widget || c.Name != "beta task" || len(c.Tags) != 0 {
		t.Fatalf("beta's card: %+v", c)
	}
	for _, r := range e.fv.Requests() {
		ok := r.Org == "" || (r.Org == a.org && r.Token == a.token) || (r.Org == b.org && r.Token == b.token)
		if !ok {
			t.Fatalf("request with another project's token: %+v", r)
		}
	}

	// Inbound by webhook: a card added on beta's board reaches beta only,
	// without a poll (workers poll hourly here).
	n := len(e.hookAnswers(pb.ID))
	card := e.fv.AddCard(b.org, favrotest.Card{WidgetCommonID: b.widget, ColumnID: b.cols[1], Name: "from Favro", DetailedDescription: "Do **it**"})
	e.eventually("webhook import", func() bool {
		td, ok := todoForCard(e.st, pb.ID, card)
		return ok && td.Status == model.StatusQueued // made, then queued
	})
	td, _ := todoForCard(e.st, pb.ID, card)
	if td.Title != "from Favro" || td.Prompt != "Do **it**" || td.Status != model.StatusQueued {
		t.Fatalf("imported: %+v", td)
	}
	if ans := e.hookAnswers(pb.ID); len(ans) <= n || ans[len(ans)-1] != 200 {
		t.Fatalf("beta's webhook answers: %v", ans)
	}
	if _, ok := todoForCard(e.st, pa.ID, card); ok || len(e.st.ListTodos(pa.ID)) != 1 {
		t.Fatal("beta's card reached alpha")
	}

	// A call signed with alpha's secret, sent to beta's address, is refused.
	body := []byte(`{"payloadId":"x1","action":"updated","card":{"cardId":"` + card + `"}}`)
	r := httptest.NewRequest("POST", hb.PostToURL, strings.NewReader(string(body)))
	r.Header.Set(favro.SignatureHeader, favro.Sign(ha.Secret, "x1", hb.PostToURL))
	if err := e.m.Webhook(pb.ID, r, body); err == nil {
		t.Fatal("beta accepted a call signed with alpha's secret")
	}
	r.Header.Set(favro.SignatureHeader, favro.Sign(hb.Secret, "x1", hb.PostToURL))
	if err := e.m.Webhook(pb.ID, r, body); err != nil {
		t.Fatalf("beta's own call: %v", err)
	}

	// Alpha's untagged card isn't alpha's.
	e.fv.AddCard(a.org, favrotest.Card{WidgetCommonID: a.widget, ColumnID: a.cols[1], Name: "for people"})
	e.sync(pa.ID)
	if n := len(e.st.ListTodos(pa.ID)); n != 1 {
		t.Fatalf("alpha has %d todos, want 1", n)
	}

	// Switching alpha back to Dash's board removes its webhook, not beta's.
	if _, err := e.m.Apply(context.Background(), pa.ID, tracker.ProjectConfig{Backend: tracker.Native}, tracker.SwitchOptions{Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if hs := e.fv.Webhooks(a.org); len(hs) != 0 {
		t.Fatalf("alpha's webhook stayed: %+v", hs)
	}
	if hs := e.fv.Webhooks(b.org); len(hs) != 1 {
		t.Fatalf("beta's webhook: %+v", hs)
	}
}

// Both projects on one account (and token), different boards: their
// requests share the account but not their boards.
func TestSameAccountDifferentBoards(t *testing.T) {
	e := newEnv(t)
	a := e.account("ada@example.com", "tok-ADA")
	coll := e.fv.AddCollection(a.org, "Other")
	other := a
	other.widget, other.cols = e.fv.AddBoard(a.org, coll, "Board 2", "Backlog", "To do", "Doing", "Review", "Done")
	p1, p2 := e.project("one"), e.project("two")
	e.connect(p1.ID, a, nil, false)
	e.connect(p2.ID, other, nil, false)
	c1 := e.fv.AddCard(a.org, favrotest.Card{WidgetCommonID: a.widget, ColumnID: a.cols[0], Name: "one's"})
	c2 := e.fv.AddCard(a.org, favrotest.Card{WidgetCommonID: other.widget, ColumnID: other.cols[0], Name: "two's"})
	e.sync(p1.ID)
	e.sync(p2.ID)
	if _, ok := todoForCard(e.st, p1.ID, c1); !ok {
		t.Fatal("one's card not imported")
	}
	if _, ok := todoForCard(e.st, p2.ID, c2); !ok {
		t.Fatal("two's card not imported")
	}
	if len(e.st.ListTodos(p1.ID)) != 1 || len(e.st.ListTodos(p2.ID)) != 1 {
		t.Fatal("a card reached the other project")
	}
}

// Dragging a card to Done (merged) in Favro moves it back, with a comment.
func TestForbiddenMoveIsRevertedWithComment(t *testing.T) {
	e := newEnv(t)
	a := e.account("ada@example.com", "tok-ADA")
	p := e.project("web")
	e.connect(p.ID, a, nil, false)
	td := e.todo(p.ID, "task")
	e.status(td.ID, model.StatusQueued)
	e.sync(p.ID)
	card := cardOf(e, td.ID)
	if c := e.fv.Card(a.org, card); c.ColumnID != a.cols[1] {
		t.Fatalf("queued card in %s, want To do", c.ColumnID)
	}
	e.fv.Move(a.org, card, a.cols[4]) // Done
	e.sync(p.ID)
	if got := e.get(td.ID); got.Status != model.StatusQueued {
		t.Fatalf("todo became %s", got.Status)
	}
	if c := e.fv.Card(a.org, card); c.ColumnID != a.cols[1] {
		t.Fatalf("card left in %s, want moved back to To do", c.ColumnID)
	}
	ks := e.fv.Comments(a.org, card)
	if len(ks) == 0 || !strings.Contains(ks[len(ks)-1].Comment, "Moved back to To do") || !strings.Contains(ks[len(ks)-1].Comment, "merged") {
		t.Fatalf("comments: %+v", ks)
	}
	// An allowed move (back to Backlog = draft) is applied.
	e.fv.Move(a.org, card, a.cols[0])
	e.sync(p.ID)
	if got := e.get(td.ID); got.Status != model.StatusDraft {
		t.Fatalf("todo is %s, want draft", got.Status)
	}
}

// Both sides change the title between syncs: Dash's edit, made after the
// last read of the board, wins; edits to different fields both land.
func TestConflict(t *testing.T) {
	e := newEnv(t)
	a := e.account("ada@example.com", "tok-ADA")
	p := e.project("web")
	e.connect(p.ID, a, nil, false)
	td := e.todo(p.ID, "task")
	e.sync(p.ID)
	card := cardOf(e, td.ID)

	e.f.held.Store(true)
	e.fv.Edit(a.org, card, func(c *favrotest.Card) { c.Name = "favro title"; c.DetailedDescription = "favro body" })
	time.Sleep(2 * time.Millisecond)
	if _, err := e.st.UpdateTodo(td.ID, func(x *model.Todo) error { x.Title = "dash title"; return nil }); err != nil {
		t.Fatal(err)
	}
	e.f.held.Store(false)
	e.sync(p.ID)
	got := e.get(td.ID)
	if got.Title != "dash title" || got.Prompt != "favro body" {
		t.Fatalf("todo: %q / %q", got.Title, got.Prompt)
	}
	if c := e.fv.Card(a.org, card); c.Name != "dash title" || c.DetailedDescription != "favro body" {
		t.Fatalf("card: %+v", c)
	}

	// The board's later edit wins over an older one in Dash's past.
	e.fv.Edit(a.org, card, func(c *favrotest.Card) { c.Name = "favro again" })
	e.sync(p.ID)
	if got := e.get(td.ID); got.Title != "favro again" {
		t.Fatalf("title %q", got.Title)
	}
}

// Status changes post the PR link, summary and failure reason as comments;
// people's comments go to the todo's chat, and with the project's choice,
// rework a todo in review.
func TestCommentsAndRework(t *testing.T) {
	e := newEnv(t)
	a := e.account("ada@example.com", "tok-ADA")
	e.fv.AddUser(a.org, "u-grace", "Grace")
	p := e.project("web")
	e.connect(p.ID, a, map[string]any{"comment_feedback": tracker.FeedbackRework}, false)
	td := e.todo(p.ID, "task")
	e.status(td.ID, model.StatusQueued)
	e.sync(p.ID)
	e.status(td.ID, model.StatusInProgress)
	if _, err := e.st.UpdateTodo(td.ID, func(x *model.Todo) error {
		x.Status, x.Summary = model.StatusReview, "Added the login page."
		x.PR = &model.PullRequest{URL: "https://github.com/acme/web/pull/7"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.sync(p.ID)
	card := cardOf(e, td.ID)
	ks := e.fv.Comments(a.org, card)
	if len(ks) != 1 || !strings.Contains(ks[0].Comment, "Added the login page.") || !strings.Contains(ks[0].Comment, "/pull/7") {
		t.Fatalf("review comment: %+v", ks)
	}
	if c := e.fv.Card(a.org, card); c.ColumnID != a.cols[3] {
		t.Fatal("card not in Review")
	}
	e.sync(p.ID) // Dash's own comment isn't read back as feedback
	if got := e.get(td.ID); got.Status != model.StatusReview || len(got.Feedback) != 0 {
		t.Fatalf("own comment read back: %+v", got)
	}

	e.fv.AddComment(a.org, card, "u-grace", "The button is the wrong colour")
	e.sync(p.ID)
	got := e.get(td.ID)
	if got.Status != model.StatusQueued || len(got.Feedback) != 1 || !strings.Contains(got.Feedback[0], "wrong colour") || !strings.Contains(got.Feedback[0], "Grace") {
		t.Fatalf("rework: %s %q", got.Status, got.Feedback)
	}
	if c := e.fv.Card(a.org, card); c.ColumnID != a.cols[1] {
		t.Fatalf("reworked card in %s, want To do", c.ColumnID)
	}

	// Failing posts the reason.
	e.status(td.ID, model.StatusInProgress)
	if _, err := e.st.UpdateTodo(td.ID, func(x *model.Todo) error { x.Status, x.Error = model.StatusFailed, "tests failed: 3"; return nil }); err != nil {
		t.Fatal(err)
	}
	e.sync(p.ID)
	ks = e.fv.Comments(a.org, card)
	if !strings.Contains(ks[len(ks)-1].Comment, "tests failed: 3") {
		t.Fatalf("failure comment: %+v", ks[len(ks)-1])
	}
}

func TestCommentFeedbackPolicies(t *testing.T) {
	for _, policy := range []string{tracker.FeedbackChat, tracker.FeedbackNote} {
		t.Run("policy="+policy, func(t *testing.T) {
			e := newEnv(t)
			a := e.account("ada@example.com", "tok-ADA")
			p := e.project("web")
			e.connect(p.ID, a, map[string]any{"comment_feedback": policy}, false)
			td := e.todo(p.ID, "task")
			e.status(td.ID, model.StatusQueued, model.StatusInProgress, model.StatusReview)
			e.sync(p.ID)
			card := cardOf(e, td.ID)
			e.fv.AddComment(a.org, card, "u-ada@example.com", "Looks good, but rename it")
			e.sync(p.ID)
			got := e.get(td.ID)
			if got.Status != model.StatusReview {
				t.Fatalf("status %s", got.Status)
			}
			if want := map[string]int{tracker.FeedbackChat: 0, tracker.FeedbackNote: 1}[policy]; len(got.Feedback) != want {
				t.Fatalf("feedback %q, want %d notes", got.Feedback, want)
			}
			chat := e.st.TodoChat(td.ID)
			if !slices.ContainsFunc(chat, func(m model.ChatMessage) bool { return strings.Contains(m.Content, "rename it") }) {
				t.Fatalf("not in the chat: %+v", chat)
			}
		})
	}
}

// Switching a project with open todos to Favro exports them as cards.
func TestExportOnSwitch(t *testing.T) {
	e := newEnv(t)
	a := e.account("ada@example.com", "tok-ADA")
	p := e.project("web")
	dep := e.todo(p.ID, "first")
	td := e.todo(p.ID, "second")
	third := e.todo(p.ID, "third")
	if _, err := e.st.UpdateTodo(td.ID, func(x *model.Todo) error { x.DependsOn = []string{dep.ID}; return nil }); err != nil {
		t.Fatal(err)
	}
	e.status(td.ID, model.StatusQueued)
	e.connect(p.ID, a, nil, true)
	e.sync(p.ID)
	c1, c2 := cardOf(e, dep.ID), cardOf(e, td.ID)
	if c1 == "" || c2 == "" {
		t.Fatal("not exported")
	}
	c := e.fv.Card(a.org, c2)
	if c.ColumnID != a.cols[1] || len(c.Dependencies) != 1 || c.Dependencies[0].CardID != c1 {
		t.Fatalf("exported card: %+v", c)
	}
	// Dependencies changed in Favro reach Dash: one added, one removed.
	c3 := cardOf(e, third.ID)
	e.fv.Edit(a.org, c3, func(x *favrotest.Card) { x.Dependencies = []favrotest.Dependency{{CardID: c1}} })
	e.fv.Edit(a.org, c2, func(x *favrotest.Card) { x.Dependencies = nil })
	e.sync(p.ID)
	if got := e.get(third.ID); !slices.Equal(got.DependsOn, []string{dep.ID}) {
		t.Fatalf("third depends on %v", got.DependsOn)
	}
	if got := e.get(td.ID); len(got.DependsOn) != 0 {
		t.Fatalf("still depends on %v", got.DependsOn)
	}
}

// The token never appears in what the engine records.
func TestTokenNotRecorded(t *testing.T) {
	e := newEnv(t)
	a := e.account("ada@example.com", "tok-ADA-SECRET")
	p := e.project("web")
	e.connect(p.ID, a, nil, false)
	cfg := e.cfg(a, nil)
	cfg.Credentials = map[string]string{"token": "tok-WRONG-SECRET"}
	if _, err := e.m.Apply(context.Background(), p.ID, cfg, tracker.SwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := e.m.SyncNow(ctx, p.ID)
	if err == nil {
		t.Fatal("wrong token synced")
	}
	saved, _ := e.st.TrackerConfig(p.ID)
	for _, s := range []string{err.Error(), saved.LastError, string(saved.Config)} {
		if strings.Contains(s, "SECRET") {
			t.Fatalf("token recorded: %q", s)
		}
	}
	if saved.LastError == "" {
		t.Fatal("no error recorded")
	}
}
