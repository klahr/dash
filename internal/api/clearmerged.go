package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/klahr/dash/internal/model"
)

// clearMergedEvery is how often StartClearMerged looks for merged tasks
// left from an earlier day: a new day is noticed this long after midnight
// at the latest.
const clearMergedEvery = 5 * time.Minute

// StartClearMerged deletes, in the projects with a ClearMergedZone, the
// merged tasks from before today (in that time zone), now and then every
// few minutes until ctx ends: the board starts each day without them.
func (s *Server) StartClearMerged(ctx context.Context) {
	go func() {
		tick := time.NewTicker(clearMergedEvery)
		defer tick.Stop()
		for {
			s.clearMerged(ctx, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}

// clearMerged deletes the merged tasks last updated before the day now is
// in, in each project's ClearMergedZone, as Delete merged on the board does.
// A runner leaves the hub's projects alone: their tasks are the hub's to
// delete. Returns how many it deleted.
func (s *Server) clearMerged(ctx context.Context, now time.Time) int {
	projects := map[string]clearDay{}
	n := 0
	for _, t := range s.Store.ListTodos("", model.StatusMerged) {
		p, seen := projects[t.ProjectID]
		if !seen {
			p = s.clearMergedProject(t.ProjectID, now)
			projects[t.ProjectID] = p
		}
		if p.start.IsZero() || !t.UpdatedAt.Before(p.start) {
			continue
		}
		if err := p.sv.removeTodo(ctx, t.ID); err != nil {
			slog.Warn("clear merged: couldn't delete a task", "project", t.ProjectID, "todo", t.ID, "err", err)
			continue
		}
		n++
	}
	if n > 0 {
		slog.Info("clear merged: deleted merged tasks from before today", "count", n)
	}
	return n
}

// clearDay is what clearMerged needs of a project.
type clearDay struct {
	sv    *service
	start time.Time // zero: the project's merged tasks stay
}

// clearMergedProject returns, for clearMerged, the service of project id's
// org and the start of today in its ClearMergedZone (zero when it has none,
// or is a hub's project on this runner).
func (s *Server) clearMergedProject(id string, now time.Time) (p clearDay) {
	org, ok := s.Store.ProjectOrg(id)
	if !ok {
		return p
	}
	data := s.Store.ForOrg(org)
	x, err := data.GetProject(id)
	if err != nil || x.ClearMergedZone == "" || x.HubProjectID != "" {
		return p
	}
	loc, err := time.LoadLocation(x.ClearMergedZone)
	if err != nil {
		return p
	}
	p.sv, p.start = &service{s: s, c: Caller{Org: org}, data: data}, startOfDay(now, loc)
	return p
}

// startOfDay is midnight, in loc, of the day now is in there.
func startOfDay(now time.Time, loc *time.Location) time.Time {
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}
