package scan

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
)

// No default tool may be a Bash rule: "Bash(git log:*)" allows
// git log --output=<file>, which writes anywhere.
func TestDefaultToolsHaveNoBash(t *testing.T) {
	t.Parallel()

	for _, tool := range DefaultTools {
		if strings.HasPrefix(tool, "Bash") {
			t.Errorf("default tool %q runs a shell command", tool)
		}
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "first")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("commit", "-qam", "second")
	return dir
}

func gitTool(t *testing.T, dir, name string) mcptools.Tool {
	t.Helper()
	for _, tool := range gitTools(dir, nil) {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no tool %s", name)
	return mcptools.Tool{}
}

func TestGitToolsRead(t *testing.T) {
	t.Parallel()

	dir := gitRepo(t)
	cases := []struct {
		tool string
		args mcptools.Args
		want string
	}{
		{"git_diff", mcptools.Args{"from": "HEAD~1", "to": "HEAD"}, "+two"},
		{"git_diff", mcptools.Args{"from": "HEAD~1", "paths": []any{"a.txt"}, "stat": true}, "a.txt"},
		{"git_log", mcptools.Args{"oneline": true}, "second"},
		{"git_log", mcptools.Args{"revision": "HEAD~1..HEAD", "patch": true}, "+two"},
		{"git_show", mcptools.Args{"revision": "HEAD~1:a.txt"}, "one"},
		{"git_show", mcptools.Args{"revision": "HEAD"}, "+two"},
		{"git_blame", mcptools.Args{"path": "a.txt", "start_line": float64(2), "end_line": float64(2)}, "two"},
		{"git_grep", mcptools.Args{"pattern": "t[w]o$"}, "a.txt:2:two"},
		{"git_grep", mcptools.Args{"pattern": "TWO", "ignore_case": true, "files_only": true}, "a.txt"},
		{"git_grep", mcptools.Args{"pattern": "two", "revision": "HEAD~1"}, "(no matches)"},
		{"git_log", mcptools.Args{"search": "^t[w]o", "oneline": true}, "second"},
		{"git_log", mcptools.Args{"all": true, "skip": float64(1), "oneline": true}, "first"},
		{"git_log", mcptools.Args{"diff_filter": "A", "stat": true}, "first"},
	}
	for _, c := range cases {
		out, err := gitTool(t, dir, c.tool).Run(c.args)
		if err != nil {
			t.Errorf("%s %v: %v", c.tool, c.args, err)
			continue
		}
		if !strings.Contains(out.(string), c.want) {
			t.Errorf("%s %v = %q, want %q in it", c.tool, c.args, out, c.want)
		}
	}
	// The filters leave out what doesn't match.
	for _, c := range []struct {
		args mcptools.Args
		not  string
	}{
		{mcptools.Args{"search": "^t[w]o", "oneline": true}, "first"},
		{mcptools.Args{"skip": float64(1), "oneline": true}, "second"},
		{mcptools.Args{"diff_filter": "A", "oneline": true}, "second"},
	} {
		out, err := gitTool(t, dir, "git_log").Run(c.args)
		if err != nil || strings.Contains(out.(string), c.not) {
			t.Errorf("git_log %v = %q %v, want no %q", c.args, out, err, c.not)
		}
	}
}

// A secret in a deleted file is still found in history.
func TestGitToolsFindDeletedFiles(t *testing.T) {
	t.Parallel()

	dir := gitRepo(t)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("API_TOKEN=s3cr3t-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".env")
	run("commit", "-qm", "add env")
	run("rm", "-q", ".env")
	run("commit", "-qm", "remove env")
	if out, _ := gitTool(t, dir, "git_grep").Run(mcptools.Args{"pattern": "API_TOKEN"}); out != "(no matches)" {
		t.Errorf("grep of the checkout: %q", out)
	}
	out, err := gitTool(t, dir, "git_log").Run(mcptools.Args{"diff_filter": "D", "stat": true})
	if err != nil || !strings.Contains(out.(string), "remove env") || !strings.Contains(out.(string), ".env") {
		t.Errorf("deleted files: %q %v", out, err)
	}
	out, err = gitTool(t, dir, "git_log").Run(mcptools.Args{"search": "API_TOKEN=", "patch": true, "all": true})
	if err != nil || !strings.Contains(out.(string), "+API_TOKEN=") || !strings.Contains(out.(string), "add env") {
		t.Errorf("history search: %q %v", out, err)
	}
}

// Nothing a scan passes reaches git as an option, so --output can't write.
func TestGitToolsRefuseOptions(t *testing.T) {
	t.Parallel()

	dir := gitRepo(t)
	target := filepath.Join(t.TempDir(), "pwned")
	cases := []struct {
		tool string
		args mcptools.Args
	}{
		{"git_log", mcptools.Args{"revision": "--output=" + target}},
		{"git_log", mcptools.Args{"revision": "HEAD --output=" + target}},
		{"git_diff", mcptools.Args{"from": "HEAD~1", "to": "--output=" + target}},
		{"git_diff", mcptools.Args{"from": "HEAD~1", "paths": []any{"--output=" + target}}},
		{"git_show", mcptools.Args{"revision": "-o" + target}},
		{"git_show", mcptools.Args{"revision": "HEAD", "paths": []any{"/etc/passwd"}}},
		{"git_blame", mcptools.Args{"path": "../x"}},
		{"git_blame", mcptools.Args{"path": "a.txt", "revision": "--contents=/etc/passwd"}},
		{"git_log", mcptools.Args{"diff_filter": "D --output=" + target}},
		{"git_log", mcptools.Args{"search": "x\n--output=" + target}},
		{"git_grep", mcptools.Args{"pattern": "one", "revision": "--open-files-in-pager=touch " + target}},
		{"git_grep", mcptools.Args{"pattern": "one", "paths": []any{"--output=" + target}}},
		{"git_grep", mcptools.Args{}},
	}
	for _, c := range cases {
		if _, err := gitTool(t, dir, c.tool).Run(c.args); err == nil {
			t.Errorf("%s %v: no error", c.tool, c.args)
		}
	}
	// A pattern that looks like an option is still only a pattern.
	for _, c := range []struct {
		tool string
		args mcptools.Args
	}{
		{"git_grep", mcptools.Args{"pattern": "--open-files-in-pager=touch " + target}},
		{"git_grep", mcptools.Args{"pattern": "-O"}},
		{"git_log", mcptools.Args{"search": "-output=" + target, "patch": true}},
	} {
		if _, err := gitTool(t, dir, c.tool).Run(c.args); err != nil {
			t.Errorf("%s %v: %v", c.tool, c.args, err)
		}
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("a git tool wrote a file")
	}
}

// A scan run gets the git tools server, with a token for its own checkout
// that stops working when the run ends.
func TestRunServesGitTools(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "s", "---\nkind: security\n---\nLook.")
	fx.runner.GitTools = &GitMCP{}
	srv := httptest.NewServer(fx.runner.GitTools)
	defer srv.Close()
	fx.runner.GitURL = srv.URL
	var cfg string
	var during int
	fx.exec.onRun = func(req agent.ExecRequest) {
		cfg, _ = flagValue((agent.ClaudeCode{}).RunArgs(req), "--mcp-config")
		during = callGitMCP(t, srv.URL, cfg)
	}
	if r := fx.run(t, "s"); r.Status != model.ScanSucceeded {
		t.Fatalf("run: %+v", r)
	}
	if !strings.Contains(cfg, GitServerName) || !strings.Contains(cfg, "Bearer ") {
		t.Fatalf("--mcp-config %q", cfg)
	}
	if during != http.StatusOK {
		t.Fatalf("git tools during the run: %d", during)
	}
	if after := callGitMCP(t, srv.URL, cfg); after != http.StatusUnauthorized {
		t.Fatalf("git tools after the run: %d", after)
	}
}

func callGitMCP(t *testing.T, url, cfg string) int {
	t.Helper()
	var c struct {
		MCPServers map[string]struct {
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	_ = json.Unmarshal([]byte(cfg), &c)
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", c.MCPServers[GitServerName].Headers["Authorization"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// noMCPExec is an agent that takes no MCP servers (no agent.MCPConfigurer).
type noMCPExec struct{ agent.Executor }

// A scan on an agent without MCP servers still runs, with the file tools
// only: no git tools server, and no token registered for one.
func TestRunWithoutMCPGetsNoGitTools(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "s", "---\nkind: security\n---\nLook.")
	fx.runner.GitTools = &GitMCP{}
	fx.runner.GitURL = "http://127.0.0.1:1"
	fx.runner.Exec = noMCPExec{fx.exec}
	var got agent.ExecRequest
	fx.exec.onRun = func(req agent.ExecRequest) { got = req }
	if r := fx.run(t, "s"); r.Status != model.ScanSucceeded {
		t.Fatalf("run: %+v", r)
	}
	if len(got.MCPServers) > 0 {
		t.Fatalf("MCP servers for an agent without them: %v", got.MCPServers)
	}
	if !got.Unattended || len(got.AllowedTools) == 0 {
		t.Fatalf("scan run should be unattended with its tools: %+v", got)
	}
}
