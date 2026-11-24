package api

import (
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/deploy"
	"github.com/klahr/dash/internal/model"
)

func TestCatchUpPhases(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now := t0.Add(time.Minute)
	runner := []model.DeployPhase{
		{Status: model.DeployBuilding, At: t0.Add(time.Second)},
		{Status: model.DeployDeploying, At: t0.Add(2 * time.Second)},
		{Status: model.DeployVerifying, At: t0.Add(3 * time.Second)},
	}
	queued := func() model.Deploy {
		var d model.Deploy
		d.SetStatus(model.DeployQueued, t0)
		return d
	}
	names := func(d model.Deploy) string {
		var s []string
		for _, p := range d.Phases {
			s = append(s, string(p.Status))
		}
		return strings.Join(s, ",")
	}

	// Every step the runner went through, even those the hub's checks missed.
	d := queued()
	catchUpPhases(&d, deploy.State{Phase: model.DeployVerifying, Phases: runner}, now)
	if names(d) != "queued,building,deploying,verifying" || d.Status != model.DeployVerifying || !d.Phases[2].At.Equal(runner[1].At) {
		t.Errorf("from queued: %+v", d.Phases)
	}

	// Only those after the one it is at, once.
	d = queued()
	d.SetStatus(model.DeployBuilding, t0.Add(time.Second))
	catchUpPhases(&d, deploy.State{Phase: model.DeployVerifying, Phases: runner}, now)
	catchUpPhases(&d, deploy.State{Phase: model.DeployVerifying, Phases: runner}, now)
	if names(d) != "queued,building,deploying,verifying" {
		t.Errorf("from building: %+v", d.Phases)
	}

	// The runner's clock behind the hub's, or ahead of it: still in order.
	d = queued()
	skewed := []model.DeployPhase{{Status: model.DeployBuilding, At: t0.Add(-time.Hour)}, {Status: model.DeployDeploying, At: now.Add(time.Hour)}}
	catchUpPhases(&d, deploy.State{Phase: model.DeployDeploying, Phases: skewed}, now)
	if names(d) != "queued,building,deploying" || !d.Phases[1].At.Equal(t0) || !d.Phases[2].At.Equal(now) {
		t.Errorf("skewed: %+v", d.Phases)
	}

	// A runner that doesn't list its steps: the one it is at.
	d = queued()
	catchUpPhases(&d, deploy.State{Phase: model.DeployDeploying}, now)
	if names(d) != "queued,deploying" || !d.Phases[1].At.Equal(now) {
		t.Errorf("no list: %+v", d.Phases)
	}
}
