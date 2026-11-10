package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/preview"
	"github.com/klahr/dash/internal/qr"
	"github.com/klahr/dash/internal/runner"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// readWS reads ws's terminal output until it contains want.
func readWS(t *testing.T, ws *websocket.Conn, want string) string {
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

// reviewTodo is a task of project pid in review, with a worktree.
func reviewTodo(t *testing.T, st *store.Store, pid string) model.Todo {
	t.Helper()
	td, err := st.CreateTodo(model.Todo{ID: store.NewID(), ProjectID: pid, Title: "CLI flag"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, status := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusReview} {
		if td, err = st.UpdateTodo(td.ID, func(x *model.Todo) error {
			x.Status, x.Worktree = status, &model.Worktree{Path: dir, Branch: "dash/todo-1"}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return td
}

// A web project needs a start server command to be previewed; a terminal
// project doesn't: its preview is a terminal in the task's worktree, which
// the UI attaches to over a WebSocket, and whose last lines can be sent back.
func TestTerminalPreviewWithoutRunCommand(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	s.Procs = runner.NewManager(s.Hub)
	t.Cleanup(s.Procs.StopAll)
	s.Previews = preview.New(s.Procs, s.Hub, raw(s), "preview.dash.test")
	td := reviewTodo(t, raw(s), p.ID)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.mux().ServeHTTP(w, asOwner(t, s, r)) })

	// Web (the default): no run_command, no preview.
	if w := do(t, h, "POST", "/api/todos/"+td.ID+"/preview", ""); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "run_command") {
		t.Fatalf("web preview without run_command: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"preview_kind":"gui"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown preview_kind: %d %s", w.Code, w.Body)
	}

	w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"preview_kind":"terminal","preview_command":" echo ready-$((6*7)); exec bash --norc --noprofile "}`)
	var got model.Project
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("set terminal preview: %d %s", w.Code, w.Body)
	}
	if got.PreviewKind != model.PreviewTerminal || got.PreviewCommand != "echo ready-$((6*7)); exec bash --norc --noprofile" || got.RunCommand != "" {
		t.Fatalf("project: %+v", got)
	}
	if w := do(t, h, "GET", "/api/projects/"+p.ID, ""); !strings.Contains(w.Body.String(), `"preview_kind":"terminal"`) {
		t.Fatalf("served project: %s", w.Body)
	}

	w = do(t, h, "POST", "/api/todos/"+td.ID+"/preview", "")
	var pv model.Preview
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &pv) != nil {
		t.Fatalf("terminal preview: %d %s", w.Code, w.Body)
	}
	if pv.Kind != model.PreviewTerminal || pv.State != model.PreviewLive || pv.URL != "" || pv.TerminalURL != "/ws/todos/"+td.ID+"/preview" {
		t.Fatalf("preview: %+v", pv)
	}

	srv := httptest.NewServer(h)
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+pv.TerminalURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	readWS(t, ws, "ready-42")
	if err := ws.WriteJSON(map[string]any{"type": "input", "data": "echo typed-$((1+1))\r"}); err != nil {
		t.Fatal(err)
	}
	readWS(t, ws, "typed-2")

	w = do(t, h, "GET", "/api/todos/"+td.ID+"/preview/log?lines=50", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ready-42") || !strings.Contains(w.Body.String(), "typed-2") || strings.Contains(w.Body.String(), "\x1b") {
		t.Fatalf("log: %d %q", w.Code, w.Body)
	}
	if w := do(t, h, "GET", "/api/todos/"+td.ID+"/preview/log?lines=x", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bad lines: %d", w.Code)
	}

	// A web preview has no terminal to attach to.
	if w := do(t, h, "DELETE", "/api/todos/"+td.ID+"/preview", ""); w.Code != http.StatusNoContent {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"preview_kind":"web","run_command":"sleep 30"}`); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "preview_kind") {
		t.Fatalf("back to web: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "POST", "/api/todos/"+td.ID+"/preview", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"kind":"web"`) {
		t.Fatalf("web preview: %d %s", w.Code, w.Body)
	}
	if _, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+pv.TerminalURL, nil); err == nil {
		t.Fatal("attached to a web preview as a terminal")
	}
}

func TestQRCode(t *testing.T) {
	parallel(t)

	s := &Server{}
	get := func(text string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.qrCode(w, httptest.NewRequest("GET", "/api/qr?text="+url.QueryEscape(text), nil))
		return w
	}

	w := get("https://example.com/preview?todo=abc")
	if w.Code != http.StatusOK {
		t.Fatalf("valid URL: %d %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type: %q", ct)
	}
	if !bytes.HasPrefix(w.Body.Bytes(), []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("body is not a PNG: % x", w.Body.Bytes()[:min(8, w.Body.Len())])
	}
	img, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != b.Dy() || b.Dx() < 200 || b.Dx() > qr.Size {
		t.Fatalf("unexpected image size %v", b)
	}
	// The quiet zone leaves the corner white.
	if r, g, b, _ := img.At(0, 0).RGBA(); r != 0xffff || g != 0xffff || b != 0xffff {
		t.Fatalf("corner pixel is not white: %v", img.At(0, 0))
	}

	for name, text := range map[string]string{
		"no scheme": "example.com/preview",
		"too long":  "https://example.com/" + strings.Repeat("a", 1024),
	} {
		if w := get(text); w.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", name, w.Code)
		}
	}
}

// A runner's terminal preview, reached from the hub through the peer proxy
// (/peer/{runner}/…): it is a shell, so like the runner's terminals it opens
// only with -hub-commands and for the user who registered the runner; its
// socket carries keystrokes both ways, and its log comes back as text.
func TestRunnerTerminalPreviewThroughTheHub(t *testing.T) {
	parallel(t)

	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
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
	rs, runnerAPI, _ := runnerSide(t, creds.OrgID)
	rs.Procs, rs.HubUser = runner.NewManager(rs.Hub), creds.UserID
	t.Cleanup(rs.Procs.StopAll)
	rs.Previews = preview.New(rs.Procs, rs.Hub, raw(rs), "")
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	p, err := raw(rs).CreateProject(model.Project{Name: "cli", Path: t.TempDir(), PreviewKind: model.PreviewTerminal,
		PreviewCommand: "echo on-the-runner; exec bash --norc --noprofile"})
	if err != nil {
		t.Fatal(err)
	}
	td := reviewTodo(t, raw(rs), p.ID)

	sess := sessionOf(t, hub, owner.User, owner.Org)
	as := func(r *http.Request) *http.Request { return r.WithContext(auth.WithSession(r.Context(), sess)) }
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hub.mux().ServeHTTP(w, as(r)) })
	start := "/peer/laptop/api/todos/" + td.ID + "/preview"

	// Without -hub-commands the runner opens no shell for the hub.
	if w := do(t, h, "POST", start, ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "-hub-commands") {
		t.Fatalf("terminal preview without -hub-commands: %d %s", w.Code, w.Body)
	}
	// Nor for anyone but its owner.
	rs.HubCommands, rs.HubUser = true, creds.UserID+1000
	if w := do(t, h, "POST", start, ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "registered it") {
		t.Fatalf("terminal preview for another user: %d %s", w.Code, w.Body)
	}
	rs.HubUser = creds.UserID

	w := do(t, h, "POST", start, "")
	var pv model.Preview
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &pv) != nil {
		t.Fatalf("terminal preview: %d %s", w.Code, w.Body)
	}
	if pv.Kind != model.PreviewTerminal || pv.State != model.PreviewLive || pv.TerminalURL != "/ws/todos/"+td.ID+"/preview" {
		t.Fatalf("preview: %+v", pv)
	}

	hs := httptest.NewServer(h)
	defer hs.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hs.URL, "http")+"/peer/laptop"+pv.TerminalURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	readWS(t, ws, "on-the-runner")
	if err := ws.WriteJSON(map[string]any{"type": "input", "data": "echo via-hub-$((40+2))\r"}); err != nil {
		t.Fatal(err)
	}
	readWS(t, ws, "via-hub-42")

	w = do(t, h, "GET", "/peer/laptop/api/todos/"+td.ID+"/preview/log?lines=20", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "via-hub-42") {
		t.Fatalf("log through the hub: %d %q", w.Code, w.Body)
	}

	// Another owner on the runner's side can't attach to it either.
	rs.HubUser = creds.UserID + 1000
	if ws2, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hs.URL, "http")+"/peer/laptop"+pv.TerminalURL, nil); err == nil {
		ws2.Close()
		t.Fatal("attached to the terminal preview for another user")
	}
}
