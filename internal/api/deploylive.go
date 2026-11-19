package api

// What is live where, of a project's commits, tasks and planning cards,
// worked out from the deploy records (model.LiveShipped): History marks
// the commits live in each environment, a task shows "Live in production
// since …", and so does a card once all its work is. The records are the
// hub's, so this works on a hub whose projects live on runners.

import (
	"net/http"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

// liveIn is an environment something has been live in, and since when.
type liveIn struct {
	Environment string    `json:"environment"`
	Since       time.Time `json:"since"`
}

// liveCommit is what one environment runs now: History's marker.
type liveCommit struct {
	Environment string    `json:"environment"`
	Commit      string    `json:"commit"`
	Deploy      string    `json:"deploy"`
	Since       time.Time `json:"since"`
}

// projectLive is what is live where in one project.
type projectLive struct {
	Environments []liveCommit `json:"environments"`
	// Shipped maps a task or card (model.ShippedKey) to the environments it
	// is live in, in the project's order.
	Shipped map[string][]liveIn `json:"-"`
	// Tasks is Shipped's tasks, by id, for the UI.
	Tasks map[string][]liveIn `json:"tasks"`
}

// liveOf works out what is live where in p, from data's deploy records.
func liveOf(data interface {
	ListDeploys(projectID, env string) []model.Deploy
}, p model.Project) projectLive {
	out := projectLive{Environments: []liveCommit{}, Shipped: map[string][]liveIn{}, Tasks: map[string][]liveIn{}}
	for _, e := range p.Environments {
		deploys := data.ListDeploys(p.ID, e.Name)
		for _, d := range deploys {
			if d.Status == model.DeployLive {
				out.Environments = append(out.Environments, liveCommit{Environment: e.Name, Commit: d.Commit, Deploy: d.ID, Since: d.LiveSince()})
				break
			}
		}
		for key, since := range model.LiveShipped(deploys) {
			out.Shipped[key] = append(out.Shipped[key], liveIn{Environment: e.Name, Since: since})
		}
	}
	prefix := model.ShippedKey(model.LinkTask, "")
	for key, ls := range out.Shipped {
		if id, ok := strings.CutPrefix(key, prefix); ok && id != "" {
			out.Tasks[id] = ls
		}
	}
	return out
}

// liveDeploys handles GET /api/projects/{pid}/deploys/live: what each
// environment runs now (commit, deploy, since when), and the tasks live in
// each.
func (s *Server) liveDeploys(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.project(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, liveOf(sv.data, p))
}

// bestLive is, of the environments something is live in (in the project's
// order), the one to name: production if it is live there, else the last
// (the furthest along). ok is false when there is none.
func bestLive(ls []liveIn) (liveIn, bool) {
	if len(ls) == 0 {
		return liveIn{}, false
	}
	for _, l := range ls {
		if (model.Environment{Name: l.Environment}).Production() {
			return l, true
		}
	}
	return ls[len(ls)-1], true
}

// commonLive is where every one of sets is live: the environments in all
// of them, since the latest of their times (in the first set's order).
func commonLive(sets [][]liveIn) []liveIn {
	if len(sets) == 0 {
		return nil
	}
	var out []liveIn
	for _, l := range sets[0] {
		since, everywhere := l.Since, true
		for _, set := range sets[1:] {
			found := false
			for _, m := range set {
				if m.Environment == l.Environment {
					found = true
					if m.Since.After(since) {
						since = m.Since
					}
				}
			}
			if !found {
				everywhere = false
				break
			}
		}
		if everywhere {
			out = append(out, liveIn{Environment: l.Environment, Since: since})
		}
	}
	return out
}

// liveText says where something is live: "Live in production since 19 Nov 2026".
func liveText(l liveIn) string {
	return "Live in " + l.Environment + " since " + l.Since.Format("2 Jan 2006")
}
