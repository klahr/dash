package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// pat is a planner of the org.
const pat int64 = 3

// patsRunner is pat's runner: a planner's, with every label of the
// project, online, on an API key and set to take other members' tasks, so
// that only its being a planner's keeps work off it.
func patsRunner(org int64) tunnel.Runner {
	r := bobsRunner(org, keyAuth, true)
	r.ID, r.Name = "pat-laptop", "pat-laptop"
	r.Owner = &store.RunnerOwner{UserID: pat, Username: "pat", Org: store.Org{ID: org}, Planner: true}
	return r
}

// A planner's runner runs only its owner's chat and interviews: the hub
// never sends it a task of the org, whoever's it is, never links (clones)
// a project to it, and doesn't count it as a runner a task could wait for.
func TestPlannersRunnerIsNeverGivenProjectWork(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	fx.fleet.set(patsRunner(fx.orgA))
	p := fx.project(t, fx.orgA, "pat-laptop")
	r, _ := fx.fleet.Get("pat-laptop")
	if r.Serves(p) || r.Deploys(p, model.Environment{Name: "prod"}) {
		t.Fatal("a planner's runner serves the project")
	}
	if len(tunnel.Serving(fx.fleet.List(), p)) != 0 {
		t.Fatal("a planner's runner is among the project's runners")
	}
	for _, by := range []int64{alice, pat} {
		td := fx.todo(t, p, func(x *model.Todo) { x.CreatedBy = by })
		_, _, err := fx.d.Dispatch(context.Background(), p, td)
		if why := waitReasonOf(t, err); strings.Contains(why, "pat-laptop") && !strings.Contains(why, "planner") {
			t.Errorf("task by %d: wait reason %q", by, why)
		}
	}
	if got := fx.fleet.callsTo("pat-laptop"); len(got) != 0 {
		t.Errorf("the planner's runner was called: %q", got)
	}
	if len(fx.fleet.tasks) != 0 || len(fx.fleet.links) != 0 {
		t.Errorf("sent to the planner's runner: tasks %+v, links %+v", fx.fleet.tasks, fx.fleet.links)
	}
	if ok, why := r.WorksFor([]int64{pat}); ok || !strings.Contains(why, "planner") {
		t.Errorf("works for its owner: %v %q", ok, why)
	}
	if ok, _ := r.TakesOthers(); ok || r.CanShare("") == "" {
		t.Error("a planner's runner takes, or can take, other members' tasks")
	}

	// The same runner, its owner made a member: it works again.
	r.Owner.Planner = false
	fx.fleet.set(r)
	if !r.Serves(p) {
		t.Error("a member's runner doesn't serve the project")
	}
}
