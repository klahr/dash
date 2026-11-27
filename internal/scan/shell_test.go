package scan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/fsutil"
	"github.com/klahr/dash/internal/model"
)

func TestAnyCommand(t *testing.T) {
	t.Parallel()

	for tool, want := range map[string]bool{
		"Bash": true, " Bash ": true, "Bash()": true, "Bash(*)": true, "Bash(:*)": true, "Bash( * )": true,
		"Bash(govulncheck:*)": false, "Bash(npm audit)": false, "Read": false, "BashOutput": false, "Bash(": false,
	} {
		if got := anyCommand(tool); got != want {
			t.Errorf("anyCommand(%q) = %v, want %v", tool, got, want)
		}
	}
}

// No built-in scan gets a shell that runs any command without the user's
// approval: one that asks for it (flaky-tests) runs only once approved, or
// on a runner that runs Claude in a sandbox.
func TestBuiltinsGetNoUnrestrictedShellUnapproved(t *testing.T) {
	t.Parallel()

	for _, b := range Builtins() {
		tools, approved := Tools(b, model.ScanSetting{}, false)
		if approved && slices.ContainsFunc(tools, anyCommand) {
			t.Errorf("%s: any command without approval: %q", b.Name, tools)
		}
		if !approved && slices.ContainsFunc(tools, func(s string) bool { return strings.HasPrefix(s, "Bash") }) {
			t.Errorf("%s: unapproved, yet a shell: %q", b.Name, tools)
		}
		if b.RunsAnyCommand() != (b.Name == "flaky-tests") {
			t.Errorf("%s: runs any command %v", b.Name, b.RunsAnyCommand())
		}
	}
	// A built-in that asks for any command is trusted for its schedule, not
	// for its tools.
	shell := Template{Name: "x", Builtin: true, Tools: []string{"Read", "Bash(*)"}}
	if tools, ok := Tools(shell, model.ScanSetting{}, false); ok || slices.Contains(tools, "Bash(*)") {
		t.Errorf("unapproved shell: %q %v", tools, ok)
	}
	if got := SandboxTools(shell, false); !slices.Equal(got, shell.Tools) {
		t.Errorf("sandbox tools %q", got)
	}
	if tools, ok := Tools(shell, model.ScanSetting{ApprovedTools: []string{"Read", "Bash(*)"}}, true); !ok || !slices.Equal(tools, shell.Tools) {
		t.Errorf("approved shell: %q %v", tools, ok)
	}
	// Approval of other tools doesn't count.
	if _, ok := Tools(shell, model.ScanSetting{ApprovedTools: []string{"Read", "Bash"}}, true); ok {
		t.Error("approval of other tools applied")
	}
	// A Dash scan is the user's own: trusted as before.
	if _, ok := Tools(Template{Origin: SourceDash, Tools: []string{"Bash"}}, model.ScanSetting{}, false); !ok {
		t.Error("Dash scan's tools not trusted")
	}
}

// The dependency audit runs only named commands, and no fix mode.
func TestDependenciesNamedCommandsOnly(t *testing.T) {
	t.Parallel()

	deps, ok := Pick(Builtins(), "dependencies")
	if !ok {
		t.Fatal("no dependencies scan")
	}
	tools, approved := Tools(deps, model.ScanSetting{}, false)
	if !approved {
		t.Fatalf("dependencies not approved: %q", tools)
	}
	allowed := []string{
		"Bash(govulncheck:*)", "Bash(npm audit)", "Bash(npm audit --json)", "Bash(pip-audit --disable-pip:*)",
		"Bash(cargo audit)", "Bash(cargo audit --json)", "Bash(pip show:*)",
	}
	for _, tool := range tools {
		if strings.HasPrefix(tool, "Bash") && !slices.Contains(allowed, tool) {
			t.Errorf("dependencies: unexpected shell rule %q", tool)
		}
	}
	for _, want := range []string{"Bash(govulncheck:*)", "Bash(npm audit --json)", "Bash(pip-audit --disable-pip:*)", "Bash(cargo audit --json)"} {
		if !slices.Contains(tools, want) {
			t.Errorf("dependencies lacks %s: %q", want, tools)
		}
	}
	for _, tool := range tools {
		if strings.Contains(tool, "audit:*") && (strings.Contains(tool, "npm") || strings.Contains(tool, "cargo")) {
			t.Errorf("%s allows a fix mode", tool)
		}
	}
}

// A scan that may run commands runs them in Claude Code's sandbox, which
// can't read the credentials the file tools can't either.
func TestShellRunsInSandbox(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	data := filepath.Join(home, ".local", "share", "dash-runner")
	root := filepath.Join(data, "scans")
	dir := filepath.Join(root, "worktrees", "p", "run")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "runner.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Read-only: no shell, no sandbox.
	if args := (agent.ClaudeCode{}).RunArgs(RunRequest(Template{}, nil, dir, data, root)); slices.Contains(args, "--settings") {
		t.Errorf("read-only scan with settings: %q", args)
	}
	for _, tools := range [][]string{{"Read", "Bash"}, {"Read", "Bash(govulncheck:*)"}} {
		args := (agent.ClaudeCode{}).RunArgs(RunRequest(Template{}, tools, dir, data, root))
		v, ok := flagValue(args, "--settings")
		if !ok {
			t.Fatalf("%q: no --settings in %q", tools, args)
		}
		var set struct {
			Sandbox struct {
				Enabled                  bool  `json:"enabled"`
				AllowUnsandboxedCommands *bool `json:"allowUnsandboxedCommands"`
				Filesystem               struct {
					DenyRead   []string `json:"denyRead"`
					AllowWrite []string `json:"allowWrite"`
				} `json:"filesystem"`
				Network struct {
					AllowedDomains []string `json:"allowedDomains"`
				} `json:"network"`
			} `json:"sandbox"`
		}
		if err := json.Unmarshal([]byte(v), &set); err != nil {
			t.Fatalf("settings %q: %v", v, err)
		}
		sb := set.Sandbox
		if !sb.Enabled || sb.AllowUnsandboxedCommands == nil || *sb.AllowUnsandboxedCommands {
			t.Errorf("sandbox not enforced: %s", v)
		}
		for _, want := range []string{
			filepath.Join(home, ".ssh"), filepath.Join(home, ".claude.json"), filepath.Join(home, ".config", "gh"),
			filepath.Join(data, "runner.json"), filepath.Join(root, "logs"),
		} {
			if !slices.Contains(sb.Filesystem.DenyRead, want) {
				t.Errorf("%s readable by commands: %q", want, sb.Filesystem.DenyRead)
			}
		}
		// Commands write a throwaway cache of the run's, not the shared
		// package caches the user's builds read.
		cache := CacheDir(dir)
		if !slices.Contains(sb.Filesystem.AllowWrite, cache) {
			t.Errorf("run's cache %s not writable: %q", cache, sb.Filesystem.AllowWrite)
		}
		for _, p := range sb.Filesystem.AllowWrite {
			for _, shared := range []string{filepath.Join(home, "go"), filepath.Join(home, ".npm"), filepath.Join(home, ".cargo", "registry")} {
				if fsutil.Within(p, shared) || fsutil.Within(shared, p) {
					t.Errorf("shared cache %s writable by commands: %q", shared, sb.Filesystem.AllowWrite)
				}
			}
			if c, err := os.UserCacheDir(); err == nil && p == c {
				t.Errorf("user cache dir writable by commands: %q", sb.Filesystem.AllowWrite)
			}
		}
		env := RunRequest(Template{}, tools, dir, data, root).Env
		for _, k := range []string{"GOCACHE", "GOMODCACHE", "npm_config_cache", "PIP_CACHE_DIR", "XDG_CACHE_HOME"} {
			if i := slices.IndexFunc(env, func(e string) bool { return strings.HasPrefix(e, k+"=") }); i < 0 || !fsutil.Within(cache, strings.TrimPrefix(env[i], k+"=")) {
				t.Errorf("%s not in the run's cache: %q", k, env)
			}
		}
		for _, p := range []string{data, root, dir} {
			if slices.Contains(sb.Filesystem.DenyRead, p) {
				t.Errorf("%s denied whole", p)
			}
		}
		if !slices.Contains(sb.Network.AllowedDomains, "vuln.go.dev") {
			t.Errorf("allowed domains %q", sb.Network.AllowedDomains)
		}
	}
}

// An unapproved flaky-tests run doesn't run where Claude has no sandbox,
// and runs with its shell where it has one.
func TestFlakyTestsNeedsApprovalOrSandbox(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, "", "")
	r := fx.run(t, "flaky-tests")
	if r.Status != model.ScanSkipped || !strings.Contains(r.Note, "approve") || len(fx.exec.reqs) != 0 {
		t.Fatalf("unapproved run: %s %q, %d runs", r.Status, r.Note, len(fx.exec.reqs))
	}
	fx.runner.Sandboxed = true
	if r := fx.run(t, "flaky-tests"); r.Status != model.ScanSucceeded {
		t.Fatalf("sandboxed run: %+v", r)
	}
	if req := fx.exec.reqs[len(fx.exec.reqs)-1]; !slices.Contains(req.AllowedTools, "Bash") || req.Settings == "" {
		t.Errorf("sandboxed run's tools %q settings %q", req.AllowedTools, req.Settings)
	} else if !strings.Contains(req.Prompt, "You may run shell commands") {
		// The prompt follows the tools the run gets, not the job's read-only ones.
		t.Errorf("sandboxed run's prompt says it is read-only:\n%s", req.Prompt)
	}
	// Approved for the project: it runs without a sandbox too.
	fx.runner.Sandboxed = false
	flaky, _ := Pick(Builtins(), "flaky-tests")
	if _, err := fx.st.SetScanSetting(fx.p.ID, "flaky-tests", func(st *model.ScanSetting) { st.ApprovedTools = flaky.Tools }); err != nil {
		t.Fatal(err)
	}
	if r := fx.run(t, "flaky-tests"); r.Status != model.ScanSucceeded {
		t.Fatalf("approved run: %+v", r)
	}
	if req := fx.exec.reqs[len(fx.exec.reqs)-1]; !slices.Contains(req.AllowedTools, "Bash") {
		t.Errorf("approved run's tools %q", req.AllowedTools)
	}
}

// A job's sandbox tools can't smuggle a flag into Claude's arguments.
func TestJobCheckSandboxTools(t *testing.T) {
	t.Parallel()

	j := Job{Template: Template{Name: "flaky-tests", Scope: model.ScopeRepo}, Tools: DefaultTools, SandboxTools: []string{"--dangerously-skip-permissions"}}
	if err := j.Check(); err == nil {
		t.Error("flag in sandbox tools accepted")
	}
}

// A run's cache goes with it, read-only Go module directories and all.
func TestRemoveCache(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "run")
	mod := filepath.Join(CacheDir(dir), "go-mod", "example.com", "m@v1.0.0")
	if err := os.MkdirAll(mod, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module example.com/m\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mod, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := RemoveCache(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(CacheDir(dir)); !os.IsNotExist(err) {
		t.Errorf("cache left behind: %v", err)
	}
}
