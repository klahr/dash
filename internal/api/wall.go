package api

// The wall view: a read-only, full-screen dashboard for a TV in the office
// (GET /wall, web/static/wall.html), fed by one JSON endpoint (GET
// /api/wall) that sums up an org's work across its projects. A signed-in
// session sees its active org; a TV opens /wall?token=<wall token> instead,
// a read-only token an org admin makes (/api/wall/tokens) that reaches
// these paths and nothing else (see auth.WallPaths). The running cards show
// their project's icon from GET /api/wall/icon.
//
// Public mode (?privacy=1, and always with a public wall token) shows only
// what anyone may see, for a screen visitors walk past or a link shared
// outside the org: counts, timers, line counts and usage, but no task
// titles or prompts, project or runner names, failure or sent-back
// reasons, activity lines, issue titles or costs.

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/textutil"
	"github.com/klahr/dash/internal/tunnel"
	"github.com/klahr/dash/internal/usage"
)

// When an item that needs a human gets a red border.
const (
	wallReviewOld = time.Hour
	wallFailedOld = 30 * time.Minute
	wallMaxItems  = 20 // per list: a TV shows fewer anyway
)

type wallData struct {
	Now     time.Time    `json:"now"`
	Org     string       `json:"org"`
	Project string       `json:"project,omitempty"` // the ?project= filter's name
	Privacy bool         `json:"privacy"`           // public mode
	Locked  bool         `json:"locked,omitempty"`  // public mode by a public wall token: can't be turned off
	Queue   wallQueue    `json:"queue"`
	Usage   wallUsage    `json:"usage"`
	Stages  wallStage    `json:"stages"`
	Running []wallRun    `json:"running"`
	Needs   []wallNeed   `json:"needs"`
	Issues  wallIssues   `json:"issues"`
	Runners []wallRunner `json:"runners"`
	Today   wallToday    `json:"today"`
}

type wallQueue struct {
	Paused bool   `json:"paused"`
	Reason string `json:"reason,omitempty"`
	Busy   int    `json:"busy"`            // tasks running
	Slots  int    `json:"slots"`           // tasks that can run at once, on the runners online
	Boost  int    `json:"boost,omitempty"` // of Slots, the extra ones while catching up with the weekly pace
}

type wallUsage struct {
	Known   bool           `json:"known"`
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
	ReviewGitHub  int        `json:"review_github"` // of Review: pull requests waiting on GitHub, Claude or checks, not a person
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
	Icon      string     `json:"icon,omitempty"` // the project's icon (GET /api/wall/icon); empty in privacy mode
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
	Waits   string    `json:"waits,omitempty"` // what its pull request waits for a person to do: model.PRWaitReview…
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
	Boost    int        `json:"boost,omitempty"` // of Slots, the extra ones while catching up with the weekly pace
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

// wallCaller is who may see r's wall: a wall token's org (?token=), else
// the session's. public is true for a public wall token: its wall is
// always in public mode.
func (s *Server) wallCaller(r *http.Request) (c Caller, public bool, err error) {
	if tok := r.URL.Query().Get("token"); tok != "" && auth.WallPaths[r.URL.Path] {
		wt, err := s.Store.Accounts().AuthenticateWall(tok, time.Now())
		if err != nil {
			return Caller{}, false, err
		}
		return Caller{Org: wt.OrgID}, wt.Public, nil
	}
	c, err = s.caller(r)
	return c, false, err
}

// wallPage serves GET /wall.
func (s *Server) wallPage(w http.ResponseWriter, r *http.Request) {
	if _, _, err := s.wallCaller(r); err != nil {
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
	c, public, err := s.wallCaller(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	q := r.URL.Query()
	loc := time.Local
	if v := q.Get("tz"); v != "" { // the TV's getTimezoneOffset(): "today" is its day
		if m, err := strconv.Atoi(v); err == nil && m >= -16*60 && m <= 16*60 {
			loc = time.FixedZone("", -m*60)
		}
	}
	d, err := s.wall(c, q.Get("project"), public || q.Get("privacy") == "1", time.Now().In(loc))
	d.Locked = public
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, d)
}

// wallIconURL is where the wall gets p's icon, or "" if it has none. The
// upload's id changes with the icon, so the URL does too.
func wallIconURL(p model.Project) string {
	if p.Icon == "" {
		return ""
	}
	return "/api/wall/icon?" + url.Values{"project": {p.ID}, "v": {p.Icon}}.Encode()
}

// wallIcon serves GET /api/wall/icon?project=<id>: the project's icon image,
// for a wall's running cards. Not in public mode, where a project's icon
// would tell which one it is.
func (s *Server) wallIcon(w http.ResponseWriter, r *http.Request) {
	c, public, err := s.wallCaller(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if public {
		writeErr(w, store.ErrNotFound)
		return
	}
	p, err := s.Store.ForOrg(c.Org).GetProject(r.URL.Query().Get("project"))
	if err != nil {
		writeErr(w, err)
		return
	}
	id, name, _ := strings.Cut(p.Icon, "/")
	a, err := s.as(c).findUpload(id)
	if err != nil || a.Name != name || !strings.HasPrefix(a.Type, "image/") {
		writeErr(w, store.ErrNotFound)
		return
	}
	serveUpload(w, r, a)
}

// wall sums up c's org's work as of now; privacy is public mode.
func (s *Server) wall(c Caller, project string, privacy bool, now time.Time) (wallData, error) {
	data := s.Store.ForOrg(c.Org)
	d := wallData{Now: now, Privacy: privacy, Running: []wallRun{}, Needs: []wallNeed{}, Runners: []wallRunner{},
		Issues: wallIssues{Severity: map[string]int{}, Top: []string{}}}
	if o, err := s.Store.Accounts().Org(c.Org); err == nil {
		d.Org = o.Name
	}
	projects, byProject, err := wallProjects(data.ListProjects(), project, privacy)
	if err != nil {
		return d, err
	}
	// Only this org's (or this project's) todos are copied: the store
	// filters before it copies and sorts.
	scope := ""
	if project != "" {
		scope = projects[0].ID
		d.Project = byProject[scope].Name
	}
	todos := data.ListTodos(scope)
	runners := s.orgRunners(c)
	v := newWallTally(s, now, privacy, byProject, model.TodosByID(todos), runners)
	v.startDay(&d)
	for _, t := range todos {
		v.card(&d, t)
	}
	v.finish(&d)
	wallOpenIssues(&d, data, byProject, privacy)
	s.wallRunners(&d, todos, projects, runners, v.runnerName)
	s.wallQueue(&d, c, runners)
	return d, nil
}

// wallProjects are the projects the wall shows (all, or the one asked
// for: its id, or its name outside public mode), and them by id with the
// names the wall shows: in public mode a project is "Project A",
// "Project B"…: its letter by when it was made, so it keeps it as
// projects come and go.
func wallProjects(projects []model.Project, project string, privacy bool) ([]model.Project, map[string]model.Project, error) {
	alias := wallAliases(projects, func(p model.Project) (string, time.Time) { return p.ID, p.CreatedAt }, "Project ")
	if project != "" {
		i := slices.IndexFunc(projects, func(p model.Project) bool {
			// Public mode matches no names: they mustn't be guessable.
			return p.ID == project || !privacy && strings.EqualFold(p.Name, project)
		})
		if i < 0 {
			return nil, nil, fmt.Errorf("%w: no project %q", store.ErrNotFound, project)
		}
		projects = projects[i : i+1]
	}
	byProject := map[string]model.Project{}
	for _, p := range projects {
		if privacy {
			p.Name = alias[p.ID]
		}
		byProject[p.ID] = p
	}
	return projects, byProject, nil
}

// orgRunners are the runners in c's org (none: this computer runs tasks
// itself).
func (s *Server) orgRunners(c Caller) []tunnel.Runner {
	var runners []tunnel.Runner
	if fl := s.fleet(); fl != nil {
		for _, r := range fl.List() {
			if r.OrgID() == c.Org {
				runners = append(runners, r)
			}
		}
	}
	return runners
}

// wallTally sums up tasks for the wall, one card at a time, with the names
// and words public mode allows.
type wallTally struct {
	s           *Server
	now         time.Time
	midnight    time.Time
	privacy     bool
	byProject   map[string]model.Project
	byID        map[string]model.Todo // once, for every queued task's blockers
	runners     []tunnel.Runner
	runnerAlias map[string]string

	runningTotal time.Duration
	spend        float64
	firstTry     int
}

func newWallTally(s *Server, now time.Time, privacy bool, byProject map[string]model.Project, byID map[string]model.Todo, runners []tunnel.Runner) *wallTally {
	y, m, dd := now.Date()
	return &wallTally{s: s, now: now, midnight: time.Date(y, m, dd, 0, 0, 0, 0, now.Location()), privacy: privacy,
		byProject: byProject, byID: byID, runners: runners,
		runnerAlias: wallAliases(runners, func(r tunnel.Runner) (string, time.Time) { return r.ID, r.Created }, "Runner ")}
}

// startDay sets up d's day: since midnight, by hour.
func (v *wallTally) startDay(d *wallData) {
	d.Today.Midnight, d.Today.Hour, d.Today.PerHour = v.midnight, v.now.Hour(), make([]int, 24)
}

// today reports whether at is today.
func (v *wallTally) today(at *time.Time) bool { return at != nil && !at.Before(v.midnight) }

// runnerName is runner id's name on the wall ("": this computer).
func (v *wallTally) runnerName(id string) string {
	if id == "" {
		return "this computer"
	}
	for _, r := range v.runners {
		if r.ID == id {
			if v.privacy {
				return v.runnerAlias[id]
			}
			return r.Name
		}
	}
	return "a runner"
}

// title is t's title on the wall.
func (v *wallTally) title(t model.Todo) string {
	if v.privacy {
		return "Task in " + v.byProject[t.ProjectID].Name
	}
	return t.Name(120)
}

// sentBack is why a task was sent back; in public mode only its kind
// (the reason names files and branches).
func (v *wallTally) sentBack(why string) string {
	if !v.privacy || why == "" {
		return why
	}
	switch {
	case strings.HasPrefix(why, "conflicts"):
		return "merge conflict"
	case strings.Contains(why, "tests fail"):
		return "failing tests"
	}
	return "sent back"
}

// reason is why a task failed: none in public mode.
func (v *wallTally) reason(err string) string {
	if v.privacy {
		return "" // an error quotes commands, paths and output
	}
	return textutil.OneLine(textutil.FirstLine(err), 160)
}

// card counts t into d: its stage, and the running task or the need for a
// person it is.
func (v *wallTally) card(d *wallData, t model.Todo) {
	if (v.today(t.StartedAt) || v.today(t.FinishedAt)) && t.Status != model.StatusDraft {
		v.spend += todaysCost(t, v.today)
	}
	switch t.Status {
	case model.StatusQueued:
		d.Stages.Queued++
		switch {
		case t.Paused:
			d.Stages.Paused++
		case len(t.BlockersIn(v.byID)) > 0:
			d.Stages.Blocked++
		case t.Waiting != "":
			d.Stages.WaitingRunner++
		}
	case model.StatusInProgress:
		d.Stages.Running++
		d.Running = append(d.Running, v.running(t))
	case model.StatusReview:
		v.review(d, t)
	case model.StatusMerging:
		d.Stages.Merging++
		if t.PR != nil {
			d.Stages.MergingPR++
		}
	case model.StatusMerged:
		if v.today(t.FinishedAt) {
			d.Stages.MergedToday++
			d.Stages.MergedAdd += t.DiffAdd
			d.Stages.MergedDel += t.DiffDel
			d.Today.PerHour[t.FinishedAt.In(v.now.Location()).Hour()]++
			if t.Runs == 1 {
				v.firstTry++
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
		p := v.byProject[t.ProjectID]
		d.Needs = append(d.Needs, wallNeed{Kind: "failed", ID: t.ID, Title: v.title(t), Project: p.Name, Since: since,
			Old: v.now.Sub(since) > wallFailedOld, Reason: v.reason(t.Error), Resume: t.Resumable})
	}
}

// running is in-progress task t on the wall.
func (v *wallTally) running(t model.Todo) wallRun {
	p := v.byProject[t.ProjectID]
	run := wallRun{ID: t.ID, Title: v.title(t), Project: p.Name, Color: p.Color, Runner: v.runnerName(t.Runner), Run: t.Runs + 1,
		StartedAt: t.StartedAt, Add: t.DiffAdd, Del: t.DiffDel, Files: t.DiffFiles, SentBack: v.sentBack(t.SentBack)}
	if !v.privacy {
		run.Icon = wallIconURL(p)
		cost := t.CostUSD
		run.CostUSD = &cost
		if v.s.Hub != nil {
			if ev, ok := v.s.Hub.Activity(t.ID); ok && (t.StartedAt == nil || !ev.At.Before(*t.StartedAt)) {
				run.Activity = textutil.OneLine(textutil.FirstLine(strings.TrimPrefix(agent.Summary(ev), "🔧 ")), 200)
			}
		}
	}
	if t.StartedAt != nil {
		v.runningTotal += v.now.Sub(*t.StartedAt)
	}
	return run
}

// review counts task t, in review, into d: a need for a reviewer.
func (v *wallTally) review(d *wallData, t model.Todo) {
	d.Stages.Review++
	since := t.UpdatedAt
	if t.FinishedAt != nil {
		since = *t.FinishedAt
	}
	if t.WaitsOnGitHub() {
		// Reviewed and merged on GitHub: listed only while a person
		// has to act there, not while it is a draft, back with
		// Claude, or waiting for its checks.
		if t.PR.Waits == "" {
			d.Stages.ReviewGitHub++
			return
		}
		if w := t.PR.WaitsSince; w != nil && w.After(since) {
			since = *w
		}
	}
	if d.Stages.ReviewOldest == nil || since.Before(*d.Stages.ReviewOldest) {
		x := since
		d.Stages.ReviewOldest = &x
	}
	n := wallNeed{Kind: "review", ID: t.ID, Title: v.title(t), Project: v.byProject[t.ProjectID].Name, Since: since,
		Old: v.now.Sub(since) > wallReviewOld, Add: t.DiffAdd, Del: t.DiffDel}
	if t.Checks != nil {
		n.Tests = map[bool]string{true: "passed", false: "failed"}[t.Checks.Passed]
	}
	if t.PR != nil {
		n.PR = t.PR.Number
		if t.WaitsOnGitHub() {
			n.Waits = t.PR.Waits
		}
	}
	d.Needs = append(d.Needs, n)
}

// finish sorts d's lists and sums up the day once every card is counted.
func (v *wallTally) finish(d *wallData) {
	if d.Stages.Running > 0 {
		d.Stages.RunningAvgS = int(v.runningTotal.Seconds()) / d.Stages.Running
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
	if !v.privacy {
		spend := v.spend
		d.Today.SpendUSD = &spend
	}
	if d.Today.Merged > 0 {
		f := float64(v.firstTry) / float64(d.Today.Merged)
		d.Today.FirstTry = &f
	}
}

// wallOpenIssues fills in the open issues: tasks made from reported issues
// that aren't merged, one pass over the store for the whole org, most
// severe first.
func wallOpenIssues(d *wallData, data *store.OrgStore, byProject map[string]model.Project, privacy bool) {
	open := data.OpenFindings(func(id string) bool { _, ok := byProject[id]; return ok })
	for _, t := range open {
		d.Issues.Severity[string(t.Finding.Severity())]++
	}
	d.Issues.Open = len(open)
	if !privacy {
		for i := 0; i < len(open) && i < 3; i++ {
			d.Issues.Top = append(d.Issues.Top, open[i].Title)
		}
	}
}

func startOf(r wallRun) time.Time {
	if r.StartedAt == nil {
		return time.Time{}
	}
	return *r.StartedAt
}

// wallRunners fills in the runners (or this computer) and how busy they are.
func (s *Server) wallRunners(d *wallData, todos []model.Todo, projects []model.Project, runners []tunnel.Runner, name func(string) string) {
	if !s.hub() {
		slots, usual := 1, 1
		if s.Todos != nil {
			slots, usual = s.Todos.EffectiveParallel(), s.Todos.MaxParallel
		}
		busy := d.Stages.Running
		wr := wallRunner{Name: "this computer", Online: true, Busy: busy, Slots: max(slots, busy), Boost: max(slots-usual, 0), Projects: len(projects)}
		d.Runners = append(d.Runners, wr)
		d.Queue.Slots, d.Queue.Busy, d.Queue.Boost = wr.Slots, busy, wr.Boost
		return
	}
	for _, r := range runners {
		wr := wallRunner{Name: name(r.ID), Online: r.Online, Paused: r.Paused, Slots: r.SlotsNow()}
		wr.Boost = wr.Slots - r.Limit()
		if !r.LastSeen.IsZero() {
			ls := r.LastSeen
			wr.LastSeen = &ls
		}
		linked := map[string]bool{}
		for _, p := range projects {
			if r.Serves(p) {
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
		// A task that started under a boost that has since ended still
		// runs: never show more tasks busy than slots.
		wr.Slots = max(wr.Slots, wr.Busy)
		if r.Online && !r.Paused {
			d.Queue.Slots += wr.Slots
			d.Queue.Boost += wr.Boost
		}
		d.Runners = append(d.Runners, wr)
	}
	d.Queue.Busy = d.Stages.Running
	d.Queue.Slots = max(d.Queue.Slots, d.Queue.Busy)
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
			// The runner's verdict may be stale: its windows may have reset since.
			snap = ru.Reevaluate(time.Now())
		}
	}
	d.Usage = wallUsage{Known: snap.Known, Windows: snap.Windows}
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

// wallAliases names xs for public mode: prefix plus a letter (A…Z, then
// AA…), in the order they were made (then by id).
func wallAliases[T any](xs []T, key func(T) (string, time.Time), prefix string) map[string]string {
	type k struct {
		id string
		at time.Time
	}
	ks := make([]k, 0, len(xs))
	for _, x := range xs {
		id, at := key(x)
		ks = append(ks, k{id, at})
	}
	sort.Slice(ks, func(i, j int) bool {
		if !ks[i].at.Equal(ks[j].at) {
			return ks[i].at.Before(ks[j].at)
		}
		return ks[i].id < ks[j].id
	})
	out := make(map[string]string, len(ks))
	for i, x := range ks {
		out[x.id] = prefix + wallLetters(i)
	}
	return out
}

// wallLetters is A for 0, B for 1, …, Z, AA, AB…
func wallLetters(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

// ---- wall tokens: an org admin's links for TVs ----

// wallAdmin is r's caller if they are an admin of their org, signed in
// with a session; otherwise it answers r.
func (s *Server) wallAdmin(w http.ResponseWriter, r *http.Request) (Caller, bool) {
	c, ok := s.callerOr(w, r)
	if !ok {
		return c, false
	}
	if c.Machine || !s.orgAdmin(c) {
		writeError(w, http.StatusForbidden, "only the organization's admins manage wall links")
		return c, false
	}
	return c, true
}

func (s *Server) wallTokenRoutes(mux routeMux) {
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
			Name   string `json:"name"`
			Public bool   `json:"public"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			req.Name = "Wall"
			if req.Public {
				req.Name = "Public wall"
			}
		}
		secret, t, err := s.Store.Accounts().CreateWallToken(c.Org, c.User, req.Name, req.Public, time.Now())
		if errors.Is(err, store.ErrBadTokenName) {
			err = fmt.Errorf("%w: %w", errBadRequest, err)
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
			writeErr(w, fmt.Errorf("%w: %w", errBadRequest, err))
			return
		}
		if err := s.Store.Accounts().RevokeWallToken(c.Org, id); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}

// todaysCost is what a task's runs that started today cost. A task from
// before Dash kept a record of each run counts its whole cost.
func todaysCost(t model.Todo, today func(*time.Time) bool) float64 {
	if len(t.RunStats) == 0 {
		return t.CostUSD
	}
	var c float64
	for _, r := range t.RunStats {
		if today(&r.At) {
			c += r.CostUSD
		}
	}
	return c
}
