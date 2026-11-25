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
	"github.com/klahr/dash/internal/mcptools"
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
// runner works on: none has the labels it runs on (or it runs on none, as
// one made on the hub before it ran nothing itself).
//
//lint:ignore ST1005 shown to the user verbatim as a sentence
var errNoRunnerLinked = errors.New("No runner has the labels this project runs on: give one those labels in Runners, or change them in Project settings → Agent & runners (start a runner with `dash runner`).")

// projectRunner picks the runner that does p's work for the hub: one of
// p's runners, online, in p's org; prefer if it is one of them. It links p
// to it and returns the runner and its project id for p.
func (sv *service) projectRunner(ctx context.Context, p model.Project, prefer string) (tunnel.Runner, string, error) {
	return sv.projectRunnerFor(ctx, p, prefer, nil)
}

// pushRunner is projectRunner for work that pushes or commits to p's
// remote (a push, a tag, a branch, a pull request): a
// runner does it with its owner's git login, so it runs only on one of the
// caller's own runners or a shared one, never on another member's.
func (sv *service) pushRunner(ctx context.Context, p model.Project, prefer string) (tunnel.Runner, string, error) {
	return sv.projectRunnerFor(ctx, p, prefer, []int64{sv.c.User})
}

// callRunner calls a project endpoint ("/file", "/brief", …) on one of p's
// runners, from a hub. Anything but a GET commits to the default branch and
// pushes, so it runs only on a runner of the caller's own or a shared one
// (pushRunner).
func (sv *service) callRunner(ctx context.Context, p model.Project, method, rest string, in, out any) error {
	pick := sv.pushRunner
	if method == http.MethodGet {
		pick = sv.projectRunner
	}
	run, rpid, err := pick(ctx, p, "")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(tunnel.WithUser(ctx, sv.c.User), 2*time.Minute)
	defer cancel()
	err = sv.s.runnerCall(ctx, run.ID, method, "/api/projects/"+url.PathEscape(rpid)+rest, in, out)
	var ce *tunnel.CallError
	if errors.As(err, &ce) && ce.Status == http.StatusConflict {
		return fmt.Errorf("%w: %w", errConflict, err)
	}
	return err
}

// runnerCall calls path on runner runnerID for a project's work, from a
// hub: a runner that is gone or offline is errNoRunnerOnline (503, see
// writeErr); a runner's error answer is a *tunnel.CallError. ctx says whom
// for (tunnel.WithUser) and how long it may take.
func (s *Server) runnerCall(ctx context.Context, runnerID, method, path string, in, out any) error {
	err := s.fleet().Call(ctx, runnerID, method, path, in, out)
	if errors.Is(err, tunnel.ErrOffline) || errors.Is(err, tunnel.ErrUnknownName) {
		return errNoRunnerOnline
	}
	return err
}

// pushRoutes are the forwarded routes (see viaRunner) that push or commit
// to the project's remote: they go to a runner through pushRunner.
var pushRoutes = map[string]bool{
	"POST /api/projects/{pid}/push":      true,
	"POST /api/projects/{pid}/tags":      true,
	"PUT /api/projects/{pid}/branch":     true,
	"POST /api/projects/{pid}/branch/pr": true,
}

// projectRunnerFor is projectRunner among the runners that do work for
// users (tunnel.Runner.WorksFor; nil: any of p's runners), for what runs a
// project command there: the caller's own runners, or shared ones.
func (sv *service) projectRunnerFor(ctx context.Context, p model.Project, prefer string, users []int64) (tunnel.Runner, string, error) {
	return sv.projectRunnerAmong(ctx, p, sv.s.onlineRunners(p), prefer, users)
}

// deployRunnerFor picks the runner that deploys p to e: among p's runners
// with every label e deploys on (model.Environment.RunsOn), online, that
// do work for users (nil: any). With none of them online it fails saying
// so; a deploy never goes to another runner.
func (sv *service) deployRunnerFor(ctx context.Context, p model.Project, e model.Environment, users []int64) (tunnel.Runner, string, error) {
	all := slices.DeleteFunc(sv.s.projectRunners(p), func(r tunnel.Runner) bool { return !r.Deploys(p, e) })
	if len(e.RunsOn) == 0 {
		return sv.projectRunnerAmong(ctx, p, sv.s.onlineRunners(p), "", users)
	}
	labels := strings.Join(e.RunsOn, ", ")
	if len(all) == 0 {
		return tunnel.Runner{}, "", fmt.Errorf("%w: no runner of this project has the labels %s deploys on (%s): give one those labels in Runners, or change them in Project settings → Deploy", errBadRequest, e.Name, labels)
	}
	online := slices.DeleteFunc(slices.Clone(all), func(r tunnel.Runner) bool { return !r.Online })
	if len(online) == 0 {
		names := make([]string, len(all))
		for i, r := range all {
			names[i] = r.Name
		}
		return tunnel.Runner{}, "", fmt.Errorf("%w (%s deploys only on runners with the labels %s: %s, all offline)", errNoRunnerOnline, e.Name, labels, strings.Join(names, ", "))
	}
	return sv.projectRunnerAmong(ctx, p, online, "", users)
}

// projectRunnerAmong is projectRunnerFor among cands, p's online runners
// (or some of them).
func (sv *service) projectRunnerAmong(ctx context.Context, p model.Project, cands []tunnel.Runner, prefer string, users []int64) (tunnel.Runner, string, error) {
	var notFor []string
	if users != nil {
		cands = slices.DeleteFunc(cands, func(r tunnel.Runner) bool {
			ok, why := r.WorksFor(users)
			if !ok {
				notFor = append(notFor, why)
			}
			return !ok
		})
	}
	if len(cands) == 0 {
		if len(sv.s.projectRunners(p)) == 0 {
			return tunnel.Runner{}, "", errNoRunnerLinked
		}
		if len(notFor) > 0 {
			return tunnel.Runner{}, "", fmt.Errorf("%w (none of yours, or a shared one, is: %s)", errNoRunnerOnline, strings.Join(notFor, "; "))
		}
		return tunnel.Runner{}, "", errNoRunnerOnline
	}
	r := mineFirst(cands, sv.c)[0]
	for _, c := range cands {
		if c.ID == prefer {
			r = c
		}
	}
	rpid, err := sv.linkOn(ctx, r, p)
	return r, rpid, err
}

// mineFirst orders runners for work done for c (User 0: no one in
// particular): c's own runners first, then shared ones, then other
// members'. A runner does its git work with its owner's git login, so a
// member's reads go to someone else's runner only when none of their own,
// and no shared one, is online (and pushes never do: pushRunner). It returns
// a sorted copy.
func mineFirst(runs []tunnel.Runner, c Caller) []tunnel.Runner {
	rank := func(r tunnel.Runner) int {
		if ownedBy(r, c) {
			return 0
		}
		if ok, _ := r.TakesOthers(); ok {
			return 1
		}
		return 2
	}
	out := slices.Clone(runs)
	slices.SortStableFunc(out, func(a, b tunnel.Runner) int { return rank(a) - rank(b) })
	return out
}

// onlineRunners are p's runners that are online, in p's org.
func (s *Server) onlineRunners(p model.Project) []tunnel.Runner {
	if s.Dispatch == nil {
		return nil
	}
	var out []tunnel.Runner
	for _, r := range s.projectRunners(p) {
		if r.Online {
			out = append(out, r)
		}
	}
	return out
}

// projectRunners are the runners that work on p, online or not: those of
// p's org with every label p runs on (tunnel.Runner.Serves).
func (s *Server) projectRunners(p model.Project) []tunnel.Runner {
	fleet := s.fleet()
	if fleet == nil {
		return nil
	}
	return tunnel.Serving(fleet.List(), p)
}

// servesProject reports whether runner id works on p.
func (s *Server) servesProject(p model.Project, id string) bool {
	return slices.ContainsFunc(s.projectRunners(p), func(r tunnel.Runner) bool { return r.ID == id })
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
	return sv.onRunnerFor(ctx, t, 30*time.Second, fn)
}

// reachesRunners says whether this Dash can send a task's runner actions.
func (sv *service) reachesRunners() error {
	if sv.s.Dispatch == nil {
		return fmt.Errorf("%w: this task ran on a runner, and this Dash doesn't reach runners", errBadRequest)
	}
	return nil
}

// onRunnerFor is onRunner with fn given timeout instead of 30 seconds.
func (sv *service) onRunnerFor(ctx context.Context, t model.Todo, timeout time.Duration, fn func(context.Context) error) error {
	if err := sv.reachesRunners(); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
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
		sv, p, ok := s.project(w, r)
		if !ok {
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
		case "GET /api/projects/{pid}/repo-info":
			s.repoInfoOnRunner(w, r, sv, p)
		case "POST /api/projects/{pid}/tags":
			// The runner trusts the hub (a Machine caller) to have checked
			// who may tag: check it here, before forwarding.
			// The hub deploys: the runner only tags, and isn't told where to.
			var req tagRequest
			if err := decode(r, &req); err != nil {
				writeErr(w, err)
				return
			}
			if err := sv.canRelease(); err != nil {
				writeErr(w, err)
				return
			}
			if err := sv.checkReleaseDeploy(p, req.Deploy); err != nil {
				writeErr(w, err)
				return
			}
			env := req.Deploy
			req.Deploy = ""
			body, _ := json.Marshal(req)
			res, err := sv.forward(r, p, "", body)
			if err != nil {
				writeErr(w, err)
				return
			}
			var rel mcptools.Release
			if !res.ok() || json.Unmarshal(res.body, &rel) != nil {
				res.write(w)
				return
			}
			s.watchRelease(p, &rel) // the runner tagged it: the hub follows its build
			writeJSON(w, res.code, sv.releaseDeploys(p, rel, env))
		case "GET /api/projects/{pid}/release":
			res, err := sv.forward(r, p, "", nil)
			if err != nil {
				writeErr(w, err)
				return
			}
			if res.ok() {
				res.body = hubCanTag(res.body, sv.canRelease())
			}
			res.write(w)
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

// hubCanTag gives a runner's release info (body) the hub's verdict on
// whether the caller may tag: the runner judged the hub, not the user. When
// the hub allows it, the runner's other reasons (not pushed yet) stand.
func hubCanTag(body []byte, denied error) []byte {
	if denied == nil {
		return body
	}
	var info map[string]any
	if json.Unmarshal(body, &info) != nil {
		return body
	}
	info["can_tag"] = false
	info["cant_tag"] = strings.TrimPrefix(denied.Error(), errForbidden.Error()+": ")
	if b, err := json.Marshal(info); err == nil {
		return b
	}
	return body
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
// replaces r's body when not nil. A request that pushes (pushRoutes) goes
// only to a runner of the caller's own or a shared one.
func (sv *service) forward(r *http.Request, p model.Project, prefer string, body []byte) (forwarded, error) {
	pick := sv.projectRunner
	if pushRoutes[r.Pattern] {
		pick = sv.pushRunner
	}
	run, rpid, err := pick(r.Context(), p, prefer)
	if err != nil {
		return forwarded{}, err
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/projects/"+p.ID)
	res, err := sv.s.forwardTo(r, run, "/api/projects/"+url.PathEscape(rpid)+rest, body, rpid, p.ID)
	if err != nil || !runnerLostProject(res) {
		return res, err
	}
	// The hub's link was cached (see dispatch.Dispatcher.Link), but the
	// runner no longer has that project: link it again, and ask again when
	// the request can be sent twice.
	sv.s.Dispatch.Relink(run.ID, p.ID)
	if body == nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		return res, nil // its body was read
	}
	run, rpid, err = pick(r.Context(), p, run.ID)
	if err != nil {
		return forwarded{}, err
	}
	return sv.s.forwardTo(r, run, "/api/projects/"+url.PathEscape(rpid)+rest, body, rpid, p.ID)
}

// runnerLostProject reports whether res is a runner's "not found" for the
// project a request was about (the answer to a project id it doesn't know).
func runnerLostProject(res forwarded) bool {
	if res.code != http.StatusNotFound {
		return false
	}
	var e struct {
		Error string `json:"error"`
	}
	return json.Unmarshal(res.body, &e) == nil && e.Error == store.ErrNotFound.Error()
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
	var req struct {
		Branch string `json:"branch"`
	}
	body, err := readJSON(r, &req)
	if err != nil {
		writeErr(w, fmt.Errorf("%w: %w", errBadRequest, err))
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
		p["runner"], p["runner_id"] = run.Name, run.ID
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
// on the hub. A terminal, or a command the caller names, runs only on a
// runner of the caller's own (the runner checks it too, from
// tunnel.UserHeader); the project's presets run for any member.
func (s *Server) startProcessOnRunner(w http.ResponseWriter, r *http.Request, sv *service, p model.Project) {
	var req map[string]any
	body, err := readJSON(r, &req)
	if err != nil {
		writeErr(w, fmt.Errorf("%w: %w", errBadRequest, err))
		return
	}
	// A terminal, or a command the caller names rather than one of the
	// project's own (a preset): a shell's, so the runner's owner's alone.
	cmd, _ := req["command"].(string)
	pty := req["kind"] == string(model.ProcPTY) || cmd != ""
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
	if rid, _ := req["runner_id"].(string); rid != "" {
		// The runner the caller picked for a terminal (the Shell view's
		// runner menu): one of theirs that works on p.
		delete(req, "runner_id")
		if body, err = json.Marshal(req); err != nil {
			writeErr(w, err)
			return
		}
		if pty && prefer == "" {
			if prefer, err = shellRunner(s.projectRunners(p), sv.c, rid); err != nil {
				writeErr(w, err)
				return
			}
		}
	}
	if pty && prefer == "" {
		prefer = s.terminalRunner(sv.c, p)
	}
	// A project command (run, test, deploy, a preview) runs only on the
	// caller's own runners or a shared one (tunnel.Runner.WorksFor); a
	// terminal only on their own (below).
	var users []int64
	if !pty {
		users = []int64{sv.c.User}
		if prefer != "" {
			if pr, ok := s.fleet().Get(prefer); ok {
				if ok, why := pr.WorksFor(users); !ok {
					writeErr(w, fmt.Errorf("%w: %s, so it doesn't run this task's commands for you", errForbidden, why))
					return
				}
			}
		}
	}
	if preset, _ := req["preset"].(string); preset == model.CmdDeploy && !pty {
		// A deploy is a deploy record now (deploys.go), never a process.
		writeErr(w, fmt.Errorf("%w: deploy to an environment from the Deploys page (POST /api/projects/{id}/deploys)", errBadRequest))
		return
	}
	run, rpid, err := sv.projectRunnerFor(r.Context(), p, prefer, users)
	if err != nil {
		writeErr(w, err)
		return
	}
	if prefer != "" && run.ID != prefer {
		why := "the task's worktree is on its runner, which is offline"
		if req["todo_id"] == nil {
			why = "the runner picked for it is offline"
		}
		writeErr(w, fmt.Errorf("%w (%s)", errNoRunnerOnline, why))
		return
	}
	if pty {
		if err := ownsRunner(sv.c, run); err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
	}
	// Whom it is for: the runner checks it too (hubshared.go, hubCheck).
	r = r.WithContext(tunnel.WithUser(r.Context(), sv.c.User))
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
// of c's own, one that said it opens terminals first; ""
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

// shellRunner is the runner id a terminal opens on when the caller picked
// one: of runners (p's, see projectRunners), one c registered. Another
// member's runner is refused (the runner would refuse it too), as is one
// that doesn't work on p; an offline one is the caller's to pick again.
func shellRunner(runners []tunnel.Runner, c Caller, id string) (string, error) {
	i := slices.IndexFunc(runners, func(r tunnel.Runner) bool { return r.ID == id })
	if i < 0 {
		return "", fmt.Errorf("%w: that runner doesn't work on this project", errBadRequest)
	}
	run := runners[i]
	if err := ownsRunner(c, run); err != nil {
		return "", fmt.Errorf("%w: %w", errForbidden, err)
	}
	if !run.Online {
		return "", fmt.Errorf("%w (%s is offline)", errNoRunnerOnline, run.Name)
	}
	return run.ID, nil
}

// ownsRunner reports whether c registered run: only its owner gets a
// terminal on it, not the other members of its org.
func ownsRunner(c Caller, run tunnel.Runner) error {
	if !ownedBy(run, c) {
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
				writeError(w, http.StatusForbidden, "origin not allowed")
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
	if rl, ok := gitops.AsRateLimit(err); ok {
		// GitHub rate limits the runner's gh: the build watcher pauses.
		rl.Machine = run.Name
		return out, rl
	}
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

func (g RunnerGitHub) PushRuns(ctx context.Context, projectID, _ string, limit int) ([]gitops.Run, error) {
	out, err := g.call(ctx, projectID, dispatch.GitHubCall{Op: dispatch.GitHubPushRuns, Limit: limit})
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
		if !s.servesProject(p, id) {
			writeErr(w, fmt.Errorf("%w: that runner doesn't work on %s (it lacks the labels it runs on)", errBadRequest, p.Name))
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
