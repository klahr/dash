package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/testbin"
)

// Another member's task (model.Todo.SharedWork) runs its project commands
// with the runner's API key and none of its owner's logins, and only while
// the runner may do other members' work; its owner's run as before.
func TestSharedWorkCommandsRunWithTheKeyAlone(t *testing.T) {
	testbin.SkipOnWindows(t, "runs sh commands")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "owner-oauth-secret")
	g, l := gitops.New(nil), &TodoLog{Pub: nopPub{}, Root: t.TempDir()} // no harness: it runs in parallel, and this sets the environment
	wt := t.TempDir()
	out := filepath.Join(t.TempDir(), "env")
	p := model.Project{ID: "p1", Name: "p", TestCommand: "env > '" + out + "'"}
	td := model.Todo{ID: "t1", ProjectID: p.ID, SharedWork: true}

	// Not set up for others' work, or no longer allowed: the tests don't run.
	if c := runChecks(context.Background(), g, l, nil, td, p, wt, "review", time.Minute); c.Passed || !strings.Contains(c.Output, "doesn't take other members' work") {
		t.Errorf("no shared work: %+v", c)
	}
	sh := &SharedWork{Dir: t.TempDir(), Check: func() error { return errors.New("it uses a Claude Max subscription") }}
	if c := runChecks(context.Background(), g, l, sh, td, p, wt, "review", time.Minute); c.Passed || !strings.Contains(c.Output, "Claude Max") {
		t.Errorf("on a subscription: %+v", c)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("the command ran")
	}

	sh.Check = func() error { return nil }
	if c := runChecks(context.Background(), g, l, sh, td, p, wt, "review", time.Minute); !c.Passed {
		t.Fatalf("shared runner: %+v", c)
	}
	env, _ := os.ReadFile(out)
	if s := string(env); !strings.Contains(s, "ANTHROPIC_API_KEY=sk-ant-test") || strings.Contains(s, "owner-oauth-secret") ||
		!strings.Contains(s, "CLAUDE_CONFIG_DIR="+sh.Dir) {
		t.Errorf("shared work's environment:\n%s", s)
	}

	td.SharedWork = false
	if c := runChecks(context.Background(), g, l, sh, td, p, wt, "review", time.Minute); !c.Passed {
		t.Fatalf("owner's work: %+v", c)
	}
	if env, _ := os.ReadFile(out); !strings.Contains(string(env), "owner-oauth-secret") {
		t.Error("the owner's own work lost their login")
	}
}

// A command the hub held (set by a member who may not run it here) fails
// its step, saying why, instead of being skipped.
func TestHeldCommandsFailTheirStep(t *testing.T) {
	p := model.Project{HeldCommands: map[string]string{model.CmdTest: "the project's test command was last changed by c"}}
	if err := heldErr(p, model.CmdTest); err == nil || !strings.Contains(err.Error(), "last changed by c") {
		t.Errorf("held test: %v", err)
	}
	if err := heldErr(p, model.CmdSetup); err != nil {
		t.Errorf("setup isn't held: %v", err)
	}
	e := &TodoEngine{}
	err := e.prepareWorktree(context.Background(), model.Todo{}, model.Project{HeldCommands: map[string]string{model.CmdSetup: "held setup"}},
		model.Worktree{Path: t.TempDir()}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "held setup") {
		t.Errorf("held setup: %v", err)
	}
}
