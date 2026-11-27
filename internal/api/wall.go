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
// reasons, activity lines, issue titles, people's names and avatars, or
// costs. ?nospend=1 leaves out the costs on a signed-in wall too.

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

// The wall's thresholds: when an item that needs a human gets a red
// border, and what turns the overall status (wallStatusOf) amber or red.
const (
	wallReviewOld = time.Hour
	wallFailedOld = 30 * time.Minute
	wallMaxItems  = 20 // per list: a TV shows fewer anyway
	wallLandedMax = 5  // the "Just landed" list
	wallSamples   = 10 // run times per kind of run, for the running cards' typical time (eta.js SAMPLES)
	// A usage window this full turns the status amber; so does one that,
	// at the rate it has been used, reaches 100% before it resets.
	wallUsageHigh = 0.9
	// An offline runner turns the status red while tasks wait that only it
	// could take, or while it was last seen this recently; one gone longer
	// is taken as retired.
	wallRunnerGone = 24 * time.Hour
	wallAvgDays    = 7 // the days the "avg" figures in Today average over
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
	// HideSpend: ?nospend=1, no costs anywhere (they are left out, not hidden).
	HideSpend bool         `json:"hide_spend,omitempty"`
	Status    wallStatus   `json:"status"`
	Landed    []wallLanded `json:"landed"` // the newest merges, newest first
	Week      wallWeek     `json:"week"`
}

// wallStatus is the wall's one overall state, from the rest of its data
// (wallStatusOf).
type wallStatus struct {
	Level   string   `json:"level"`   // ok | warn | bad
	Label   string   `json:"label"`   // "All good", "Waiting on people", "Needs action"
	Reasons []string `json:"reasons"` // why it isn't ok, the red ones first
	Running int      `json:"running"`
	Waiting int      `json:"waiting"` // tasks waiting on a person: reviews and failures
}

// wallLanded is a merged task in "Just landed".
type wallLanded struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Project string    `json:"project"`
	At      time.Time `json:"at"`
	Add     int       `json:"add,omitempty"`
	Del     int       `json:"del,omitempty"`
}

// wallWeek sums up the week so far (since Monday 00:00), for the off-hours
// summary.
type wallWeek struct {
	Since    time.Time `json:"since"`
	Merged   int       `json:"merged"`
	FirstTry *float64  `json:"first_try,omitempty"` // 0..1
}

// wallWindow is a Claude usage window and where it is heading.
type wallWindow struct {
	usage.Window
	// FullAt is when it would reach 100% at the rate it has been used so far
	// in the window (nil: unused, or too early in the window to tell).
	FullAt *time.Time `json:"full_at,omitempty"`
	// RunsOut: FullAt is before it resets, so the queue would pause.
	RunsOut bool `json:"runs_out,omitempty"`
}

type wallQueue struct {
	Paused bool   `json:"paused"`
	Reason string `json:"reason,omitempty"`
	Busy   int    `json:"busy"`            // tasks running
	Slots  int    `json:"slots"`           // tasks that can run at once, on the runners online
	Boost  int    `json:"boost,omitempty"` // of Slots, the extra ones while catching up with the weekly pace
}

type wallUsage struct {
	Known   bool         `json:"known"`
	Windows []wallWindow `json:"windows"`
	// Accounts are the meters of each Claude account the runners online are
	// on (on a hub; none elsewhere). Known and Windows are one of them, for
	// an older wall page.
	Accounts []wallAccount `json:"accounts"`
}

// wallAccount is one Claude account's usage on the wall.
type wallAccount struct {
	// Label names it: its email for a signed-in wall outside public mode,
	// else its runners' names (as the wall shows them), never an email.
	Label   string       `json:"label"`
	Windows []wallWindow `json:"windows"`
	Paused  bool         `json:"paused,omitempty"`
	Reason  string       `json:"reason,omitempty"`
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
	CostUSD   *float64   `json:"cost_usd,omitempty"` // nil in privacy mode and with nospend
	StartedAt *time.Time `json:"started_at,omitempty"`
	Add       int        `json:"add"`
	Del       int        `json:"del"`
	Files     int        `json:"files"`
	Activity  string     `json:"activity,omitempty"`
	// ActivityAt is when the activity line last changed.
	ActivityAt *time.Time `json:"activity_at,omitempty"`
	SentBack   string     `json:"sent_back,omitempty"`
	// Took is how long comparable runs took, in seconds, newest first: the
	// project's other first runs for a task's first run, its later runs
	// (rework, retries) for a later one. The wall takes their median
	// (eta.js typicalOf) as the typical time.
	Took []int `json:"took,omitempty"`
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
	// Person is who should act: a review's author (who created the task), a
	// failure's who started its latest run (else its creator). Avatar is
	// their picture (GET /api/wall/avatar), if they have one. Both are empty
	// in public mode.
	Person string `json:"person,omitempty"`
	Avatar string `json:"avatar,omitempty"`
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
	SpendUSD *float64  `json:"spend_usd,omitempty"` // nil in privacy mode and with nospend
	FirstTry *float64  `json:"first_try,omitempty"` // merged today after a single run, 0..1
	PerHour  []int     `json:"per_hour"`            // merges in each hour of today, 0..23
	Hour     int       `json:"hour"`                // the current hour
	Midnight time.Time `json:"midnight"`
	// Avg is the same figures on an average day of the last week, up to
	// the same time of day.
	Avg wallAvg `json:"avg"`
}

// wallAvg is an average day of the last wallAvgDays, up to this time of day.
type wallAvg struct {
	Merged   float64  `json:"merged"`
	SpendUSD *float64 `json:"spend_usd,omitempty"` // nil in privacy mode and with nospend
	FirstTry *float64 `json:"first_try,omitempty"` // nil with no merges
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

// getWall serves GET /api/wall[?project=<id or name>][&privacy=1][&nospend=1][&tz=<minutes>].
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
	d, err := s.wall(c, q.Get("project"), wallOpts{privacy: public || q.Get("privacy") == "1", noSpend: q.Get("nospend") == "1"}, time.Now().In(loc))
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

// wallAvatar serves GET /api/wall/avatar?user=<id>: a member's picture, for
// the "Needs a human" rows. Not in public mode, where names aren't shown.
func (s *Server) wallAvatar(w http.ResponseWriter, r *http.Request) {
	c, public, err := s.wallCaller(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	uid, err := strconv.ParseInt(r.URL.Query().Get("user"), 10, 64)
	if err != nil || public {
		writeErr(w, store.ErrNotFound)
		return
	}
	acc := s.Store.Accounts()
	if _, err := acc.Membership(uid, c.Org); err != nil {
		writeErr(w, store.ErrNotFound)
		return
	}
	data, ct, v, err := acc.Avatar(uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if r.URL.Query().Get("v") == strconv.FormatInt(v, 10) {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "private, no-cache")
	}
	_, _ = w.Write(data)
}

// wallOpts are a wall's options: privacy is public mode, noSpend leaves
// out every cost.
type wallOpts struct{ privacy, noSpend bool }

// wall sums up c's org's work as of now.
func (s *Server) wall(c Caller, project string, o wallOpts, now time.Time) (wallData, error) {
	privacy := o.privacy
	data := s.Store.ForOrg(c.Org)
	d := wallData{Now: now, Privacy: privacy, HideSpend: o.noSpend, Running: []wallRun{}, Needs: []wallNeed{}, Runners: []wallRunner{},
		Issues: wallIssues{Severity: map[string]int{}, Top: []string{}}, Landed: []wallLanded{}}
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
	v.noSpend = o.noSpend
	v.took = wallRunTimes(todos)
	if !privacy {
		if ms, err := s.Store.Accounts().Members(c.Org); err == nil {
			v.people = make(map[int64]store.Member, len(ms))
			for _, m := range ms {
				v.people[m.ID] = m
			}
		}
	}
	v.startDay(&d)
	for _, t := range todos {
		v.card(&d, t)
		v.history(t)
	}
	v.finish(&d)
	wallOpenIssues(&d, data, byProject, privacy)
	s.wallRunners(&d, todos, projects, runners, v.runnerName)
	s.wallQueue(&d, c, runners, func(r tunnel.Runner) string { return v.runnerName(r.ID) })
	d.Status = wallStatusOf(&d, now)
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

	noSpend      bool
	people       map[int64]store.Member // the org's members, by id; nil in public mode
	took         map[wallRunKind][]int  // wallRunTimes
	runningTotal time.Duration
	spend        float64
	firstTry     int

	// The last wallAvgDays, each up to this time of day, summed, and the
	// week so far.
	days                           [wallAvgDays][2]time.Time // each day's start and cut-off
	weekStart                      time.Time
	avgMerged, avgFirst, weekFirst int
	weekMerged                     int
	avgSpend                       float64
	landed                         []wallLanded
}

func newWallTally(s *Server, now time.Time, privacy bool, byProject map[string]model.Project, byID map[string]model.Todo, runners []tunnel.Runner) *wallTally {
	y, m, dd := now.Date()
	v := &wallTally{s: s, now: now, midnight: time.Date(y, m, dd, 0, 0, 0, 0, now.Location()), privacy: privacy,
		byProject: byProject, byID: byID, runners: runners,
		runnerAlias: wallAliases(runners, func(r tunnel.Runner) (string, time.Time) { return r.ID, r.Created }, "Runner ")}
	sinceMidnight := now.Sub(v.midnight)
	for i := range v.days {
		start := v.midnight.AddDate(0, 0, -(i + 1))
		v.days[i] = [2]time.Time{start, start.Add(sinceMidnight)}
	}
	v.weekStart = v.midnight.AddDate(0, 0, -((int(now.Weekday()) + 6) % 7)) // Monday
	return v
}

// pastDay reports whether at falls in one of the last days, up to the
// same time of day as now.
func (v *wallTally) pastDay(at time.Time) bool {
	for _, d := range v.days {
		if !at.Before(d[0]) && at.Before(d[1]) {
			return true
		}
	}
	return false
}

// person is a member's name and avatar URL on the wall: none in public
// mode, or for someone who isn't in the org (Dash itself, a deleted
// account).
func (v *wallTally) person(uid int64) (string, string) {
	m, ok := v.people[uid]
	if !ok || uid == 0 {
		return "", ""
	}
	avatar := ""
	if m.Avatar != 0 {
		avatar = "/api/wall/avatar?" + url.Values{"user": {strconv.FormatInt(uid, 10)}, "v": {strconv.FormatInt(m.Avatar, 10)}}.Encode()
	}
	return m.Username, avatar
}

// history counts t into the past days' figures, the week's and the newest
// merges.
func (v *wallTally) history(t model.Todo) {
	if t.Status != model.StatusDraft {
		for _, r := range t.RunStats {
			if v.pastDay(r.At) {
				v.avgSpend += r.CostUSD
			}
		}
	}
	if t.Status != model.StatusMerged || t.FinishedAt == nil {
		return
	}
	at := *t.FinishedAt
	if v.pastDay(at) {
		v.avgMerged++
		if t.Runs == 1 {
			v.avgFirst++
		}
	}
	if !at.Before(v.weekStart) {
		v.weekMerged++
		if t.Runs == 1 {
			v.weekFirst++
		}
	}
	if !t.NoChanges { // closed with nothing to land
		v.landed = append(v.landed, wallLanded{ID: t.ID, Title: v.title(t), Project: v.byProject[t.ProjectID].Name, At: at, Add: t.DiffAdd, Del: t.DiffDel})
	}
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
		since := failedSince(t)
		p := v.byProject[t.ProjectID]
		n := wallNeed{Kind: "failed", ID: t.ID, Title: v.title(t), Project: p.Name, Since: since,
			Old: v.now.Sub(since) > wallFailedOld, Reason: v.reason(t.Error), Resume: t.Resumable}
		by := t.RunBy
		if by == 0 {
			by = t.CreatedBy
		}
		n.Person, n.Avatar = v.person(by)
		d.Needs = append(d.Needs, n)
	}
}

// running is in-progress task t on the wall.
func (v *wallTally) running(t model.Todo) wallRun {
	p := v.byProject[t.ProjectID]
	run := wallRun{ID: t.ID, Title: v.title(t), Project: p.Name, Color: p.Color, Runner: v.runnerName(t.Runner), Run: t.Runs + 1,
		StartedAt: t.StartedAt, Add: t.DiffAdd, Del: t.DiffDel, Files: t.DiffFiles, SentBack: v.sentBack(t.SentBack),
		Took: v.took[wallRunKind{t.ProjectID, t.Runs > 0}]}
	if !v.privacy {
		run.Icon = wallIconURL(p)
		if !v.noSpend {
			cost := t.CostUSD
			run.CostUSD = &cost
		}
		if v.s.Hub != nil {
			if ev, ok := v.s.Hub.Activity(t.ID); ok && (t.StartedAt == nil || !ev.At.Before(*t.StartedAt)) {
				run.Activity = textutil.OneLine(textutil.FirstLine(strings.TrimPrefix(agent.Summary(ev), "🔧 ")), 200)
				if !ev.At.IsZero() {
					at := ev.At
					run.ActivityAt = &at
				}
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
	since, waits := reviewWait(t)
	if !waits {
		d.Stages.ReviewGitHub++
		return
	}
	if d.Stages.ReviewOldest == nil || since.Before(*d.Stages.ReviewOldest) {
		x := since
		d.Stages.ReviewOldest = &x
	}
	n := wallNeed{Kind: "review", ID: t.ID, Title: v.title(t), Project: v.byProject[t.ProjectID].Name, Since: since,
		Old: v.now.Sub(since) > wallReviewOld, Add: t.DiffAdd, Del: t.DiffDel}
	n.Person, n.Avatar = v.person(t.CreatedBy)
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

// reviewWait is since when task t, in review, has waited on a person, and
// whether it does at all. Wall and Needs you (needs.go) both go by it. A
// task reviewed and merged on GitHub waits on a person only while one has
// to act there, not while it is a draft, back with Claude, or waiting for
// its checks; it waits from when its pull request started waiting, or from
// when Claude last finished if that is later.
func reviewWait(t model.Todo) (since time.Time, waits bool) {
	since = t.UpdatedAt
	if t.FinishedAt != nil {
		since = *t.FinishedAt
	}
	if t.WaitsOnGitHub() {
		if t.PR.Waits == "" {
			return since, false
		}
		if w := t.PR.WaitsSince; w != nil && w.After(since) {
			since = *w
		}
	}
	return since, true
}

// failedSince is when failed task t failed.
func failedSince(t model.Todo) time.Time {
	if t.FinishedAt != nil {
		return *t.FinishedAt
	}
	return t.UpdatedAt
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
	spends := !v.privacy && !v.noSpend
	if spends {
		spend := v.spend
		d.Today.SpendUSD = &spend
	}
	if d.Today.Merged > 0 {
		f := float64(v.firstTry) / float64(d.Today.Merged)
		d.Today.FirstTry = &f
	}
	d.Today.Avg.Merged = float64(v.avgMerged) / wallAvgDays
	if spends {
		spend := v.avgSpend / wallAvgDays
		d.Today.Avg.SpendUSD = &spend
	}
	if v.avgMerged > 0 {
		f := float64(v.avgFirst) / float64(v.avgMerged)
		d.Today.Avg.FirstTry = &f
	}
	d.Week = wallWeek{Since: v.weekStart, Merged: v.weekMerged}
	if v.weekMerged > 0 {
		f := float64(v.weekFirst) / float64(v.weekMerged)
		d.Week.FirstTry = &f
	}
	sort.Slice(v.landed, func(i, j int) bool { return v.landed[i].At.After(v.landed[j].At) })
	d.Landed = append(d.Landed, v.landed[:min(len(v.landed), wallLandedMax)]...)
}

// wallRunKind sorts runs for their typical time: a project's first runs of
// tasks apart from its later ones (rework, retries), which are shorter.
type wallRunKind struct {
	project string
	again   bool
}

// wallRunTimes are how long the runs of todos took, in seconds, by kind,
// the newest wallSamples of each. A run from before Dash recorded run times
// counts only while its task is in Review or failed, where the task's
// start and finish times are its latest run's.
func wallRunTimes(todos []model.Todo) map[wallRunKind][]int {
	type took struct {
		at time.Time
		s  int
	}
	by := map[wallRunKind][]took{}
	for _, t := range todos {
		for i, r := range t.RunStats {
			if r.Seconds > 0 {
				k := wallRunKind{t.ProjectID, i > 0}
				by[k] = append(by[k], took{r.At, r.Seconds})
			}
		}
		latest := len(t.RunStats) > 0 && t.RunStats[len(t.RunStats)-1].Seconds > 0
		if !latest && (t.Status == model.StatusReview || t.Status == model.StatusFailed) && t.StartedAt != nil && t.FinishedAt != nil &&
			t.FinishedAt.After(*t.StartedAt) {
			k := wallRunKind{t.ProjectID, t.Runs > 1}
			by[k] = append(by[k], took{*t.StartedAt, int(t.FinishedAt.Sub(*t.StartedAt).Seconds())})
		}
	}
	out := make(map[wallRunKind][]int, len(by))
	for k, ts := range by {
		sort.Slice(ts, func(i, j int) bool { return ts[i].at.After(ts[j].at) })
		s := make([]int, 0, min(len(ts), wallSamples))
		for _, t := range ts[:min(len(ts), wallSamples)] {
			s = append(s, max(t.s, 1))
		}
		out[k] = s
	}
	return out
}

// wallWindowLength is how long a Claude usage window is, by its name (0:
// not known).
var wallWindowLength = map[string]time.Duration{"five_hour": 5 * time.Hour, "seven_day": 7 * 24 * time.Hour}

// wallWindows are ws with where each is heading: when it would reach 100%
// at the rate it has been used since the window began. Too early in a
// window (its first twentieth) the rate says little, so there is none.
func wallWindows(ws []usage.Window, now time.Time) []wallWindow {
	out := make([]wallWindow, 0, len(ws))
	for _, w := range ws {
		ww := wallWindow{Window: w}
		length := wallWindowLength[w.Name]
		if length > 0 && !w.ResetsAt.IsZero() && w.Utilization > 0 && now.Before(w.ResetsAt) {
			used := now.Sub(w.ResetsAt.Add(-length))
			if w.Utilization >= 1 {
				full := now
				ww.FullAt, ww.RunsOut = &full, true
			} else if used >= length/20 && used <= length {
				left := time.Duration(float64(used) * (1 - w.Utilization) / w.Utilization)
				full := now.Add(left).Truncate(time.Minute)
				ww.FullAt, ww.RunsOut = &full, full.Before(w.ResetsAt)
			}
		}
		out = append(out, ww)
	}
	return out
}

// wallWindowNames are the wall's names of the usage windows.
var wallWindowNames = map[string]string{"five_hour": "Claude 5h", "seven_day": "Claude 7d"}

// wallStatusOf is d's overall state. Red (bad): something waits on a
// person too long (a need that's old), the queue is paused, or a runner
// is offline (see wallRunnerGone). Amber (warn): something waits on a
// person (a review, a failure, an open issue), or a usage window is high
// (wallUsageHigh) or will run out before it resets. Green (ok) otherwise.
func wallStatusOf(d *wallData, now time.Time) wallStatus {
	st := wallStatus{Level: "ok", Label: "All good", Running: d.Stages.Running,
		Waiting: d.Stages.Review - d.Stages.ReviewGitHub + d.Stages.Failed, Reasons: []string{}}
	var bad, warn []string
	oldReview, oldFailed := 0, 0
	for _, n := range d.Needs {
		switch {
		case !n.Old:
		case n.Kind == "review":
			oldReview++
		default:
			oldFailed++
		}
	}
	if oldReview > 0 {
		bad = append(bad, wallCount(oldReview, "review")+" waiting over "+wallDuration(wallReviewOld))
	}
	if oldFailed > 0 {
		bad = append(bad, wallCount(oldFailed, "failure")+" unseen over "+wallDuration(wallFailedOld))
	}
	if d.Queue.Paused {
		bad = append(bad, "queue paused")
	}
	for _, r := range d.Runners {
		if !r.Online && (r.Waiting > 0 || r.LastSeen != nil && now.Sub(*r.LastSeen) < wallRunnerGone) {
			bad = append(bad, r.Name+" offline")
		}
	}
	if d.Issues.Open > 0 {
		warn = append(warn, wallCount(d.Issues.Open, "open issue"))
	}
	usageWarn := func(prefix string, ws []wallWindow) {
		for _, w := range ws {
			name := wallWindowNames[w.Name]
			if name == "" {
				name = w.Name
			}
			switch {
			case w.Utilization >= 1:
				warn = append(warn, prefix+name+" at its limit")
			case w.RunsOut && w.FullAt != nil:
				warn = append(warn, prefix+name+" 100% in ~"+wallDuration(w.FullAt.Sub(now)))
			case w.Utilization >= wallUsageHigh:
				warn = append(warn, fmt.Sprintf("%s%s at %d%%", prefix, name, int(w.Utilization*100)))
			}
		}
	}
	if len(d.Usage.Accounts) > 1 {
		for _, a := range d.Usage.Accounts {
			usageWarn(a.Label+": ", a.Windows)
		}
	} else if d.Usage.Known {
		usageWarn("", d.Usage.Windows)
	}
	st.Reasons = append(append(st.Reasons, bad...), warn...)
	switch {
	case len(bad) > 0:
		st.Level, st.Label = "bad", "Needs action"
	case st.Waiting > 0:
		st.Level, st.Label = "warn", "Waiting on people"
	case len(warn) > 0:
		st.Level, st.Label = "warn", "Heads up"
	}
	return st
}

// wallCount is "1 review", "3 reviews".
func wallCount(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// wallDuration is a short, rounded duration: "40 min", "2 h", "3 d".
func wallDuration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return strconv.Itoa(max(1, int(d.Round(time.Minute)/time.Minute))) + " min"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Round(time.Hour)/time.Hour)) + " h"
	}
	return strconv.Itoa(int(d.Round(24*time.Hour)/(24*time.Hour))) + " d"
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

// wallQueue fills in whether the queue runs and the Claude usage it pauses
// on: on a hub, each Claude account's (the caller's own runners' when there
// is a caller with any, else the org's), named by name as the wall shows
// runners, or by email for a caller outside public mode.
func (s *Server) wallQueue(d *wallData, c Caller, runners []tunnel.Runner, name func(tunnel.Runner) string) {
	var snap usage.Snapshot
	if s.Usage != nil {
		snap = s.Usage.Snapshot()
	}
	d.Usage.Accounts = []wallAccount{}
	if s.hub() {
		now := time.Now()
		manual, reason := snap.ManualPause, snap.Reason
		// The hub runs no Claude: the usage is a runner's, as it last
		// reported it (the caller's own runner's when there is a caller).
		reported := func(u usage.Snapshot) usage.Snapshot {
			u.ManualPause = manual
			if manual {
				u.Paused, u.Reason = true, reason
			}
			// The runner's verdict may be stale: its windows may have reset since.
			return u.Reevaluate(now)
		}
		var u *usage.Snapshot
		var groups []usageGroup
		if c.User != 0 {
			u = s.runnerUsage(c)
			groups = usageGroups(s.claudeRunners(c))
		}
		if u == nil {
			u = bestUsage(runners)
		}
		if len(groups) == 0 {
			groups = usageGroups(runners)
		}
		if u != nil {
			snap = reported(*u)
		}
		var ws []waits
		for _, g := range groups {
			a := reported(*g.usage)
			label := g.account
			if label == "" || c.User == 0 || d.Privacy {
				label = strings.Join(g.names(name), ", ")
			}
			d.Usage.Accounts = append(d.Usage.Accounts, wallAccount{Label: label, Windows: wallWindows(a.Windows, now), Paused: a.Paused, Reason: a.Reason})
			ws = append(ws, waits{active: g.active, snap: a})
		}
		// Waiting on limits only while every account that could run is.
		combinePause(&snap, ws)
	}
	d.Usage.Known, d.Usage.Windows = snap.Known, wallWindows(snap.Windows, time.Now())
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
