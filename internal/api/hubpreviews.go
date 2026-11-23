package api

// Previews of a hub's tasks. The hub runs no preview itself: every task of
// a hub project has its worktree on the runner that ran it (model.Todo.Runner,
// RemoteID), so its preview starts, stops, logs and opens a terminal there,
// forwarded by the hub, and only there: when that runner is offline the hub
// says so, as no other runner has the worktree. The runner reports its
// previews of the hub's tasks (dispatch.Report.Previews); the hub keeps them
// by its own task ids, so its preview list and events look like a plain
// Dash's, and serves each at <remote id>-<runner>.<preview domain>.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/preview"
	"github.com/klahr/dash/internal/tunnel"
)

// hubPreview is a runner's preview of a hub's task, as the hub shows it.
type hubPreview struct {
	pv    model.Preview // with the hub's ids and addresses
	label string        // <remote id>-<runner name>: its preview host's first label
}

// runnerPreviews are the hub's tasks' previews on runners, as last reported
// or started and stopped through the hub.
type runnerPreviews struct {
	mu sync.Mutex
	m  map[string]map[string]hubPreview // runner id → hub task id → its preview
}

// RunnerPreviews takes runner r's report of its previews of the hub's tasks
// (all of them; see dispatch.Dispatcher.OnPreviews) and tells the dashboards
// what changed. Previews of tasks the hub didn't send r are ignored.
func (s *Server) RunnerPreviews(r tunnel.Runner, list []dispatch.PreviewState) {
	now := map[string]hubPreview{}
	for _, ps := range list {
		t, err := s.as(Caller{Org: r.OrgID(), Machine: true}).data.GetTodo(ps.HubTodoID)
		if err != nil || t.Runner != r.ID || t.RemoteID == "" || t.RemoteID != ps.Preview.TodoID {
			continue
		}
		now[t.ID] = s.hubPreviewOf(r, t, ps.Preview)
	}
	s.runnerPv.mu.Lock()
	if s.runnerPv.m == nil {
		s.runnerPv.m = map[string]map[string]hubPreview{}
	}
	old := s.runnerPv.m[r.ID]
	s.runnerPv.m[r.ID] = now
	s.runnerPv.mu.Unlock()
	for id, hp := range now {
		if o, ok := old[id]; !ok || !samePreview(o.pv, hp.pv) {
			s.publishPreview(hp.pv)
		}
	}
	for id, o := range old {
		if _, ok := now[id]; !ok {
			s.publishStopped(o.pv)
		}
	}
}

// samePreview reports whether a and b differ only in when they were seen.
func samePreview(a, b model.Preview) bool {
	a.LastSeen, b.LastSeen = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

// hubPreviewOf is runner r's preview p of t, the hub's task, as the hub
// shows it: by the hub's ids, its log streamed and its terminal attached to
// through the hub, its page served at the hub's preview host for it (never
// the runner's own address).
func (s *Server) hubPreviewOf(r tunnel.Runner, t model.Todo, p model.Preview) hubPreview {
	label := t.RemoteID + "-" + r.Name
	p.TodoID, p.ProjectID, p.Title = t.ID, t.ProjectID, t.Name(80)
	if p.ProcessID != "" {
		p.ProcessID = hubProcID(r.ID, p.ProcessID) // processViaRunner
	}
	p.LocalURL, p.URL, p.OpenURL, p.TerminalURL = "", "", "", ""
	if p.Kind == model.PreviewTerminal {
		p.TerminalURL = preview.TerminalPath(t.ID)
	} else if s.Previews != nil && s.Previews.Domain != "" {
		p.URL, p.OpenURL = s.Previews.URL(label), "/preview/"+t.ID
	}
	return hubPreview{pv: p, label: label}
}

func (s *Server) publishPreview(p model.Preview) {
	if s.Hub != nil {
		s.Hub.Publish(model.Event{Type: model.EvPreview, ProjectID: p.ProjectID, TodoID: p.TodoID, Data: p, Time: time.Now()})
	}
}

func (s *Server) publishStopped(p model.Preview) {
	p.State, p.Reason = model.PreviewStopped, "stopped"
	s.publishPreview(p)
}

// keepHubPreview records hp, a preview of the hub's task on runner runID,
// and tells the dashboards; with gone, that it stopped.
func (s *Server) keepHubPreview(runID string, hp hubPreview, gone bool) {
	s.runnerPv.mu.Lock()
	if s.runnerPv.m == nil {
		s.runnerPv.m = map[string]map[string]hubPreview{}
	}
	if s.runnerPv.m[runID] == nil {
		s.runnerPv.m[runID] = map[string]hubPreview{}
	}
	if gone {
		delete(s.runnerPv.m[runID], hp.pv.TodoID)
	} else {
		s.runnerPv.m[runID][hp.pv.TodoID] = hp
	}
	s.runnerPv.mu.Unlock()
	if gone {
		s.publishStopped(hp.pv)
	} else {
		s.publishPreview(hp.pv)
	}
}

// hubPreviews are the previews of sv's org's tasks on runners that are
// online, newest first.
func (sv *service) hubPreviews() []model.Preview {
	s := sv.s
	out := []model.Preview{}
	s.runnerPv.mu.Lock()
	defer s.runnerPv.mu.Unlock()
	for runID, pvs := range s.runnerPv.m {
		if r, ok := s.fleet().Get(runID); !ok || !r.Online {
			continue
		}
		for _, hp := range pvs {
			if sv.data.OwnsProject(hp.pv.ProjectID) {
				out = append(out, hp.pv)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// hubPreviewTask is the hub's task whose preview host is label
// (<remote id>-<runner name>), if the hub knows of that preview.
func (s *Server) hubPreviewTask(label string) (string, bool) {
	s.runnerPv.mu.Lock()
	defer s.runnerPv.mu.Unlock()
	for _, pvs := range s.runnerPv.m {
		for id, hp := range pvs {
			if hp.label == label {
				return id, true
			}
		}
	}
	return "", false
}

// hubPreviewLabel is the preview host label a member c opens for tid on
// the hub: tid is the hub's task (its open_url), or the label itself (the
// preview proxy sends a browser that isn't signed in to /preview/<label>).
// It is false when tid is neither, or not a task of c's org.
func (s *Server) hubPreviewLabel(c Caller, tid string) (string, bool) {
	sv := s.as(c)
	if t, err := sv.data.GetTodo(tid); err == nil {
		if !t.OnRunner() || t.RemoteID == "" {
			return "", false
		}
		r, ok := s.fleet().Get(t.Runner)
		if !ok {
			return "", false
		}
		return t.RemoteID + "-" + r.Name, true
	}
	if htid, ok := s.hubPreviewTask(tid); ok {
		if _, err := sv.data.GetTodo(htid); err == nil {
			return tid, true
		}
	}
	return "", false
}

// previewRunner is the runner with t's worktree, which runs t's preview,
// for what (e.g. "preview it"): online, and doing work for sv's caller.
func (sv *service) previewRunner(t model.Todo, what string) (tunnel.Runner, error) {
	if !t.OnRunner() || t.RemoteID == "" {
		return tunnel.Runner{}, fmt.Errorf("%w, so it has nothing to preview", errNotRunYet)
	}
	r, ok := sv.s.fleet().Get(t.Runner)
	if !ok || !r.Online {
		name := "its runner"
		if ok {
			name = "its runner, " + r.Name
		}
		return tunnel.Runner{}, fmt.Errorf("%w (the task's worktree is on %s, which is offline)", errNoRunnerOnline, name)
	}
	if err := sv.s.actionOnRunner(sv.c, t, what); err != nil {
		return tunnel.Runner{}, err
	}
	return r, nil
}

// previewOnRunner handles the preview routes for a hub's task (rest:
// "/preview" or "/preview/log"): they go to the task's runner, for the task
// there, saying whom for (the runner opens a terminal preview only for its
// owner).
func (s *Server) previewOnRunner(w http.ResponseWriter, r *http.Request, rest string) {
	sv, t, ok := s.todo(w, r)
	if !ok {
		return
	}
	what := "preview it"
	if r.Method == http.MethodDelete {
		what = "stop its preview"
	}
	run, err := sv.previewRunner(t, what)
	if err != nil {
		writeErr(w, err)
		return
	}
	if r.Method == http.MethodPost {
		if p, err := sv.data.GetProject(t.ProjectID); err == nil && p.TerminalPreview() {
			if err := ownsRunner(sv.c, run); err != nil { // a terminal preview is a shell
				writeError(w, http.StatusForbidden, err.Error())
				return
			}
		}
	}
	fr := r.WithContext(tunnel.WithUser(r.Context(), sv.c.User))
	res, err := s.forwardTo(fr, run, "/api/todos/"+url.PathEscape(t.RemoteID)+rest, nil, "", "")
	if err != nil {
		writeErr(w, err)
		return
	}
	if !res.ok() {
		if r.Method == http.MethodDelete && res.code == http.StatusNotFound {
			// Not running there (any more): nor in the hub's list.
			s.keepHubPreview(run.ID, s.hubPreviewOf(run, t, model.Preview{TodoID: t.RemoteID}), true)
		}
		res.write(w)
		return
	}
	switch r.Method {
	case http.MethodPost:
		var p model.Preview
		if err := json.Unmarshal(res.body, &p); err != nil {
			writeErr(w, fmt.Errorf("%s answered: %w", run.Name, err))
			return
		}
		hp := s.hubPreviewOf(run, t, p)
		s.keepHubPreview(run.ID, hp, false)
		writeJSON(w, http.StatusOK, hp.pv)
	case http.MethodDelete:
		s.keepHubPreview(run.ID, s.hubPreviewOf(run, t, model.Preview{TodoID: t.RemoteID}), true)
		w.WriteHeader(http.StatusNoContent)
	default:
		res.write(w)
	}
}

// stopHubPreviews stops the previews of sv's org's tasks on runners (Stop
// all), those on runners that work for the caller, and says how many.
func (sv *service) stopHubPreviews(r *http.Request) int {
	n := 0
	for _, p := range sv.hubPreviews() {
		t, err := sv.data.GetTodo(p.TodoID)
		if err != nil {
			continue
		}
		run, err := sv.previewRunner(t, "stop its preview")
		if err != nil {
			continue
		}
		ctx := tunnel.WithUser(r.Context(), sv.c.User)
		if err := sv.s.runnerCall(ctx, run.ID, http.MethodDelete, "/api/todos/"+url.PathEscape(t.RemoteID)+"/preview", nil, nil); err != nil {
			continue
		}
		s := sv.s
		s.keepHubPreview(run.ID, hubPreview{pv: p}, true)
		n++
	}
	return n
}

// previewTerminalOnRunner handles GET /ws/todos/{tid}/preview for a hub's
// task: the socket goes through to the terminal preview on its runner, as
// it is, for the runner's owner only.
func (s *Server) previewTerminalOnRunner(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.todo(w, r)
	if !ok {
		return
	}
	run, err := sv.previewRunner(t, "preview it")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := ownsRunner(sv.c, run); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if !hub.Upgrader.CheckOrigin(r) {
		writeError(w, http.StatusForbidden, "origin not allowed")
		return
	}
	px, ok := s.fleet().Proxy(run.Name)
	if !ok {
		writeErr(w, errNoRunnerOnline)
		return
	}
	out := r.Clone(tunnel.WithUser(r.Context(), sv.c.User))
	out.URL.Path, out.URL.RawPath = "/peer/"+run.Name+preview.TerminalPath(t.RemoteID), ""
	px.ServeHTTP(w, out)
}
