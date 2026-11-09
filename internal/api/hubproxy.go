package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// The hub runs nothing on its own computer (Server.hub): it has no
// working tree of its projects, no git, no gh and no shell. Each of its
// projects is shared with one or more runners (model.Project.Runners), each
// of which keeps a clone of the project's remote. Everything about a
// project that needs a working tree — its git history, branches, files,
// releases, CI, run/test/deploy commands — is forwarded over the tunnel
// to the runner's clone: the hub links the project to the runner (which
// clones it the first time, see hubLink), and sends the request to the
// runner's project for it. When none of the project's runners is online,
// the request fails with errNoRunnerOnline; it never falls back to the hub.

// errNoRunnerOnline is why the hub can't do a project's work right now.
//
//lint:ignore ST1005 shown to the user verbatim as a sentence
var errNoRunnerOnline = errors.New("No runner is online for this project. Start one with `dash runner`.")

// errNoRunnerLinked is why the hub can't do the work of a project no
// runner is linked to (one made on the hub before it ran nothing itself).
//
//lint:ignore ST1005 shown to the user verbatim as a sentence
var errNoRunnerLinked = errors.New("No runner is linked to this project: link one in Project settings → Runners (start one with `dash runner`).")

// projectRunner picks the runner that does p's work for the hub: one of
// p's runners, online, in p's org; prefer if it is one of them. It links p
// to it and returns the runner and its project id for p.
func (sv *service) projectRunner(ctx context.Context, p model.Project, prefer string) (tunnel.Runner, string, error) {
	cands := sv.s.onlineRunners(p)
	if len(cands) == 0 {
		if len(p.Runners) == 0 {
			return tunnel.Runner{}, "", errNoRunnerLinked
		}
		return tunnel.Runner{}, "", errNoRunnerOnline
	}
	r := cands[0]
	for _, c := range cands {
		if c.ID == prefer {
			r = c
		}
	}
	rpid, err := sv.linkOn(ctx, r, p)
	return r, rpid, err
}

// onlineRunners are p's runners that are online, in p's org.
func (s *Server) onlineRunners(p model.Project) []tunnel.Runner {
	fleet := s.fleet()
	if fleet == nil || s.Dispatch == nil {
		return nil
	}
	var out []tunnel.Runner
	for _, id := range p.Runners {
		if r, ok := fleet.Get(id); ok && r.Online && r.OrgID() == p.OrgID {
			out = append(out, r)
		}
	}
	return out
}

// linkOn links p to runner r and returns r's project id for it. When r's
// clone found origin's default branch to be another one than the hub
// guessed, the hub's project follows.
func (sv *service) linkOn(ctx context.Context, r tunnel.Runner, p model.Project) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute) // the first link clones
	defer cancel()
	linked, err := sv.s.Dispatch.Link(cctx, r.ID, p)
	if err != nil {
		if errors.Is(err, tunnel.ErrOffline) || errors.Is(err, tunnel.ErrUnknownName) {
			return "", errNoRunnerOnline
		}
		return "", err
	}
	if linked.ProjectID == "" {
		return "", fmt.Errorf("%s didn't say which of its projects is %s; update it (dash runner)", r.Name, p.Name)
	}
	if b := linked.DefaultBranch; b != "" && b != p.DefaultBranch {
		_, _ = sv.data.UpdateProject(p.ID, func(x *model.Project) error { x.DefaultBranch = b; return nil })
	}
	return linked.ProjectID, nil
}

// onRunner runs fn, an action on t's runner (where t's worktree is), and
// says plainly when that runner is offline.
func (sv *service) onRunner(ctx context.Context, t model.Todo, fn func(context.Context) error) error {
	if sv.s.Dispatch == nil {
		return fmt.Errorf("%w: this task ran on a runner, and this Dash doesn't reach runners", errBadRequest)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := fn(cctx)
	if errors.Is(err, tunnel.ErrOffline) || errors.Is(err, tunnel.ErrUnknownName) {
		name := "its runner"
		if r, ok := sv.s.fleet().Get(t.Runner); ok {
			name = r.Name
		}
		return fmt.Errorf("%w (this task's worktree is on %s)", errNoRunnerOnline, name)
	}
	return err
}

// viaRunner is h on a runner. On the hub it forwards the request, about
// the project {pid}, to the project's clone on one of its runners instead.
func (s *Server) viaRunner(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hub() {
			h(w, r)
			return
		}
		sv, ok := s.service(w, r)
		if !ok {
			return
		}
		p, err := sv.data.GetProject(r.PathValue("pid"))
		if err != nil {
			writeErr(w, err)
			return
		}
		switch r.Pattern {
		case "PUT /api/projects/{pid}/branch":
			s.switchBranchOnRunner(w, r, sv, p)
		case "GET /api/projects/{pid}/processes":
			s.listProcessesOnRunners(w, r, sv, p)
		case "POST /api/projects/{pid}/processes":
			s.startProcessOnRunner(w, r, sv, p)
		case "POST /api/projects/{pid}/remote/pull":
			s.pullOnRunner(w, r, sv, p)
		default:
			res, err := sv.forward(r, p, "", nil)
			if err != nil {
				writeErr(w, err)
				return
			}
			res.write(w)
		}
	}
}

// forwarded is a runner's answer to a forwarded request, with the runner's
// project id replaced by the hub's.
type forwarded struct {
	runner tunnel.Runner
	code   int
	header http.Header
	body   []byte
}

func (f forwarded) ok() bool { return f.code >= 200 && f.code < 300 }

// forwardedHeaders are the headers of a runner's answer the hub passes on;
// the rest (cookies, policies, …) are the hub's to set, not a runner's.
var forwardedHeaders = []string{"Content-Type", "Content-Disposition", "Cache-Control", "Etag", "Last-Modified", "Retry-After"}

func (f forwarded) write(w http.ResponseWriter) {
	h := http.Header{}
	for _, k := range forwardedHeaders {
		if v := f.header.Values(k); len(v) > 0 {
			h[k] = v
		}
	}
	// Served on the hub's origin: never as the runner's HTML or script.
	tunnel.SafeRunnerHeader(h, f.code)
	for k, v := range h {
		w.Header()[k] = v
	}
	w.WriteHeader(f.code)
	_, _ = w.Write(f.body)
}

// forward sends r, a request about the hub's project p, to p's clone on
// one of its runners (prefer if it is online) and returns the answer. body
// replaces r's body when not nil.
func (sv *service) forward(r *http.Request, p model.Project, prefer string, body []byte) (forwarded, error) {
	run, rpid, err := sv.projectRunner(r.Context(), p, prefer)
	if err != nil {
		return forwarded{}, err
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/projects/"+p.ID)
	return sv.s.forwardTo(r, run, "/api/projects/"+url.PathEscape(rpid)+rest, body, rpid, p.ID)
}

// forwardTo sends r to path on runner run and returns the answer, with
// every JSON string from equal to it replaced by to (the runner's id for a
// project by the hub's).
func (s *Server) forwardTo(r *http.Request, run tunnel.Runner, path string, body []byte, from, to string) (forwarded, error) {
	px, ok := s.fleet().Proxy(run.Name)
	if !ok {
		return forwarded{}, errNoRunnerOnline
	}
	out := r.Clone(r.Context())
	out.URL.Path, out.URL.RawPath = "/peer/"+run.Name+path, ""
	if body != nil {
		out.Body, out.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
		out.Header.Del("Content-Length")
	}
	rec := &recorder{header: http.Header{}, code: http.StatusOK}
	px.ServeHTTP(rec, out)
	res := forwarded{runner: run, code: rec.code, header: rec.header, body: rec.buf.Bytes()}
	if from != "" && strings.Contains(res.header.Get("Content-Type"), "json") {
		res.body = bytes.ReplaceAll(res.body, []byte(`"`+from+`"`), []byte(`"`+to+`"`))
	}
	if res.code == http.StatusBadGateway && bytes.Contains(res.body, []byte(" is offline")) {
		return forwarded{}, errNoRunnerOnline // it went away since it was picked
	}
	return res, nil
}

// recorder keeps a forwarded answer, to rewrite it before it goes on.
type recorder struct {
	header http.Header
	code   int
	buf    bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) WriteHeader(code int)        { r.code = code }
func (r *recorder) Write(b []byte) (int, error) { return r.buf.Write(b) }

// switchBranchOnRunner handles PUT /api/projects/{pid}/branch on the hub:
// the runner checks the branch, makes it when asked and pushes it to
// origin; the hub's project then lands its tasks there (every runner
// follows it when the project is next linked).
func (s *Server) switchBranchOnRunner(w http.ResponseWriter, r *http.Request, sv *service, p model.Project) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	var req struct {
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	res, err := sv.forward(r, p, "", body)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !res.ok() {
		res.write(w)
		return
	}
	work := strings.TrimSpace(req.Branch)
	if work == p.DefaultBranch {
		work = ""
	}
	np, err := sv.data.UpdateProject(p.ID, func(x *model.Project) error {
		x.WorkBranch, x.SyncError = work, ""
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, np)
}

// ---- processes: the project's run/test/deploy commands, on a runner ----

// A process the hub lists runs on a runner; its id on the hub is
// "<runner id>~<the runner's process id>", so stopping it and watching its
// output go to the same runner.
const procSep = "~"

func hubProcID(runnerID, id string) string { return runnerID + procSep + id }

// rewriteProcs gives the processes in body (one or a list) their hub ids:
// runner run's ids, and the hub's ids for the tasks they belong to.
func rewriteProcs(body []byte, run tunnel.Runner, todos map[string]string) []byte {
	fix := func(p map[string]any) {
		if id, ok := p["id"].(string); ok {
			p["id"] = hubProcID(run.ID, id)
		}
		if tid, ok := p["todo_id"].(string); ok && tid != "" {
			p["todo_id"] = todos[tid]
		}
		p["runner"] = run.Name
	}
	var list []map[string]any
	if json.Unmarshal(body, &list) == nil {
		for _, p := range list {
			fix(p)
		}
		b, _ := json.Marshal(list)
		return b
	}
	var one map[string]any
	if json.Unmarshal(body, &one) == nil {
		fix(one)
		b, _ := json.Marshal(one)
		return b
	}
	return body
}

// runnerTodos maps the runner's ids of p's tasks to the hub's.
func (sv *service) runnerTodos(p model.Project) map[string]string {
	out := map[string]string{}
	for _, t := range sv.data.ListTodos(p.ID) {
		if t.RemoteID != "" {
			out[t.RemoteID] = t.ID
		}
	}
	return out
}

// listProcessesOnRunners lists p's processes on every runner of p that is online.
func (s *Server) listProcessesOnRunners(w http.ResponseWriter, r *http.Request, sv *service, p model.Project) {
	todos := sv.runnerTodos(p)
	all := []json.RawMessage{}
	for _, run := range s.onlineRunners(p) {
		rpid, err := sv.linkOn(r.Context(), run, p)
		if err != nil {
			continue
		}
		res, err := s.forwardTo(r, run, "/api/projects/"+url.PathEscape(rpid)+"/processes", nil, rpid, p.ID)
		if err != nil || !res.ok() {
			continue
		}
		var list []json.RawMessage
		if json.Unmarshal(rewriteProcs(res.body, run, todos), &list) == nil {
			all = append(all, list...)
		}
	}
	writeJSON(w, http.StatusOK, all)
}

// startProcessOnRunner starts one of p's commands on a runner: with todo_id,
// on the task's runner, in its worktree there. Terminals and commands the
// hub makes up are the runner's to allow (see hubCheck); neither ever runs
// on the hub. A terminal opens only on a runner of the caller's own (the
// runner checks it too, from tunnel.UserHeader).
func (s *Server) startProcessOnRunner(w http.ResponseWriter, r *http.Request, sv *service, p model.Project) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	pty := req["kind"] == string(model.ProcPTY)
	prefer := ""
	if tid, _ := req["todo_id"].(string); tid != "" {
		t, err := sv.data.GetTodo(tid)
		if err != nil {
			writeErr(w, err)
			return
		}
		if t.ProjectID != p.ID || !t.OnRunner() || t.RemoteID == "" {
			writeErr(w, fmt.Errorf("%w: task has no worktree on a runner", errBadRequest))
			return
		}
		prefer, req["todo_id"] = t.Runner, t.RemoteID
		if body, err = json.Marshal(req); err != nil {
			writeErr(w, err)
			return
		}
	}
	if pty && prefer == "" {
		prefer = s.terminalRunner(sv.c, p)
	}
	run, rpid, err := sv.projectRunner(r.Context(), p, prefer)
	if err != nil {
		writeErr(w, err)
		return
	}
	if prefer != "" && run.ID != prefer {
		writeErr(w, fmt.Errorf("%w (the task's worktree is on its runner, which is offline)", errNoRunnerOnline))
		return
	}
	if pty {
		if err := ownsRunner(sv.c, run); err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		r = r.WithContext(tunnel.WithUser(r.Context(), sv.c.User))
	}
	res, err := s.forwardTo(r, run, "/api/projects/"+url.PathEscape(rpid)+"/processes", body, rpid, p.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if res.ok() {
		res.body = rewriteProcs(res.body, run, sv.runnerTodos(p))
	}
	res.write(w)
}

// terminalRunner is the online runner of p a terminal for c opens on: one
// of c's own, one that said it opens terminals (-hub-commands) first; ""
// when none of them is c's.
func (s *Server) terminalRunner(c Caller, p model.Project) string {
	id := ""
	for _, run := range s.onlineRunners(p) {
		if ownsRunner(c, run) != nil {
			continue
		}
		if run.Reported != nil && run.Reported.Terminals {
			return run.ID
		}
		if id == "" {
			id = run.ID
		}
	}
	return id
}

// ownsRunner reports whether c registered run: only its owner gets a
// terminal on it, not the other members of its org.
func ownsRunner(c Caller, run tunnel.Runner) error {
	if c.User == 0 || run.Owner == nil || run.Owner.UserID != c.User {
		return fmt.Errorf("terminals on %s open only for the user who registered it", run.Name)
	}
	return nil
}

// processRunner is the runner of a process the hub listed (see hubProcID),
// if it is in the caller's org, and the runner's id for the process.
func (sv *service) processRunner(id string) (tunnel.Runner, string, error) {
	rid, pid, ok := strings.Cut(id, procSep)
	if !ok || pid == "" {
		return tunnel.Runner{}, "", fmt.Errorf("%w: no such process", store.ErrNotFound)
	}
	run, ok := sv.s.orgRunner(sv.c, rid)
	if !ok {
		return tunnel.Runner{}, "", fmt.Errorf("%w: no such process", store.ErrNotFound)
	}
	if !run.Online {
		return run, pid, errNoRunnerOnline
	}
	return run, pid, nil
}

// processViaRunner handles DELETE /api/processes/{procid} and GET
// /ws/proc/{procid} on the hub: they go to the process's runner. They say
// whom they are for (tunnel.UserHeader): the runner lets only its owner
// attach to a terminal (hubTerminal). The socket goes through as it is,
// both ways: keyboard input and resizes to the runner, the scrollback and
// output back.
func (s *Server) processViaRunner(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hub() {
			h(w, r)
			return
		}
		sv, ok := s.service(w, r)
		if !ok {
			return
		}
		run, pid, err := sv.processRunner(r.PathValue("procid"))
		if err != nil {
			writeErr(w, err)
			return
		}
		rest, _ := strings.CutPrefix(r.Pattern, r.Method+" ")
		path := strings.Replace(rest, "{procid}", url.PathEscape(pid), 1)
		if strings.HasPrefix(path, "/ws/") {
			// A socket streams: it goes through as it is.
			if !hub.Upgrader.CheckOrigin(r) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin not allowed"})
				return
			}
			px, ok := s.fleet().Proxy(run.Name)
			if !ok {
				writeErr(w, errNoRunnerOnline)
				return
			}
			out := r.Clone(tunnel.WithUser(r.Context(), sv.c.User))
			out.URL.Path, out.URL.RawPath = "/peer/"+run.Name+path, ""
			px.ServeHTTP(w, out)
			return
		}
		res, err := s.forwardTo(r.WithContext(tunnel.WithUser(r.Context(), sv.c.User)), run, path, nil, "", "")
		if err != nil {
			writeErr(w, err)
			return
		}
		res.write(w)
	}
}

// ---- GitHub Actions, read through a runner ----

// RunnerGitHub is the hub's ci.GitHub: gh runs on a runner linked to the
// project, in its clone (see hubGitHub), as the hub has neither.
type RunnerGitHub struct{ S *Server }

func (g RunnerGitHub) call(ctx context.Context, projectID string, req dispatch.GitHubCall) (dispatch.GitHubAnswer, error) {
	var out dispatch.GitHubAnswer
	s := g.S
	if s.Store == nil {
		return out, errNoRunnerOnline
	}
	org, ok := s.Store.ProjectOrg(projectID)
	if !ok {
		return out, fmt.Errorf("%w: no such project", store.ErrNotFound)
	}
	sv := s.as(Caller{Org: org, Machine: true})
	p, err := sv.data.GetProject(projectID)
	if err != nil {
		return out, err
	}
	run, _, err := sv.projectRunner(ctx, p, "")
	if err != nil {
		return out, err
	}
	err = s.Dispatch.GitHub(ctx, run.ID, p.ID, req, &out)
	return out, err
}

func (g RunnerGitHub) Runs(ctx context.Context, projectID, _, ref string, limit int) ([]gitops.Run, error) {
	out, err := g.call(ctx, projectID, dispatch.GitHubCall{Op: dispatch.GitHubRuns, Ref: ref, Limit: limit})
	return safeRuns(out.Runs), err
}

func (g RunnerGitHub) RunsForCommit(ctx context.Context, projectID, _, sha string, limit int) ([]gitops.Run, error) {
	out, err := g.call(ctx, projectID, dispatch.GitHubCall{Op: dispatch.GitHubCommitRuns, Ref: sha, Limit: limit})
	return safeRuns(out.Runs), err
}

func (g RunnerGitHub) ReleaseURL(ctx context.Context, projectID, _, tag string) (string, error) {
	out, err := g.call(ctx, projectID, dispatch.GitHubCall{Op: dispatch.GitHubReleaseURL, Ref: tag})
	return model.GitHubWebURL(out.URL), err
}

// safeRuns are runs a runner reported, without links that aren't pages on
// GitHub (see model.IsGitHubWebURL).
func safeRuns(runs []gitops.Run) []gitops.Run {
	for i := range runs {
		runs[i].URL = model.GitHubWebURL(runs[i].URL)
	}
	return runs
}

func (g RunnerGitHub) RerunRun(ctx context.Context, projectID, _ string, runID int64, failedOnly bool) error {
	_, err := g.call(ctx, projectID, dispatch.GitHubCall{Op: dispatch.GitHubRerun, RunID: runID, FailedOnly: failedOnly})
	return err
}

func (g RunnerGitHub) RunFailedLog(ctx context.Context, projectID, _ string, runID int64, lines int) string {
	out, _ := g.call(ctx, projectID, dispatch.GitHubCall{Op: dispatch.GitHubFailedLog, RunID: runID, Limit: lines})
	return out.Log
}

// pullOnRunner is POST /api/projects/{pid}/remote/pull on a hub: the
// runner named by ?runner= (it must be online and linked to p) pulls origin
// into its checkout, fast-forward only.
func (s *Server) pullOnRunner(w http.ResponseWriter, r *http.Request, sv *service, p model.Project) {
	id := r.URL.Query().Get("runner")
	online := s.onlineRunners(p)
	if id == "" && len(online) == 1 {
		id = online[0].ID
	}
	if id == "" {
		writeErr(w, fmt.Errorf("%w: say which runner's checkout to pull (?runner=)", errBadRequest))
		return
	}
	if !slices.ContainsFunc(online, func(x tunnel.Runner) bool { return x.ID == id }) {
		if !slices.Contains(p.Runners, id) {
			writeErr(w, fmt.Errorf("%w: that runner isn't linked to %s", errBadRequest, p.Name))
			return
		}
		writeErr(w, errNoRunnerOnline)
		return
	}
	res, err := sv.forward(r, p, id, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	res.write(w)
}
