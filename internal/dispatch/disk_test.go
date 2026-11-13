package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/klahr/dash/internal/disk"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
)

// events records what the dispatcher publishes.
type events struct {
	mu  sync.Mutex
	evs []model.Event
}

func (e *events) Publish(ev model.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.evs = append(e.evs, ev)
}

func (e *events) disk() []model.RunnerDisk {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []model.RunnerDisk
	for _, ev := range e.evs {
		if ev.Type == model.EvRunnerDisk {
			out = append(out, ev.Data.(model.RunnerDisk))
		}
	}
	return out
}

// withDisk is runner r having reported free of total bytes on its
// projects' disk, as the hub's tunnel.Hub works it out.
func withDisk(r tunnel.Runner, free, total uint64) tunnel.Runner {
	st := &disk.Status{Volumes: []disk.Volume{{Label: "projects", Path: "/home/u/dash-projects", Free: free, Total: total}},
		Largest: []disk.Folder{{Path: "/home/u/.cache/go-build", Size: 110 * disk.GB}, {Path: "/home/u/.cache/quadrate/builds", Size: 30 * disk.GB}}}
	r.Reported = &tunnel.Reported{Disk: st}
	r.DiskLow, r.DiskFull = "", false
	if v, low := r.Disk.Low(*st); low {
		r.DiskLow = r.Name + " has only " + disk.Size(v.Free) + " free"
		_, r.DiskFull = r.Disk.TooFull(*st)
	}
	return r
}

func reportDisk(t *testing.T, fx *fixture, id string, free, total uint64) {
	t.Helper()
	r, _ := fx.fleet.Get(id)
	r = withDisk(r, free, total)
	fx.fleet.set(r)
	body, _ := json.Marshal(Report{Disk: r.Reported.Disk})
	if err := fx.d.Report(id, body); err != nil {
		t.Fatal(err)
	}
}

func TestDiskLowNotifiesOnceAndClears(t *testing.T) {
	fx := newFixture(t)
	pub := &events{}
	fx.d.Pub = pub
	fx.fleet.set(runnerOf("box", fx.orgA, true))
	p := fx.project(t, fx.orgA, "box")

	reportDisk(t, fx, "box", 200*disk.GB, 500*disk.GB)
	if got := pub.disk(); len(got) != 0 {
		t.Fatalf("plenty of space: %+v", got)
	}
	// Below 10 GB (10% of 500 GB is more): low, told once…
	reportDisk(t, fx, "box", 8*disk.GB, 500*disk.GB)
	reportDisk(t, fx, "box", 7*disk.GB, 500*disk.GB)
	reportDisk(t, fx, "box", 6*disk.GB, 500*disk.GB)
	got := pub.disk()
	if len(got) != 1 || !got[0].Low {
		t.Fatalf("crossing the threshold must tell once, not on every heartbeat: %+v", got)
	}
	ev := got[0]
	if ev.RunnerName != "box" || ev.OrgID != fx.orgA || len(ev.Projects) != 1 || ev.Projects[0] != p.ID {
		t.Errorf("event = %+v", ev)
	}
	if !strings.Contains(ev.Message, "8.0 GB free") || !strings.Contains(ev.Message, "/home/u/.cache/quadrate/builds (30.0 GB)") {
		t.Errorf("the warning must say what's left and list the largest cache folders: %q", ev.Message)
	}
	// …cleared once space is freed…
	reportDisk(t, fx, "box", 80*disk.GB, 500*disk.GB)
	reportDisk(t, fx, "box", 81*disk.GB, 500*disk.GB)
	if got = pub.disk(); len(got) != 2 || got[1].Low {
		t.Fatalf("freeing space must clear it, once: %+v", got)
	}
	// …and told again when it runs low again.
	reportDisk(t, fx, "box", 1*disk.GB, 500*disk.GB)
	if got = pub.disk(); len(got) != 3 || !got[2].Low || !strings.Contains(got[2].Message, "starts no tasks") {
		t.Fatalf("running low again: %+v", got)
	}
	// A report without disk news (an older runner) changes nothing.
	if err := fx.d.Report("box", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if len(pub.disk()) != 3 {
		t.Error("a report without disk space cleared the warning")
	}
}

func TestNoTaskBelowMinimumFree(t *testing.T) {
	fx := newFixture(t)
	var notes []string
	fx.d.Note = func(_ model.Todo, line string) { notes = append(notes, line) }
	fx.fleet.set(withDisk(runnerOf("box", fx.orgA, true), disk.GB+disk.GB/5, 500*disk.GB))
	p := fx.project(t, fx.orgA, "box")
	td := fx.todo(t, p, nil)

	_, _, err := fx.d.Dispatch(context.Background(), p, td)
	var we *WaitError
	if !errors.As(err, &we) {
		t.Fatalf("Dispatch = %v, want a WaitError", err)
	}
	if !strings.Contains(we.Reason, "runner box has only 1.2 GB free") {
		t.Errorf("reason = %q", we.Reason)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "runner box has only 1.2 GB free") {
		t.Errorf("the task log must say why: %q", notes)
	}
	if calls := fx.fleet.callsTo("box"); len(calls) != 0 {
		t.Errorf("the task was sent anyway: %v", calls)
	}
	// Still waiting for the same reason: the log doesn't repeat it.
	td.Waiting = we.Reason
	_, _, _ = fx.d.Dispatch(context.Background(), p, td)
	if len(notes) != 1 {
		t.Errorf("the log repeats the reason on every try: %q", notes)
	}

	// With a lower minimum set for it, it starts.
	r, _ := fx.fleet.Get("box")
	r.Disk = disk.Settings{MinFreeGB: 1}
	fx.fleet.set(withDisk(r, disk.GB+disk.GB/5, 500*disk.GB))
	if _, _, err := fx.d.Dispatch(context.Background(), p, td); err != nil {
		t.Fatalf("1.2 GB free with a 1 GB minimum: %v", err)
	}
	// The runner gets its settings with the hub's answer to its reports.
	if rep, ok := fx.d.Reply("box").(Reply); !ok || rep.Disk == nil || rep.Disk.MinFreeGB != 1 {
		t.Errorf("reply = %+v", fx.d.Reply("box"))
	}
}

// fullFleet is a runner that filled up since its last report: it refuses
// the task as the runner's POST /api/hub/tasks does.
type fullFleet struct{ *fakeFleet }

func (f fullFleet) Call(ctx context.Context, id, method, path string, in, out any) error {
	if _, ok := in.(Task); ok {
		return &tunnel.CallError{Status: 507, Msg: id + ": runner " + id + " has only 900 MB free (it needs 2.0 GB to start a task)"}
	}
	return f.fakeFleet.Call(ctx, id, method, path, in, out)
}

func TestRunnerRefusesWhenFull(t *testing.T) {
	fx := newFixture(t)
	var notes []string
	fx.d.Note = func(_ model.Todo, line string) { notes = append(notes, line) }
	fx.d.Fleet = fullFleet{fx.fleet}
	fx.fleet.set(runnerOf("box", fx.orgA, true))
	p := fx.project(t, fx.orgA, "box")
	td := fx.todo(t, p, nil)
	_, _, err := fx.d.Dispatch(context.Background(), p, td)
	var we *WaitError
	if !errors.As(err, &we) || !strings.Contains(we.Reason, "900 MB free") {
		t.Fatalf("Dispatch = %v", err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "900 MB free") {
		t.Errorf("task log: %q", notes)
	}
}
