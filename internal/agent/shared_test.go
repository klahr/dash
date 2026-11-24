package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/testbin"
)

// sharedClaude is a stand-in for claude that saves its environment in
// <dir>/env and prints out: what a run sees and says.
func sharedClaude(t *testing.T, out string) (bin, dir string) {
	t.Helper()
	testbin.SkipOnWindows(t, "runs a #!/bin/sh stand-in for claude")
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "out.jsonl"), []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncat >/dev/null\nenv >'" + dir + "/env'\ncat '" + dir + "/out.jsonl'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

const (
	initKey  = `{"type":"system","subtype":"init","session_id":"s1","model":"m","apiKeySource":"ANTHROPIC_API_KEY"}`
	initNone = `{"type":"system","subtype":"init","session_id":"s1","model":"m","apiKeySource":"none"}`
	resultOK = `{"type":"result","subtype":"success","is_error":false,"session_id":"s1","num_turns":1,"total_cost_usd":0,"result":"ok"}`
)

// Other members' work (ExecRequest.Shared) runs Claude with the runner's API
// key and none of its owner's logins, in a config folder of its own; the
// owner's own work keeps the runner's environment.
func TestSharedRunGetsTheKeyAndNoLogin(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "owner-oauth-secret")
	t.Setenv("CLAUDE_CONFIG_DIR", "/home/owner/.claude")
	bin, dir := sharedClaude(t, initKey+"\n"+resultOK+"\n")
	shared := filepath.Join(t.TempDir(), "shared-claude")

	if _, err := (ClaudeCode{Bin: bin}).Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "hi", Shared: shared}, func(model.RunEvent) {}); err != nil {
		t.Fatal(err)
	}
	env := readEnv(t, dir)
	if !strings.Contains(env, "\nANTHROPIC_API_KEY=sk-ant-test\n") {
		t.Error("the shared run lacks the API key")
	}
	if strings.Contains(env, "owner-oauth-secret") || strings.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Error("the shared run got the owner's OAuth token")
	}
	if !strings.Contains(env, "\nCLAUDE_CONFIG_DIR="+shared+"\n") || strings.Contains(env, "/home/owner/.claude") {
		t.Errorf("the shared run's config folder isn't its own: %s", env)
	}

	// The owner's own work: their environment, as it is.
	if _, err := (ClaudeCode{Bin: bin}).Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "hi"}, func(model.RunEvent) {}); err != nil {
		t.Fatal(err)
	}
	if env := readEnv(t, dir); !strings.Contains(env, "owner-oauth-secret") {
		t.Error("the owner's run lost their login")
	}
}

// A shared run stops when Claude Code says it signed in another way than
// with the API key, or doesn't say, and doesn't start without a key.
func TestSharedRunStopsWithoutTheKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	shared := t.TempDir()
	for name, out := range map[string]string{
		"subscription": initNone + "\n" + resultOK + "\n",
		"no init":      `{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}` + "\n" + resultOK + "\n",
	} {
		bin, _ := sharedClaude(t, out)
		var texts []string
		_, err := (ClaudeCode{Bin: bin}).Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "hi", Shared: shared},
			func(e model.RunEvent) { texts = append(texts, e.Text) })
		if err == nil || !strings.Contains(err.Error(), "must use the runner's API key") {
			t.Errorf("%s: err = %v, want it stopped", name, err)
		}
		if strings.Contains(strings.Join(texts, "\n"), "working") {
			t.Errorf("%s: Claude's work went on after the check: %q", name, texts)
		}
	}

	t.Setenv("ANTHROPIC_API_KEY", "")
	bin, dir := sharedClaude(t, initKey+"\n"+resultOK+"\n")
	if _, err := (ClaudeCode{Bin: bin}).Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "hi", Shared: shared}, func(model.RunEvent) {}); err == nil {
		t.Error("a shared run started with no API key")
	}
	if _, err := os.Stat(filepath.Join(dir, "env")); err == nil {
		t.Error("claude was started with no API key")
	}
}

func readEnv(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "env"))
	if err != nil {
		t.Fatal(err)
	}
	return "\n" + string(b)
}

// Codex work for another member (ExecRequest.Shared) signs in with the
// runner's API key only, from a config folder of its own (CODEX_HOME,
// private to the runner's user), never the owner's Codex login.
func TestCodexSharedRunGetsTheKeyAndItsOwnHome(t *testing.T) {
	t.Setenv(CodexKeyEnv, "sk-codex-test")
	t.Setenv("CODEX_HOME", "/home/owner/.codex")
	bin, dir := fakeCodex(t, "task.jsonl", 0, "")
	shared := filepath.Join(t.TempDir(), "shared-codex")

	if _, err := (Codex{Bin: bin}).Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "hi", Shared: shared}, func(model.RunEvent) {}); err != nil {
		t.Fatal(err)
	}
	env := readEnv(t, dir)
	if !strings.Contains(env, "\n"+CodexKeyEnv+"=sk-codex-test\n") {
		t.Error("the shared run lacks the API key")
	}
	if !strings.Contains(env, "\nCODEX_HOME="+shared+"\n") || strings.Contains(env, "/home/owner/.codex") {
		t.Errorf("the shared run's config folder isn't its own: %s", env)
	}
	fi, err := os.Stat(shared)
	if err != nil || !fi.IsDir() {
		t.Fatalf("the shared config folder wasn't made: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("the shared config folder's mode is %o, want 700", perm)
	}

	// The owner's own work: their Codex home, as it is.
	if _, err := (Codex{Bin: bin}).Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "hi"}, func(model.RunEvent) {}); err != nil {
		t.Fatal(err)
	}
	if env := readEnv(t, dir); !strings.Contains(env, "\nCODEX_HOME=/home/owner/.codex\n") || strings.Contains(env, shared) {
		t.Errorf("the owner's run lost their Codex home: %s", env)
	}
}

// Without the runner's API key, Codex work for another member stops before
// Codex starts, rather than run on the owner's Codex login.
func TestCodexSharedRunStopsWithoutTheKey(t *testing.T) {
	for name, key := range map[string]string{"unset": "", "blank": "  "} {
		t.Setenv(CodexKeyEnv, key)
		bin, dir := fakeCodex(t, "task.jsonl", 0, "")
		shared := filepath.Join(t.TempDir(), "shared-codex")
		_, err := (Codex{Bin: bin}).Run(context.Background(), ExecRequest{Dir: t.TempDir(), Prompt: "hi", Shared: shared}, func(model.RunEvent) {})
		if err == nil || !strings.Contains(err.Error(), "runner's API key") || !strings.Contains(err.Error(), CodexKeyEnv) {
			t.Errorf("%s: err = %v, want it stopped for the API key", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "args")); err == nil {
			t.Errorf("%s: codex was started with no API key", name)
		}
		if _, err := os.Stat(shared); err == nil {
			t.Errorf("%s: the shared config folder was made for a run that stopped", name)
		}
	}
}
