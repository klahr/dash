package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store/storetest"
)

// agentHub is a hub with one registered runner, which reports with report.
func agentHub(t *testing.T) (h *Hub, id string, report func(body map[string]any)) {
	t.Helper()
	acc, owner := storetest.Accounts(t)
	h = &Hub{Accounts: acc}
	code, _, _ := h.NewCode(owner)
	id, token, _, err := h.register(code, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(h.ServeReport))
	t.Cleanup(srv.Close)
	creds := Credentials{Server: srv.URL, Token: token}
	return h, id, func(body map[string]any) {
		t.Helper()
		if err := Report(context.Background(), creds, body); err != nil {
			t.Fatal(err)
		}
	}
}

func runnerOf(t *testing.T, h *Hub, id string) Runner {
	t.Helper()
	r, ok := h.Get(id)
	if !ok {
		t.Fatal("runner gone")
	}
	return r
}

// project is a project of r's org on labels, run by agent.
func project(r Runner, agent string, labels ...string) model.Project {
	return model.Project{OrgID: r.OrgID(), RunsOn: labels, Agent: agent}
}

// A runner reports each agent it has; the hub keeps them (bounded), gives
// it a default label per agent that is installed and logged in, and sends
// it only the projects whose agent it has.
func TestReportCarriesAgents(t *testing.T) {
	t.Parallel()
	h, id, report := agentHub(t)
	now := time.Now()
	report(map[string]any{"parallel": 1,
		"claude": map[string]any{"version": "2.1.326", "checked_at": now},
		"agents": []map[string]any{
			{"name": "claude", "version": "2.1.326", "installed": true, "logged_in": true, "checked_at": now,
				"features": []string{"resume", "mcp", "permission-hook", "usage"}, "warnings": []string{strings.Repeat("w", 600)}},
			{"name": "Codex", "version": "0.9.1", "installed": true, "logged_in": true, "checked_at": now},
			{"name": "codex", "version": "dup"}, // named once
			{"name": "gemini", "installed": true, "logged_in": false, "checked_at": now},
			{"name": "", "installed": true},
		}})
	r := runnerOf(t, h, id)
	if len(r.Agents) != 3 || r.Agents[0].Name != "claude" || r.Agents[1].Name != "codex" || r.Agents[1].Version != "0.9.1" || r.Agents[2].Name != "gemini" {
		t.Fatalf("agents %+v", r.Agents)
	}
	if w := r.Agents[0].Warnings; len(w) != 1 || len(w[0]) > 504 {
		t.Errorf("warnings not clipped: %d", len(w[0]))
	}
	if !r.Agents[0].Has(model.FeatureResume) || r.Agents[1].Has(model.FeatureResume) {
		t.Errorf("features %+v", r.Agents)
	}
	if !slices.Equal(r.DefaultLabels, []string{"claude", "codex"}) {
		t.Errorf("default labels %v: gemini isn't logged in", r.DefaultLabels)
	}
	for _, c := range []struct {
		agent string
		want  bool
	}{{"", true}, {"claude", true}, {"codex", true}, {"gemini", false}, {"other", false}} {
		if got := r.Serves(project(r, c.agent, model.LabelAny)); got != c.want {
			t.Errorf("serves a project on %q: %v, want %v", c.agent, got, c.want)
		}
		if got := r.HasAgent(c.agent); got != c.want {
			t.Errorf("HasAgent(%q) = %v", c.agent, got)
		}
	}
	// Still no project without labels.
	if r.Serves(project(r, "claude")) {
		t.Error("serves a project on no labels")
	}

	// Claude logs out: its projects wait for another runner.
	report(map[string]any{"parallel": 1, "agents": []map[string]any{
		{"name": "claude", "version": "2.1.326", "installed": true, "logged_in": false, "checked_at": now}}})
	r = runnerOf(t, h, id)
	if r.Serves(project(r, "", model.LabelAny)) || slices.Contains(r.DefaultLabels, "claude") {
		t.Errorf("logged out, still %v", r.DefaultLabels)
	}
	if got := r.Serves(project(r, "", "claude")); got {
		t.Error("a project on the label claude, by hand, still served")
	}
}

// A runner that just restarted reports its agents before it has checked
// them: the hub keeps what it found before, and until it has a check at
// all, the agent counts as ready, so the runner doesn't lose its labels.
func TestReportAgentsAfterRestart(t *testing.T) {
	t.Parallel()
	h, id, report := agentHub(t)
	report(map[string]any{"parallel": 1, "agents": []map[string]any{{"name": "claude"}, {"name": "codex"}}})
	if r := runnerOf(t, h, id); !slices.Equal(r.DefaultLabels, []string{"claude", "codex"}) {
		t.Errorf("not checked yet: %v", r.DefaultLabels)
	}
	checked := time.Now().Add(-time.Hour)
	report(map[string]any{"parallel": 1, "agents": []map[string]any{
		{"name": "claude", "version": "2.1.0", "installed": true, "logged_in": true, "checked_at": checked},
		{"name": "codex", "installed": false, "checked_at": checked}}})
	report(map[string]any{"parallel": 1, "agents": []map[string]any{{"name": "claude"}, {"name": "codex"}}})
	r := runnerOf(t, h, id)
	if !slices.Equal(r.DefaultLabels, []string{"claude"}) || r.Agents[0].Version != "2.1.0" {
		t.Errorf("after restart: %v %+v", r.DefaultLabels, r.Agents)
	}
}

// A runner older than agent reports sends only its Claude Code status:
// the hub shows that as its agent, and it keeps the label claude whatever
// it says of its login, so projects on Claude keep running on it.
func TestOlderRunnerFallsBackToClaude(t *testing.T) {
	t.Parallel()
	h, id, report := agentHub(t)
	r := runnerOf(t, h, id)
	if len(r.Agents) != 0 || !slices.Equal(r.DefaultLabels, []string{"claude"}) || !r.Serves(project(r, "", model.LabelAny)) {
		t.Fatalf("before any report: %+v %v", r.Agents, r.DefaultLabels)
	}
	report(map[string]any{"parallel": 1, "claude": map[string]any{"version": "2.1.326", "checked_at": time.Now(),
		"auth": map[string]any{"method": "none"}, "warnings": []string{"changed"}}})
	r = runnerOf(t, h, id)
	if len(r.Agents) != 1 {
		t.Fatalf("agents %+v", r.Agents)
	}
	a := r.Agents[0]
	if a.Name != "claude" || a.Version != "2.1.326" || !a.Installed || a.LoggedIn || !slices.Equal(a.Warnings, []string{"changed"}) || !a.Has(model.FeatureResume) {
		t.Errorf("claude from its old report: %+v", a)
	}
	if r.Reported.Agents != nil {
		t.Errorf("the report itself got agents: %+v", r.Reported.Agents)
	}
	if !slices.Equal(r.DefaultLabels, []string{"claude"}) || !r.Serves(project(r, "claude", model.LabelAny)) || r.Serves(project(r, "codex", model.LabelAny)) {
		t.Errorf("older runner: labels %v", r.DefaultLabels)
	}
	// Its system's labels come first, as before.
	h.mu.Lock()
	h.runners[0].OS, h.runners[0].Arch = "darwin", "arm64"
	h.mu.Unlock()
	if r := runnerOf(t, h, id); !slices.Equal(r.DefaultLabels, []string{"macOS", "ARM64", "claude"}) || !slices.Equal(r.AllLabels(), r.DefaultLabels) {
		t.Errorf("labels %v, all %v", r.DefaultLabels, r.AllLabels())
	}
}
