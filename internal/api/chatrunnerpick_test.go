package api

import (
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// A project chat's runner menu steers the turn only to one of the caller's
// online runners that works on the project; anything else falls back to
// the hub's own choice, never to an error and never off the project.
func TestProjectChatRunnerPick(t *testing.T) {
	parallel(t)

	const me, org = 7, 3
	c := Caller{User: me, Org: org}
	owner := func(user int64) *store.RunnerOwner {
		return &store.RunnerOwner{UserID: user, Org: store.Org{ID: org}}
	}
	runner := func(id string, user int64, online bool, labels ...string) tunnel.Runner {
		return tunnel.Runner{ID: id, Name: id, Online: online, Owner: owner(user), Labels: labels}
	}
	all := []tunnel.Runner{
		runner("asleep", me, false, "gpu"),       // eligible, offline
		runner("joachims-mini", me, true, "gpu"), // eligible, the first online
		runner("klarre-rg", me, true, "gpu"),     // eligible, online
		runner("spare", me, true),                // mine, online, but lacks the label
		runner("theirs", 99, true, "gpu"),        // another member's
	}
	p := model.Project{ID: "p1", Name: "dash", OrgID: org, RunsOn: []string{"gpu"}}

	for _, tc := range []struct{ name, want, got string }{
		{"nothing picked", "", "joachims-mini"},
		{"picked eligible runner", "klarre-rg", "klarre-rg"},
		{"picked offline runner", "asleep", "joachims-mini"},
		{"picked ineligible runner", "spare", "joachims-mini"},
		{"picked someone else's runner", "theirs", "joachims-mini"},
		{"picked unknown runner", "gone", "joachims-mini"},
	} {
		r, err := pickProjectChatRunner(ownRunners(all, c), p, tc.want)
		if err != nil || r.ID != tc.got {
			t.Errorf("%s: got %q, %v; want %q", tc.name, r.ID, err, tc.got)
		}
	}

	// The menu lists the eligible ones only, offline ones included.
	var ids []string
	for _, r := range projectChatRunners(ownRunners(all, c), p) {
		ids = append(ids, r.ID)
	}
	if got := strings.Join(ids, ","); got != "asleep,joachims-mini,klarre-rg" {
		t.Errorf("project chat runners: %s", got)
	}

	// None of the eligible online: an error that names the offline one,
	// whatever was picked.
	offline := []tunnel.Runner{runner("asleep", me, false, "gpu"), runner("spare", me, true)}
	if _, err := pickProjectChatRunner(ownRunners(offline, c), p, "spare"); err == nil || !strings.Contains(err.Error(), "asleep is offline") {
		t.Errorf("no eligible runner online: %v", err)
	}
	// None eligible at all: says how to link one.
	if _, err := pickProjectChatRunner(ownRunners(all[3:], c), p, ""); err == nil || !strings.Contains(err.Error(), "Agent & runners") {
		t.Errorf("no eligible runner: %v", err)
	}
}
