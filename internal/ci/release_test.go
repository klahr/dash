package ci

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/gitops"
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
