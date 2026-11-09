package api

// Try: a terminal on a project itself, for trying its CLI at the tip of its
// default branch or at a commit picked from its history, with the project
// chat beside it (which reads the terminal with read_try_terminal and files
// what the user finds as issues or draft tasks).
//
// The hub keeps no files of its projects, so a try session always runs on a
// runner the user picks: one that is online, has the project linked and
// opens terminals for them (tryRunners). On that runner it runs in a
// detached worktree Dash makes from the runner's clone, under its data
// folder (Server.TryRoot), never in the clone's own checkout; the worktree
// goes when the session stops (by hand, replaced, or idle for 30 minutes,
// see preview.Manager.StopTry).
//
// The hub's routes (/api/projects/{pid}/try…, /ws/projects/{pid}/try) check
// the caller, the project and the runner, and go through to the runner's
// routes (/api/hub/projects/{hpid}/…, /ws/hub/projects/{hpid}/try) over its
// tunnel, as the hub's peer proxy does. The runner checks again: the hub
// only, the project linked to it, and terminals (hubTerminal) for all but
// the commit list.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/preview"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

func (s *Server) tryRoutes(mux *http.ServeMux) {
	// The hub's side, for the UI and the chat's read_try_terminal.
	mux.HandleFunc("GET /api/projects/{pid}/try", s.getTry)
	mux.HandleFunc("POST /api/projects/{pid}/try", s.startTry)
	mux.HandleFunc("DELETE /api/projects/{pid}/try", s.stopTry)
	mux.HandleFunc("GET /api/projects/{pid}/try/commits", s.tryCommits)
	mux.HandleFunc("GET /api/projects/{pid}/try/log", s.tryLog)
	mux.HandleFunc("GET /ws/projects/{pid}/try", s.tryWS)
	// A runner's side: only its hub asks.
	mux.HandleFunc("GET /api/hub/projects/{hpid}/commits", s.hubCommits)
	mux.HandleFunc("GET /api/hub/projects/{hpid}/try", s.hubTryGet)
	mux.HandleFunc("POST /api/hub/projects/{hpid}/try", s.hubTryStart)
	mux.HandleFunc("DELETE /api/hub/projects/{hpid}/try", s.hubTryStop)
	mux.HandleFunc("GET /api/hub/projects/{hpid}/try/log", s.hubTryLog)
	mux.HandleFunc("GET /ws/hub/projects/{hpid}/try", s.hubTryWS)
}

// Commit picker bounds: how many commits a list shows, and how far back a
// search looks.
const (
	tryCommitLimit = 100
	tryCommitScan  = 2000
	// tryLogLines is how many lines read_try_terminal returns by default.
	tryLogLines = 80
)

// ---- which runners may try a project ----

// tryRunner is a runner a try session may run on.
type tryRunner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// tryRunners are the runners p's try session may run on for c: online,
// linked to p (p.Runners), in p's org, opening terminals for the hub
// (-hub-commands) and registered by c (terminals open only for a runner's
// owner). When there is none, why says so in a sentence for the UI.
func tryRunners(fleet []tunnel.Runner, p model.Project, c Caller) (out []tryRunner, why string) {
	linked, online, terms := 0, 0, 0
	for _, r := range fleet {
		if !slices.Contains(p.Runners, r.ID) || r.OrgID() != p.OrgID {
			continue
		}
		linked++
		if !r.Online {
			continue
		}
		online++
		if r.Reported == nil || !r.Reported.Terminals {
			continue
		}
		terms++
		if ownsRunner(c, r) != nil {
			continue
		}
		out = append(out, tryRunner{ID: r.ID, Name: r.Name})
	}
	switch {
	case len(out) > 0:
	case linked == 0:
		why = "No runner is linked to this project: link one in Project settings → Runners."
	case online == 0:
		why = "No runner with this project is connected."
	case terms == 0:
		why = "No connected runner with this project allows terminals (start it with -hub-commands)."
	default:
		why = "Terminals open only on your own runner, and no runner of yours with this project is connected."
	}
	return out, why
}

// tryState is GET /api/projects/{pid}/try: the runners Try may use and the
// session running, if any.
type tryState struct {
	Runners []tryRunner `json:"runners"`
	// Why Try can't run when Runners is empty.
	Disabled string `json:"disabled,omitempty"`
	// Default is the runner picked when there is only one.
	Default string            `json:"default,omitempty"`
	Session *model.TrySession `json:"session,omitempty"`
}

// tryProject is the caller's project {pid} and the runners it may try it on.
func (s *Server) tryProject(w http.ResponseWriter, r *http.Request) (*service, model.Project, []tryRunner, string, bool) {
	sv, ok := s.service(w, r)
	if !ok {
		return nil, model.Project{}, nil, "", false
	}
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return nil, model.Project{}, nil, "", false
	}
	if !s.hub() || s.Dispatch == nil {
		return sv, p, nil, "Try runs on a runner, and this Dash has none (start one with `dash runner`).", true
	}
	runs, why := tryRunners(s.fleet().List(), p, sv.c)
	return sv, p, runs, why, true
}

// pickRunner is the runner id (one of runs) the request names, or the only one.
func pickRunner(runs []tryRunner, id, why string) (tryRunner, error) {
	if len(runs) == 0 {
		return tryRunner{}, fmt.Errorf("%w: %s", errBadRequest, why)
	}
	if id == "" && len(runs) == 1 {
		return runs[0], nil
	}
	for _, x := range runs {
		if x.ID == id {
			return x, nil
		}
	}
	if id == "" {
		return tryRunner{}, fmt.Errorf("%w: pick the runner to try it on", errBadRequest)
	}
	return tryRunner{}, fmt.Errorf("%w: that runner can't run Try for this project", errBadRequest)
}

// ---- the hub's side ----

// trySessions remembers, on the hub, which runner each project's try
// session runs on (one at a time).
type trySessions struct {
	mu sync.Mutex
	m  map[string]string // hub project id → runner id
}

func (t *trySessions) get(pid string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[pid]
}

func (t *trySessions) set(pid, runner string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]string{}
	}
	if runner == "" {
		delete(t.m, pid)
	} else {
		t.m[pid] = runner
	}
}

// tryCall calls a runner's try route for p on behalf of c.
func (s *Server) tryCall(ctx context.Context, c Caller, runnerID, method, rest string, in, out any) error {
	ctx, cancel := context.WithTimeout(tunnel.WithUser(ctx, c.User), 2*time.Minute)
	defer cancel()
	err := s.fleet().Call(ctx, runnerID, method, rest, in, out)
	if errors.Is(err, tunnel.ErrOffline) || errors.Is(err, tunnel.ErrUnknownName) {
		return errNoRunnerOnline
	}
	return err
}

// session is p's try session as the runner it runs on has it (nil when
// there is none any more); the hub forgets a session its runner doesn't have.
func (s *Server) trySession(ctx context.Context, c Caller, p model.Project) (*model.TrySession, error) {
	id := s.tries.get(p.ID)
	if id == "" || s.fleet() == nil {
		return nil, nil
	}
	run, ok := s.fleet().Get(id)
	if !ok || !run.Online || !slices.Contains(p.Runners, id) {
		s.tries.set(p.ID, "")
		return nil, nil
	}
	var sess model.TrySession
	err := s.tryCall(ctx, c, id, "GET", "/api/hub/projects/"+url.PathEscape(p.ID)+"/try", nil, &sess)
	var ce *tunnel.CallError
	if errors.As(err, &ce) && ce.Status == http.StatusNotFound {
		s.tries.set(p.ID, "")
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.hubSession(sess, run, p), nil
}

// findTry asks runs for a try session of p the hub has no record of (it
// restarted since), and remembers the runner of the first one found, so
// opening Try reattaches to it rather than starting another.
func (s *Server) findTry(ctx context.Context, c Caller, p model.Project, runs []tryRunner) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, x := range runs {
		var sess model.TrySession
		if s.tryCall(ctx, c, x.ID, "GET", "/api/hub/projects/"+url.PathEscape(p.ID)+"/try", nil, &sess) == nil {
			s.tries.set(p.ID, x.ID)
			return
		}
	}
}

// hubSession is a runner's try session as the hub's UI sees it.
func (s *Server) hubSession(sess model.TrySession, run tunnel.Runner, p model.Project) *model.TrySession {
	sess.ProjectID, sess.Runner, sess.RunnerID = p.ID, run.Name, run.ID
	sess.TerminalURL = "/ws/projects/" + url.PathEscape(p.ID) + "/try"
	return &sess
}

// getTry handles GET /api/projects/{pid}/try.
func (s *Server) getTry(w http.ResponseWriter, r *http.Request) {
	sv, p, runs, why, ok := s.tryProject(w, r)
	if !ok {
		return
	}
	st := tryState{Runners: runs, Disabled: why}
	if st.Runners == nil {
		st.Runners = []tryRunner{}
	}
	if len(runs) == 1 {
		st.Default = runs[0].ID
	}
	if s.hub() {
		if s.tries.get(p.ID) == "" {
			s.findTry(r.Context(), sv.c, p, runs)
		}
		sess, err := s.trySession(r.Context(), sv.c, p)
		if err != nil {
			slog.Info("try: session status", "project", p.Name, "err", err)
		}
		st.Session = sess
	}
	writeJSON(w, http.StatusOK, st)
}

// startTry handles POST /api/projects/{pid}/try {"runner": id, "ref": rev}:
// the project's try session at ref ("" for the default branch's tip, as
// fetched now) on that runner, replacing the one running (also on another
// runner).
func (s *Server) startTry(w http.ResponseWriter, r *http.Request) {
	sv, p, runs, why, ok := s.tryProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Runner string `json:"runner"`
		Ref    string `json:"ref"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	req.Ref = strings.TrimSpace(req.Ref)
	if req.Ref != "" {
		if err := gitops.CheckRev(req.Ref); err != nil {
			writeErr(w, fmt.Errorf("%w: %v", errBadRequest, err))
			return
		}
	}
	pick, err := pickRunner(runs, req.Runner, why)
	if err != nil {
		writeErr(w, err)
		return
	}
	run, _ := s.fleet().Get(pick.ID)
	// The runner has the project linked (cloned) before it is asked to try it.
	if _, err := sv.linkOn(r.Context(), run, p); err != nil {
		writeErr(w, err)
		return
	}
	if prev := s.tries.get(p.ID); prev != "" && prev != run.ID {
		// One session per project: the one on the other runner goes.
		if err := s.tryCall(r.Context(), sv.c, prev, "DELETE", "/api/hub/projects/"+url.PathEscape(p.ID)+"/try", nil, nil); err != nil {
			slog.Info("try: stop on the previous runner", "project", p.Name, "err", err)
		}
		s.tries.set(p.ID, "")
	}
	var sess model.TrySession
	if err := s.tryCall(r.Context(), sv.c, run.ID, "POST", "/api/hub/projects/"+url.PathEscape(p.ID)+"/try", map[string]string{"ref": req.Ref}, &sess); err != nil {
		writeErr(w, err)
		return
	}
	s.tries.set(p.ID, run.ID)
	slog.Info("try: started", "project", p.Name, "runner", run.Name, "commit", sess.Commit)
	writeJSON(w, http.StatusOK, s.hubSession(sess, run, p))
}

// stopTry handles DELETE /api/projects/{pid}/try.
func (s *Server) stopTry(w http.ResponseWriter, r *http.Request) {
	sv, p, _, _, ok := s.tryProject(w, r)
	if !ok {
		return
	}
	if id := s.tries.get(p.ID); id != "" && s.hub() {
		err := s.tryCall(r.Context(), sv.c, id, "DELETE", "/api/hub/projects/"+url.PathEscape(p.ID)+"/try", nil, nil)
		var ce *tunnel.CallError
		if err != nil && !(errors.As(err, &ce) && ce.Status == http.StatusNotFound) {
			writeErr(w, err)
			return
		}
		s.tries.set(p.ID, "")
	}
	w.WriteHeader(http.StatusNoContent)
}

// tryCommits handles GET /api/projects/{pid}/try/commits?runner=&q=&fetch=1:
// recent commits of the project's default branch, read from the runner's
// clone (the hub has no git data), newest first.
func (s *Server) tryCommits(w http.ResponseWriter, r *http.Request) {
	sv, p, runs, why, ok := s.tryProject(w, r)
	if !ok {
		return
	}
	pick, err := pickRunner(runs, r.URL.Query().Get("runner"), why)
	if err != nil {
		writeErr(w, err)
		return
	}
	run, _ := s.fleet().Get(pick.ID)
	if _, err := sv.linkOn(r.Context(), run, p); err != nil {
		writeErr(w, err)
		return
	}
	q := url.Values{}
	for _, k := range []string{"q", "fetch", "limit"} {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	var raw []byte
	if err := s.tryCall(r.Context(), sv.c, run.ID, "GET", "/api/hub/projects/"+url.PathEscape(p.ID)+"/commits?"+q.Encode(), nil, &raw); err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// tryOutput is a try terminal's recent output, for the UI and the chat.
type tryOutput struct {
	Session model.TrySession `json:"session"`
	Text    string           `json:"text"`
}

// readTry is the end of p's try terminal (lines of it), with the session.
func (s *Server) readTry(ctx context.Context, c Caller, p model.Project, lines int) (tryOutput, error) {
	var out tryOutput
	id := s.tries.get(p.ID)
	if !s.hub() || id == "" {
		return out, preview.ErrNoTry
	}
	run, ok := s.fleet().Get(id)
	if !ok {
		return out, preview.ErrNoTry
	}
	if lines <= 0 {
		lines = tryLogLines
	}
	err := s.tryCall(ctx, c, id, "GET", fmt.Sprintf("/api/hub/projects/%s/try/log?lines=%d", url.PathEscape(p.ID), min(lines, maxPreviewLogLines)), nil, &out)
	var ce *tunnel.CallError
	if errors.As(err, &ce) && ce.Status == http.StatusNotFound {
		s.tries.set(p.ID, "")
		return out, preview.ErrNoTry
	}
	if err != nil {
		return out, err
	}
	out.Session = *s.hubSession(out.Session, run, p)
	return out, nil
}

// tryLog handles GET /api/projects/{pid}/try/log?lines=N.
func (s *Server) tryLog(w http.ResponseWriter, r *http.Request) {
	sv, p, _, _, ok := s.tryProject(w, r)
	if !ok {
		return
	}
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	out, err := s.readTry(r.Context(), sv.c, p, lines)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// tryWS handles GET /ws/projects/{pid}/try: the socket goes through to the
// try session's runner as it is (procWS's protocol), like a process's.
func (s *Server) tryWS(w http.ResponseWriter, r *http.Request) {
	sv, p, runs, _, ok := s.tryProject(w, r)
	if !ok {
		return
	}
	id := s.tries.get(p.ID)
	if id == "" || !s.hub() {
		writeErr(w, preview.ErrNoTry)
		return
	}
	if !slices.ContainsFunc(runs, func(x tryRunner) bool { return x.ID == id }) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "that runner doesn't open terminals for you"})
		return
	}
	if !hub.Upgrader.CheckOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin not allowed"})
		return
	}
	run, _ := s.fleet().Get(id)
	px, ok := s.fleet().Proxy(run.Name)
	if !ok {
		writeErr(w, errNoRunnerOnline)
		return
	}
	out := r.Clone(tunnel.WithUser(r.Context(), sv.c.User))
	out.URL.Path, out.URL.RawPath = "/peer/"+run.Name+preview.TryPath(p.ID), ""
	out.URL.RawQuery = ""
	px.ServeHTTP(w, out)
}

// tryContext is what the project chat is told while p has a try session:
// where it runs, and how to file what the user finds.
func (s *Server) tryContext(ctx context.Context, c Caller, p model.Project) string {
	if !s.hub() || s.tries.get(p.ID) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sess, err := s.trySession(ctx, c, p)
	if err != nil || sess == nil {
		return ""
	}
	return TryInstructions(*sess)
}

// TryInstructions tell the project chat about the user's try session and
// how to handle what they find while trying.
func TryInstructions(sess model.TrySession) string {
	short := sess.Commit
	if len(short) > 12 {
		short = short[:12]
	}
	at := "the tip of " + sess.Branch
	if sess.Ref != "" {
		at = sess.Ref
	}
	return fmt.Sprintf(`<try_session runner=%q commit=%q state=%q>
I am trying the project in its Try terminal next to this chat: %s (commit %s, %q) in a throwaway worktree on runner %s, with the project's preview command.
- When I refer to what just happened there ("that crashed", "the last command", "this output"), call read_try_terminal first and read what the terminal shows. Don't guess.
- When I say "note …" or describe a bug or problem while trying, file it with report_issue (type bug unless it's something else). Put in details the command I ran and its output as a short excerpt from the terminal (in a code block) and the commit hash %s. Don't fix it here.
- When I say "make a task of this" (or similar), create a draft task with create_todo instead, with the same excerpt and commit hash in its prompt, phrased as instructions for Claude Code.
- Answer briefly: say what you filed, with its title.
</try_session>

`, sess.Runner, sess.Commit, sess.State, at, short, sess.Subject, sess.Runner, sess.Commit)
}

// ---- a runner's side ----

// tryHubProject is the runner's clone of the hub's project {hpid} for a
// request from the hub; terminal says the request opens or reads a terminal
// (hubCheck has already checked hubTerminal for those).
func (s *Server) tryHubProject(w http.ResponseWriter, r *http.Request) (model.Project, bool) {
	sv, ok := s.fromHub(w, r)
	if !ok {
		return model.Project{}, false
	}
	if s.Previews == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this runner has no terminal previews"})
		return model.Project{}, false
	}
	p, ok := sv.hubProject(r.PathValue("hpid"))
	if !ok {
		if r.Method != http.MethodGet { // the hub looks for sessions on every runner of a project
			slog.Warn("runner: refused a try of a project not linked to it", "project", r.PathValue("hpid"))
		}
		writeJSON(w, http.StatusForbidden, map[string]string{"error": errNotLinked.Error()})
		return model.Project{}, false
	}
	return p, true
}

// tryRoot is where this runner keeps try worktrees: <root>/<project>/<id>.
func (s *Server) tryRoot() string {
	if s.TryRoot != "" {
		return s.TryRoot
	}
	return filepath.Join(os.TempDir(), "dash-try")
}

// tryLocks serialises starting and stopping a project's try session here.
var tryLocks sync.Map // runner project id → *sync.Mutex

func tryLock(id string) func() {
	mu, _ := tryLocks.LoadOrStore(id, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	return mu.(*sync.Mutex).Unlock
}

// fetchMain fetches p's default branch from origin into the clone's
// origin/<branch> (no local branch or checkout moves) and returns the ref
// of its tip: origin's, else the clone's own branch when origin has none or
// can't be reached (warn says so).
func (s *Server) fetchMain(ctx context.Context, p model.Project) (ref, warn string) {
	err := s.withRemoteLock(ctx, p, func() error {
		fctx, cancel := context.WithTimeout(ctx, branchFetchTimeout)
		defer cancel()
		return s.Git.FetchBranch(fctx, p.ID, p.Path, p.DefaultBranch, false)
	})
	if err != nil {
		warn = "couldn't fetch " + p.DefaultBranch + " from origin, using this runner's copy: " + err.Error()
	}
	if s.Git.RemoteHasBranch(ctx, p.ID, p.Path, p.DefaultBranch) {
		return "refs/remotes/origin/" + p.DefaultBranch, warn
	}
	return "refs/heads/" + p.DefaultBranch, warn
}

// tryCheckout makes a new detached worktree of p's clone at rev ("" for
// the default branch's tip, fetched first) for a try session, after
// removing what earlier sessions left (they are stopped by now). The
// clone's own checkout is never touched.
func (s *Server) tryCheckout(ctx context.Context, p model.Project, rev string) (dir string, c model.CommitInfo, err error) {
	main, _ := s.fetchMain(ctx, p)
	if rev == "" {
		rev = main
	}
	c, err = s.Git.CommitOf(ctx, p.ID, p.Path, rev)
	if err != nil {
		if c2, err2 := s.Git.CommitOf(ctx, p.ID, p.Path, "refs/remotes/origin/"+rev); err2 == nil {
			c, err = c2, nil
		}
	}
	if err != nil {
		return "", c, fmt.Errorf("%w: no commit %s in %s", errBadRequest, rev, p.Name)
	}
	base := filepath.Join(s.tryRoot(), p.ID)
	if old, _ := os.ReadDir(base); len(old) > 0 {
		for _, d := range old {
			s.removeTryWorktree(p.ID, p.Path, filepath.Join(base, d.Name()))
		}
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", c, err
	}
	dir = filepath.Join(base, store.NewID())
	if err := s.Git.AddDetachedWorktree(ctx, p.ID, p.Path, dir, c.SHA); err != nil {
		return "", c, err
	}
	return dir, c, nil
}

// removeTryWorktree removes a try session's worktree and git's record of
// it. What the session started may still be writing there for a moment
// (a build its shell ran), so the folder is removed again until it's gone.
func (s *Server) removeTryWorktree(projectID, repo, dir string) {
	ctx := context.Background()
	s.Git.RemoveDetachedWorktree(ctx, projectID, repo, dir)
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		time.Sleep(200 * time.Millisecond)
		_ = os.RemoveAll(dir)
	}
	s.Git.PruneWorktrees(ctx, projectID, repo)
}

// hubCommits handles GET /api/hub/projects/{hpid}/commits?q=&limit=&fetch=1:
// the commits of the default branch's tip (origin's, fetched first with
// fetch=1), newest first.
func (s *Server) hubCommits(w http.ResponseWriter, r *http.Request) {
	p, ok := s.tryHubProject(w, r)
	if !ok {
		return
	}
	ref := "refs/heads/" + p.DefaultBranch
	warn := ""
	if r.URL.Query().Get("fetch") != "" {
		ref, warn = s.fetchMain(r.Context(), p)
	} else if s.Git.RemoteHasBranch(r.Context(), p.ID, p.Path, p.DefaultBranch) {
		ref = "refs/remotes/origin/" + p.DefaultBranch
	}
	limit := tryCommitLimit
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = min(n, tryCommitLimit)
	}
	cs, err := s.Git.Commits(r.Context(), p.ID, p.Path, ref, r.URL.Query().Get("q"), limit, tryCommitScan)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"branch": p.DefaultBranch, "commits": cs, "warning": warn})
}

// hubTryGet handles GET /api/hub/projects/{hpid}/try.
func (s *Server) hubTryGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.tryHubProject(w, r); !ok {
		return
	}
	sess, ok := s.Previews.Try(r.PathValue("hpid"))
	if !ok {
		writeErr(w, preview.ErrNoTry)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

// hubTryStart handles POST /api/hub/projects/{hpid}/try {"ref": rev}: the
// project's try session in a new worktree at rev, replacing the one running.
func (s *Server) hubTryStart(w http.ResponseWriter, r *http.Request) {
	p, ok := s.tryHubProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Ref string `json:"ref"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	req.Ref = strings.TrimSpace(req.Ref)
	if req.Ref != "" {
		if err := gitops.CheckRev(req.Ref); err != nil {
			writeErr(w, fmt.Errorf("%w: %v", errBadRequest, err))
			return
		}
	}
	key := r.PathValue("hpid")
	defer tryLock(p.ID)()
	_ = s.Previews.StopTry(key, "replaced") // its worktree goes before the new one is made
	// Once the old session is gone the new one is made, even if the hub
	// stops waiting (the fetch has its own timeout).
	ctx := context.WithoutCancel(r.Context())
	dir, c, err := s.tryCheckout(ctx, p, req.Ref)
	if err != nil {
		writeErr(w, err)
		return
	}
	pid, path := p.ID, p.Path
	sess, err := s.Previews.StartTry(preview.TryStart{Key: key, Project: p, Dir: dir, Ref: req.Ref, Commit: c,
		Cleanup: func() { s.removeTryWorktree(pid, path, dir) }})
	if err != nil {
		writeErr(w, err)
		return
	}
	slog.Info("runner: try session", "project", p.Name, "commit", c.Short, "dir", dir)
	writeJSON(w, http.StatusOK, sess)
}

// hubTryStop handles DELETE /api/hub/projects/{hpid}/try.
func (s *Server) hubTryStop(w http.ResponseWriter, r *http.Request) {
	p, ok := s.tryHubProject(w, r)
	if !ok {
		return
	}
	defer tryLock(p.ID)()
	if err := s.Previews.StopTry(r.PathValue("hpid"), "stopped"); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// hubTryLog handles GET /api/hub/projects/{hpid}/try/log?lines=N: the end
// of the try terminal as plain text, with the session.
func (s *Server) hubTryLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.tryHubProject(w, r); !ok {
		return
	}
	key := r.PathValue("hpid")
	sess, ok := s.Previews.Try(key)
	if !ok {
		writeErr(w, preview.ErrNoTry)
		return
	}
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	b, err := s.Previews.TryLog(key, min(max(lines, 0), maxPreviewLogLines))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tryOutput{Session: sess, Text: string(b)})
}

// hubTryWS handles GET /ws/hub/projects/{hpid}/try: it attaches to the try
// terminal (procWS's protocol); typing keeps it from going idle.
func (s *Server) hubTryWS(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.tryHubProject(w, r); !ok {
		return
	}
	sess, touch, err := s.Previews.TryTerminal(r.PathValue("hpid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	s.terminalWS(w, r, sess, touch)
}
