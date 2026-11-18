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
