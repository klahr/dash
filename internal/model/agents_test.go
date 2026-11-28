package model

import (
	"slices"
	"testing"
	"time"
)

func TestAgentLabels(t *testing.T) {
	now := time.Now()
	agents := []AgentStatus{
		{Name: "claude", Installed: true, LoggedIn: true, CheckedAt: now},
		{Name: "codex", Installed: true, LoggedIn: false, CheckedAt: now}, // logged out
		{Name: "gemini", Installed: false, CheckedAt: now},                // not installed
		{Name: "aider"}, // not checked yet: counts
		{Name: "Claude", Installed: true, LoggedIn: true, CheckedAt: now}, // once
		{Name: "", Installed: true, LoggedIn: true, CheckedAt: now},
	}
	if got := AgentLabels(agents, true); !slices.Equal(got, []string{"claude", "aider"}) {
		t.Errorf("AgentLabels = %v", got)
	}
	if got := AgentLabels(nil, true); len(got) != 0 {
		t.Errorf("reported none: %v", got)
	}
	// A runner that never said which agents it has runs Claude Code.
	if got := AgentLabels(nil, false); !slices.Equal(got, []string{"claude"}) {
		t.Errorf("not reported: %v", got)
	}
}

func TestProjectRunnerLabels(t *testing.T) {
	for _, c := range []struct {
		p    Project
		want []string
	}{
		{Project{}, nil}, // no labels: no runner, whatever its agent
		{Project{Agent: "codex"}, nil},
		{Project{RunsOn: []string{"any"}}, []string{"any", "claude"}},
		{Project{RunsOn: []string{"gpu"}, Agent: "codex"}, []string{"gpu", "codex"}},
		{Project{RunsOn: []string{"Claude", "gpu"}}, []string{"Claude", "gpu"}},
	} {
		if got := c.p.RunnerLabels(); !slices.Equal(got, c.want) {
			t.Errorf("%+v: %v, want %v", c.p, got, c.want)
		}
	}
	p := Project{RunsOn: []string{"gpu"}}
	_ = p.RunnerLabels()
	if !slices.Equal(p.RunsOn, []string{"gpu"}) {
		t.Errorf("RunnerLabels changed RunsOn: %v", p.RunsOn)
	}
	// Matching: a runner without the agent's label doesn't work on it.
	have := []string{"macOS", "ARM64", "gpu"}
	if LabelsMatch(have, Project{RunsOn: []string{"gpu"}}.RunnerLabels()) {
		t.Error("matched without the claude label")
	}
	if !LabelsMatch(append(have, "claude"), Project{RunsOn: []string{"gpu"}}.RunnerLabels()) {
		t.Error("didn't match with it")
	}
	if got := MissingLabels(have, Project{RunsOn: []string{"any"}, Agent: "codex"}.RunnerLabels()); !slices.Equal(got, []string{"codex"}) {
		t.Errorf("missing %v", got)
	}
}

func TestCleanAgent(t *testing.T) {
	for in, want := range map[string]string{"": "", " Claude ": "claude", "codex-2": "codex-2"} {
		if got, ok := CleanAgent(in); !ok || got != want {
			t.Errorf("CleanAgent(%q) = %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"no spaces", "a/b", "x123456789012345678901234567890123"} {
		if _, ok := CleanAgent(in); ok {
			t.Errorf("CleanAgent(%q) took it", in)
		}
	}
}

func TestSignInState(t *testing.T) {
	now := time.Now()
	for _, x := range []struct {
		a    AgentStatus
		want string
	}{
		{AgentStatus{Installed: true, LoggedIn: true}, ""},                       // not checked yet
		{AgentStatus{CheckedAt: now}, ""},                                        // not installed
		{AgentStatus{CheckedAt: now, Installed: true, LoggedIn: true}, SignedIn}, // an older runner
		{AgentStatus{CheckedAt: now, Installed: true}, SignedOut},                // an older runner
		{AgentStatus{CheckedAt: now, Installed: true, LoggedIn: true, SignIn: SignInUnknown}, SignInUnknown},
		{AgentStatus{CheckedAt: now, Installed: true, SignIn: SignedOut}, SignedOut},
	} {
		if got := x.a.SignInState(); got != x.want {
			t.Errorf("%+v: %q, want %q", x.a, got, x.want)
		}
	}
}
