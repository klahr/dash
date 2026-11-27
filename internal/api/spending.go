package api

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/spend"
)

// Spending caps (docs/spending.md): Project settings → Spending sets them
// (PATCH /api/projects/{pid} with "spending", and update_project), the
// queue enforces them (queue/spendcap.go), and GET
// /api/projects/{pid}/spending shows what the project spent.

// applySpending sets p's caps to s (all 0: none), keeping the periods
// already warned about.
func applySpending(p *model.Project, s model.Spending) {
	s.Zone = strings.TrimSpace(s.Zone)
	if !s.On() {
		p.Spending, p.SpendHold = nil, nil
		return
	}
	next := s.Clone()
	next.Warned = nil
	if p.Spending != nil {
		next.Warned = append([]string(nil), p.Spending.Warned...)
	}
	p.Spending = next
}

// raiseTaskCap raises the cap of task id once when its last run stopped at
// it: Retry continues it with room for another task cap's worth.
func (sv *service) raiseTaskCap(id string) {
	t, err := sv.data.GetTodo(id)
	if err != nil || t.CapHit <= 0 || t.Status != model.StatusFailed {
		return
	}
	var projectCap float64
	if p, err := sv.data.GetProject(t.ProjectID); err == nil && p.Spending != nil {
		projectCap = p.Spending.TaskUSD
	}
	_, _ = sv.data.UpdateTodo(id, func(x *model.Todo) error {
		model.RaiseTaskCap(x, projectCap)
		return nil
	})
}

// hubTaskCap is, on a runner, the cap of its copy of the hub's task req,
// which cost spent here: what the hub still allows on top of that (0: no
// cap; a hub's task at its cap already gets a cent, so its run stops at once).
func hubTaskCap(req dispatch.Task, spent float64) float64 {
	if req.CapUSD <= 0 {
		return 0
	}
	return spent + max(req.CapUSD-req.SpentUSD, 0.01)
}

// spendingView is GET /api/projects/{pid}/spending.
type spendingView struct {
	Spending *model.Spending  `json:"spending,omitempty"`
	Hold     *model.SpendHold `json:"hold,omitempty"`
	Zone     string           `json:"zone"` // where its days start
	Today    float64          `json:"today_usd"`
	Month    float64          `json:"month_usd"`
	// Days are the last 30 days' costs, oldest first.
	Days []spend.Day `json:"days"`
	// Merged are the tasks merged in those days, newest first, with what
	// each cost; PerMerged is their average.
	Merged    []mergedCost `json:"merged"`
	PerMerged float64      `json:"per_merged_usd"`
	// Estimated: some of the costs are Dash's estimates from tokens (Codex).
	Estimated bool `json:"estimated,omitempty"`
}

type mergedCost struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CostUSD   float64   `json:"cost_usd"`
	Estimated bool      `json:"estimated,omitempty"`
	MergedAt  time.Time `json:"merged_at"`
}

const trendDays = 30

func (s *Server) getSpending(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, spendingOf(p, sv.data.ListTodos(p.ID), sv.data.ListScanRuns(p.ID, ""), time.Now()))
}

// spendingOf is project p's spending as of now, from its tasks and scans.
func spendingOf(p model.Project, todos []model.Todo, scans []model.ScanRun, now time.Time) spendingView {
	loc := p.Spending.Location()
	es := spend.Entries(todos, scans)
	t := spend.TotalsAt(es, now, loc)
	v := spendingView{Spending: p.Spending, Hold: p.SpendHold, Zone: loc.String(), Today: t.Day, Month: t.Month,
		Days: spend.Trend(es, now, loc, trendDays), Merged: []mergedCost{}}
	for _, d := range v.Days {
		v.Estimated = v.Estimated || d.Estimated
	}
	from := spend.DayStart(now, loc).AddDate(0, 0, 1-trendDays)
	var sum float64
	for _, td := range todos {
		if td.Status != model.StatusMerged || td.NoChanges || td.MergedCommit == "" && td.PR == nil {
			continue
		}
		at := td.UpdatedAt // a merged task's last change is its merge
		if at.Before(from) {
			continue
		}
		m := mergedCost{ID: td.ID, Title: td.Name(80), CostUSD: td.CostUSD, Estimated: spend.TaskEstimated(td), MergedAt: at}
		v.Merged = append(v.Merged, m)
		sum += m.CostUSD
	}
	sort.Slice(v.Merged, func(i, j int) bool { return v.Merged[i].MergedAt.After(v.Merged[j].MergedAt) })
	if n := len(v.Merged); n > 0 {
		v.PerMerged = sum / float64(n)
	}
	return v
}
