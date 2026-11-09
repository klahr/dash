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
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/preview"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// Try runs only on a runner that is connected, has the project linked and
// opens terminals for the caller (its owner); with none, Try says why.
func TestTryRunnerEligibility(t *testing.T) {
	me := Caller{User: 1, Org: 10}
	own := &store.RunnerOwner{UserID: 1, Org: store.Org{ID: 10}}
	other := &store.RunnerOwner{UserID: 2, Org: store.Org{ID: 10}}
	terms := &tunnel.Reported{Terminals: true}
	p := model.Project{ID: "p", OrgID: 10, Runners: []string{"ok", "off", "noterm", "notmine", "wrongorg"}}
	fleet := []tunnel.Runner{
		{ID: "ok", Name: "laptop", Online: true, Owner: own, Reported: terms},
		{ID: "off", Name: "desk", Online: false, Owner: own, Reported: terms},
		{ID: "noterm", Name: "ci", Online: true, Owner: own, Reported: &tunnel.Reported{}},
		{ID: "notmine", Name: "theirs", Online: true, Owner: other, Reported: terms},
		{ID: "wrongorg", Name: "elsewhere", Online: true, Owner: &store.RunnerOwner{UserID: 1, Org: store.Org{ID: 11}}, Reported: terms},
		{ID: "unlinked", Name: "spare", Online: true, Owner: own, Reported: terms},
	}
	got, why := tryRunners(fleet, p, me)
	if len(got) != 1 || got[0].ID != "ok" || got[0].Name != "laptop" || why != "" {
		t.Fatalf("eligible: %+v %q", got, why)
	}
	if r, err := pickRunner(got, "", why); err != nil || r.ID != "ok" {
		t.Errorf("the only eligible runner isn't picked by itself: %+v %v", r, err)
	}
	if _, err := pickRunner(got, "notmine", why); err == nil {
		t.Error("picked a runner that isn't eligible")
	}

	for _, c := range []struct {
		name   string
		fleet  []tunnel.Runner
		linked []string
		want   string
	}{
		{"nothing linked", fleet, nil, "No runner is linked"},
		{"linked runner offline", fleet, []string{"off"}, "is connected"},
		{"no terminals", fleet, []string{"off", "noterm"}, "allows terminals"},
		{"another member's", fleet, []string{"notmine"}, "your own runner"},
	} {
		q := p
		q.Runners = c.linked
		got, why := tryRunners(c.fleet, q, me)
		if len(got) != 0 || !strings.Contains(why, c.want) {
			t.Errorf("%s: %+v %q, want %q", c.name, got, why, c.want)
		}
		if _, err := pickRunner(got, "", why); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: start without a runner: %v", c.name, err)
		}
	}
}

// commitTool commits a tool.sh that prints version to the remote's main.
func commitTool(t *testing.T, remote, version string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(remote, "tool.sh"), []byte("#!/bin/sh\necho tool-"+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, remote, "add", "tool.sh")
	gitIn(t, remote, "commit", "-q", "-m", "tool "+version)
	return gitIn(t, remote, "rev-parse", "HEAD")
}

// A try session runs on the runner, in a detached worktree Dash makes from
// the runner's clone at origin's main as fetched now, never in the clone's
// own checkout; switching commit replaces it; stopping (by hand or idle)
// removes the worktree. The hub reaches it all over the runner's tunnel:
// the commit list, the terminal, and its output for the chat.
func TestTryOnARunnerThroughTheHub(t *testing.T) {
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
	rs.Procs, rs.HubCommands, rs.HubUser = runner.NewManager(rs.Hub), true, creds.UserID
	t.Cleanup(rs.Procs.StopAll)
	rs.Previews = preview.New(rs.Procs, rs.Hub, raw(rs), "")
	rs.TryRoot = t.TempDir()
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	v1 := commitTool(t, remote, "v1")
	p, err := raw(hub).CreateProject(model.Project{Name: "cli", OrgID: owner.Org, RemoteURL: remote, DefaultBranch: "main",
		Runners: []string{creds.ID}, PreviewKind: model.PreviewTerminal,
		PreviewCommand: `cp tool.sh "$DASH_BIN/tool"; echo built-$(git rev-parse --short HEAD); exec bash --norc --noprofile`})
	if err != nil {
		t.Fatal(err)
	}
	sess := sessionOf(t, hub, owner.User, owner.Org)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.mux().ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), sess)))
	})
	state := func() tryState {
		t.Helper()
		w := do(t, h, "GET", "/api/projects/"+p.ID+"/try", "")
		var st tryState
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &st) != nil {
			t.Fatalf("try state: %d %s", w.Code, w.Body)
		}
		return st
	}

	// The runner hasn't said it opens terminals: Try is off, and says why.
	if st := state(); len(st.Runners) != 0 || !strings.Contains(st.Disabled, "allows terminals") {
		t.Fatalf("before the runner allows terminals: %+v", st)
	}
	if w := do(t, h, "POST", "/api/projects/"+p.ID+"/try", `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("try without an eligible runner: %d %s", w.Code, w.Body)
	}
	if err := tunnel.Report(ctx, creds, dispatch.Report{Terminals: true}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the runner's report", func() bool { r, _ := rh.Get(creds.ID); return r.Reported != nil && r.Reported.Terminals })
	if st := state(); len(st.Runners) != 1 || st.Default != creds.ID || st.Disabled != "" || st.Session != nil {
		t.Fatalf("eligible runner: %+v", st)
	}

	// Commits come from the runner's clone (linked, cloned and fetched on the way).
	v2 := commitTool(t, remote, "v2")
	w := do(t, h, "GET", "/api/projects/"+p.ID+"/try/commits?fetch=1", "")
	var list struct {
		Branch  string
		Commits []model.CommitInfo
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil {
		t.Fatalf("commits: %d %s", w.Code, w.Body)
	}
	if list.Branch != "main" || len(list.Commits) < 2 || list.Commits[0].SHA != v2 || list.Commits[1].SHA != v1 ||
		list.Commits[0].Subject != "tool v2" || list.Commits[0].Author != "t" || list.Commits[0].Short == "" || list.Commits[0].Date.IsZero() {
		t.Fatalf("commits: %+v", list)
	}
	w = do(t, h, "GET", "/api/projects/"+p.ID+"/try/commits?q=V1", "")
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Commits) != 1 || list.Commits[0].SHA != v1 {
		t.Fatalf("searched commits: %s", w.Body)
	}

	// The clone, with its owner's own work in it: Try never touches it.
	rp, ok := rs.as(Caller{Org: rs.HubOrg, Machine: true}).hubProject(p.ID)
	if !ok {
		t.Fatal("the project wasn't linked to the runner")
	}
	cloneHead := gitIn(t, rp.Path, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(rp.Path, "wip.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	cloneStatus := gitIn(t, rp.Path, "status", "--porcelain")
	v3 := commitTool(t, remote, "v3") // after the clone: only a fetch finds it

	start := func(body string) model.TrySession {
		t.Helper()
		w := do(t, h, "POST", "/api/projects/"+p.ID+"/try", body)
		var s model.TrySession
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &s) != nil {
			t.Fatalf("start try %s: %d %s", body, w.Code, w.Body)
		}
		return s
	}
	tip := start(`{}`)
	if tip.Commit != v3 || tip.Subject != "tool v3" || tip.Runner != "laptop" || tip.RunnerID != creds.ID || tip.State != model.PreviewLive ||
		tip.TerminalURL != "/ws/projects/"+p.ID+"/try" || tip.Branch != "main" {
		t.Fatalf("try at main: %+v", tip)
	}
	dirs := func() []string {
		es, _ := os.ReadDir(filepath.Join(rs.TryRoot, rp.ID))
		var out []string
		for _, e := range es {
			out = append(out, filepath.Join(rs.TryRoot, rp.ID, e.Name()))
		}
		return out
	}
	wt := dirs()
	if len(wt) != 1 || gitIn(t, wt[0], "rev-parse", "HEAD") != v3 {
		t.Fatalf("try worktree: %v", wt)
	}
	if b := gitIn(t, wt[0], "rev-parse", "--abbrev-ref", "HEAD"); b != "HEAD" {
		t.Errorf("try worktree is on branch %q, want detached", b)
	}

	// The terminal, through the hub: the build is the commit's.
	hs := httptest.NewServer(h)
	defer hs.Close()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + tip.TerminalURL
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	readWS(t, ws, "built-")
	if err := ws.WriteJSON(map[string]any{"type": "input", "data": "tool\r"}); err != nil {
		t.Fatal(err)
	}
	readWS(t, ws, "tool-v3")
	ws.Close()

	// The chat reads it with read_try_terminal: output, runner and commit.
	token, release := hub.Board.Register(chatCaller(t, hub), p.ID)
	defer release()
	eventually(t, "the output to reach the log", func() bool {
		text, isErr := callResult(t, rpc(hub.Routes(), token, "tools/call", `{"name":"read_try_terminal","arguments":{"lines":20}}`))
		return !isErr && strings.Contains(text, "tool-v3") && strings.Contains(text, v3) && strings.Contains(text, `"runner": "laptop"`)
	})
	w = do(t, h, "GET", "/api/projects/"+p.ID+"/try/log?lines=5", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "tool-v3") {
		t.Fatalf("try log: %d %s", w.Code, w.Body)
	}
	// And the project chat is told about the session.
	if note := hub.tryContext(ctx, ownerCaller(t, hub), p); !strings.Contains(note, v3) || !strings.Contains(note, "report_issue") ||
		!strings.Contains(note, "create_todo") || !strings.Contains(note, "read_try_terminal") {
		t.Errorf("chat context: %q", note)
	}

	// Opening Try again reattaches to it.
	if st := state(); st.Session == nil || st.Session.ProcessID != tip.ProcessID {
		t.Fatalf("session after start: %+v", st.Session)
	}
	// Also after the hub restarted and forgot where it runs.
	hub.tries.set(p.ID, "")
	if st := state(); st.Session == nil || st.Session.ProcessID != tip.ProcessID || st.Session.Runner != "laptop" {
		t.Fatalf("session after the hub forgot it: %+v", st.Session)
	}

	// Switching to an older commit replaces the session and its worktree.
	old := start(`{"ref":"` + v1[:10] + `"}`)
	if old.Commit != v1 || old.Ref != v1[:10] || old.ProcessID == tip.ProcessID {
		t.Fatalf("try at v1: %+v", old)
	}
	wt2 := dirs()
	if len(wt2) != 1 || wt2[0] == wt[0] || gitIn(t, wt2[0], "rev-parse", "HEAD") != v1 {
		t.Fatalf("worktrees after switching: %v (was %v)", wt2, wt)
	}
	if _, err := os.Stat(wt[0]); !os.IsNotExist(err) {
		t.Errorf("the old try worktree is still there: %v", err)
	}
	ws, _, err = websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteJSON(map[string]any{"type": "input", "data": "tool\r"}); err != nil {
		t.Fatal(err)
	}
	readWS(t, ws, "tool-v1")
	ws.Close()

	// A bad ref is refused before it reaches git.
	if w := do(t, h, "POST", "/api/projects/"+p.ID+"/try", `{"ref":"--upload-pack=x"}`); w.Code != http.StatusBadRequest {
		t.Errorf("option as ref: %d %s", w.Code, w.Body)
	}

	// Stopping removes the worktree; the session is gone.
	if w := do(t, h, "DELETE", "/api/projects/"+p.ID+"/try", ""); w.Code != http.StatusNoContent {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	if d := dirs(); len(d) != 0 {
		t.Errorf("worktrees after stop: %v", d)
	}
	if st := state(); st.Session != nil {
		t.Errorf("session after stop: %+v", st.Session)
	}
	if text, isErr := callResult(t, rpc(hub.Routes(), token, "tools/call", `{"name":"read_try_terminal","arguments":{}}`)); !isErr || !strings.Contains(text, "no Try terminal") {
		t.Errorf("read after stop: %v %q", isErr, text)
	}

	// Idle: no keystrokes for IdleAfter stops it, and its worktree goes.
	start(`{}`)
	if len(dirs()) != 1 {
		t.Fatal("no worktree for the new session")
	}
	rs.Previews.IdleAfter = time.Minute
	rs.Previews.Sweep(time.Now().Add(2 * time.Minute))
	if _, ok := rs.Previews.Try(p.ID); ok {
		t.Error("an idle try session kept running")
	}
	if d := dirs(); len(d) != 0 {
		t.Errorf("worktrees after idle stop: %v", d)
	}

	// The clone's own checkout never moved, and its owner's work is untouched.
	if h := gitIn(t, rp.Path, "rev-parse", "HEAD"); h != cloneHead {
		t.Errorf("clone HEAD moved: %s, was %s", h, cloneHead)
	}
	if st := gitIn(t, rp.Path, "status", "--porcelain"); st != cloneStatus {
		t.Errorf("clone status changed: %q, was %q", st, cloneStatus)
	}
	if b := gitIn(t, rp.Path, "rev-parse", "--abbrev-ref", "HEAD"); b != "main" {
		t.Errorf("clone is on %q", b)
	}
}

// Only the hub reaches a runner's try routes, for projects linked to it;
// the terminal ones only for the runner's owner, with -hub-commands.
func TestRunnerTryRoutesAreGuarded(t *testing.T) {
	s, h, _ := runnerSide(t, 7)
	s.Procs = runner.NewManager(s.Hub)
	s.Previews = preview.New(s.Procs, s.Hub, raw(s), "")
	if w := send(h, "POST", "/api/hub/projects/hub-p1/try", `{}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "-hub-commands") {
		t.Errorf("try without -hub-commands: %d %s", w.Code, w.Body)
	}
	s.HubCommands, s.HubUser = true, 5
	r := httptest.NewRequest("POST", "/api/hub/projects/hub-p1/try", strings.NewReader(`{}`))
	r.Header.Set(tunnel.UserHeader, "5")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "isn't linked") {
		t.Errorf("try of a project not linked: %d %s", w.Code, w.Body)
	}
	if w := send(h, "GET", "/api/hub/projects/hub-p1/commits", ""); w.Code != http.StatusForbidden {
		t.Errorf("commits of a project not linked: %d %s", w.Code, w.Body)
	}
	// Not through the runner's own dashboard.
	w = httptest.NewRecorder()
	s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("GET", "/api/hub/projects/hub-p1/commits", nil)))
	if w.Code != http.StatusForbidden {
		t.Errorf("commits not from the hub: %d %s", w.Code, w.Body)
	}
}

// read_try_terminal is a read-only board tool, offered with the board tools.
func TestReadTryTerminalIsAReadTool(t *testing.T) {
	for _, list := range [][]string{mcptools.ReadTools, mcptools.BoardTools} {
		found := false
		for _, n := range list {
			found = found || n == "read_try_terminal"
		}
		if !found {
			t.Errorf("read_try_terminal missing from %v", list)
		}
	}
}
