package scan

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

// block makes the next agent runs wait until release is closed (or their
// context ends), and returns release.
func (fx *fixture) block() chan struct{} {
	fx.exec.mu.Lock()
	defer fx.exec.mu.Unlock()
	fx.exec.release, fx.exec.started = make(chan struct{}), make(chan struct{}, 16)
	return fx.exec.release
}

// unblock lets later agent runs answer at once.
func (fx *fixture) unblock() {
	fx.exec.mu.Lock()
	fx.exec.release = nil
	fx.exec.mu.Unlock()
}

// restart replaces the runner as a new Dash process would (same store and
// data directory, a fresh context).
func (fx *fixture) restart(t *testing.T) {
	old := fx.runner
	old.Wait()
	fx.runner = &Runner{Store: fx.st, Git: gitops.New(nil), Exec: fx.exec, Root: old.Root, Now: old.Now}
	t.Cleanup(fx.runner.Wait)
	fx.unblock()
}

// interrupt starts a run of the scan, shuts Dash down while the agent
// works, restarts the runner, and returns the run as stored.
func (fx *fixture) interrupt(t *testing.T, name, trigger string) model.ScanRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	fx.runner.Context = ctx
	fx.block()
	r, err := fx.runner.Start(fx.p.ID, name, trigger)
	if err != nil {
		t.Fatal(err)
	}
	<-fx.exec.started
	cancel()
	fx.restart(t)
	got, _ := fx.st.GetScanRun(r.ID)
	if got.Status != model.ScanInterrupted || got.FinishedAt == nil || got.Error != "" {
		t.Fatalf("shut down mid-scan: %+v", got)
	}
	return got
}

func TestShutdownInterruptsUserCancelCancels(t *testing.T) {
	fx := newFixture(t, "slow", "Go.")
	r := fx.interrupt(t, "slow", "manual")
	if r.Note == "" {
		t.Fatalf("no note on interrupted run: %+v", r)
	}
	// The user's cancel, even with Dash's context set: canceled.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.runner.Context = ctx
	fx.block()
	r, _ = fx.runner.Start(fx.p.ID, "slow", "manual")
	<-fx.exec.started
	fx.runner.Cancel(r.ID)
	fx.runner.Wait()
	if got, _ := fx.st.GetScanRun(r.ID); got.Status != model.ScanCanceled || got.Note != "" {
		t.Fatalf("user cancel: %+v", got)
	}
	// Canceled runs are never restarted.
	if got := fx.runner.ResumeInterrupted(); len(got) != 0 {
		t.Fatalf("restarted after a cancel: %+v", got)
	}
}

func TestStoreMarksStaleRunningInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dash.db")
	st, _ := store.Open(path, nil)
	p, _ := st.CreateProject(model.Project{Name: "p", Path: t.TempDir()})
	r, _ := st.CreateScanRun(model.ScanRun{ProjectID: p.ID, Scan: "x", Status: model.ScanRunning, StartedAt: time.Now()})
	st, _ = store.Open(path, nil) // after a crash: the run was never finished
	if got, _ := st.GetScanRun(r.ID); got.Status != model.ScanInterrupted || got.FinishedAt == nil || got.Note == "" {
		t.Fatalf("after restart: %+v", got)
	}
}

func TestResumeOnceAfterCrash(t *testing.T) {
	fx := newFixture(t, "sec", "---\nscope: repo\n---\nGo.")
	// A crash: the run is still "running" in the saved state.
	orig, err := fx.st.CreateScanRun(model.ScanRun{ProjectID: fx.p.ID, Scan: "sec", Trigger: "schedule", Scope: model.ScopeRepo, Status: model.ScanRunning, StartedAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(fx.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	fx.st = st
	fx.restart(t)

	got := fx.runner.ResumeInterrupted()
	if len(got) != 1 || got[0].Trigger != model.TriggerResume || got[0].ResumedFrom != orig.ID {
		t.Fatalf("resumed: %+v", got)
	}
	fx.runner.Wait()
	if r, _ := fx.st.GetScanRun(got[0].ID); r.Status != model.ScanSucceeded {
		t.Fatalf("resumed run: %+v", r)
	}
	if o, _ := fx.st.GetScanRun(orig.ID); o.Status != model.ScanInterrupted || o.ResumedAs != got[0].ID {
		t.Fatalf("original: %+v", o)
	}
	// The next start doesn't restart it again.
	fx.restart(t)
	if again := fx.runner.ResumeInterrupted(); len(again) != 0 {
		t.Fatalf("restarted twice: %+v", again)
	}
}

func TestResumedRunNotRestartedAgain(t *testing.T) {
	fx := newFixture(t, "sec", "Go.")
	fx.interrupt(t, "sec", "manual")
	// The restart is cut off too, as if the scan itself crashed Dash.
	ctx, cancel := context.WithCancel(context.Background())
	fx.runner.Context = ctx
	fx.block()
	got := fx.runner.ResumeInterrupted()
	if len(got) != 1 {
		t.Fatalf("resumed: %+v", got)
	}
	<-fx.exec.started
	cancel()
	fx.restart(t)
	if r, _ := fx.st.GetScanRun(got[0].ID); r.Status != model.ScanInterrupted {
		t.Fatalf("resumed run: %+v", r)
	}
	if again := fx.runner.ResumeInterrupted(); len(again) != 0 {
		t.Fatalf("restarted a restart: %+v", again)
	}
	if r, _ := fx.st.GetScanRun(got[0].ID); r.ResumedAs != "" || !strings.Contains(r.Note, "Not restarted") {
		t.Fatalf("restart of a restart: %+v", r)
	}
}

func TestResumeSkipsOldGoneAndSuperseded(t *testing.T) {
	fx := newFixture(t, "sec", "Go.")
	now := time.Now()
	fx.runner.Now = func() time.Time { return now }
	old := fx.interrupt(t, "sec", "manual")

	// Older than a day: left alone.
	fx.runner.Now = func() time.Time { return now.Add(ResumeMaxAge + time.Minute) }
	if got := fx.runner.ResumeInterrupted(); len(got) != 0 {
		t.Fatalf("restarted a day-old run: %+v", got)
	}
	fx.runner.Now = func() time.Time { return now }

	// The template is gone: left interrupted, with a note.
	if err := os.Remove(filepath.Join(fx.repo, ".dash", "scans", "sec.md")); err != nil {
		t.Fatal(err)
	}
	writeTemplate(t, fx, "other", "Go.")
	if got := fx.runner.ResumeInterrupted(); len(got) != 0 {
		t.Fatalf("restarted without a template: %+v", got)
	}
	if r, _ := fx.st.GetScanRun(old.ID); r.Status != model.ScanInterrupted || !strings.Contains(r.Note, "template is gone") {
		t.Fatalf("template gone: %+v", r)
	}

	// The scan ran again since: that run covers it.
	writeTemplate(t, fx, "sec", "Go.")
	fx.runner.Now = func() time.Time { return now.Add(time.Minute) }
	fx.run(t, "sec")
	if got := fx.runner.ResumeInterrupted(); len(got) != 0 {
		t.Fatalf("restarted a superseded run: %+v", got)
	}
}

func TestResumeNoDuplicateWithSchedulerCatchUp(t *testing.T) {
	fx, s, c := schedFixture(t, "---\nschedule: 0 */6 * * *\n---\nGo.")
	enable(t, fx, "nightly")
	// The 06:00 run is cut off by a shutdown.
	c.t = at("2026-11-08 06:00")
	ctx, cancel := context.WithCancel(context.Background())
	fx.runner.Context = ctx
	fx.block()
	if got := s.Tick(); len(got) != 1 {
		t.Fatalf("scheduled: %+v", got)
	}
	<-fx.exec.started
	cancel()
	fx.restart(t)
	s.Runner = fx.runner

	// Dash is back at 13:00, after the 12:00 slot: one run, not two.
	c.t = at("2026-11-08 13:00")
	release := fx.block()
	resumed := fx.runner.ResumeInterrupted()
	if len(resumed) != 1 {
		t.Fatalf("resumed: %+v", resumed)
	}
	if got := s.Tick(); len(got) != 0 {
		t.Fatalf("catch-up started beside the restart: %+v", got)
	}
	close(release)
	fx.runner.Wait()
	c.t = at("2026-11-08 13:01")
	if got := s.Tick(); len(got) != 0 {
		t.Fatalf("catch-up ran after the restart: %+v", got)
	}
	// The next slot runs as usual.
	fx.unblock()
	c.t = at("2026-11-08 18:00")
	if got := s.Tick(); len(got) != 1 {
		t.Fatalf("next slot: %+v", got)
	}
}

func TestResumeKeepsTarget(t *testing.T) {
	fx := newFixture(t, "daily", "---\nscope: since_last_scan\npath: pkg\n---\nCheck {{since}}.")
	commitFile(t, fx.repo, "pkg/a.go", "package pkg\n")
	first := fx.run(t, "daily")
	if first.Status != model.ScanSucceeded {
		t.Fatalf("first: %+v", first)
	}
	commitFile(t, fx.repo, "pkg/b.go", "package pkg\n")
	orig := fx.interrupt(t, "daily", "schedule")
	if !orig.Incremental || orig.Since != first.Commit {
		t.Fatalf("interrupted run: %+v", orig)
	}
	// More commits, and the template now scans the whole repository.
	commitFile(t, fx.repo, "pkg/c.go", "package pkg\n")
	head := commitFile(t, fx.repo, "web/d.js", "x\n")
	writeTemplate(t, fx, "daily", "---\nscope: repo\n---\nCheck.")

	got := fx.runner.ResumeInterrupted()
	if len(got) != 1 {
		t.Fatalf("resumed: %+v", got)
	}
	fx.runner.Wait()
	r, _ := fx.st.GetScanRun(got[0].ID)
	if r.Status != model.ScanSucceeded || r.Scope != model.ScopeSinceLastScan || r.Path != "pkg" ||
		!r.Incremental || r.Since != orig.Since || r.Commit != head {
		t.Fatalf("resumed run: %+v", r)
	}
	p := fx.exec.lastPrompt()
	if !strings.Contains(p, "- pkg/b.go") || !strings.Contains(p, "- pkg/c.go") || strings.Contains(p, "web/d.js") || strings.Contains(p, "- pkg/a.go") {
		t.Fatalf("prompt:\n%s", p)
	}
}
