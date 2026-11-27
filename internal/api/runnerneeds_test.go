package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
)

// runnerNeedsOf is GET /api/runners' needs as h's user sees them: project
// id → why their tasks of it would wait for a runner.
func runnerNeedsOf(t *testing.T, h http.Handler) map[string]tunnel.Need {
	t.Helper()
	w := send(h, "GET", "/api/runners", "")
	var out struct{ Needs map[string]tunnel.Need }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Needs == nil {
		t.Fatalf("list runners: %d %s", w.Code, w.Body)
	}
	return out.Needs
}

// Each member's GET /api/runners says which projects their tasks would wait
// on for want of a runner: a member with no runner of their own (d) needs
// one, or another member's shared runner, while one with a runner (c) only
// waits for it to come online; a project whose labels no runner has needs
// its labels changed. Another org's members hear nothing of it.
func TestRunnerNeedsArePerMember(t *testing.T) {
	parallel(t)
	f := newFleetServer(t)
	setRunsOn := func(labels ...string) {
		t.Helper()
		if _, err := raw(f.s).UpdateProject(f.web.ID, func(x *model.Project) error { x.RunsOn = labels; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	check := func(who string, h http.Handler, want tunnel.Need) {
		t.Helper()
		if got := runnerNeedsOf(t, h)[f.web.ID]; got != want {
			t.Errorf("%s: need %q, want %q", who, got, want)
		}
	}

	setRunsOn("no-such-label")
	check("c, labels no runner has", f.c, tunnel.NeedLabels)
	check("d, labels no runner has", f.d, tunnel.NeedLabels)

	setRunsOn(model.LabelAny)
	check("c, with a runner (offline)", f.c, tunnel.NeedOnline)
	check("d, with no runner", f.d, tunnel.NeedRunner)
	if _, ok := runnerNeedsOf(t, f.b)[f.web.ID]; ok {
		t.Error("another org's member heard of the project")
	}

	// u's runner takes other members' tasks: d's would run on it.
	reportAuth(t, f, "u-box", keySignIn)
	if err := f.rh.SetShared(f.ids["u-box"], true); err != nil {
		t.Fatal(err)
	}
	check("d, with a shared runner (offline)", f.d, tunnel.NeedOnline)
}
