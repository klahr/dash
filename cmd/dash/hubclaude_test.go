package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/tunnel"
)

// A hub started with -hub-claude off never runs `claude` and never calls
// the Anthropic API, whatever its users do: the general and project chats,
// cleaning up instructions, untitled and queued tasks, scans. A fake
// claude that leaves a mark and a fake API that counts its calls catch any
// code path that would.
func TestHubWithoutClaudeNeverRunsIt(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs dash")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "dash")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	mark := filepath.Join(dir, "claude-ran")
	fake := filepath.Join(dir, "claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"$@\" >> "+mark+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var apiCalls atomic.Int32
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		http.Error(w, `{"type":"error","error":{"type":"authentication_error","message":"no"}}`, http.StatusUnauthorized)
	}))
	defer anthropic.Close()

	addr := freeAddr(t)
	cmd := exec.Command(bin, "-addr", addr, "-data", filepath.Join(dir, "data"), "-projects", filepath.Join(dir, "projects"),
		"-claude", fake, "-hub-claude", "off", "-chat", "api", "-env", filepath.Join(dir, "none.env"))
	cmd.Env = append(os.Environ(), "DASH_USERNAME=admin", "DASH_PASSWORD=a-long-enough-password",
		"ANTHROPIC_BASE_URL="+anthropic.URL, "ANTHROPIC_API_KEY=sk-test", "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("dash's log:\n%s", logs.String())
		}
	}()
	base := "http://" + addr
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if res, err := c.Get(base + "/api/version"); err == nil {
			res.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dash didn't start")
		}
	}
	call := func(method, path string, body any) (int, string) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(out)
	}
	if code, body := call("POST", "/api/login", map[string]string{"username": "admin", "password": "a-long-enough-password"}); code != http.StatusOK {
		t.Fatalf("login: %d %s", code, body)
	}
	code, body := call("POST", "/api/projects", map[string]string{"name": "app"})
	var p struct{ ID string }
	if code != http.StatusCreated && code != http.StatusOK || json.Unmarshal([]byte(body), &p) != nil || p.ID == "" {
		t.Fatalf("create project: %d %s", code, body)
	}

	// Chats: the general one (no runner of the user's) and the project's.
	u, _ := url.Parse(base)
	hdr := http.Header{"Cookie": {cookieHeader(jar.Cookies(u))}}
	for _, target := range []map[string]any{{}, {"project": p.ID}} {
		ws, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws/chat", hdr)
		if err != nil {
			t.Fatal(err)
		}
		msg := map[string]any{"type": "send", "messages": []map[string]string{{"role": "user", "content": "hi"}}}
		for k, v := range target {
			msg[k] = v
		}
		_ = ws.WriteJSON(msg)
		_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
		for {
			var d struct{ Kind, Text string }
			if err := ws.ReadJSON(&d); err != nil {
				t.Fatalf("chat %v: %v", target, err)
			}
			if d.Kind == "done" {
				t.Errorf("chat %v answered on the hub", target)
			}
			if d.Kind == "done" || d.Kind == "error" {
				break
			}
		}
		ws.Close()
	}
	// Cleaning up instructions: no runner, so it says so.
	if code, body := call("POST", "/api/refine", map[string]string{"prompt": "hey, please add a dark mode"}); code != http.StatusServiceUnavailable || !strings.Contains(body, "runner") {
		t.Errorf("refine: %d %s", code, body)
	}
	// An untitled task, queued: named from its first line, and it waits.
	code, body = call("POST", "/api/projects/"+p.ID+"/todos", map[string]any{"prompt": "Add a dark mode\nwith a toggle", "enqueue": true})
	var td struct{ ID string }
	if code != http.StatusCreated || json.Unmarshal([]byte(body), &td) != nil {
		t.Fatalf("create task: %d %s", code, body)
	}
	// Scans, by hand.
	if code, body := call("GET", "/api/projects/"+p.ID+"/scans", nil); code == http.StatusOK {
		var scans []struct{ Name string }
		_ = json.Unmarshal([]byte(body), &scans)
		for _, s := range scans {
			call("POST", "/api/projects/"+p.ID+"/scans/"+s.Name+"/run", nil)
		}
	}
	var task struct{ Title, Status, Waiting string }
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		_, body := call("GET", "/api/todos/"+td.ID, nil)
		_ = json.Unmarshal([]byte(body), &task)
		if task.Title != "" && task.Waiting != "" || time.Now().After(deadline) {
			break
		}
	}
	if task.Title != "Add a dark mode" || task.Status != "queued" {
		t.Errorf("task: %+v", task)
	}
	time.Sleep(500 * time.Millisecond) // anything started in the background
	if b, err := os.ReadFile(mark); err == nil {
		t.Errorf("the hub ran claude: %s", b)
	}
	if n := apiCalls.Load(); n > 0 {
		t.Errorf("the hub called the Anthropic API %d times", n)
	}
}

func TestHubClaudePolicy(t *testing.T) {
	runners, users := &fakeRunners{}, &fakeUsers{n: 1}
	for mode, want := range map[string]bool{"on": true, "true": true, "off": false, "false": false, "auto": true} {
		f, err := hubClaudePolicy(mode, runners, users)
		if err != nil || f() != want {
			t.Errorf("%s: %v %v", mode, err, err == nil && f())
		}
	}
	if _, err := hubClaudePolicy("maybe", runners, users); err == nil {
		t.Error("accepted -hub-claude maybe")
	}
	auto, _ := hubClaudePolicy("auto", runners, users)
	users.n = 2
	if auto() {
		t.Error("auto: on with a second user")
	}
	users.n, runners.list = 1, []tunnel.Runner{{ID: "r"}}
	if auto() {
		t.Error("auto: on with a runner registered")
	}
}

type fakeRunners struct{ list []tunnel.Runner }

func (f *fakeRunners) List() []tunnel.Runner { return f.list }

type fakeUsers struct{ n int }

func (f *fakeUsers) UserCount() (int, error) { return f.n, nil }

func freeAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func cookieHeader(cs []*http.Cookie) string {
	var parts []string
	for _, c := range cs {
		parts = append(parts, fmt.Sprintf("%s=%s", c.Name, c.Value))
	}
	return strings.Join(parts, "; ")
}
