package api

// The wall view: a read-only, full-screen dashboard for a TV in the office
// (GET /wall, web/static/wall.html), fed by one JSON endpoint (GET
// /api/wall) that sums up an org's work across its projects. A signed-in
// session sees its active org; a TV opens /wall?token=<wall token> instead,
// a read-only token an org admin makes (/api/wall/tokens) that reaches
// these two paths and nothing else (see auth.WallPaths).

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
	"github.com/klahr/dash/internal/usage"
)

// When an item that needs a human gets a red border.
const (
	wallReviewOld = time.Hour
	wallFailedOld = 30 * time.Minute
	wallMaxItems  = 20 // per list: a TV shows fewer anyway
	wallMaxEvents = 30
)

type wallData struct {
	Now     time.Time    `json:"now"`
	Org     string       `json:"org"`
	Project string       `json:"project,omitempty"` // the ?project= filter's name
	Privacy bool         `json:"privacy"`
	Queue   wallQueue    `json:"queue"`
	Usage   wallUsage    `json:"usage"`
	Stages  wallStage    `json:"stages"`
	Running []wallRun    `json:"running"`
	Needs   []wallNeed   `json:"needs"`
	Issues  wallIssues   `json:"issues"`
	Runners []wallRunner `json:"runners"`
	Today   wallToday    `json:"today"`
	Events  []wallEvent  `json:"events"`
}

type wallQueue struct {
	Paused bool   `json:"paused"`
	Reason string `json:"reason,omitempty"`
	Busy   int    `json:"busy"`  // tasks running
	Slots  int    `json:"slots"` // tasks that can run at once, on the runners online
}

type wallUsage struct {
	Known   bool           `json:"known"`
	PauseAt float64        `json:"pause_at"`
	Windows []usage.Window `json:"windows"`
}

type wallStage struct {
	Queued        int        `json:"queued"`
	Blocked       int        `json:"blocked"`        // waiting on dependencies
	WaitingRunner int        `json:"waiting_runner"` // no runner can take them
	Paused        int        `json:"paused"`
	Running       int        `json:"running"`
	RunningAvgS   int        `json:"running_avg_s"`
	Review        int        `json:"review"`
	ReviewOldest  *time.Time `json:"review_oldest,omitempty"`
	Merging       int        `json:"merging"`
	MergingPR     int        `json:"merging_pr"` // waiting on a pull request's checks
	MergedToday   int        `json:"merged_today"`
	MergedAdd     int        `json:"merged_add"`
	MergedDel     int        `json:"merged_del"`
	Failed        int        `json:"failed"`
	Resumable     int        `json:"resumable"`
}

type wallRun struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Project   string     `json:"project"`
	Color     string     `json:"color,omitempty"`
	Runner    string     `json:"runner"`
	Run       int        `json:"run"`
	CostUSD   *float64   `json:"cost_usd,omitempty"` // nil in privacy mode
	StartedAt *time.Time `json:"started_at,omitempty"`
	Add       int        `json:"add"`
	Del       int        `json:"del"`
	Files     int        `json:"files"`
	Activity  string     `json:"activity,omitempty"`
	SentBack  string     `json:"sent_back,omitempty"`
}

type wallNeed struct {
	Kind    string    `json:"kind"` // review | failed
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Project string    `json:"project"`
	Since   time.Time `json:"since"`
	Old     bool      `json:"old"`
	Add     int       `json:"add,omitempty"`
	Del     int       `json:"del,omitempty"`
	Tests   string    `json:"tests,omitempty"` // passed | failed | ""
	PR      int       `json:"pr,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Resume  bool      `json:"resumable,omitempty"`
}

type wallIssues struct {
	Open     int            `json:"open"`
	Severity map[string]int `json:"severity"`
	Top      []string       `json:"top"` // a few titles, most severe first (empty in privacy mode)
}

type wallRunner struct {
	Name     string     `json:"name"`
	Online   bool       `json:"online"`
	Paused   bool       `json:"paused,omitempty"`
	Busy     int        `json:"busy"`
	Slots    int        `json:"slots"`
	Projects int        `json:"projects"`
	Waiting  int        `json:"waiting"` // queued tasks that only it could take
	LastSeen *time.Time `json:"last_seen,omitempty"`
}

type wallToday struct {
	Merged   int       `json:"merged"`
	SpendUSD *float64  `json:"spend_usd,omitempty"` // nil in privacy mode
	FirstTry *float64  `json:"first_try,omitempty"` // merged today after a single run, 0..1
	PerHour  []int     `json:"per_hour"`            // merges in each hour of today, 0..23
	Hour     int       `json:"hour"`                // the current hour
	Midnight time.Time `json:"midnight"`
}

type wallEvent struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // merged | started | sent_back | ci | review | failed | offline
	Text string    `json:"text"`
}

// wallCaller is who may see r's wall: a wall token's org (?token=), else
// the session's.
func (s *Server) wallCaller(r *http.Request) (Caller, error) {
	if tok := r.URL.Query().Get("token"); tok != "" && auth.WallPaths[r.URL.Path] {
		wt, err := s.Store.Accounts().AuthenticateWall(tok, time.Now())
		if err != nil {
			return Caller{}, err
		}
		return Caller{Org: wt.OrgID}, nil
	}
	return s.caller(r)
}

// wallPage serves GET /wall.
func (s *Server) wallPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.wallCaller(r); err != nil {
		http.Error(w, "this wall link is invalid or was revoked", http.StatusUnauthorized)
		return
	}
	b, err := fs.ReadFile(s.Static, "wall.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer") // the token is in the URL
	_, _ = w.Write(s.Brand.WithDefaults().Page(b))
}

// getWall serves GET /api/wall[?project=<id or name>][&privacy=1][&tz=<minutes>].
func (s *Server) getWall(w http.ResponseWriter, r *http.Request) {
	c, err := s.wallCaller(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	q := r.URL.Query()
	loc := time.Local
	if v := q.Get("tz"); v != "" { // the TV's getTimezoneOffset(): "today" is its day
		if m, err := strconv.Atoi(v); err == nil && m >= -16*60 && m <= 16*60 {
			loc = time.FixedZone("", -m*60)
		}
	}
	d, err := s.wall(c, q.Get("project"), q.Get("privacy") == "1", time.Now().In(loc))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, d)
}

// wall sums up c's org's work as of now.
func (s *Server) wall(c Caller, project string, privacy bool, now time.Time) (wallData, error) {
	data := s.Store.ForOrg(c.Org)
	d := wallData{Now: now, Privacy: privacy, Running: []wallRun{}, Needs: []wallNeed{}, Runners: []wallRunner{}, Events: []wallEvent{},
		Issues: wallIssues{Severity: map[string]int{}, Top: []string{}}}
	if o, err := s.Store.Accounts().Org(c.Org); err == nil {
		d.Org = o.Name
	}

	projects := data.ListProjects()
	if project != "" {
		i := slices.IndexFunc(projects, func(p model.Project) bool { return p.ID == project || strings.EqualFold(p.Name, project) })
		if i < 0 {
			return d, fmt.Errorf("%w: no project %q", store.ErrNotFound, project)
		}
		projects = projects[i : i+1]
		d.Project = projects[0].Name
	}
	byProject := map[string]model.Project{}
	for _, p := range projects {
		byProject[p.ID] = p
	}
	var todos []model.Todo
	for _, t := range data.ListTodos("") {
		if _, ok := byProject[t.ProjectID]; ok {
			todos = append(todos, t)
		}
	}

	// Runners: the org's, or this computer when it runs tasks itself.
	var runners []tunnel.Runner
	if fl := s.fleet(); fl != nil {
		for _, r := range fl.List() {
			if r.OrgID() == c.Org {
				runners = append(runners, r)
			}
		}
	}
	runnerName := func(id string) string {
		if id == "" {
			return "this computer"
		}
		for _, r := range runners {
			if r.ID == id {
				return r.Name
			}
		}
		return "a runner"
	}
	title := func(t model.Todo) string {
		if privacy {
			return "Task in " + byProject[t.ProjectID].Name
		}
		if strings.TrimSpace(t.Title) != "" {
			return t.Title
		}
		return wallLine(t.Prompt, 120)
	}

	y, m, dd := now.Date()
	midnight := time.Date(y, m, dd, 0, 0, 0, 0, now.Location())
	today := func(at *time.Time) bool { return at != nil && !at.Before(midnight) }
	d.Today.Midnight, d.Today.Hour, d.Today.PerHour = midnight, now.Hour(), make([]int, 24)

	var runningTotal time.Duration
	var spend float64
	firstTry := 0
	for _, t := range todos {
		p := byProject[t.ProjectID]
		if (today(t.StartedAt) || today(t.FinishedAt)) && t.Status != model.StatusDraft {
			spend += t.CostUSD // its whole cost: Dash doesn't keep cost per run
		}
		switch t.Status {
		case model.StatusQueued:
			d.Stages.Queued++
			switch {
			case t.Paused:
				d.Stages.Paused++
			case len(t.Blockers(todos)) > 0:
				d.Stages.Blocked++
			case t.Waiting != "":
				d.Stages.WaitingRunner++
			}
		case model.StatusInProgress:
			d.Stages.Running++
			run := wallRun{ID: t.ID, Title: title(t), Project: p.Name, Color: p.Color, Runner: runnerName(t.Runner), Run: t.Runs + 1,
				StartedAt: t.StartedAt, Add: t.DiffAdd, Del: t.DiffDel, Files: t.DiffFiles, SentBack: t.SentBack}
			if !privacy {
				cost := t.CostUSD
				run.CostUSD = &cost
				if s.Hub != nil {
					if ev, ok := s.Hub.Activity(t.ID); ok && (t.StartedAt == nil || !ev.At.Before(*t.StartedAt)) {
						run.Activity = wallLine(strings.TrimPrefix(agent.Summary(ev), "🔧 "), 200)
					}
				}
			}
			if t.StartedAt != nil {
				runningTotal += now.Sub(*t.StartedAt)
			}
			d.Running = append(d.Running, run)
		case model.StatusReview:
			d.Stages.Review++
			since := t.UpdatedAt
			if t.FinishedAt != nil {
				since = *t.FinishedAt
			}
			if d.Stages.ReviewOldest == nil || since.Before(*d.Stages.ReviewOldest) {
				x := since
				d.Stages.ReviewOldest = &x
			}
			n := wallNeed{Kind: "review", ID: t.ID, Title: title(t), Project: p.Name, Since: since, Old: now.Sub(since) > wallReviewOld,
				Add: t.DiffAdd, Del: t.DiffDel}
			if t.Checks != nil {
				n.Tests = map[bool]string{true: "passed", false: "failed"}[t.Checks.Passed]
			}
			if t.PR != nil {
				n.PR = t.PR.Number
			}
			d.Needs = append(d.Needs, n)
		case model.StatusMerging:
			d.Stages.Merging++
			if t.PR != nil {
				d.Stages.MergingPR++
			}
		case model.StatusMerged:
			if today(t.FinishedAt) {
				d.Stages.MergedToday++
				d.Stages.MergedAdd += t.DiffAdd
				d.Stages.MergedDel += t.DiffDel
				d.Today.PerHour[t.FinishedAt.In(now.Location()).Hour()]++
				if t.Runs == 1 {
					firstTry++
				}
			}
		case model.StatusFailed:
			d.Stages.Failed++
			if t.Resumable {
				d.Stages.Resumable++
			}
			since := t.UpdatedAt
			if t.FinishedAt != nil {
				since = *t.FinishedAt
			}
			d.Needs = append(d.Needs, wallNeed{Kind: "failed", ID: t.ID, Title: title(t), Project: p.Name, Since: since,
				Old: now.Sub(since) > wallFailedOld, Reason: wallLine(t.Error, 160), Resume: t.Resumable})
		}
	}
	if d.Stages.Running > 0 {
		d.Stages.RunningAvgS = int(runningTotal.Seconds()) / d.Stages.Running
	}
	sort.Slice(d.Running, func(i, j int) bool { return startOf(d.Running[i]).Before(startOf(d.Running[j])) })
	// Reviews first, oldest first; then failures, oldest first.
	sort.SliceStable(d.Needs, func(i, j int) bool {
		a, b := d.Needs[i], d.Needs[j]
		if a.Kind != b.Kind {
			return a.Kind == "review"
		}
		return a.Since.Before(b.Since)
	})
	if len(d.Needs) > wallMaxItems {
		d.Needs = d.Needs[:wallMaxItems]
	}
	d.Today.Merged = d.Stages.MergedToday
	if !privacy {
		d.Today.SpendUSD = &spend
	}
	if d.Today.Merged > 0 {
		f := float64(firstTry) / float64(d.Today.Merged)
		d.Today.FirstTry = &f
	}

	// Open issues
	var open []model.Issue
	for _, p := range projects {
		for _, is := range data.ListIssues(p.ID) {
			if is.Status == model.IssueOpen {
				open = append(open, is)
				d.Issues.Severity[string(is.Severity)]++
			}
		}
	}
	d.Issues.Open = len(open)
	if !privacy {
		rank := map[model.Severity]int{model.SevCritical: 0, model.SevHigh: 1, model.SevMedium: 2, model.SevLow: 3, model.SevInfo: 4}
		sort.SliceStable(open, func(i, j int) bool { return sevRank(rank, open[i].Severity) < sevRank(rank, open[j].Severity) })
		for i := 0; i < len(open) && i < 3; i++ {
			d.Issues.Top = append(d.Issues.Top, open[i].Title)
		}
	}

	s.wallRunners(&d, todos, projects, runners)
	s.wallQueue(&d, c, runners)
	d.Events = wallEvents(todos, byProject, runners, title, runnerName, now)
	return d, nil
}

func sevRank(rank map[model.Severity]int, s model.Severity) int {
	if r, ok := rank[s]; ok {
		return r
	}
	return len(rank)
}

func startOf(r wallRun) time.Time {
	if r.StartedAt == nil {
		return time.Time{}
	}
	return *r.StartedAt
}

// wallRunners fills in the runners (or this computer) and how busy they are.
func (s *Server) wallRunners(d *wallData, todos []model.Todo, projects []model.Project, runners []tunnel.Runner) {
	if !s.hub() {
		slots := 1
		if s.Todos != nil {
			slots = s.Todos.MaxParallel
		}
		d.Runners = append(d.Runners, wallRunner{Name: "this computer", Online: true, Busy: d.Stages.Running, Slots: slots, Projects: len(projects)})
		d.Queue.Slots, d.Queue.Busy = slots, d.Stages.Running
		return
	}
	for _, r := range runners {
		wr := wallRunner{Name: r.Name, Online: r.Online, Paused: r.Paused, Slots: r.Limit()}
		if !r.LastSeen.IsZero() {
			ls := r.LastSeen
			wr.LastSeen = &ls
		}
		linked := map[string]bool{}
		for _, p := range projects {
			if slices.Contains(p.Runners, r.ID) {
				wr.Projects++
				linked[p.ID] = true
			}
		}
		for _, t := range todos {
			switch {
			case t.Status == model.StatusInProgress && t.Runner == r.ID:
				wr.Busy++
			case t.Status == model.StatusQueued && t.Waiting != "" && linked[t.ProjectID]:
				wr.Waiting++
			}
		}
		if r.Online && !r.Paused {
			d.Queue.Slots += wr.Slots
		}
		d.Runners = append(d.Runners, wr)
	}
	d.Queue.Busy = d.Stages.Running
	sort.SliceStable(d.Runners, func(i, j int) bool {
		if d.Runners[i].Online != d.Runners[j].Online {
			return d.Runners[i].Online
		}
		return d.Runners[i].Name < d.Runners[j].Name
	})
}

// wallQueue fills in whether the queue runs and the Claude usage it pauses on.
func (s *Server) wallQueue(d *wallData, c Caller, runners []tunnel.Runner) {
	var snap usage.Snapshot
	if s.Usage != nil {
		snap = s.Usage.Snapshot()
	}
	if s.hub() {
		// The hub runs no Claude: the usage is a runner's, as it last
		// reported it (the caller's own runner's when there is a caller).
		var u *usage.Snapshot
		if c.User != 0 {
			u = s.runnerUsage(c)
		}
		if u == nil {
			u = bestUsage(runners)
		}
		if u != nil {
			ru := *u
			ru.ManualPause = snap.ManualPause
			if snap.ManualPause {
				ru.Paused, ru.Reason = true, snap.Reason
			}
			snap = ru
		}
	}
	d.Usage = wallUsage{Known: snap.Known, PauseAt: snap.PauseAt, Windows: snap.Windows}
	if d.Usage.Windows == nil {
		d.Usage.Windows = []usage.Window{}
	}
	d.Queue.Paused, d.Queue.Reason = snap.Paused, snap.Reason
	if snap.Paused && snap.Reason == "" {
		d.Queue.Reason = "paused"
	}
	if s.Todos != nil && s.Todos.Hold != nil {
		if why := s.Todos.Hold(c.Org); why != "" {
			d.Queue.Paused, d.Queue.Reason = true, why
		}
	}
}

// bestUsage is the newest usage report of the runners, online ones first.
func bestUsage(runners []tunnel.Runner) *usage.Snapshot {
	var best *tunnel.Runner
	for i, r := range runners {
		if r.Reported == nil || r.Reported.Usage == nil || !r.Reported.Usage.Known {
			continue
		}
		if best == nil || (r.Online && !best.Online) || (r.Online == best.Online && r.Reported.At.After(best.Reported.At)) {
			best = &runners[i]
		}
	}
	if best == nil {
		return nil
	}
	return best.Reported.Usage
}

// wallEvents is the ticker: what happened lately, newest first, as told by
// the tasks' own timestamps (Dash keeps no event history).
func wallEvents(todos []model.Todo, byProject map[string]model.Project, runners []tunnel.Runner,
	title func(model.Todo) string, runnerName func(string) string, now time.Time) []wallEvent {
	since := now.Add(-24 * time.Hour)
	out := []wallEvent{}
	add := func(at *time.Time, kind, text string) {
		if at != nil && at.After(since) && !at.After(now.Add(time.Minute)) {
			out = append(out, wallEvent{At: *at, Kind: kind, Text: text})
		}
	}
	for _, t := range todos {
		name := "“" + title(t) + "” · " + byProject[t.ProjectID].Name
		switch t.Status {
		case model.StatusMerged:
			add(t.FinishedAt, "merged", "merged "+name)
		case model.StatusReview:
			add(t.FinishedAt, "review", "review ready "+name)
		case model.StatusFailed:
			add(t.FinishedAt, "failed", "failed "+name)
		case model.StatusInProgress:
			if t.SentBack != "" {
				at := t.UpdatedAt
				if t.StartedAt != nil {
					at = *t.StartedAt
				}
				add(&at, "sent_back", "sent back "+name+": "+wallLine(t.SentBack, 80))
			} else {
				add(t.StartedAt, "started", "started "+name+" on "+runnerName(t.Runner))
			}
		}
		if t.CIRun != 0 {
			at := t.CreatedAt
			add(&at, "ci", fmt.Sprintf("CI failed on %s · run #%d → task queued", byProject[t.ProjectID].Name, t.CIRun))
		}
	}
	for _, r := range runners {
		if !r.Online && !r.LastSeen.IsZero() {
			at := r.LastSeen
			add(&at, "offline", r.Name+" went offline")
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > wallMaxEvents {
		out = out[:wallMaxEvents]
	}
	return out
}

// wallLine is s's first non-empty line, cut to max runes.
func wallLine(s string, max int) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if r := []rune(l); len(r) > max {
				return string(r[:max-1]) + "…"
			}
			return l
		}
	}
	return ""
}

// ---- wall tokens: an org admin's links for TVs ----

// wallAdmin is r's caller if they are an admin of their org, signed in
// with a session; otherwise it answers r.
func (s *Server) wallAdmin(w http.ResponseWriter, r *http.Request) (Caller, bool) {
	c, err := s.caller(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return c, false
	}
	if c.Machine || !s.orgAdmin(c) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the organization's admins manage wall links"})
		return c, false
	}
	return c, true
}

func (s *Server) wallTokenRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/wall/tokens", auth.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.wallAdmin(w, r)
		if !ok {
			return
		}
		ts, err := s.Store.Accounts().WallTokens(c.Org)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ts)
	})))
	mux.Handle("POST /api/wall/tokens", auth.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.wallAdmin(w, r)
		if !ok {
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			req.Name = "Wall"
		}
		secret, t, err := s.Store.Accounts().CreateWallToken(c.Org, c.User, req.Name, time.Now())
		if errors.Is(err, store.ErrBadTokenName) {
			err = errors.Join(errBadRequest, err)
		}
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"token": t, "secret": secret, "path": "/wall?token=" + secret})
	})))
	mux.Handle("DELETE /api/wall/tokens/{id}", auth.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.wallAdmin(w, r)
		if !ok {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeErr(w, errors.Join(errBadRequest, err))
			return
		}
		if err := s.Store.Accounts().RevokeWallToken(c.Org, id); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}
