package queue

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

// byTitle is a Claude that writes, in each task, the files files gives for
// the task's title (one of titles, found in its prompt).
func byTitle(t *testing.T, titles []string, files func(title string) map[string]string) *fakeExec {
	return &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		for _, title := range titles {
			if strings.Contains(req.Prompt, "do "+title+"\n") || strings.HasSuffix(req.Prompt, "do "+title) || strings.Contains(req.Prompt, "do "+title+" ") {
				for name, content := range files(title) {
					write(t, req.Dir, name, content)
				}
				return nil
			}
		}
		return nil
	}}
}

// holdMerges blocks the project's merge worker until the returned func is
// called (or the test ends), so jobs queued meanwhile wait together.
func (h *harness) holdMerges() (release func()) {
	h.t.Helper()
	hold, started := make(chan struct{}), make(chan struct{})
	if err := h.merges.push(h.proj.ID, mergeItem{fn: func(context.Context) { close(started); <-hold }}); err != nil {
		h.t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(waitTimeout):
		h.t.Fatal("the merge worker didn't start")
	}
	release = sync.OnceFunc(func() { close(hold) })
	h.t.Cleanup(release) // before the harness stops: the worker must get free
	return release
}

// reviewed adds a task per title and waits for each to be in review.
func (h *harness) reviewed(titles ...string) []model.Todo {
	h.t.Helper()
	var tds []model.Todo
	for _, title := range titles {
		tds = append(tds, h.add(title))
	}
	for i, td := range tds {
		tds[i] = h.waitFor(td.ID, model.StatusReview)
	}
	return tds
}

// approveTogether approves tds in order while the merge worker is held, so
// they wait in the queue together.
func (h *harness) approveTogether(tds []model.Todo) {
	h.t.Helper()
	release := h.holdMerges()
	for _, td := range tds {
		h.approve(td.ID)
	}
	release()
}

// runsLog is a file the test command appends its folder to: a test command
// recording each run.
func runsLog(t *testing.T) string {
	return filepath.ToSlash(filepath.Join(t.TempDir(), "runs"))
}

// batchRuns counts the test runs in a batch's checkout.
func batchRuns(t *testing.T, runs string) int {
	t.Helper()
	b, err := os.ReadFile(runs)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Count(string(b), "_batch")
}

func (h *harness) logOf(id string) string {
	h.t.Helper()
	b, err := h.log.Read(id)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

// mainSubjects are the subjects of main's last n commits, newest first.
func (h *harness) mainSubjects(n int) []string {
	return strings.Split(gitIn(h.t, h.proj.Path, "log", fmt.Sprintf("-%d", n), "--format=%s", "main"), "\n")
}

func ownFile(title string) map[string]string { return map[string]string{title + ".txt": title + "\n"} }

// Approved tasks waiting together are tested in one run and land one
// squashed commit each, in order.
func TestBatchLandsTasksTestedTogether(t *testing.T) {
	parallel(t)

	titles := []string{"a", "b", "c"}
	runs := runsLog(t)
	h := newHarness(t, byTitle(t, titles, ownFile), model.Project{Name: "p", MergeBatch: 3,
		TestCommand: fmt.Sprintf("pwd >> '%s'", runs)})
	tds := h.reviewed(titles...)
	h.approveTogether(tds)

	var ids []string
	for _, td := range tds {
		ids = append(ids, td.ID)
	}
	for i, td := range tds {
		td = h.waitFor(td.ID, model.StatusMerged)
		if body := gitIn(t, h.proj.Path, "log", "-1", "--format=%B", td.MergedCommit); !strings.Contains(body, "Dash-Todo: "+td.ID) {
			t.Errorf("%s's merged commit %s is another task's:\n%s", td.Title, td.MergedCommit, body)
		}
		if td.Checks == nil || !slices.Equal(td.Checks.Batch, ids) || !td.Checks.Passed {
			t.Errorf("%s: want the batch's passing checks, got %+v", td.Title, td.Checks)
		}
		log := h.logOf(td.ID)
		if !strings.Contains(log, "in a batch of 3, tested together with") || !strings.Contains(log, "batch of 3 passed") {
			t.Errorf("%s's log should record the batch:\n%s", td.Title, log)
		}
		if got := h.onMain(titles[i] + ".txt"); got != titles[i]+"\n" {
			t.Errorf("main has %s.txt %q", titles[i], got)
		}
	}
	if n := batchRuns(t, runs); n != 1 {
		t.Errorf("the batch's tests ran %d times, want once", n)
	}
	if got := h.mainSubjects(3); !slices.Equal(got, []string{"c", "b", "a"}) {
		t.Errorf("main's commits = %q, want one per task, in order", got)
	}
}

// When a batch fails, the task that fails on its own is sent back and the
// others land.
func TestBatchFailureBlamesOnlyTheCulprit(t *testing.T) {
	parallel(t)

	titles := []string{"good1", "bad", "good2"}
	runs := runsLog(t)
	files := func(title string) map[string]string {
		if title == "bad" {
			return map[string]string{"bad.txt": "broken\n"}
		}
		return ownFile(title)
	}
	h := newHarness(t, byTitle(t, titles, files), model.Project{Name: "p", MergeBatch: 3,
		TestCommand: fmt.Sprintf("pwd >> '%s'; ! test -e bad.txt", runs)})
	tds := h.reviewed(titles...)
	h.approveTogether(tds)

	// Wait in the order the queue gets there: the good ones land after the
	// batch's run and three runs one by one (slow on a busy machine, so a
	// longer bound), and only then is the culprit sent back.
	goods := []model.Todo{tds[0], tds[2]}
	for _, td := range goods {
		h.waitForWithin(td.ID, model.StatusMerged, 3*waitTimeout)
	}
	bad := h.waitFor(tds[1].ID, model.StatusFailed)
	if !strings.Contains(bad.Error, "tests fail") {
		t.Errorf("bad's error = %q", bad.Error)
	}
	if fb := strings.Join(bad.Feedback, "\n"); !strings.Contains(fb, "in a batch of 3") {
		t.Errorf("bad's feedback should say it was batched:\n%s", fb)
	}
	if log := h.logOf(bad.ID); !strings.Contains(log, "testing it on its own, on top of main and #"+tds[0].ID) {
		t.Errorf("bad's log should show it tested on its own on top of good1:\n%s", log)
	}
	for _, td := range goods {
		if got := h.onMain(td.Title + ".txt"); got != td.Title+"\n" {
			t.Errorf("main has %s.txt %q", td.Title, got)
		}
	}
	if got := h.onMain("bad.txt"); got != "" {
		t.Errorf("the culprit landed: bad.txt = %q", got)
	}
	if n := batchRuns(t, runs); n != 4 {
		t.Errorf("tests ran %d times in the batch's checkout, want 4 (the batch, then each task)", n)
	}
	if got := h.mainSubjects(2); !slices.Equal(got, []string{"good2", "good1"}) {
		t.Errorf("main's commits = %q", got)
	}
}

// A batch failing once, flakily, blames nobody: each task passes on its own
// and they all land.
func TestFlakyBatchBlamesNobody(t *testing.T) {
	parallel(t)

	titles := []string{"a", "b"}
	runs := runsLog(t)
	flag := filepath.ToSlash(filepath.Join(t.TempDir(), "flaked"))
	h := newHarness(t, byTitle(t, titles, ownFile), model.Project{Name: "p", MergeBatch: 2,
		TestCommand: fmt.Sprintf("pwd >> '%s'; case \"$PWD\" in *_batch*) test -e '%s' || { touch '%s'; exit 1; };; esac", runs, flag, flag)})
	tds := h.reviewed(titles...)
	h.approveTogether(tds)
	for _, td := range tds {
		h.waitFor(td.ID, model.StatusMerged)
	}
	if n := batchRuns(t, runs); n != 3 {
		t.Errorf("tests ran %d times in the batch's checkout, want 3", n)
	}
}

// Tasks that conflict with each other aren't tested together: the later one
// waits for a later merge (here, sent back to resolve the conflict).
func TestConflictingTasksAreNotBatchedTogether(t *testing.T) {
	parallel(t)

	titles := []string{"x1", "x2", "c"}
	runs := runsLog(t)
	files := func(title string) map[string]string {
		if title == "c" {
			return ownFile(title)
		}
		return map[string]string{"shared.txt": "from " + title + "\n"}
	}
	h := newHarness(t, byTitle(t, titles, files), model.Project{Name: "p", MergeBatch: 3,
		TestCommand: fmt.Sprintf("pwd >> '%s'", runs)})
	tds := h.reviewed(titles...)
	h.approveTogether(tds)

	x1, c := h.waitFor(tds[0].ID, model.StatusMerged), h.waitFor(tds[2].ID, model.StatusMerged)
	x2 := h.waitFor(tds[1].ID, model.StatusFailed)
	if !strings.Contains(x2.Error, "conflicts with main") {
		t.Errorf("x2's error = %q", x2.Error)
	}
	if log := h.logOf(x2.ID); !strings.Contains(log, "not merged in a batch with #"+x1.ID) || strings.Contains(log, "tested together") {
		t.Errorf("x2 should be kept out of x1's batch:\n%s", log)
	}
	if c.Checks == nil || !slices.Equal(c.Checks.Batch, []string{x1.ID, c.ID}) {
		t.Errorf("c should be tested with x1, checks %+v", c.Checks)
	}
	if got := h.onMain("shared.txt"); got != "from x1\n" {
		t.Errorf("main has shared.txt %q", got)
	}
	if n := batchRuns(t, runs); n != 1 {
		t.Errorf("tests ran %d times in the batch's checkout, want 1", n)
	}
}

// When main moves while a batch is tested, nothing untested lands: the
// batch is built and tested again on top of the new main.
func TestBatchRetestsWhenMainMoves(t *testing.T) {
	parallel(t)

	titles := []string{"a", "b"}
	runs := runsLog(t)
	h := newHarness(t, byTitle(t, titles, ownFile), model.Project{Name: "p", MergeBatch: 2,
		TestCommand: fmt.Sprintf("pwd >> '%s'", runs)})
	tds := h.reviewed(titles...)
	var once sync.Once
	h.merges.batchTested = func() {
		once.Do(func() {
			write(t, h.proj.Path, "m.txt", "main\n")
			for _, args := range [][]string{{"add", "m.txt"}, {"commit", "-qm", "add m.txt"}} {
				out, err := exec.Command("git", append([]string{"-C", h.proj.Path, "-c", "commit.gpgsign=false",
					"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
				if err != nil {
					t.Errorf("git %v: %v: %s", args, err, out)
				}
			}
		})
	}
	h.approveTogether(tds)
	for _, td := range tds {
		td = h.waitFor(td.ID, model.StatusMerged)
		if log := h.logOf(td.ID); !strings.Contains(log, "moved during the batch") {
			t.Errorf("%s's log should say main moved:\n%s", td.Title, log)
		}
	}
	if n := batchRuns(t, runs); n != 2 {
		t.Errorf("the batch's tests ran %d times, want 2 (again on top of the new main)", n)
	}
	if got := h.mainSubjects(3); !slices.Equal(got, []string{"b", "a", "add m.txt"}) {
		t.Errorf("main's commits = %q", got)
	}
	// What landed is what was tested last: main's tree is the batch's.
	if tree, tested := gitIn(t, h.proj.Path, "rev-parse", "main^{tree}"), h.batchTree(); tree != tested {
		t.Errorf("main's tree %s isn't the tested tree %s", tree, tested)
	}
}

// batchTree is the tree of what the batch's checkout last had.
func (h *harness) batchTree() string {
	return gitIn(h.t, h.merges.batchDir(h.proj), "rev-parse", "HEAD^{tree}")
}

// A batch size of 1, or a batch of one task, merges as before: one task at
// a time, and no test run again on a commit it passed on.
func TestBatchOfOneMergesAsBefore(t *testing.T) {
	parallel(t)

	for _, size := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			parallel(t)
			titles := []string{"a", "b"}
			runs := runsLog(t)
			h := newHarness(t, byTitle(t, titles, ownFile), model.Project{Name: "p", MergeBatch: size,
				TestCommand: fmt.Sprintf("pwd >> '%s'", runs)})
			if size <= 1 {
				tds := h.reviewed(titles...)
				h.approveTogether(tds)
				a, b := h.waitFor(tds[0].ID, model.StatusMerged), h.waitFor(tds[1].ID, model.StatusMerged)
				if n := testRuns(t, h, a.ID); n != 1 {
					t.Errorf("a's tests ran %d times, want once (in review)", n)
				}
				if n := testRuns(t, h, b.ID); n != 2 {
					t.Errorf("b's tests ran %d times, want twice (main moved)", n)
				}
				for _, td := range []model.Todo{a, b} {
					if log := h.logOf(td.ID); strings.Contains(log, "batch") {
						t.Errorf("%s was batched:\n%s", td.Title, log)
					}
				}
			} else {
				td := h.reviewed("a")[0]
				h.approve(td.ID)
				td = h.waitFor(td.ID, model.StatusMerged)
				if n := testRuns(t, h, td.ID); n != 1 {
					t.Errorf("tests ran %d times, want once (in review)", n)
				}
			}
			if n := batchRuns(t, runs); n != 0 {
				t.Errorf("tests ran %d times in a batch's checkout", n)
			}
		})
	}
}
