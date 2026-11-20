package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/checklist"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// The hub's dashboard finds a project's quick actions (Run, Run Tests,
// Deploy) and its shell among the processes the hub lists from its
// runners by their preset and Main, since their dir is the runner's clone,
// never the hub project's path. The runner marks them, and the hub keeps
// the marks, also once the command has finished (as after a deploy that
// restarted the hub).
func TestHubListsRunnerQuickActionsWithTheirPreset(t *testing.T) {
	parallel(t)

	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	d := &dispatch.Dispatcher{Store: raw(hub), Fleet: rh, Grace: 50 * time.Millisecond}
	rh.OnState, rh.OnRevoke, rh.OnReport = d.State, d.Revoked, d.Report
	hub.Dispatch = d
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	rs.Procs, rs.HubUser = runner.NewManager(rs.Hub), creds.UserID // an own command is the owner's, as a terminal is
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, Path: "/hub/has/its/own/path",
		RemoteURL: remote, DefaultBranch: "main", MergeMode: model.MergePR, RunsOn: []string{"any"},
		TestCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest(method, "/api/projects/"+p.ID+path, strings.NewReader(body))))
		return w
	}
	start := func(body string) model.Process {
		t.Helper()
		w := call("POST", "/processes", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("start %s: %d %s", body, w.Code, w.Body)
		}
		var proc model.Process
		if err := json.Unmarshal(w.Body.Bytes(), &proc); err != nil {
			t.Fatal(err)
		}
		return proc
	}

	test := start(`{"kind":"task","preset":"test"}`)
	if test.Preset != "test" || !test.Main || test.ProjectID != p.ID || test.Dir == p.Path {
		t.Errorf("started test run: %+v", test)
	}
	// Deploys aren't processes any more: they go to an environment (deploys.go).
	if w := call("POST", "/processes", `{"kind":"task","preset":"deploy"}`); w.Code != http.StatusBadRequest {
		t.Errorf("the deploy preset: %d %s", w.Code, w.Body)
	}
	own := start(`{"kind":"task","command":"true"}`) // not a quick action
	if own.Preset != "" || !own.Main {
		t.Errorf("own command: %+v", own)
	}

	list := func() map[string]model.Process {
		w := call("GET", "/processes", "")
		var ps []model.Process
		if err := json.Unmarshal(w.Body.Bytes(), &ps); err != nil {
			t.Fatalf("list: %v %s", err, w.Body)
		}
		out := map[string]model.Process{}
		for _, x := range ps {
			out[x.ID] = x
		}
		return out
	}
	eventually(t, "the commands to finish", func() bool {
		ps := list()
		return len(ps) == 2 && !ps[test.ID].Running && !ps[own.ID].Running
	})
	ps := list()
	if x := ps[test.ID]; x.Preset != "test" || !x.Main || x.ExitCode == nil || *x.ExitCode != 0 {
		t.Errorf("listed test run: %+v", x)
	}
	if x := ps[own.ID]; x.Preset != "" {
		t.Errorf("listed own command: %+v", x)
	}
	if !strings.Contains(call("GET", "/processes", "").Body.String(), `"runner":"laptop"`) {
		t.Errorf("listed processes don't name their runner")
	}
}

// rewriteProcs gives a runner's processes the hub's ids and keeps the rest,
// the quick-action marks among them.
func TestRewriteProcsKeepsQuickActionMarks(t *testing.T) {
	parallel(t)

	body := []byte(`[{"id":"p1","kind":"task","preset":"deploy","main":true,"todo_id":"rt"},{"id":"p2","kind":"pty"}]`)
	var got []map[string]any
	if err := json.Unmarshal(rewriteProcs(body, tunnel.Runner{ID: "r1", Name: "laptop"}, map[string]string{"rt": "ht"}), &got); err != nil {
		t.Fatal(err)
	}
	if got[0]["id"] != "r1~p1" || got[0]["preset"] != "deploy" || got[0]["main"] != true || got[0]["todo_id"] != "ht" || got[0]["runner"] != "laptop" {
		t.Errorf("rewritten: %v", got[0])
	}
	if _, ok := got[1]["main"]; ok {
		t.Errorf("a process outside the main checkout got main: %v", got[1])
	}
}

// sessionOf is user uid's session in org, stored and read back the way a
// signed-in request's is.
func sessionOf(t *testing.T, s *Server, uid, org int64) store.Session {
	t.Helper()
	acc := raw(s).Accounts()
	now := time.Now()
	key := fmt.Sprintf("test-session-%d", sessionSeq.Add(1))
	if err := acc.CreateSessionIn(key, uid, org, "", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	sess, err := acc.Session(key, now)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Org == nil || sess.Org.ID != org {
		t.Fatalf("user %d is not a member of org %d", uid, org)
	}
	return sess
}

var sessionSeq atomic.Int64

// A hub opens a terminal on a project's runner only for the user who
// registered the runner: another member of the org gets neither a new one
// nor the owner's. The terminal starts in the runner's checkout, whatever
// the hub asks, and its socket carries keyboard input and resizes through
// the hub, with the scrollback again on reattaching.
func TestHubOpensTerminalsOnlyForTheRunnersOwner(t *testing.T) {
	parallel(t)
	needTerminals(t)

	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	d := &dispatch.Dispatcher{Store: raw(hub), Fleet: rh, Grace: 50 * time.Millisecond}
	rh.OnState, rh.OnRevoke, rh.OnReport = d.State, d.Revoked, d.Report
	hub.Dispatch = d
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
	rs, runnerAPI, remote := runnerSide(t, creds.OrgID)
	rs.HubUser = creds.UserID
	rs.Procs, rs.HubUser = runner.NewManager(rs.Hub), creds.UserID
	t.Cleanup(rs.Procs.StopAll)
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, Path: "/hub/has/its/own/path",
		RemoteURL: remote, DefaultBranch: "main", MergeMode: model.MergePR, RunsOn: []string{"any"}})
	if err != nil {
		t.Fatal(err)
	}
	as := func(uid int64) func(*http.Request) *http.Request {
		sess := sessionOf(t, hub, uid, owner.Org)
		return func(r *http.Request) *http.Request { return r.WithContext(auth.WithSession(r.Context(), sess)) }
	}
	start := func(uid int64, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		hub.mux().ServeHTTP(w, as(uid)(httptest.NewRequest("POST", "/api/projects/"+p.ID+"/processes", strings.NewReader(body))))
		return w
	}

	// Another member of the org: refused, by the hub and by the runner.
	if w := start(other, `{"kind":"pty"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "registered it") {
		t.Fatalf("terminal for another member: %d %s", w.Code, w.Body)
	}
	// Nor a command they name, of any kind: it is a shell's.
	marker := filepath.Join(t.TempDir(), "ran")
	for _, kind := range []string{"task", "web", ""} {
		b, _ := json.Marshal(map[string]string{"kind": kind, "command": "touch " + marker})
		if w := start(other, string(b)); w.Code != http.StatusForbidden {
			t.Fatalf("command (%q) for another member: %d %s", kind, w.Code, w.Body)
		}
	}
	// Nor can they make one of the project's own commands run one.
	patch := func(uid int64, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		hub.mux().ServeHTTP(w, as(uid)(httptest.NewRequest("PATCH", "/api/projects/"+p.ID, strings.NewReader(body))))
		return w
	}
	b, _ := json.Marshal(map[string]any{"environments": []model.Environment{{Name: "production", DeployCommand: "touch " + marker}}})
	if w := patch(other, string(b)); w.Code != http.StatusForbidden {
		t.Fatalf("deploy command set by another member: %d %s", w.Code, w.Body)
	}
	if w := patch(other, `{"environments":[],"color":"#3a7bd5"}`); w.Code != http.StatusOK {
		t.Fatalf("other settings, commands unchanged, by another member: %d %s", w.Code, w.Body)
	}
	time.Sleep(200 * time.Millisecond) // a command that got through would have run by now
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("another member's command ran on the owner's runner")
	}

	// A runner they pick: another member can't pick the owner's, and an id
	// that isn't one of the project's runners is refused.
	pick := `{"kind":"pty","runner_id":"` + creds.ID + `"}`
	if w := start(other, pick); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "registered it") {
		t.Fatalf("another member picking the owner's runner: %d %s", w.Code, w.Body)
	}
	if w := start(owner.User, `{"kind":"pty","runner_id":"nope"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("terminal on a runner that isn't the project's: %d %s", w.Code, w.Body)
	}

	// The owner: in the runner's checkout, not a folder the hub names.
	w := start(owner.User, `{"kind":"pty","command":"bash --norc --noprofile","dir":"/etc","cols":80,"rows":24}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("terminal for the owner: %d %s", w.Code, w.Body)
	}
	var proc model.Process
	if err := json.Unmarshal(w.Body.Bytes(), &proc); err != nil {
		t.Fatal(err)
	}
	rp := raw(rs).ForOrg(creds.OrgID).ListProjects()
	checkout := ""
	for _, x := range rp {
		if x.LinkedFromHub() {
			checkout = x.Path
		}
	}
	if checkout == "" || proc.Dir != checkout || !proc.Main || !strings.HasPrefix(proc.ID, creds.ID+"~") {
		t.Fatalf("terminal: %+v (checkout %q)", proc, checkout)
	}

	// On the runner they picked, beside the first: a new terminal, and the
	// first one keeps running. Its process names its runner.
	w = start(owner.User, pick)
	if w.Code != http.StatusCreated {
		t.Fatalf("terminal on the picked runner: %d %s", w.Code, w.Body)
	}
	var picked map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &picked); err != nil {
		t.Fatal(err)
	}
	if id, _ := picked["id"].(string); id == proc.ID || !strings.HasPrefix(id, creds.ID+"~") || picked["runner_id"] != creds.ID || picked["runner"] != "laptop" {
		t.Fatalf("terminal on the picked runner: %v", picked)
	}

	// Its socket through the hub, as the owner and as another member.
	dial := func(uid int64) (*websocket.Conn, error) {
		wrap := as(uid)
		hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hub.mux().ServeHTTP(w, wrap(r)) }))
		t.Cleanup(hs.Close)
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hs.URL, "http")+"/ws/proc/"+proc.ID, nil)
		return ws, err
	}
	if ws, err := dial(other); err == nil {
		ws.Close()
		t.Fatal("another member attached to the owner's terminal")
	}
	ws, err := dial(owner.User)
	if err != nil {
		t.Fatal(err)
	}
	read := func(ws *websocket.Conn, want string) string {
		t.Helper()
		var out strings.Builder
		_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
		for !strings.Contains(out.String(), want) {
			_, b, err := ws.ReadMessage()
			if err != nil {
				t.Fatalf("waiting for %q: %v\n%s", want, err, out.String())
			}
			out.Write(b)
		}
		return out.String()
	}
	send := func(msg map[string]any) {
		t.Helper()
		if err := ws.WriteJSON(msg); err != nil {
			t.Fatal(err)
		}
	}
	send(map[string]any{"type": "resize", "cols": 101, "rows": 33})
	send(map[string]any{"type": "input", "data": "pwd; stty size; echo hub-$((40+2))\r"})
	out := read(ws, "hub-42\r\n")
	if !strings.Contains(out, checkout) || !strings.Contains(out, "33 101") {
		t.Errorf("terminal output through the hub:\n%s", out)
	}
	ws.Close()

	// Reattached: the scrollback comes first.
	ws, err = dial(owner.User)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	read(ws, "hub-42\r\n")

	// Stopping it: the owner's too.
	stop := func(uid int64) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		hub.mux().ServeHTTP(w, as(uid)(httptest.NewRequest("DELETE", "/api/processes/"+proc.ID, nil)))
		return w
	}
	if w := stop(other); w.Code != http.StatusForbidden {
		t.Fatalf("another member stopping the owner's terminal: %d %s", w.Code, w.Body)
	}
	if w := stop(owner.User); w.Code >= 300 {
		t.Fatalf("the owner stopping their terminal: %d %s", w.Code, w.Body)
	}
}

// A terminal opens on the runner the caller picks only when it is one of
// theirs that works on the project and is online.
func TestShellRunnerPick(t *testing.T) {
	parallel(t)

	me := Caller{User: 1, Org: 10}
	own := &store.RunnerOwner{UserID: 1, Org: store.Org{ID: 10}}
	other := &store.RunnerOwner{UserID: 2, Org: store.Org{ID: 10}}
	runners := []tunnel.Runner{
		{ID: "mine", Name: "laptop", Online: true, Owner: own},
		{ID: "off", Name: "desk", Online: false, Owner: own},
		{ID: "theirs", Name: "theirs", Online: true, Owner: other},
	}
	if id, err := shellRunner(runners, me, "mine"); err != nil || id != "mine" {
		t.Errorf("own online runner: %q %v", id, err)
	}
	for _, c := range []struct {
		id   string
		want error
	}{
		{"off", errNoRunnerOnline},
		{"theirs", errForbidden},
		{"elsewhere", errBadRequest}, // not one of the project's runners
	} {
		if _, err := shellRunner(runners, me, c.id); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.id, err, c.want)
		}
	}
	if _, err := shellRunner(runners, Caller{Org: 10, Machine: true}, "mine"); !errors.Is(err, errForbidden) {
		t.Errorf("a caller with no user: %v", err)
	}
}

// A runner pushes with its owner's git login, so a member's pushes (push,
// tag, branch, pull request, a checklist save) never run on another
// member's runner, even when none of theirs is online; their reads still
// fall back to it.
func TestHubPushesOnlyOnTheCallersOrASharedRunner(t *testing.T) {
	parallel(t)

	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	d := &dispatch.Dispatcher{Store: raw(hub), Fleet: rh, Grace: 50 * time.Millisecond}
	rh.OnState, rh.OnRevoke, rh.OnReport = d.State, d.Revoked, d.Report
	hub.Dispatch = d
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, RemoteURL: remote,
		DefaultBranch: "main", MergeMode: model.MergeLocal, RunsOn: []string{"any"}})
	if err != nil {
		t.Fatal(err)
	}
	mid := member(t, hub, owner.Org, "m", store.RoleMember)
	m := loginAs(t, hub.Routes(), "m", "long-enough-m")

	for _, rt := range []struct{ method, path, body string }{
		{"POST", "/push", ""}, {"PUT", "/branch", `{"branch":"dev","create":true}`}, {"POST", "/branch/pr", `{}`},
	} {
		w := send(m, rt.method, "/api/projects/"+p.ID+rt.path, rt.body)
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "none of yours, or a shared one") {
			t.Errorf("member's %s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
	sv := hub.as(Caller{User: mid, Org: owner.Org})
	if _, err := sv.changeChecklist(ctx, p, mcptools.ChecklistChange{Ops: []checklist.Op{{Op: "add", Text: "x"}}}); err == nil ||
		!strings.Contains(err.Error(), "none of yours, or a shared one") {
		t.Errorf("member's checklist save: %v", err)
	}
	// A read still goes to the owner's runner.
	if w := send(m, "GET", "/api/projects/"+p.ID+"/branches", ""); w.Code != http.StatusOK {
		t.Errorf("member's GET /branches: %d %s", w.Code, w.Body)
	}
}
