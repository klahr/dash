package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

type eventRec struct {
	mu  sync.Mutex
	evs []model.Event
}

func (r *eventRec) Publish(ev model.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
}

// checklists counts the checklist.updated events of project id.
func (r *eventRec) checklists(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ev := range r.evs {
		if ev.Type == model.EvChecklist && ev.ProjectID == id {
			n++
		}
	}
	return n
}

// Open checklists hear when TODO.md changes on the default branch, however
// it changed — committed by CommitFile (the checklist UI, the board's MCP
// tools), merged with a task, or pulled from origin — and not when the
// branch moves without touching it.
func TestChecklistChangesAreAnnounced(t *testing.T) {
	ctx := context.Background()
	var file atomic.Value // what the next task changes
	file.Store("TODO.md")
	h := newHarness(t, &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		return writeFile("", file.Load().(string))(n, req)
	}}, model.Project{Name: "p"})
	rec := &eventRec{}
	h.merges.Remote.Pub = rec
	id := h.proj.ID

	// A commit of TODO.md (none there yet) tells.
	_, blob, err := h.merges.CommitFile(ctx, h.proj, "TODO.md", "", "- [ ] One\n", "add TODO.md")
	if err != nil {
		t.Fatal(err)
	}
	if n := rec.checklists(id); n != 1 {
		t.Fatalf("after committing TODO.md: %d events, want 1", n)
	}
	// So does a second one, but a commit of another file doesn't.
	if _, _, err := h.merges.CommitFile(ctx, h.proj, "TODO.md", blob, "- [x] One\n", "check"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.merges.CommitFile(ctx, h.proj, "README.md", "", "hi\n", "readme"); err != nil {
		t.Fatal(err)
	}
	if n := rec.checklists(id); n != 2 {
		t.Fatalf("after another TODO.md commit and a README: %d events, want 2", n)
	}

	// A merged task that changed TODO.md tells; one that didn't, doesn't.
	td := h.waitFor(h.add("todo").ID, model.StatusReview)
	h.approve(td.ID)
	h.waitFor(td.ID, model.StatusMerged)
	if n := rec.checklists(id); n != 3 {
		t.Fatalf("after merging a change to TODO.md: %d events, want 3", n)
	}
	file.Store("other.txt")
	td = h.waitFor(h.add("other").ID, model.StatusReview)
	h.approve(td.ID)
	h.waitFor(td.ID, model.StatusMerged)
	if n := rec.checklists(id); n != 3 {
		t.Fatalf("after merging another file: %d events, want 3", n)
	}

	// Someone pushes a change to TODO.md; pulling it in tells.
	_, other := h.addOrigin(false, "")
	write(t, other, "TODO.md", "- [ ] Theirs\n")
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-m", "theirs")
	gitRun(t, other, "push", "origin", "main")
	if moved, err := h.merges.Remote.Pull(ctx, h.proj); err != nil || moved != 1 {
		t.Fatalf("pull: moved %d, %v", moved, err)
	}
	if n := rec.checklists(id); n != 4 {
		t.Fatalf("after pulling a change to TODO.md: %d events, want 4", n)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, ev := range rec.evs {
		if ev.Type == model.EvChecklist && ev.Time.IsZero() {
			t.Errorf("event without a time: %+v", ev)
		}
	}
}
