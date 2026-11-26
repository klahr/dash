package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/testbin"
)

func TestParseRESTRun(t *testing.T) {
	t.Parallel()
	r, err := ParseRESTRun([]byte(`{"id":42,"name":"Release","display_title":"v1.0.0","status":"completed","conclusion":null,
		"head_sha":"abc","head_branch":"v1.0.0","html_url":"https://github.com/o/r/actions/runs/42","event":"push",
		"created_at":"2026-11-25T10:00:00Z","run_started_at":"2026-11-25T10:00:05Z","updated_at":"2026-11-25T10:03:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != 42 || r.Workflow != "Release" || r.Branch != "v1.0.0" || r.SHA != "abc" || r.Conclusion != "" || r.Event != "push" ||
		r.URL != "https://github.com/o/r/actions/runs/42" || r.StartedAt.IsZero() || r.UpdatedAt.IsZero() {
		t.Fatalf("%+v", r)
	}
	if _, err := ParseRESTRun([]byte(`{"message":"Not Found"}`)); err == nil {
		t.Fatal("no run: want an error")
	}
}

func TestGitHubHTTPStatus(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		err  error
		want int
	}{
		{nil, 0},
		{errors.New("exit status 1"), 0},
		{&CmdError{Args: []string{"gh", "api"}, Output: "gh: Not Found (HTTP 404)\n", Err: errors.New("exit status 1")}, 404},
		{errors.New("runner: gh api repos/{owner}/{repo}/hooks: exit status 1: gh: Resource not accessible by integration (HTTP 403)"), 403},
	} {
		if got := GitHubHTTPStatus(c.err); got != c.want {
			t.Errorf("%v: got %d, want %d", c.err, got, c.want)
		}
	}
}

// The webhook's secret goes to gh on stdin: never on its command line.
func TestCreateHookSecretOnStdin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	args, input := filepath.Join(dir, "args"), filepath.Join(dir, "input")
	gh := filepath.Join(dir, "gh")
	testbin.Write(t, gh, `#!/bin/sh
echo "$*" >> `+args+`
cat > `+input+`
echo '{"id":7,"config":{"url":"https://dash.example.com/api/github/hook","secret":"********"}}'
`)
	g := New(nil)
	g.GhBin = gh
	id, err := g.CreateHook(context.Background(), "", dir, HookConfig{URL: "https://dash.example.com/api/github/hook", Secret: "topsecret", Events: []string{"workflow_run"}})
	if err != nil || id != 7 {
		t.Fatalf("id %d, %v", id, err)
	}
	a, _ := os.ReadFile(args)
	in, _ := os.ReadFile(input)
	if strings.Contains(string(a), "topsecret") || !strings.Contains(string(a), "api -X POST repos/{owner}/{repo}/hooks --input -") {
		t.Fatalf("args: %s", a)
	}
	for _, want := range []string{`"secret":"topsecret"`, `"content_type":"json"`, `"name":"web"`, `"workflow_run"`} {
		if !strings.Contains(string(in), want) {
			t.Fatalf("input lacks %s: %s", want, in)
		}
	}
}

// Hooks reads each webhook's URL from its config: ci.Hooks finds Dash's
// own webhook by it.
func TestHooksReadsConfigURL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	gh := filepath.Join(dir, "gh")
	testbin.Write(t, gh, `#!/bin/sh
echo "$*" >> `+args+`
echo '[{"id":3,"active":true,"events":["workflow_run"],"config":{"url":"https://x.example.com/api/github/hook","content_type":"json"}}]'
`)
	g := New(nil)
	g.GhBin = gh
	hooks, err := g.Hooks(context.Background(), "", dir)
	if err != nil || len(hooks) != 1 {
		t.Fatalf("%+v, %v", hooks, err)
	}
	if h := hooks[0]; h.ID != 3 || h.URL != "https://x.example.com/api/github/hook" || !h.Active || len(h.Events) != 1 || h.Events[0] != "workflow_run" {
		t.Fatalf("%+v", h)
	}
	if a, _ := os.ReadFile(args); !strings.Contains(string(a), "api repos/{owner}/{repo}/hooks?per_page=100") {
		t.Fatalf("args: %s", a)
	}
}

// UpdateHook patches the webhook by its id, with the secret on gh's stdin.
func TestUpdateHookSecretOnStdin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	args, input := filepath.Join(dir, "args"), filepath.Join(dir, "input")
	gh := filepath.Join(dir, "gh")
	testbin.Write(t, gh, `#!/bin/sh
echo "$*" >> `+args+`
cat > `+input+`
echo '{"id":7}'
`)
	g := New(nil)
	g.GhBin = gh
	if err := g.UpdateHook(context.Background(), "", dir, 7, HookConfig{URL: "https://dash.example.com/api/github/hook", Secret: "topsecret", Events: []string{"workflow_run"}}); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(args)
	in, _ := os.ReadFile(input)
	if strings.Contains(string(a), "topsecret") || !strings.Contains(string(a), "api -X PATCH repos/{owner}/{repo}/hooks/7 --input -") {
		t.Fatalf("args: %s", a)
	}
	if !strings.Contains(string(in), `"secret":"topsecret"`) || !strings.Contains(string(in), `"url":"https://dash.example.com/api/github/hook"`) || strings.Contains(string(in), `"name"`) {
		t.Fatalf("input: %s", in)
	}
}
