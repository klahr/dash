package tunnel

import (
	"testing"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// needRunner is a runner of user uid in org 1 with labels, online or not.
func needRunner(name string, uid int64, online bool, labels ...string) Runner {
	return Runner{ID: name, Name: name, Online: online, OS: "linux", Arch: "amd64", Labels: labels,
		Owner: &store.RunnerOwner{UserID: uid, Username: name + "-owner", Org: store.Org{ID: 1}}}
}

// shared makes r a shared runner: Take other members' tasks, with an API key.
func shared(r Runner) Runner {
	r.Shared = true
	r.Reported = &Reported{Claude: &claudecli.Status{Auth: &claudecli.Auth{Method: claudecli.AuthAPIKey, KeySource: claudecli.KeyEnv}}}
	return r
}

// The board warns a member only when no runner can ever run their tasks of
// a project, or none that could is online: another member's shared runner
// counts, another member's own runner doesn't.
func TestRunnerNeed(t *testing.T) {
	t.Parallel()
	const ada, bob = 1, 2
	p := model.Project{ID: "p", OrgID: 1, RunsOn: []string{"gpu"}}
	planner := needRunner("plan", bob, true, "gpu")
	planner.Owner.Planner = true
	otherOrg := needRunner("far", bob, true, "gpu")
	otherOrg.Owner.Org.ID = 2
	for _, c := range []struct {
		name    string
		runners []Runner
		users   []int64
		want    Need
	}{
		{"no runners at all", nil, []int64{bob}, NeedLabels},
		{"none with the labels", []Runner{needRunner("a", ada, true, "cpu")}, []int64{bob}, NeedLabels},
		{"only another org's", []Runner{otherOrg}, []int64{bob}, NeedLabels},
		{"only a planner's", []Runner{planner}, []int64{bob}, NeedLabels},
		{"their own, online", []Runner{needRunner("b", bob, true, "gpu")}, []int64{bob}, NeedNone},
		{"their own, offline", []Runner{needRunner("b", bob, false, "gpu")}, []int64{bob}, NeedOnline},
		{"another member's only", []Runner{needRunner("a", ada, true, "gpu")}, []int64{bob}, NeedRunner},
		{"another member's shared one", []Runner{shared(needRunner("a", ada, true, "gpu"))}, []int64{bob}, NeedNone},
		{"another member's shared one, offline", []Runner{shared(needRunner("a", ada, false, "gpu"))}, []int64{bob}, NeedOnline},
		{"their own without the labels", []Runner{needRunner("a", ada, true, "gpu"), needRunner("b", bob, true, "cpu")}, []int64{bob}, NeedYourLabels},
		{"their planner's runner doesn't count as theirs", []Runner{needRunner("a", ada, true, "gpu"), planner}, []int64{bob}, NeedRunner},
		{"their own offline, a shared one online", []Runner{needRunner("b", bob, false, "gpu"), shared(needRunner("a", ada, true, "gpu"))}, []int64{bob}, NeedNone},
		{"no one's work needs a shared runner", []Runner{needRunner("a", ada, true, "gpu")}, nil, NeedRunner},
	} {
		if got := RunnerNeed(c.runners, p, c.users); got != c.want {
			t.Errorf("%s: RunnerNeed = %q, want %q", c.name, got, c.want)
		}
	}
}
