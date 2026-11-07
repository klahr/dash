package scan

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// ResumeInterrupted starts over the runs that Dash's last shutdown or crash
// cut off (status "interrupted"), each as a new run with trigger "resume"
// that keeps the original's scope, path and base commit. Call it at
// startup, after Cleanup and before the Scheduler's first check, so a
// missed scheduled slot of the same scan finds it running and is skipped.
//
// A run is started over at most once: not if it already was, not if it was
// itself a restart (a scan that crashes Dash would otherwise loop), not if
// it is older than ResumeMaxAge, and not if the scan ran again since.
func (r *Runner) ResumeInterrupted() []model.ScanRun {
	var cands []model.ScanRun
	for _, p := range r.Store.ListProjects() {
		for _, run := range r.Store.ListScanRuns(p.ID, "") {
			if run.Status == model.ScanInterrupted && run.ResumedAs == "" {
				cands = append(cands, run)
			}
		}
	}
	// Oldest first, so the order is the same on every start.
	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].StartedAt.Equal(cands[j].StartedAt) {
			return cands[i].StartedAt.Before(cands[j].StartedAt)
		}
		return cands[i].ID < cands[j].ID
	})
	now := r.now()
	var started []model.ScanRun
	for _, orig := range cands {
		note := func(why string) {
			_, _ = r.Store.UpdateScanRun(orig.ID, true, func(x *model.ScanRun) { x.Note = why })
		}
		switch {
		case orig.Trigger == model.TriggerResume:
			if orig.Note != notRestartedAgain {
				note(notRestartedAgain)
			}
			continue
		case now.Sub(orig.StartedAt) > ResumeMaxAge:
			continue
		}
		if last, ok := r.Store.LastScanRun(orig.ProjectID, orig.Scan, nil); ok && last.ID != orig.ID {
			continue // the scan ran again since: that run covers it
		}
		run, err := r.start(orig.ProjectID, orig.Scan, model.TriggerResume, &orig)
		switch {
		case errors.Is(err, ErrRunning):
			continue
		case errors.Is(err, store.ErrNotFound):
			note("Not restarted: the project is gone")
			continue
		case errors.Is(err, ErrNoScan):
			note("Not restarted: the scan template is gone")
			continue
		case err != nil:
			note(fmt.Sprintf("Not restarted: %v", err))
			slog.Warn("resume scan", "project", orig.ProjectID, "scan", orig.Scan, "run", orig.ID, "err", err)
			continue
		}
		_, _ = r.Store.UpdateScanRun(orig.ID, true, func(x *model.ScanRun) { x.ResumedAs = run.ID })
		slog.Info("interrupted scan restarted", "project", orig.ProjectID, "scan", orig.Scan, "run", orig.ID, "as", run.ID)
		started = append(started, run)
	}
	return started
}

const notRestartedAgain = "Not restarted: it was already a restart of an interrupted run"
