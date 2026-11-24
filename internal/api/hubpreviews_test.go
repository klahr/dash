package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/preview"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// A hub's task previews on its runner, where its worktree is: the hub
// forwards start, log and stop there by the runner's id for the task, lists
// the preview by its own id (as the runner reports it), and serves it at
// <remote id>-<runner>.<preview domain>. A member the runner doesn't work
// for is refused, and with the runner offline the hub says so.
func TestHubPreviewsATaskOnItsRunner(t *testing.T) {
	parallel(t)

	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	d := &dispatch.Dispatcher{Store: raw(hub), Fleet: rh, Grace: time.Minute, OnPreviews: hub.RunnerPreviews}
	rh.OnState, rh.OnRevoke, rh.OnReport = d.State, d.Revoked, d.Report
	hub.Dispatch = d
	hub.Previews = preview.New(nil, hub.Hub, raw(hub), "preview.dash.test")
	hub.Previews.Remote = rh
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(hub.Routes())
	defer srv.Close()

	owner := testOwner(t, hub)
	other := member(t, hub, owner.Org, "bob", store.RoleMember)
	code, _, err := rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := tunnel.Register(ctx, srv.URL, code, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	rs, runnerAPI, _ := runnerSide(t, creds.OrgID)
	rs.Procs, rs.HubUser = runner.NewManager(rs.Hub), creds.UserID
	t.Cleanup(rs.Procs.StopAll)
	rs.Previews = preview.New(rs.Procs, rs.Hub, raw(rs), "")
	rctx, stopRunner := context.WithCancel(ctx)
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(rctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })
	reporter := &dispatch.Reporter{Store: raw(rs), Previews: rs.Previews.List,
		Send: func(ctx context.Context, r dispatch.Report) error { return tunnel.Report(ctx, creds, r) }}
	report := func() {
		t.Helper()
		if err := reporter.Send(ctx, reporter.Build(time.Now())); err != nil {
			t.Fatal(err)
		}
	}

	// The hub's task, in review on the runner.
	const run = "echo hello-from-the-runner; sleep 30"
	hp, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, DefaultBranch: "main", RunsOn: []string{"any"}, RunCommand: run})
	if err != nil {
		t.Fatal(err)
	}
	rp, err := raw(rs).CreateProject(model.Project{Name: "shared", Path: t.TempDir(), HubProjectID: hp.ID, RunCommand: run})
	if err != nil {
		t.Fatal(err)
	}
	ht := reviewTodo(t, raw(hub), hp.ID)
	rt := reviewTodo(t, raw(rs), rp.ID)
	if _, err := raw(rs).UpdateTodo(rt.ID, func(x *model.Todo) error { x.HubTodoID = ht.ID; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := raw(hub).UpdateTodo(ht.ID, func(x *model.Todo) error { x.Runner, x.RemoteID = creds.ID, rt.ID; return nil }); err != nil {
		t.Fatal(err)
	}

	as := func(uid int64) http.Handler {
		sess := sessionOf(t, hub, uid, owner.Org)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hub.mux().ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), sess)))
		})
	}
	h := as(owner.User)
	list := func() []model.Preview {
		t.Helper()
		w := do(t, h, "GET", "/api/previews", "")
		var out []model.Preview
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("list: %d %s", w.Code, w.Body)
		}
		return out
	}

	// Another member: the runner doesn't work for them.
	if w := do(t, as(other), "POST", "/api/todos/"+ht.ID+"/preview", ""); w.Code != http.StatusForbidden {
		t.Fatalf("preview for a member the runner doesn't work for: %d %s", w.Code, w.Body)
	}
	if _, ok := rs.Previews.Get(rt.ID); ok {
		t.Fatal("the runner started it anyway")
	}

	w := do(t, h, "POST", "/api/todos/"+ht.ID+"/preview", "")
	var pv model.Preview
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &pv) != nil {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	label := rt.ID + "-laptop"
	if pv.TodoID != ht.ID || pv.ProjectID != hp.ID || pv.Kind != model.PreviewWeb || pv.LocalURL != "" ||
		!strings.Contains(pv.URL, "://"+label+".preview.dash.test") || pv.OpenURL != "/preview/"+ht.ID ||
		!strings.HasPrefix(pv.ProcessID, creds.ID+procSep) {
		t.Fatalf("started: %+v", pv)
	}
	if _, ok := rs.Previews.Get(rt.ID); !ok {
		t.Fatal("not started on the runner")
	}
	if got := list(); len(got) != 1 || got[0].TodoID != ht.ID {
		t.Fatalf("listed: %+v", got)
	}
	report() // the runner's report keeps it, by the hub's id
	if got := list(); len(got) != 1 || got[0].TodoID != ht.ID || got[0].URL != pv.URL {
		t.Fatalf("listed after a report: %+v", got)
	}
	eventually(t, "the log from the runner", func() bool {
		w := do(t, h, "GET", "/api/todos/"+ht.ID+"/preview/log", "")
		return w.Code == http.StatusOK && strings.Contains(w.Body.String(), "hello-from-the-runner")
	})

	// Its open_url signs the browser in to the hub's host for it, as does
	// the proxy's own sign-in link (by its label) for a member of the org.
	for _, path := range []string{pv.OpenURL, "/preview/" + label} {
		w := do(t, h, "GET", path, "")
		if loc := w.Header().Get("Location"); w.Code != http.StatusFound || !strings.Contains(loc, "://"+label+".preview.dash.test") {
			t.Fatalf("open %s: %d %s", path, w.Code, loc)
		}
	}
	if w := do(t, as(other), "GET", "/preview/"+label, ""); w.Code != http.StatusFound {
		t.Fatalf("open for another member of the org: %d %s", w.Code, w.Body)
	}
	// A user of another org gets neither: not by the hub's task, nor by the
	// label (a runner's preview, which only a platform admin may open).
	two, err := hub.Auth.Accounts().CreateOrg(owner.User, "Two")
	if err != nil {
		t.Fatal(err)
	}
	outsider := member(t, hub, two.ID, "eve", store.RoleMember)
	osess := sessionOf(t, hub, outsider, two.ID)
	asOutsider := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.mux().ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), osess)))
	})
	for _, path := range []string{pv.OpenURL, "/preview/" + label} {
		w := do(t, asOutsider, "GET", path, "")
		if loc := w.Header().Get("Location"); w.Code != http.StatusNotFound || loc != "" ||
			strings.Contains(w.Body.String(), "hello-from-the-runner") {
			t.Fatalf("open %s for a user of another org: %d %q %s", path, w.Code, loc, w.Body)
		}
	}

	if w := do(t, h, "DELETE", "/api/todos/"+ht.ID+"/preview", ""); w.Code != http.StatusNoContent {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	if _, ok := rs.Previews.Get(rt.ID); ok {
		t.Fatal("still running on the runner")
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("listed after stopping: %+v", got)
	}

	// Stopped on the runner (idle, say): the next report drops it. Stop all
	// stops them there.
	if w := do(t, h, "POST", "/api/todos/"+ht.ID+"/preview", ""); w.Code != http.StatusOK {
		t.Fatalf("start again: %d %s", w.Code, w.Body)
	}
	_ = rs.Previews.Stop(rt.ID, "idle")
	report()
	if got := list(); len(got) != 0 {
		t.Fatalf("listed after the runner stopped it: %+v", got)
	}
	if w := do(t, h, "POST", "/api/todos/"+ht.ID+"/preview", ""); w.Code != http.StatusOK {
		t.Fatalf("start again: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "DELETE", "/api/previews", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"stopped":1`) {
		t.Fatalf("stop all: %d %s", w.Code, w.Body)
	}
	if _, ok := rs.Previews.Get(rt.ID); ok {
		t.Fatal("stop all left it running on the runner")
	}

	// The runner goes offline: the worktree is only there.
	stopRunner()
	eventually(t, "the runner to go offline", func() bool { r, _ := rh.Get(creds.ID); return !r.Online })
	w = do(t, h, "POST", "/api/todos/"+ht.ID+"/preview", "")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "worktree is on its runner, laptop, which is offline") {
		t.Fatalf("runner offline: %d %s", w.Code, w.Body)
	}
}

// A runner's report keeps only its previews of the hub's tasks it ran: one
// for another runner's task, for an id the hub didn't send it, or for a task
// the hub doesn't have is dropped, so it is neither listed nor served.
func TestHubPreviewsIgnoreOtherRunnersTasks(t *testing.T) {
	parallel(t)

	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	hub.Previews = preview.New(nil, hub.Hub, raw(hub), "preview.dash.test")
	hub.Previews.Remote = rh
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(hub.Routes())
	defer srv.Close()

	owner := testOwner(t, hub)
	online := func(name string) tunnel.Runner {
		t.Helper()
		code, _, err := rh.NewCode(owner)
		if err != nil {
			t.Fatal(err)
		}
		creds, err := tunnel.Register(ctx, srv.URL, code, name, "")
		if err != nil {
			t.Fatal(err)
		}
		client := &tunnel.Client{Creds: creds, Handler: http.NotFoundHandler(), MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
		go func() { _ = client.Run(ctx) }()
		var r tunnel.Runner
		eventually(t, name+" to come online", func() bool { r, _ = rh.Get(creds.ID); return r.Online })
		return r
	}
	a, b := online("laptop"), online("desktop")

	hp, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, DefaultBranch: "main", RunsOn: []string{"any"}})
	if err != nil {
		t.Fatal(err)
	}
	ht := reviewTodo(t, raw(hub), hp.ID)
	if _, err := raw(hub).UpdateTodo(ht.ID, func(x *model.Todo) error { x.Runner, x.RemoteID = a.ID, "r1"; return nil }); err != nil {
		t.Fatal(err)
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hub.mux().ServeHTTP(w, asOwner(t, hub, r)) })
	list := func() []model.Preview {
		t.Helper()
		w := do(t, h, "GET", "/api/previews", "")
		var out []model.Preview
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("list: %d %s", w.Code, w.Body)
		}
		return out
	}
	running := func(todo string) dispatch.PreviewState {
		return dispatch.PreviewState{HubTodoID: ht.ID, Preview: model.Preview{TodoID: todo, Kind: model.PreviewWeb, State: model.PreviewLive}}
	}
	served := func() bool {
		_, okA := hub.hubPreviewTask("r1-" + a.Name)
		_, okB := hub.hubPreviewTask("r1-" + b.Name)
		return okA || okB
	}

	// Runner B reports runner A's task, by its right remote id.
	hub.RunnerPreviews(b, []dispatch.PreviewState{running("r1")})
	// Runner A reports the task under an id the hub didn't send it, and
	// a task the hub doesn't have.
	hub.RunnerPreviews(a, []dispatch.PreviewState{running("other"),
		{HubTodoID: "no-such-task", Preview: model.Preview{TodoID: "r1", Kind: model.PreviewWeb, State: model.PreviewLive}}})
	if got := list(); len(got) != 0 {
		t.Fatalf("listed a preview the reporting runner doesn't have: %+v", got)
	}
	if served() {
		t.Fatal("serves a preview the reporting runner doesn't have")
	}

	// Runner A's honest report is kept, by the hub's id.
	hub.RunnerPreviews(a, []dispatch.PreviewState{running("r1")})
	if got := list(); len(got) != 1 || got[0].TodoID != ht.ID {
		t.Fatalf("listed after an honest report: %+v", got)
	}
	if id, ok := hub.hubPreviewTask("r1-" + a.Name); !ok || id != ht.ID {
		t.Fatalf("served after an honest report: %q %v", id, ok)
	}
	// B's report of it again changes nothing.
	hub.RunnerPreviews(b, []dispatch.PreviewState{running("r1")})
	if _, ok := hub.hubPreviewTask("r1-" + b.Name); ok {
		t.Fatal("serves another runner's task under the reporting runner's host")
	}
	if got := list(); len(got) != 1 {
		t.Fatalf("listed after another runner's report: %+v", got)
	}
}

// A note sent back with Rework… on the hub goes to the runner with its
// attached files (Draw's picture): the runner keeps a copy, and the note
// names it there.
func TestHubReworkSendsAttachmentsToTheRunner(t *testing.T) {
	parallel(t)

	hub, _, _ := boardServer(t)
	hub.UploadRoot = t.TempDir()
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	d := &dispatch.Dispatcher{Store: raw(hub), Fleet: rh, Grace: time.Minute}
	rh.OnState, rh.OnRevoke, rh.OnReport = d.State, d.Revoked, d.Report
	engine := queue.NewTodoEngine(&queue.TodoEngine{Store: raw(hub), Pub: hub.Hub, Log: &queue.TodoLog{Root: t.TempDir()}, Remote: d})
	d.Kick = engine.Kick
	hub.Todos, hub.Dispatch = engine, d
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go engine.Run(ctx)
	srv := httptest.NewServer(hub.Routes())
	defer srv.Close()

	owner := testOwner(t, hub)
	code, _, err := rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := tunnel.Register(ctx, srv.URL, code, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	rs, runnerAPI, remote := runnerSide(t, creds.OrgID)
	rs.HubUser = creds.UserID
	rs.UploadRoot = t.TempDir()
	reporter := &dispatch.Reporter{Store: raw(rs), Parallel: 2,
		Send: func(ctx context.Context, r dispatch.Report) error { return tunnel.Report(ctx, creds, r) }}
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, RemoteURL: remote, DefaultBranch: "main",
		MergeMode: model.MergePR, RunsOn: []string{"any"}})
	if err != nil {
		t.Fatal(err)
	}
	td, _ := raw(hub).CreateTodo(model.Todo{ProjectID: p.ID, CreatedBy: owner.User, Title: "add a page", Prompt: "add /about"})
	if _, err := raw(hub).SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	engine.Kick()
	eventually(t, "the task to reach the runner", func() bool {
		got, _ := raw(hub).GetTodo(td.ID)
		return got.Status == model.StatusInProgress && got.Runner == creds.ID
	})
	sent, _ := raw(hub).GetTodo(td.ID)
	for _, st := range []model.TodoStatus{model.StatusInProgress, model.StatusReview} {
		if _, err := raw(rs).UpdateTodo(sent.RemoteID, func(x *model.Todo) error {
			x.Status, x.Worktree, x.SessionID = st, &model.Worktree{Branch: "dash/todo-x", Path: t.TempDir()}, "s1"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reporter.Send(ctx, reporter.Build(time.Now())); err != nil {
		t.Fatal(err)
	}
	if got, _ := raw(hub).GetTodo(td.ID); got.Status != model.StatusReview {
		t.Fatalf("hub's task: %+v", got)
	}

	// Draw's picture, uploaded on the hub, and Rework… with it.
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hub.mux().ServeHTTP(w, asOwner(t, hub, r)) })
	picture := []byte("\x89PNG\r\n\x1a\nnot really")
	r := httptest.NewRequest("POST", "/api/uploads?name=drawing.png", strings.NewReader(string(picture)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var up model.Attachment
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &up) != nil {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "POST", "/api/todos/"+td.ID+"/rework", `{"feedback":"make the button blue","attachments":["`+up.ID+`"]}`); w.Code != http.StatusOK {
		t.Fatalf("rework with an attachment: %d %s", w.Code, w.Body)
	}
	var copyOnRunner model.Todo
	eventually(t, "the note to reach the runner", func() bool {
		copyOnRunner, _ = raw(rs).GetTodo(sent.RemoteID)
		return len(copyOnRunner.Feedback) == 1
	})
	if len(copyOnRunner.Attachments) != 1 {
		t.Fatalf("runner's attachments: %+v", copyOnRunner.Attachments)
	}
	a := copyOnRunner.Attachments[0]
	if a.ID != up.ID || a.Name != "drawing.png" || !strings.HasPrefix(a.Path, rs.UploadRoot+string(filepath.Separator)) {
		t.Fatalf("runner's attachment: %+v", a)
	}
	if b, err := os.ReadFile(a.Path); err != nil || string(b) != string(picture) {
		t.Fatalf("runner's copy: %q %v", b, err)
	}
	note := copyOnRunner.Feedback[0]
	if !strings.Contains(note, "make the button blue") || !strings.Contains(note, a.Path) || strings.Contains(note, up.Path) {
		t.Fatalf("runner's note: %q", note)
	}

	// Editor changes still can't be sent to a runner.
	if err := hub.as(ownerCaller(t, hub)).sendable(p.ID, true); err == nil || !strings.Contains(err.Error(), "editor changes can't be sent to runners") {
		t.Fatalf("editor changes for a hub's task: %v", err)
	}
}

// A hub's task's terminal preview opens on its runner for the runner's
// owner, and its socket goes through the hub by the hub's task id.
func TestHubTerminalPreviewOnTheTasksRunner(t *testing.T) {
	parallel(t)
	needTerminals(t)

	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(hub.Routes())
	defer srv.Close()
	owner := testOwner(t, hub)
	other := member(t, hub, owner.Org, "bob", store.RoleMember)
	code, _, err := rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := tunnel.Register(ctx, srv.URL, code, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	rs, runnerAPI, _ := runnerSide(t, creds.OrgID)
	rs.Procs, rs.HubUser = runner.NewManager(rs.Hub), creds.UserID
	t.Cleanup(rs.Procs.StopAll)
	rs.Previews = preview.New(rs.Procs, rs.Hub, raw(rs), "")
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	const cmd = "echo on-the-runner; exec bash --norc --noprofile"
	hp, err := raw(hub).CreateProject(model.Project{Name: "cli", OrgID: owner.Org, RunsOn: []string{"any"}, PreviewKind: model.PreviewTerminal, PreviewCommand: cmd})
	if err != nil {
		t.Fatal(err)
	}
	rp, err := raw(rs).CreateProject(model.Project{Name: "cli", Path: t.TempDir(), HubProjectID: hp.ID, PreviewKind: model.PreviewTerminal, PreviewCommand: cmd})
	if err != nil {
		t.Fatal(err)
	}
	ht, rt := reviewTodo(t, raw(hub), hp.ID), reviewTodo(t, raw(rs), rp.ID)
	if _, err := raw(rs).UpdateTodo(rt.ID, func(x *model.Todo) error { x.HubTodoID = ht.ID; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := raw(hub).UpdateTodo(ht.ID, func(x *model.Todo) error { x.Runner, x.RemoteID = creds.ID, rt.ID; return nil }); err != nil {
		t.Fatal(err)
	}
	as := func(uid int64) http.Handler {
		sess := sessionOf(t, hub, uid, owner.Org)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hub.mux().ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), sess)))
		})
	}

	if w := do(t, as(other), "POST", "/api/todos/"+ht.ID+"/preview", ""); w.Code != http.StatusForbidden {
		t.Fatalf("terminal preview for another member: %d %s", w.Code, w.Body)
	}
	w := do(t, as(owner.User), "POST", "/api/todos/"+ht.ID+"/preview", "")
	var pv model.Preview
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &pv) != nil {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	if pv.Kind != model.PreviewTerminal || pv.TodoID != ht.ID || pv.TerminalURL != "/ws/todos/"+ht.ID+"/preview" || pv.URL != "" {
		t.Fatalf("preview: %+v", pv)
	}
	hs := httptest.NewServer(as(owner.User))
	defer hs.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hs.URL, "http")+pv.TerminalURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	readPrompt(t, ws, "on-the-runner")
	if err := ws.WriteJSON(map[string]any{"type": "input", "data": "echo via-hub-$((40+2))\r"}); err != nil {
		t.Fatal(err)
	}
	readWS(t, ws, "via-hub-42")

	hs2 := httptest.NewServer(as(other))
	defer hs2.Close()
	if ws2, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hs2.URL, "http")+pv.TerminalURL, nil); err == nil {
		ws2.Close()
		t.Fatal("another member attached to the terminal preview")
	}
}
