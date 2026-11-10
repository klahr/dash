package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
)

func reportPerms(t *testing.T, d *Dispatcher, runner string, ps ...Permission) {
	t.Helper()
	body, _ := json.Marshal(Report{Permissions: ps})
	if err := d.Report(runner, body); err != nil {
		t.Fatal(err)
	}
}

func permEvents(r *recorder) []perm.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []perm.Request
	for _, ev := range r.evs {
		if q, ok := ev.Data.(perm.Request); ok && ev.Type == model.EvPermission {
			out = append(out, q)
		}
	}
	return out
}

// Claude's permission requests in the hub's tasks wait on the runner's
// broker: the runner reports those of the hub's tasks, with the hub's ids,
// and leaves out its own tasks' and those already answered.
func TestReporterSendsTheHubsTasksPermissionRequests(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	rp := fx.project(t, fx.orgA)
	mine := fx.todo(t, rp, nil)
	hubs := fx.todo(t, rp, func(x *model.Todo) { x.Status, x.HubTodoID = model.StatusInProgress, "h1" })
	big := `{"file_path":"a.go","content":"` + strings.Repeat("x", 100<<10) + `"}`
	reqs := []perm.Request{
		{ID: "p1", TodoID: hubs.ID, Tool: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
		{ID: "p2", TodoID: mine.ID, Tool: "Bash", Input: json.RawMessage(`{}`)},
		{ID: "p3", TodoID: hubs.ID, Tool: "Write", Input: json.RawMessage(big)},
		{ID: "p4", TodoID: hubs.ID, Tool: "Bash", Decision: "allow"},
	}
	rep := &Reporter{Store: fx.st, Perms: func() []perm.Request { return reqs }}
	r := rep.Build(time.Now())
	if len(r.Permissions) != 2 || r.Permissions[0].ID != "p1" || r.Permissions[0].HubTodoID != "h1" ||
		string(r.Permissions[0].Input) != `{"command":"ls"}` || r.Permissions[1].ID != "p3" {
		t.Fatalf("reported: %+v", r.Permissions)
	}
	var in map[string]string
	if err := json.Unmarshal(r.Permissions[1].Input, &in); err != nil || in["file_path"] != "a.go" || len(in["content"]) > maxPermInput {
		t.Fatalf("big input not cut down: %v %d", err, len(r.Permissions[1].Input))
	}
}

// The hub shows a runner's requests of its tasks on its board, answers
// them on that runner, and stops showing them once they are answered or
// the runner no longer has them.
func TestHubRelaysPermissionRequestsOfTasksOnRunners(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	pub := &recorder{}
	fx.d.Pub = pub
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	fx.fleet.set(runnerOf("desk", fx.orgA, true))
	fx.fleet.set(runnerOf("beta-box", fx.orgB, true))
	p := fx.project(t, fx.orgA, "laptop", "desk")
	td := fx.todo(t, p, func(x *model.Todo) { x.Status, x.Runner, x.RemoteID = model.StatusInProgress, "laptop", "r1" })
	req := Permission{ID: "p1", HubTodoID: td.ID, Tool: "Bash", Input: json.RawMessage(`{"command":"ls"}`), Created: time.Now()}

	// Only the task's own runner may raise a request for it.
	reportPerms(t, fx.d, "desk", req)
	reportPerms(t, fx.d, "beta-box", req)
	if got := fx.d.Permissions(""); len(got) != 0 {
		t.Fatalf("another runner's request was shown: %+v", got)
	}

	reportPerms(t, fx.d, "laptop", req)
	reportPerms(t, fx.d, "laptop", req) // every report lists it again
	got := fx.d.Permissions(p.ID)
	if len(got) != 1 || got[0].ID != "p1" || got[0].TodoID != td.ID || got[0].ProjectID != p.ID || got[0].OrgID != fx.orgA {
		t.Fatalf("pending: %+v", got)
	}
	if evs := permEvents(pub); len(evs) != 1 || evs[0].Decision != "" || evs[0].TodoID != td.ID {
		t.Fatalf("published: %+v", evs)
	}
	if other := fx.d.Permissions("another"); len(other) != 0 {
		t.Fatalf("listed for another project: %+v", other)
	}

	// Answered on the hub: on the runner, over its tunnel.
	if err := fx.d.DecidePermission(context.Background(), "p1", "allow", ""); err != nil {
		t.Fatal(err)
	}
	calls := fx.fleet.callsTo("laptop")
	if last := calls[len(calls)-1]; last != "laptop POST /api/hub/tasks/"+td.ID+"/permissions/p1" {
		t.Fatalf("call: %s", last)
	}
	if evs := permEvents(pub); len(evs) != 2 || evs[1].Decision != "allow" {
		t.Fatalf("published: %+v", evs)
	}
	// A report built before the answer reached the runner doesn't bring it back.
	reportPerms(t, fx.d, "laptop", req)
	if got := fx.d.Permissions(""); len(got) != 0 {
		t.Fatalf("answered request came back: %+v", got)
	}
	if err := fx.d.DecidePermission(context.Background(), "p1", "allow", ""); !errors.Is(err, perm.ErrNotFound) {
		t.Fatalf("answering twice: %v", err)
	}

	// Answered on the runner (or its task ended): the next report drops it.
	req.ID = "p2"
	reportPerms(t, fx.d, "laptop", req)
	reportPerms(t, fx.d, "laptop")
	if got := fx.d.Permissions(""); len(got) != 0 {
		t.Fatalf("still shown: %+v", got)
	}
	if evs := permEvents(pub); evs[len(evs)-1].ID != "p2" || evs[len(evs)-1].Decision == "" {
		t.Fatalf("published: %+v", evs)
	}

	// A runner that goes offline can't be answered: its requests go.
	req.ID = "p3"
	reportPerms(t, fx.d, "laptop", req)
	fx.d.State("laptop", false)
	if got := fx.d.Permissions(""); len(got) != 0 {
		t.Fatalf("offline runner's request shown: %+v", got)
	}
}
