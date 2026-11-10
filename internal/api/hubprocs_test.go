package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/dispatch"
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
	rs.Procs, rs.HubCommands = runner.NewManager(rs.Hub), true
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, Path: "/hub/has/its/own/path",
		RemoteURL: remote, DefaultBranch: "main", MergeMode: model.MergePR, Runners: []string{creds.ID},
		TestCommand: "true", DeployCommand: "exit 3"})
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
	deploy := start(`{"kind":"task","preset":"deploy"}`)
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
		return len(ps) == 3 && !ps[test.ID].Running && !ps[deploy.ID].Running && !ps[own.ID].Running
	})
	ps := list()
	if x := ps[test.ID]; x.Preset != "test" || !x.Main || x.ExitCode == nil || *x.ExitCode != 0 {
		t.Errorf("listed test run: %+v", x)
	}
	if x := ps[deploy.ID]; x.Preset != "deploy" || !x.Main || x.ExitCode == nil || *x.ExitCode != 3 {
		t.Errorf("listed deploy: %+v", x)
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

// A hub opens a terminal on a project's runner only when the runner takes
// commands from the hub (-hub-commands), and only for the user who
// registered the runner: another member of the org gets neither a new one
// nor the owner's. The terminal starts in the runner's checkout, whatever
// the hub asks, and its socket carries keyboard input and resizes through
// the hub, with the scrollback again on reattaching.
func TestHubOpensTerminalsOnlyForTheRunnersOwner(t *testing.T) {
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
	rs.Procs, rs.HubUser = runner.NewManager(rs.Hub), creds.UserID
	t.Cleanup(rs.Procs.StopAll)
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, Path: "/hub/has/its/own/path",
		RemoteURL: remote, DefaultBranch: "main", MergeMode: model.MergePR, Runners: []string{creds.ID}})
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

	// Without -hub-commands: refused, naming the flag.
	if w := start(owner.User, `{"kind":"pty"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "-hub-commands") {
		t.Fatalf("terminal without -hub-commands: %d %s", w.Code, w.Body)
	}

	rs.HubCommands = true
	// Another member of the org: refused, by the hub and by the runner.
	if w := start(other, `{"kind":"pty"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "registered it") {
		t.Fatalf("terminal for another member: %d %s", w.Code, w.Body)
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
}
