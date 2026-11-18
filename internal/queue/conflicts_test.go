package queue

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

func TestConflictPairs(t *testing.T) {
	t.Parallel()
	task := func(base string, files ...string) *branchTask { return &branchTask{base: base, files: files} }
	tasks := []*branchTask{
		0: task("main", "a.go", "b.go"),
		1: task("main", "b.go"),
		2: task("main", "c.go"),
		3: task("main"), // no commits yet
		4: task("dev", "a.go"),
		5: task("main", "c.go", "a.go"),
	}
	want := [][2]int{{0, 1}, {0, 5}, {2, 5}} // 4 shares a.go but branches from another branch
	if got := conflictPairs(tasks); !slices.Equal(got, want) {
		t.Fatalf("conflictPairs = %v, want %v", got, want)
	}
	if conflictPairs(nil) != nil || conflictPairs(tasks[:1]) != nil {
		t.Fatal("no pairs without two tasks")
	}
}

func TestNewlyAhead(t *testing.T) {
	t.Parallel()
	byID := map[string]model.Todo{
		"m": {ID: "m", Status: model.StatusMerging},
		"r": {ID: "r", Status: model.StatusReview},
		"n": {ID: "n", Status: model.StatusMerging},
	}
	cs := []model.Conflict{
		{Base: true, Name: "main", Files: []string{"x"}},
		{TodoID: "m", Files: []string{"a"}},
		{TodoID: "r", Files: []string{"b"}},
		{TodoID: "n", Files: []string{"c"}},
		{TodoID: "gone", Files: []string{"d"}},
	}
	ids := func(cs []model.Conflict) (out []string) {
		for _, c := range cs {
			out = append(out, c.TodoID)
		}
		return out
	}
	// Only merging tasks are ahead; one already noted is left out.
	got := newlyAhead(model.Todo{ID: "t", Status: model.StatusInProgress, ConflictNoted: []string{"n"}}, cs, byID)
	if !slices.Equal(ids(got), []string{"m"}) {
		t.Fatalf("newlyAhead = %v, want [m]", ids(got))
	}
	// A task merging itself isn't behind anyone.
	if got := newlyAhead(model.Todo{ID: "t", Status: model.StatusMerging}, cs, byID); got != nil {
		t.Fatalf("newlyAhead of a merging task = %v", ids(got))
	}
}

// The watch finds which tasks' branches conflict, with each other and with
// the default branch, notes a conflict with a task ahead in the merge queue
// once, and forgets a task once it has left.
func TestConflictWatch(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		name := "shared.txt"
		if strings.Contains(req.Prompt, "elsewhere") {
			name = "other.txt"
		}
		return os.WriteFile(filepath.Join(req.Dir, name), []byte("line from "+filepath.Base(req.Dir)+"\n"), 0o644)
	}}
	h := newHarness(t, exe, model.Project{Name: "p"})
	a, b, c := h.add("first"), h.add("second"), h.add("elsewhere")
	h.waitFor(a.ID, model.StatusReview)
	h.waitFor(b.ID, model.StatusReview)
	h.waitFor(c.ID, model.StatusReview)

	ctx := context.Background()
	w := NewConflictWatch(&ConflictWatch{Store: h.st, Git: h.git, Log: h.log})
	check := func() {
		t.Helper()
		p, err := h.st.GetProject(h.proj.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Check(ctx, p); err != nil {
			if strings.Contains(err.Error(), "2.38") {
				t.Skip("git is older than 2.38")
			}
			t.Fatal(err)
		}
	}
	get := func(id string) model.Todo {
		t.Helper()
		td, err := h.st.GetTodo(id)
		if err != nil {
			t.Fatal(err)
		}
		return td
	}
	conflicts := func(id string) string {
		var out []string
		for _, c := range get(id).Conflicts {
			who := c.TodoID
			if c.Base {
				who = "base:" + c.Name
			}
			out = append(out, who+" "+strings.Join(c.Files, ","))
		}
		return strings.Join(out, "; ")
	}

	check()
	if got := conflicts(a.ID); got != b.ID+" shared.txt" {
		t.Fatalf("a's conflicts = %q", got)
	}
	if got := conflicts(b.ID); got != a.ID+" shared.txt" {
		t.Fatalf("b's conflicts = %q", got)
	}
	if got := conflicts(c.ID); got != "" {
		t.Fatalf("c's conflicts = %q, want none", got)
	}
	if fb := get(b.ID).Feedback; len(fb) != 0 {
		t.Fatalf("a note while nobody is ahead: %q", fb)
	}
	if conflicts := get(a.ID).Conflicts; conflicts[0].Name != "first" && conflicts[0].Name != "second" {
		t.Fatalf("conflict named %q", conflicts[0].Name)
	}
	// Both sides record the conflict found as a link, once.
	links := func(id string) string {
		var out []string
		for _, l := range get(id).Links {
			dir := "in"
			if l.Out {
				dir = "out"
			}
			out = append(out, l.Kind+" "+dir+" "+l.TodoID+" "+strings.Join(l.Files, ","))
		}
		return strings.Join(out, "; ")
	}
	check()
	if got, want := links(a.ID), "conflict in "+b.ID+" shared.txt"; got != want {
		t.Fatalf("a's links = %q, want %q", got, want)
	}
	if got, want := links(b.ID), "conflict in "+a.ID+" shared.txt"; got != want {
		t.Fatalf("b's links = %q, want %q", got, want)
	}
	if got := links(c.ID); got != "" {
		t.Fatalf("c's links = %q, want none", got)
	}

	// a is approved: it is ahead of b in the merge queue, so b hears of it, once.
	if _, err := h.st.SetStatus(a.ID, model.StatusMerging, ""); err != nil {
		t.Fatal(err)
	}
	check()
	check()
	fb := get(b.ID).Feedback
	if len(fb) != 1 || !strings.Contains(fb[0], "#"+a.ID) || !strings.Contains(fb[0], "shared.txt") {
		t.Fatalf("b's notes = %q, want one about %s in shared.txt", fb, a.ID)
	}
	if got := get(b.ID).ConflictNoted; !slices.Equal(got, []string{a.ID}) {
		t.Fatalf("b's ConflictNoted = %q", got)
	}
	if fb := get(a.ID).Feedback; len(fb) != 0 {
		t.Fatalf("a, ahead, got a note: %q", fb)
	}
	// The note shows on both: a gave it (out), b got it, not yet delivered.
	if got, want := links(b.ID), "conflict in "+a.ID+" shared.txt; note in "+a.ID+" shared.txt"; got != want {
		t.Fatalf("b's links = %q, want %q", got, want)
	}
	if got, want := links(a.ID), "conflict in "+b.ID+" shared.txt; note out "+b.ID+" shared.txt"; got != want {
		t.Fatalf("a's links = %q, want %q", got, want)
	}
	// b's run log has the note as an entry linked to a.
	evs, err := h.log.Events(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var noted []model.RunEvent
	for _, ev := range evs {
		if len(ev.Tasks) > 0 {
			noted = append(noted, ev)
		}
	}
	if len(noted) != 1 || noted[0].Tasks[0].ID != a.ID || !strings.HasPrefix(noted[0].Text, "📨 From #"+a.ID) || !strings.Contains(noted[0].Text, "shared.txt") {
		t.Fatalf("b's linked log entries = %+v", noted)
	}

	// The default branch moves on and changes other.txt too: c conflicts with it.
	write(t, h.proj.Path, "other.txt", "from main\n")
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "main"}} {
		if out, err := exec.Command("git", append([]string{"-C", h.proj.Path}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	check()
	if got := conflicts(c.ID); got != "base:main other.txt" {
		t.Fatalf("c's conflicts = %q, want main in other.txt", got)
	}

	// a fails: it is no longer anyone's conflict, nor has any.
	if _, err := h.st.SetStatus(a.ID, model.StatusFailed, "x"); err != nil {
		t.Fatal(err)
	}
	check()
	if got := conflicts(a.ID); got != "" {
		t.Fatalf("a's conflicts after it failed = %q", got)
	}
	if got := conflicts(b.ID); got != "" {
		t.Fatalf("b's conflicts after a failed = %q", got)
	}
	if fb := get(b.ID).Feedback; len(fb) != 1 {
		t.Fatalf("b's notes = %q, want still one", fb)
	}
}

// Run looks at a project shortly after one of its tasks changes.
func TestConflictWatchRunsOnChange(t *testing.T) {
	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		return os.WriteFile(filepath.Join(req.Dir, "shared.txt"), []byte(filepath.Base(req.Dir)+"\n"), 0o644)
	}}
	h := newHarness(t, exe, model.Project{Name: "p"})
	w := NewConflictWatch(&ConflictWatch{Store: h.st, Git: h.git, Delay: 10 * time.Millisecond, Every: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	a, b := h.add("first"), h.add("second")
	h.waitFor(a.ID, model.StatusReview)
	h.waitFor(b.ID, model.StatusReview)
	w.Observe(model.Event{Type: model.EvTodoUpdated, ProjectID: h.proj.ID})
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if td, _ := h.st.GetTodo(a.ID); len(td.Conflicts) == 1 && td.Conflicts[0].TodoID == b.ID {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	td, _ := h.st.GetTodo(a.ID)
	t.Fatalf("a's conflicts = %+v, want one with %s", td.Conflicts, b.ID)
}

// A run marks the conflict notes the task got as sent to Claude, once, in
// its links and, linked to the task each is from, in its run log.
func TestDeliverNotes(t *testing.T) {
	h := newHarness(t, &fakeExec{}, model.Project{Name: "p"})
	td, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: "behind", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	td, err = h.st.UpdateTodo(td.ID, func(x *model.Todo) error {
		x.Links = model.AddLink(x.Links, model.Link{Kind: model.LinkNote, TodoID: "ahead", HubTodoID: "hub-ahead", Name: "ahead", Files: []string{"a.go"}})
		x.Links = model.AddLink(x.Links, model.Link{Kind: model.LinkNote, Out: true, TodoID: "behind-it"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []model.RunEvent
	emit := func(ev model.RunEvent) { got = append(got, ev) }
	h.engine.deliverNotes(&td, emit)
	if len(got) != 1 || got[0].Text != "📬 Note from #ahead sent to Claude: conflicts in a.go" ||
		len(got[0].Tasks) != 1 || got[0].Tasks[0] != (model.TaskRef{ID: "ahead", HubID: "hub-ahead", Name: "ahead"}) {
		t.Fatalf("events = %+v", got)
	}
	saved, err := h.st.GetTodo(td.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Links[0].Delivered || saved.Links[1].Delivered {
		t.Fatalf("links = %+v, want only the note got delivered", saved.Links)
	}
	got = nil
	h.engine.deliverNotes(&saved, emit)
	if len(got) != 0 {
		t.Fatalf("delivered again: %+v", got)
	}
}
