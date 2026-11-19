package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
)

// withAgents is r reporting agents (each installed and logged in).
func withAgents(r tunnel.Runner, names ...string) tunnel.Runner {
	rep := &tunnel.Reported{Parallel: 1}
	for _, n := range names {
		rep.Agents = append(rep.Agents, model.AgentStatus{Name: n, Installed: true, LoggedIn: true, CheckedAt: time.Now()})
	}
	r.Reported = rep
	return r
}

// A project's tasks go only to runners that have its agent: a runner too
// old to report agents still runs Claude Code's, and nothing else.
func TestDispatchSendsTasksToRunnersWithTheProjectsAgent(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	fx.fleet.set(runnerOf("old", fx.orgA, true)) // reports no agents
	fx.fleet.set(withAgents(runnerOf("codex-box", fx.orgA, true), "codex"))
	p := fx.project(t, fx.orgA, "old", "codex-box")

	// Claude Code (the default): only the older runner has it.
	td := fx.todo(t, p, nil)
	if rid, _, err := fx.d.Dispatch(context.Background(), p, td); err != nil || rid != "old" {
		t.Fatalf("claude task went to %q (%v), want old", rid, err)
	}

	p.Agent = "codex"
	td = fx.todo(t, p, nil)
	if rid, _, err := fx.d.Dispatch(context.Background(), p, td); err != nil || rid != "codex-box" {
		t.Fatalf("codex task went to %q (%v), want codex-box", rid, err)
	}
	fx.fleet.mu.Lock()
	last := fx.fleet.links[len(fx.fleet.links)-1]
	fx.fleet.mu.Unlock()
	if last.Agent != "codex" {
		t.Errorf("the link doesn't carry the agent: %+v", last)
	}

	// No runner has it: the task waits, naming the label it needs.
	p.Agent = "gemini"
	_, _, err := fx.d.Dispatch(context.Background(), p, fx.todo(t, p, nil))
	var w *WaitError
	if !errors.As(err, &w) || !strings.Contains(w.Reason, "no runner has the labels this project runs on") || !strings.Contains(w.Reason, "gemini") {
		t.Fatalf("err = %v, want a wait naming gemini", err)
	}
}

// Scans run on Claude Code whatever agent the project's tasks run on, so
// they go to a runner with Claude Code, not one with only the project's
// agent.
func TestScanRunnerNeedsClaudeCodeNotTheProjectsAgent(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	fx.fleet.set(withAgents(runnerOf("claude-box", fx.orgA, true), "claude"))
	fx.fleet.set(withAgents(runnerOf("codex-box", fx.orgA, true), "codex"))
	p := fx.project(t, fx.orgA, "claude-box", "codex-box")
	p.Agent = "codex"
	r, why, linked := fx.d.scanRunner(p, []int64{1})
	if r == nil || r.ID != "claude-box" || !linked {
		t.Fatalf("scan runner = %v (%q, linked %v), want claude-box", r, why, linked)
	}

	// No runner has Claude Code: no runner takes the scan.
	fx.fleet.set(withAgents(runnerOf("claude-box", fx.orgA, true), "codex"))
	if r, why, _ := fx.d.scanRunner(p, []int64{1}); r != nil {
		t.Fatalf("scan went to %s, which has no Claude Code (%q)", r.ID, why)
	}
}

// A runner reports its agents, and still its Claude Code status for hubs
// older than agent reports.
func TestReportCarriesAgentsAndClaude(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	rep := &Reporter{Store: fx.st, Claude: &claudecli.Monitor{},
		Agents: func() []model.AgentStatus {
			return []model.AgentStatus{{Name: "claude", Features: []string{model.FeatureResume}}}
		}}
	r := rep.Build(time.Now())
	if r.Claude == nil || len(r.Agents) != 1 || r.Agents[0].Name != "claude" {
		t.Fatalf("report: claude %v, agents %+v", r.Claude, r.Agents)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	// An older hub reads only what it knows of.
	var old struct {
		Claude *claudecli.Status `json:"claude"`
	}
	if err := json.Unmarshal(b, &old); err != nil || old.Claude == nil {
		t.Errorf("an older hub doesn't get claude: %s", b)
	}
	if !strings.Contains(string(b), `"agents":[{"name":"claude","installed":false,"logged_in":false,"features":["resume"]}]`) {
		t.Errorf("agents in %s", b)
	}
}
