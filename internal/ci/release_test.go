package ci

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

func tagRun(id int, sha, status, conclusion string) string {
	return strings.Replace(run(id, "Release", sha, status, conclusion), `"event":"push"`, `"event":"push","headBranch":"v1.0.0"`, 1)
}

func TestReleaseBuild(t *testing.T) {
	releaseEvery = time.Hour // the test refreshes by itself
	w, p, setRuns := newWatcher(t, false)
	ctx := context.Background()
	setRuns(runs())
	w.WatchRelease(p.ID, "v1.0.0", "ccc")
	if rel := w.Status(p.ID).Release; rel == nil || rel.State != ReleaseBuilding || rel.Tag != "v1.0.0" {
		t.Fatalf("right after pushing: %+v", rel)
	}

	setRuns(runs(tagRun(31, "ccc", "in_progress", ""), run(30, "CI", "bbb", "completed", "success")))
	st, _ := w.Refresh(ctx, p.ID)
	if st.Release == nil || st.Release.State != ReleaseBuilding || len(st.Release.Runs) != 1 || st.Release.Runs[0].ID != 31 {
		t.Fatalf("building: %+v", st.Release)
	}

	setRuns(runs(tagRun(31, "ccc", "completed", "success")))
	st, _ = w.Refresh(ctx, p.ID)
	if st.Release.State != ReleasePublished || st.Release.URL != "https://github.com/o/r/releases/tag/v1.0.0" {
		t.Fatalf("published: %+v", st.Release)
	}
	// Finished: later polls leave it as it is.
	setRuns(runs())
	if st, _ = w.Refresh(ctx, p.ID); st.Release.State != ReleasePublished {
		t.Fatalf("after: %+v", st.Release)
	}
}

func TestReleaseFailureCanBeInvestigated(t *testing.T) {
	releaseEvery = time.Hour
	w, p, setRuns := newWatcher(t, false)
	ctx := context.Background()
	w.WatchRelease(p.ID, "v1.0.0", "ccc")
	setRuns(runs(tagRun(41, "ccc", "completed", "failure")))
	st, _ := w.Refresh(ctx, p.ID)
	if st.Release.State != ReleaseFailed {
		t.Fatalf("failed: %+v", st.Release)
	}
	// The default branch's own runs pass; the release's failure is what gets fixed.
	setRuns(runs(run(40, "CI", "ccc", "completed", "success")))
	fix, err := w.Investigate(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fix.CIRun != 41 || !strings.Contains(fix.Prompt, "`v1.0.0` (a release tag)") {
		t.Fatalf("fix: %+v", fix)
	}
}

func TestReleaseWithoutBuild(t *testing.T) {
	releaseEvery, noBuildAfter = time.Hour, 0
	defer func() { noBuildAfter = 3 * time.Minute }()
	w, p, setRuns := newWatcher(t, false)
	w.WatchRelease(p.ID, "1.0.0", "ccc")
	setRuns(runs(run(50, "CI", "bbb", "completed", "success"))) // another commit's run
	st, _ := w.Refresh(context.Background(), p.ID)
	if st.Release.State != ReleaseNoBuild || st.Release.URL == "" {
		t.Fatalf("no build: %+v", st.Release)
	}
}

func TestCommitState(t *testing.T) {
	rs := []gitops.Run{
		{ID: 3, Workflow: "CI", SHA: "a", Status: "completed", Conclusion: "success"},
		{ID: 2, Workflow: "Lint", SHA: "a", Status: "in_progress"},
		{ID: 1, Workflow: "CI", SHA: "b", Status: "completed", Conclusion: "failure"},
	}
	for sha, want := range map[string]string{"a": Running, "b": Failing, "c": None} {
		if got := CommitState(rs, sha); got != want {
			t.Errorf("%s: %s, want %s", sha, got, want)
		}
	}
}

// pushRuns makes the stand-in gh print runs.json for the default branch's
// runs and push.json for any other ref's and for `run list --event push`
// (the runs of every branch and tag).
func pushRuns(t *testing.T, w *Watcher, p model.Project) func(string) {
	t.Helper()
	push := filepath.Join(p.Path, "push.json")
	script := "#!/bin/sh\ncase \"$*\" in\n\"run list --branch main \"*) cat " + filepath.Join(p.Path, "runs.json") + " ;;\n" +
		"\"run list \"*) cat " + push + " ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(w.Git.(*gitops.Git).GhBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return func(js string) {
		if err := os.WriteFile(push, []byte(js), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runOf(id int, wf, ref, sha, status, conclusion, at string) string {
	r := strings.Replace(run(id, wf, sha, status, conclusion), `"event":"push"`, `"event":"push","headBranch":"`+ref+`"`, 1)
	return strings.Replace(r, "2026-11-06T10:00:00Z", at, 1)
}

// A release tag pushed outside Dash (a terminal, GitHub, an agent's git
// push) is found from its runs, followed like one Dash pushed, and saved;
// runs of branches and of tags in another format are not releases.
func TestReleasePushedElsewhereIsFound(t *testing.T) {
	releaseEvery = time.Hour
	w, p, setRuns := newWatcher(t, false)
	setPush := pushRuns(t, w, p)
	ctx := context.Background()
	setRuns(runs(run(60, "CI", "aaa", "completed", "success")))
	setPush(runs(
		runOf(63, "Release", "1.3.0", "ccc", "in_progress", "", "2026-11-06T12:00:00Z"),
		runOf(62, "CI", "feature", "bbb", "completed", "failure", "2026-11-06T11:30:00Z"),
		runOf(61, "Release", "nightly-5", "bbb", "completed", "failure", "2026-11-06T11:00:00Z"),
		runOf(59, "Release", "1.2.0", "aaa", "completed", "success", "2026-11-05T10:00:00Z"),
	))
	st, _ := w.Refresh(ctx, p.ID)
	if st.Release == nil || st.Release.Tag != "1.3.0" || st.Release.State != ReleaseBuilding || !st.Release.Found {
		t.Fatalf("found: %+v", st.Release)
	}
	if st.State != Passing {
		t.Fatalf("the tag's runs changed the default branch's status: %+v", st)
	}
	builds, err := w.ReleaseBuilds(p.ID, 10)
	if err != nil || len(builds) != 2 || builds[0].Tag != "1.3.0" || builds[1].Tag != "1.2.0" || builds[1].State != ReleasePublished {
		t.Fatalf("saved builds: %v %+v", err, builds)
	}

	// The followed tag's own runs say it passed.
	setPush(runs(runOf(63, "Release", "1.3.0", "ccc", "completed", "success", "2026-11-06T12:00:00Z")))
	if st, _ = w.Refresh(ctx, p.ID); st.Release.State != ReleasePublished || st.Release.URL == "" {
		t.Fatalf("published: %+v", st.Release)
	}
	if b, err := w.Store.ReleaseBuild(p.ID, "1.3.0"); err != nil || b.State != ReleasePublished {
		t.Fatalf("saved: %v %+v", err, b)
	}

	// An older tag found later doesn't replace the newer one shown.
	setPush(runs(runOf(70, "Release", "1.1.9", "ddd", "completed", "failure", "2026-11-01T10:00:00Z")))
	if st, _ = w.Refresh(ctx, p.ID); st.Release.Tag != "1.3.0" {
		t.Fatalf("an older tag took over: %+v", st.Release)
	}
	if b, err := w.Store.ReleaseBuild(p.ID, "1.1.9"); err != nil || b.State != ReleaseFailed {
		t.Fatalf("the older tag: %v %+v", err, b)
	}
}

// A release being built is saved: after a restart (the store reopened, a
// new watcher) the status shows it again, and following it goes on.
func TestReleaseBuildSurvivesRestart(t *testing.T) {
	releaseEvery = time.Hour
	w, p, setRuns := newWatcher(t, false)
	setRuns(runs(tagRun(31, "ccc", "in_progress", "")))
	w.WatchRelease(p.ID, "v1.0.0", "ccc")
	if _, err := w.Refresh(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.Store.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(p.Path, "dash.db"), nopPub{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	w2 := &Watcher{Store: st, Git: w.Git, Pub: nopPub{}}
	w2.Resume()
	rel := w2.Status(p.ID).Release
	if rel == nil || rel.Tag != "v1.0.0" || rel.State != ReleaseBuilding || len(rel.Runs) != 1 || rel.Runs[0].ID != 31 {
		t.Fatalf("after the restart: %+v", rel)
	}
	setRuns(runs(tagRun(31, "ccc", "completed", "failure")))
	status, _ := w2.Refresh(context.Background(), p.ID)
	if status.Release == nil || status.Release.State != ReleaseFailed {
		t.Fatalf("followed after the restart: %+v", status.Release)
	}
	if b, err := st.ReleaseBuild(p.ID, "v1.0.0"); err != nil || b.State != ReleaseFailed {
		t.Fatalf("saved: %v %+v", err, b)
	}
}
