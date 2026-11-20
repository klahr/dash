package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/release"
	"github.com/klahr/dash/internal/store"
)

// Planning: the org's plan, for a product owner (docs/planning.md). Cards
// in fixed columns link to the tasks and goals that carry them out; what a
// card shows of those is worked out here, in words for its owner (cardStatus),
// never stored. Planners reach these routes and their own account only (see
// planner.go); the routes that list or link the org's tasks and goals
// themselves are for members.

func (s *Server) planningRoutes(mux routeMux) {
	mux.HandleFunc("GET /api/planning/cards", s.listCards)
	mux.HandleFunc("POST /api/planning/cards", s.createCard)
	mux.HandleFunc("GET /api/planning/cards/{cid}", s.getCard)
	mux.HandleFunc("PATCH /api/planning/cards/{cid}", s.updateCard)
	mux.HandleFunc("DELETE /api/planning/cards/{cid}", s.deleteCard)
	mux.HandleFunc("POST /api/planning/cards/{cid}/move", s.moveCard)
	mux.HandleFunc("GET /api/planning/cards/{cid}/comments", s.cardComments)
	mux.HandleFunc("POST /api/planning/cards/{cid}/comments", s.addCardComment)
	mux.HandleFunc("DELETE /api/planning/cards/{cid}/comments/{mid}", s.deleteCardComment)
	mux.HandleFunc("POST /api/planning/cards/{cid}/start", s.startCard)
	// A card's files: planners attach and see them here (they can't use
	// /api/uploads), and only the files of the cards of their org.
	mux.HandleFunc("POST /api/planning/cards/{cid}/files", s.addCardFile)
	mux.HandleFunc("GET /api/planning/cards/{cid}/files/{fid}/{name}", s.getCardFile)
	mux.HandleFunc("DELETE /api/planning/cards/{cid}/files/{fid}", s.removeCardFile)
	// The projects a card may name or start work in: names only, for planners
	// too (they can't list the projects themselves).
	mux.HandleFunc("GET /api/planning/projects", s.planningProjects)
	// A project's icon, for planners too (they can't read uploads).
	mux.HandleFunc("GET /api/planning/projects/{pid}/icon", s.planningProjectIcon)
	// The org's members a card may be assigned to: names and pictures, for
	// planners too.
	mux.HandleFunc("GET /api/planning/people", s.planningPeople)
	// Members only (not in plannerRoutes): the org's tasks and goals to link,
	// linking and unlinking them, and the cards a task or goal is linked to.
	mux.HandleFunc("GET /api/planning/work", s.linkableWork)
	mux.HandleFunc("POST /api/planning/cards/{cid}/links", s.linkCard)
	mux.HandleFunc("DELETE /api/planning/cards/{cid}/links/{kind}/{wid}", s.unlinkCard)
	mux.HandleFunc("GET /api/planning/linked/{kind}/{wid}", s.linkedCards)
}

// cardStatus is what a card's linked work adds up to, in an owner's words.
type cardStatus struct {
	Text string `json:"text"` // "Waiting for review", "Shipped in v1.4.0", …
	// Progress counts the linked work done when there is more than one:
	// "3 of 5 done".
	Progress string `json:"progress,omitempty"`
	// Tone is for color: none, idle, working, waiting, failed, done, shipped.
	Tone  string `json:"tone"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
	// SuggestDone: everything linked is done but the card isn't in Done.
	// Dash suggests the move; it never makes it.
	SuggestDone bool `json:"suggest_done,omitempty"`
	// Live is where all of it is live, once it is, and since when
	// (deploylive.go): Text says "Live in production since 19 Nov 2026".
	Live *liveIn `json:"live,omitempty"`
}

// linkView is one task or goal of a card, as its owner sees it: title,
// project and state, never branches, worktrees, diffs or runners.
type linkView struct {
	Kind string `json:"kind"`
	// ID is the task's or goal's id, for members (a planner sees none).
	ID      string `json:"id,omitempty"`
	Title   string `json:"title"`
	Project string `json:"project,omitempty"`
	// ProjectID is its project's id, for members, to open it.
	ProjectID string `json:"project_id,omitempty"`
	State     string `json:"state"`
	Tone      string `json:"tone"`
	// Shipped is the first release with all of it, once done and released.
	Shipped string `json:"shipped,omitempty"`
	// Live is where it is live (production if it is there), once deployed.
	Live    *liveIn  `json:"live,omitempty"`
	lives   []liveIn // every environment it is live in
	done    bool
	failed  bool
	started bool
}

// cardView is a card as the API answers it.
type cardView struct {
	model.Card
	// Version is the card's model.Card.Version: send it back with a PATCH to
	// have the edit refused (409) if someone changed the card meanwhile.
	Version     string `json:"version"`
	ProjectName string `json:"project_name,omitempty"`
	// People are the card's members (model.Card.Members, the same order)
	// with their usernames and pictures, to show their faces on the card.
	People []cardPerson `json:"people"`
	Status cardStatus   `json:"status"`
	Links  []linkView   `json:"links"`
	// Deps are the cards it depends on (model.Card.DependsOn, the same
	// order), NeededBy the cards that depend on it. Waiting counts the
	// dependencies not done yet: the card is blocked while it isn't 0.
	Deps       []cardRef     `json:"deps"`
	NeededBy   []cardRef     `json:"needed_by"`
	Waiting    int           `json:"waiting"`
	Comments   int           `json:"comments"`
	Discussion []cardComment `json:"discussion,omitempty"` // GET of one card
}

// cardRef is another card, as a card that depends on it (or that it
// depends on) names it: done when it is in the Done column.
type cardRef struct {
	ID     string           `json:"id"`
	Title  string           `json:"title"`
	Column model.CardColumn `json:"column"`
	Done   bool             `json:"done"`
}

func refOf(c model.Card) cardRef {
	return cardRef{ID: c.ID, Title: c.Title, Column: c.Column, Done: c.Column == model.CardDone}
}

// cardPerson is one of a card's members: their user id, username and
// picture's version (0: none).
type cardPerson struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Avatar   int64  `json:"avatar,omitempty"`
}

type cardComment = model.CardComment

// planningWork is what card views are worked out from: the org's projects,
// tasks and goals, read once for any number of cards.
type planningWork struct {
	sv       *service
	ctx      context.Context
	projects map[string]model.Project
	todos    map[string]model.Todo
	goals    map[string]model.Goal
	goalWork map[string][]model.Todo // goal id → its tasks
	people   map[int64]store.Member  // the org's members, by user id
	live     map[string]projectLive  // project id → what is live where, read when needed
}

func (sv *service) planningWork(ctx context.Context) *planningWork {
	w := &planningWork{sv: sv, ctx: ctx, projects: map[string]model.Project{}, todos: map[string]model.Todo{},
		goals: map[string]model.Goal{}, goalWork: map[string][]model.Todo{}, people: map[int64]store.Member{},
		live: map[string]projectLive{}}
	if ms, err := sv.s.Store.Accounts().Members(sv.c.Org); err == nil {
		for _, m := range ms {
			w.people[m.ID] = m
		}
	}
	for _, p := range sv.data.ListProjects() {
		w.projects[p.ID] = p
	}
	for _, t := range sv.data.ListTodos("") {
		w.todos[t.ID] = t
		if t.GoalID != "" {
			w.goalWork[t.GoalID] = append(w.goalWork[t.GoalID], t)
		}
	}
	for _, g := range sv.data.ListGoals("") {
		w.goals[g.ID] = g
	}
	return w
}

// taskState is a task's state in an owner's words.
func taskState(t model.Todo) (state, tone string) {
	switch t.Status {
	case model.StatusDraft:
		return "Not started", "idle"
	case model.StatusQueued:
		return "Waiting to start", "idle"
	case model.StatusInProgress:
		return "In progress", "working"
	case model.StatusReview:
		return "Waiting for review", "waiting"
	case model.StatusMerging:
		return "Approved, landing", "working"
	case model.StatusMerged:
		return "Done", "done"
	case model.StatusFailed:
		return "Failed, needs a developer", "failed"
	}
	return string(t.Status), "idle"
}

// goalState is a goal's state in an owner's words.
func goalState(g model.Goal) (state, tone string) {
	if g.IsBlocked() {
		return "Blocked, needs its owner", "waiting"
	}
	switch g.Status {
	case model.GoalPlanning:
		return "Breaking into tasks", "working"
	case model.GoalWorking:
		return "In progress", "working"
	case model.GoalWaiting:
		return "Waiting for a decision", "waiting"
	case model.GoalPaused:
		return "Paused", "idle"
	case model.GoalDone:
		return "Done", "done"
	}
	return "In progress", "working"
}

// link is one link's view.
func (w *planningWork) link(l model.CardLink) linkView {
	v := linkView{Kind: l.Kind, ID: l.WorkID}
	project := func(id string) string { return w.projects[id].Name }
	switch l.Kind {
	case model.LinkTask:
		t, ok := w.todos[l.WorkID]
		if !ok {
			return linkView{Kind: l.Kind, ID: l.WorkID, Title: "Removed task", State: "Removed", Tone: "idle", done: true}
		}
		v.Title, v.Project, v.ProjectID = t.Title, project(t.ProjectID), t.ProjectID
		v.State, v.Tone = taskState(t)
		v.done, v.failed = t.Status == model.StatusMerged, t.Status == model.StatusFailed
		v.started = t.Status != model.StatusDraft
		if v.done {
			v.Shipped = w.shipped([]model.Todo{t})
			v.lives = w.liveTask(t)
		}
	case model.LinkGoal:
		g, ok := w.goals[l.WorkID]
		if !ok {
			return linkView{Kind: l.Kind, ID: l.WorkID, Title: "Removed goal", State: "Removed", Tone: "idle", done: true}
		}
		v.Title, v.Project, v.ProjectID = g.Title, project(g.ProjectID), g.ProjectID
		v.State, v.Tone = goalState(g)
		tasks := w.goalWork[g.ID]
		for _, t := range tasks {
			v.failed = v.failed || t.Status == model.StatusFailed
		}
		if v.failed && g.Status != model.GoalDone {
			v.State, v.Tone = "Failed, needs a developer", "failed"
		}
		v.done, v.started = g.Status == model.GoalDone, true
		if v.done {
			var merged []model.Todo
			for _, t := range tasks {
				if t.Status == model.StatusMerged {
					merged = append(merged, t)
				}
			}
			v.Shipped = w.shipped(merged)
			var sets [][]liveIn
			for _, t := range merged {
				sets = append(sets, w.liveTask(t))
			}
			v.lives = commonLive(sets)
		}
	}
	if v.Shipped != "" {
		v.State, v.Tone = "Shipped in "+v.Shipped, "shipped"
	}
	if l, ok := bestLive(v.lives); ok {
		v.Live = &l
		v.State, v.Tone = liveText(l), "live"
	}
	return v
}

// liveTask is where merged task t is live, from its project's deploys.
func (w *planningWork) liveTask(t model.Todo) []liveIn {
	pl, ok := w.live[t.ProjectID]
	if !ok {
		p, found := w.projects[t.ProjectID]
		if !found || len(p.Environments) == 0 {
			return nil
		}
		pl = liveOf(w.sv.data, p)
		w.live[t.ProjectID] = pl
	}
	return pl.Shipped[model.ShippedKey(model.LinkTask, t.ID)]
}

// shipped is the first release that has every one of the tasks' merged
// commits ("" when one isn't released yet, or Dash can't tell).
func (w *planningWork) shipped(ts []model.Todo) string {
	var latest release.Tag
	found := false
	for _, t := range ts {
		p, ok := w.projects[t.ProjectID]
		if !ok || t.MergedCommit == "" {
			return ""
		}
		tag, ok := w.sv.s.releaseOf(w.ctx, p, t.MergedCommit)
		if !ok {
			return ""
		}
		if !found || latest.Version.Less(tag.Version) {
			latest, found = tag, true
		}
	}
	if !found {
		return ""
	}
	return latest.Name
}

// views makes the views of cards, with their links (from links), comment
// counts and dependencies (all is every card of the org, to name them).
func (w *planningWork) views(cards, all []model.Card, links map[string][]model.CardLink, comments map[string]int) []cardView {
	byID := make(map[string]model.Card, len(all))
	neededBy := map[string][]cardRef{}
	for _, c := range all {
		byID[c.ID] = c
		for _, d := range c.DependsOn {
			neededBy[d] = append(neededBy[d], refOf(c))
		}
	}
	out := make([]cardView, 0, len(cards))
	for _, c := range cards {
		v := cardView{Card: c, Version: c.Version(), ProjectName: w.projects[c.ProjectID].Name, Links: []linkView{}, Comments: comments[c.ID],
			Deps: []cardRef{}, NeededBy: []cardRef{}}
		for _, id := range c.DependsOn {
			if d, ok := byID[id]; ok {
				r := refOf(d)
				v.Deps = append(v.Deps, r)
				if !r.Done {
					v.Waiting++
				}
			}
		}
		if n := neededBy[c.ID]; n != nil {
			v.NeededBy = n
		}
		// A member who has left the org since is no longer shown.
		v.Members, v.People = []int64{}, []cardPerson{}
		for _, id := range c.Members {
			if m, ok := w.people[id]; ok {
				v.Members = append(v.Members, id)
				v.People = append(v.People, cardPerson{ID: id, Username: m.Username, Avatar: m.Avatar})
			}
		}
		if !slices.Contains(v.Members, v.Owner) {
			v.Owner = 0
		}
		for _, l := range links[c.ID] {
			lv := w.link(l)
			if w.sv.c.Planner {
				lv.ID, lv.ProjectID = "", "" // a planner sees the work through the card only
			}
			v.Links = append(v.Links, lv)
		}
		v.Status = status(c, v.Links)
		out = append(out, v)
	}
	return out
}

// status adds up a card's links.
func status(c model.Card, links []linkView) cardStatus {
	st := cardStatus{Total: len(links)}
	if len(links) == 0 {
		st.Text, st.Tone = "Nothing started yet", "none"
		return st
	}
	var failed, waiting, review, started int
	shipped, allShipped := "", true
	var lives [][]liveIn
	for _, l := range links {
		lives = append(lives, l.lives)
		switch {
		case l.done:
			st.Done++
		case l.failed:
			failed++
		case l.Tone == "waiting":
			waiting++
			if l.State == "Waiting for review" {
				review++
			}
		}
		if l.started {
			started++
		}
		if l.Shipped == "" {
			allShipped = false
		} else if shipped == "" || laterTag(l.Shipped, shipped) {
			shipped = l.Shipped
		}
	}
	if st.Total > 1 {
		st.Progress = fmt.Sprintf("%d of %d done", st.Done, st.Total)
	}
	switch {
	case failed > 0:
		st.Text, st.Tone = "Failed, needs a developer", "failed"
	case st.Done == st.Total && len(commonLive(lives)) > 0:
		l, _ := bestLive(commonLive(lives))
		st.Text, st.Tone, st.Live = liveText(l), "live", &l
	case st.Done == st.Total && allShipped:
		st.Text, st.Tone = "Shipped in "+shipped, "shipped"
	case st.Done == st.Total:
		st.Text, st.Tone = "All done", "done"
	case review > 0 && review == waiting:
		st.Text, st.Tone = "Waiting for review", "waiting"
	case waiting > 0:
		st.Text, st.Tone = "Waiting for a decision", "waiting"
	case started > 0:
		st.Text, st.Tone = "In progress", "working"
	default:
		st.Text, st.Tone = "Not started", "idle"
	}
	st.SuggestDone = st.Done == st.Total && c.Column != model.CardDone
	return st
}

// laterTag reports whether release tag a is a later version than b (by the
// numbers in them; tags of one project share a format).
func laterTag(a, b string) bool {
	va, oka := tagVersion(a)
	vb, okb := tagVersion(b)
	return oka && okb && vb.Less(va)
}

func tagVersion(tag string) (release.Version, bool) {
	i := strings.IndexAny(tag, "0123456789")
	if i < 0 {
		return release.Version{}, false
	}
	v, err := release.ParseVersion(strings.TrimRightFunc(tag[i:], func(r rune) bool { return r < '0' || r > '9' }))
	return v, err == nil
}

// ---- which release shipped a commit ----

// shipCache remembers which release first shipped a commit: for good once
// found, for a minute while none has.
type shipCache struct {
	mu sync.Mutex
	m  map[string]shipEntry
}

type shipEntry struct {
	tag release.Tag
	ok  bool
	at  time.Time
}

const shipRecheck = time.Minute

// releaseOf is the first release (a tag in the project's format) whose
// history has sha. Only a Dash that has the project's repository can tell
// (not a hub, whose runners have it).
func (s *Server) releaseOf(ctx context.Context, p model.Project, sha string) (release.Tag, bool) {
	if s.Git == nil || s.hub() || p.Path == "" {
		return release.Tag{}, false
	}
	f, err := release.ParseFormat(p.TagFormat)
	if err != nil {
		return release.Tag{}, false
	}
	key := p.ID + "@" + sha
	s.ships.mu.Lock()
	e, ok := s.ships.m[key]
	s.ships.mu.Unlock()
	if ok && (e.ok || time.Since(e.at) < shipRecheck) {
		return e.tag, e.ok
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tags, err := s.Git.TagsContaining(ctx, p.ID, p.Path, sha)
	if err != nil {
		return release.Tag{}, false // not cached: the repository may be busy
	}
	e = shipEntry{at: time.Now()}
	e.tag, e.ok = f.Earliest(tags)
	s.ships.mu.Lock()
	if s.ships.m == nil {
		s.ships.m = map[string]shipEntry{}
	}
	s.ships.m[key] = e
	s.ships.mu.Unlock()
	return e.tag, e.ok
}

// ---- handlers ----

// cardViews are views of the cards, with their links and comment counts.
func (sv *service) cardViews(ctx context.Context, cards []model.Card) ([]cardView, error) {
	ids := make([]string, len(cards))
	for i, c := range cards {
		ids[i] = c.ID
	}
	links := map[string][]model.CardLink{}
	if len(ids) > 0 {
		var err error
		if links, err = sv.data.CardLinks(ids...); err != nil {
			return nil, err
		}
	}
	comments, err := sv.data.CommentCounts()
	if err != nil {
		return nil, err
	}
	all, err := sv.data.ListCards("")
	if err != nil {
		return nil, err
	}
	return sv.planningWork(ctx).views(cards, all, links, comments), nil
}

func (sv *service) cardView(ctx context.Context, id string) (cardView, error) {
	c, err := sv.data.GetCard(id)
	if err != nil {
		return cardView{}, err
	}
	vs, err := sv.cardViews(ctx, []model.Card{c})
	if err != nil {
		return cardView{}, err
	}
	return vs[0], nil
}

// listCards handles GET /api/planning/cards[?project=<id>].
func (s *Server) listCards(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	cards, err := sv.data.ListCards(r.URL.Query().Get("project"))
	if err != nil {
		writeErr(w, err)
		return
	}
	vs, err := sv.cardViews(r.Context(), cards)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vs)
}

// cardRequest is what POST /api/planning/cards and PATCH …/{cid} take;
// nil fields stay as they are.
type cardRequest struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Project     *string `json:"project"`
	Column      *string `json:"column"` // create only; moves are POST …/move
	Priority    *string `json:"priority"`
	Target      *string `json:"target"`
	// Members are the user ids of the people in the org on the card, all of
	// them ([] is nobody).
	Members *[]int64 `json:"members"`
	// Owner is the user id of the member who owns the card (0: nobody); one
	// who isn't a member yet joins. Members without the owner in them, and
	// no owner given, leave the card with no owner.
	Owner *int64 `json:"owner"`
	// DependsOn are the ids of the org's cards to be done before this one,
	// all of them ([] is none).
	DependsOn *[]string `json:"depends_on"`
	// Tags are the card's tags, all of them ([] is none).
	Tags *[]string `json:"tags"`
	// Version (PATCH only) is the card's version the edit was made on;
	// given, the edit is refused with a 409 if the card has changed since.
	Version *string `json:"version"`
}

func (req cardRequest) apply(c *model.Card) error {
	set(&c.Title, req.Title)
	set(&c.Description, req.Description)
	set(&c.ProjectID, req.Project)
	set(&c.Priority, req.Priority)
	set(&c.Target, req.Target)
	if req.Members != nil && req.Owner == nil && !slices.Contains(*req.Members, c.Owner) {
		c.Owner = 0
	}
	set(&c.Members, req.Members)
	set(&c.Owner, req.Owner)
	set(&c.DependsOn, req.DependsOn)
	set(&c.Tags, req.Tags)
	if req.Column != nil {
		c.Column = model.CardColumn(*req.Column)
	}
	if err := model.CleanCard(c); err != nil {
		return fmt.Errorf("%w: %v", errBadRequest, err)
	}
	return nil
}

func (s *Server) createCard(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req cardRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	c, err := sv.createCard(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	v, err := sv.cardView(r.Context(), c.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// createCard adds a card for the caller (the REST API and the create_card tool).
func (sv *service) createCard(req cardRequest) (model.Card, error) {
	if err := sv.writable(); err != nil {
		return model.Card{}, err
	}
	var c model.Card
	if err := req.apply(&c); err != nil {
		return c, err
	}
	if c.ProjectID != "" {
		p, err := sv.data.GetProject(c.ProjectID)
		if err != nil {
			return c, err
		}
		c.ProjectID = p.ID
	}
	c, err := sv.data.CreateCard(c)
	if err == nil {
		sv.membersAdded(c, nil)
	}
	return c, err
}

func (s *Server) getCard(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	v, err := sv.cardView(r.Context(), r.PathValue("cid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if v.Discussion, err = sv.data.CardComments(v.ID); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updateCard(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req cardRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := sv.updateCard(r.PathValue("cid"), req); err != nil {
		writeErr(w, err)
		return
	}
	s.answerCard(w, r, sv, r.PathValue("cid"))
}

// updateCard changes a card's fields (not its column: see moveCard), for
// the REST API and the update_card tool.
func (sv *service) updateCard(id string, req cardRequest) error {
	req.Column = nil
	if err := sv.writable(); err != nil {
		return err
	}
	var before []int64
	c, err := sv.data.UpdateCard(id, func(c *model.Card) error {
		if req.Version != nil && *req.Version != c.Version() {
			return fmt.Errorf("%w: someone else changed the card since you opened it: reload it to see their change", errConflict)
		}
		before = slices.Clone(c.Members)
		return req.apply(c)
	})
	if err == nil {
		sv.membersAdded(c, before)
	}
	return err
}

// answerCard answers r with the card's view.
func (s *Server) answerCard(w http.ResponseWriter, r *http.Request, sv *service, id string) {
	v, err := sv.cardView(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteCard(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := sv.writable(); err != nil {
		writeErr(w, err)
		return
	}
	if err := sv.data.DeleteCard(r.PathValue("cid")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// moveCard handles POST /api/planning/cards/{cid}/move: {column, position}.
// position is where in the column, from 0 (the top); left out, the end.
func (s *Server) moveCard(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		Column   string `json:"column"`
		Position *int   `json:"position"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := sv.moveCard(r.PathValue("cid"), req.Column, req.Position); err != nil {
		writeErr(w, err)
		return
	}
	s.answerCard(w, r, sv, r.PathValue("cid"))
}

// moveCard puts a card in column, at position (nil: the end), for the REST
// API and the update_card tool.
func (sv *service) moveCard(id, column string, position *int) error {
	col := model.CardColumn(column)
	if !col.Valid() {
		return fmt.Errorf("%w: unknown column %q: use ideas, planned, in_progress or done", errBadRequest, column)
	}
	if err := sv.writable(); err != nil {
		return err
	}
	pos := 1 << 30
	if position != nil {
		pos = *position
	}
	_, err := sv.data.MoveCard(id, col, pos)
	return err
}

func (s *Server) cardComments(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	ms, err := sv.data.CardComments(r.PathValue("cid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ms)
}

func (s *Server) addCardComment(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	m, err := sv.addCardComment(r.PathValue("cid"), req.Text)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// addCardComment adds the caller's comment to a card, for the REST API and
// the comment_card tool.
func (sv *service) addCardComment(cardID, text string) (model.CardComment, error) {
	text, err := model.CleanComment(text)
	if err != nil {
		return model.CardComment{}, fmt.Errorf("%w: %v", errBadRequest, err)
	}
	if sv.c.User == 0 {
		return model.CardComment{}, fmt.Errorf("%w: a comment is a person's: sign in to comment", errBadRequest)
	}
	if err := sv.writable(); err != nil {
		return model.CardComment{}, err
	}
	m, err := sv.data.AddCardComment(cardID, text)
	if err == nil {
		sv.mentionedIn(cardID, m)
	}
	return m, err
}

// deleteCardComment handles DELETE …/comments/{mid}: its author, or an
// admin of the org, may delete a comment.
func (s *Server) deleteCardComment(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
	if err != nil {
		writeErr(w, fmt.Errorf("%w: bad comment id", errBadRequest))
		return
	}
	m, err := sv.data.CardComment(r.PathValue("cid"), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if m.Author != sv.c.User && !s.orgAdmin(sv.c) {
		writeErr(w, fmt.Errorf("%w: only its author or an admin can delete a comment", errForbidden))
		return
	}
	if err := sv.writable(); err != nil {
		writeErr(w, err)
		return
	}
	if err := sv.data.DeleteCardComment(m.CardID, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// addCardFile handles POST /api/planning/cards/{cid}/files?name=shot.png:
// it stores the body as an upload and attaches it to the card.
func (s *Server) addCardFile(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := sv.writable(); err != nil {
		writeErr(w, err)
		return
	}
	cid := r.PathValue("cid")
	c, err := sv.data.GetCard(cid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(c.Files) >= model.MaxCardFiles {
		writeErr(w, store.ErrTooManyFiles)
		return
	}
	a, err := sv.receiveUpload(w, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := sv.data.AddCardFile(cid, model.CardFile{ID: a.ID, Name: a.Name, Type: a.Type, Size: a.Size}); err != nil {
		_ = os.RemoveAll(filepath.Dir(a.Path))
		writeErr(w, err)
		return
	}
	v, err := sv.cardView(r.Context(), cid)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// cardFile is one of a card's files, as an upload (with its path).
func (sv *service) cardFile(cid, fid string) (model.Attachment, error) {
	c, err := sv.data.GetCard(cid)
	if err != nil {
		return model.Attachment{}, err
	}
	for _, f := range c.Files {
		if f.ID == fid {
			return sv.findUpload(fid)
		}
	}
	return model.Attachment{}, store.ErrNotFound
}

// getCardFile handles GET /api/planning/cards/{cid}/files/{fid}/{name}.
func (s *Server) getCardFile(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	a, err := sv.cardFile(r.PathValue("cid"), r.PathValue("fid"))
	if err != nil || a.Name != r.PathValue("name") {
		writeErr(w, store.ErrNotFound)
		return
	}
	serveUpload(w, r, a)
}

// removeCardFile handles DELETE /api/planning/cards/{cid}/files/{fid}.
func (s *Server) removeCardFile(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := sv.writable(); err != nil {
		writeErr(w, err)
		return
	}
	if err := sv.data.RemoveCardFile(r.PathValue("cid"), r.PathValue("fid")); err != nil {
		writeErr(w, err)
		return
	}
	s.answerCard(w, r, sv, r.PathValue("cid"))
}

// planningProjects handles GET /api/planning/projects: the org's projects'
// ids, names, colors and icons (the icon is a planning URL a planner can
// load, or empty for none). On a hub these are the shared projects only: a
// runner's own projects live in that runner's store, not the org's.
func (s *Server) planningProjects(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	type project struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Color string `json:"color,omitempty"`
		Icon  string `json:"icon,omitempty"`
	}
	out := []project{}
	for _, p := range sv.data.ListProjects() {
		icon := ""
		if p.Icon != "" {
			// The upload's id changes with the icon, so the URL does too.
			icon = "/api/planning/projects/" + url.PathEscape(p.ID) + "/icon?v=" + url.QueryEscape(p.Icon)
		}
		out = append(out, project{p.ID, p.Name, p.Color, icon})
	}
	writeJSON(w, http.StatusOK, out)
}

// planningProjectIcon handles GET /api/planning/projects/{pid}/icon: the
// project's icon image, for Planning's project picker.
func (s *Server) planningProjectIcon(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	id, name, _ := strings.Cut(p.Icon, "/")
	a, err := sv.findUpload(id)
	if err != nil || a.Name != name || !strings.HasPrefix(a.Type, "image/") {
		writeErr(w, store.ErrNotFound)
		return
	}
	serveUpload(w, r, a)
}

// planningPeople handles GET /api/planning/people: the org's members a card
// may be assigned to, with their username and picture's version (not their
// email: planners see it too).
func (s *Server) planningPeople(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	type person struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Avatar   int64  `json:"avatar,omitempty"`
		// Planner: they can't own a goal (Start's owner picker).
		Planner bool `json:"planner,omitempty"`
	}
	ms, err := s.Store.Accounts().Members(sv.c.Org)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := []person{}
	for _, m := range ms {
		out = append(out, person{m.ID, m.Username, m.Avatar, m.Role.Planner()})
	}
	slices.SortFunc(out, func(a, b person) int {
		return strings.Compare(strings.ToLower(a.Username), strings.ToLower(b.Username))
	})
	writeJSON(w, http.StatusOK, out)
}

// startCard handles POST /api/planning/cards/{cid}/start: {project, kind,
// owner}. It makes a task (queued; the card's title and description are its
// prompt) or a goal (kind "goal") in the project, links it to the card,
// and moves the card to In progress if it was still in Ideas or Planned.
// A planner starting a goal names its owner, a developer (see goalOwner).
func (s *Server) startCard(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		Project string `json:"project"`
		Kind    string `json:"kind"`
		Owner   int64  `json:"owner"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	id, err := sv.startCard(r.PathValue("cid"), req.Project, req.Kind, req.Owner)
	if err != nil {
		writeErr(w, err)
		return
	}
	v, err := sv.cardView(r.Context(), r.PathValue("cid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	kind := req.Kind
	if kind == "" {
		kind = model.LinkTask
	}
	// started is the work Start made, for dash-mcp's start_card.
	type started struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	writeJSON(w, http.StatusOK, struct {
		cardView
		Started started `json:"started"`
	}{v, started{kind, id}})
}

// startCard is what Start does (the REST API's and the start_card tool's):
// it returns the new task's or goal's id. owner is the goal's owner (0: the
// caller); see goalOwner.
func (sv *service) startCard(cardID, project, kind string, owner int64) (string, error) {
	c, err := sv.data.GetCard(cardID)
	if err != nil {
		return "", err
	}
	if project == "" {
		project = c.ProjectID
	}
	if project == "" {
		return "", fmt.Errorf("%w: choose the project to start the work in", errBadRequest)
	}
	p, err := sv.data.GetProject(project)
	if err != nil {
		return "", err
	}
	var id string
	switch kind {
	case "", model.LinkTask:
		kind = model.LinkTask
		prompt := c.Title
		if c.Description != "" {
			prompt += "\n\n" + c.Description
		}
		t, err := sv.addTodo(model.Todo{ProjectID: p.ID, Title: c.Title, Prompt: prompt, Attachments: sv.cardAttachments(c)}, true)
		if err != nil {
			return "", err
		}
		id = t.ID
	case model.LinkGoal:
		title, desc := c.Title, c.Description
		if desc == "" {
			desc = c.Title
		}
		owner, err := sv.goalOwner(owner)
		if err != nil {
			return "", err
		}
		g, err := sv.createGoal(goalRequest{Project: p.ID, Title: &title, Description: &desc, owner: owner})
		if err != nil {
			return "", err
		}
		id = g.ID
	default:
		return "", fmt.Errorf("%w: start a task or a goal", errBadRequest)
	}
	if err := sv.data.LinkCard(c.ID, kind, id); err != nil {
		return id, err
	}
	if c.ProjectID == "" {
		if _, err := sv.data.UpdateCard(c.ID, func(x *model.Card) error { x.ProjectID = p.ID; return nil }); err != nil {
			return id, err
		}
	}
	sv.cardStarted(c)
	return id, nil
}

// errGoalOwner: a planner's goal needs a developer to own it.
var errGoalOwner = fmt.Errorf("%w: choose the developer who owns the goal: its coordinator runs as them, on their runner, and they accept its plan", errBadRequest)

// goalOwner is who owns a goal the caller starts from a card, given the
// owner they asked for (0: none). A goal's coordinator acts as its owner,
// on their runner, and its plan waits for them, so a planner (who has no
// runner and can't open goals) must name a member of the org who isn't a
// planner. Anyone else owns the goals they start.
func (sv *service) goalOwner(want int64) (int64, error) {
	if !sv.c.Planner {
		if want != 0 && want != sv.c.User {
			return 0, fmt.Errorf("%w: a goal you start is yours", errBadRequest)
		}
		return 0, nil
	}
	if want == 0 {
		return 0, errGoalOwner
	}
	m, err := sv.s.Store.Accounts().Membership(want, sv.c.Org)
	if errors.Is(err, store.ErrNotMember) {
		return 0, fmt.Errorf("%w: they aren't in this organization", errGoalOwner)
	}
	if err != nil {
		return 0, err
	}
	if m.Role.Planner() {
		return 0, fmt.Errorf("%w: they are a planner", errGoalOwner)
	}
	return want, nil
}

// cardAttachments are the card's files, for a task started from it. A hub
// can't send files to its runners, so there it gives none; nor does it give
// a file whose upload is gone.
func (sv *service) cardAttachments(c model.Card) []model.Attachment {
	if sv.s.hub() {
		return nil
	}
	var out []model.Attachment
	for _, f := range c.Files {
		if a, err := sv.findUpload(f.ID); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// cardStarted moves a card whose work has started to In progress, if it
// was still in Ideas or Planned. (Done is never automatic: the card only
// suggests it.)
func (sv *service) cardStarted(c model.Card) {
	if c.Column == model.CardIdeas || c.Column == model.CardPlanned {
		_, _ = sv.data.MoveCard(c.ID, model.CardInProgress, 1<<30)
	}
}

// linkableWork handles GET /api/planning/work[?project=<id>]: the org's
// tasks and goals a card may link to, newest first (members only).
func (s *Server) linkableWork(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	pid := r.URL.Query().Get("project")
	pw := sv.planningWork(r.Context())
	type item struct {
		Kind    string    `json:"kind"`
		ID      string    `json:"id"`
		Title   string    `json:"title"`
		Project string    `json:"project"`
		State   string    `json:"state"`
		Created time.Time `json:"created_at"`
	}
	out := []item{}
	for _, g := range pw.goals {
		if pid == "" || g.ProjectID == pid {
			st, _ := goalState(g)
			out = append(out, item{model.LinkGoal, g.ID, g.Title, pw.projects[g.ProjectID].Name, st, g.CreatedAt})
		}
	}
	for _, t := range pw.todos {
		if pid == "" || t.ProjectID == pid {
			st, _ := taskState(t)
			out = append(out, item{model.LinkTask, t.ID, t.Title, pw.projects[t.ProjectID].Name, st, t.CreatedAt})
		}
	}
	slices.SortFunc(out, func(a, b item) int { return b.Created.Compare(a.Created) })
	if len(out) > 500 {
		out = out[:500]
	}
	writeJSON(w, http.StatusOK, out)
}

// linkCard handles POST /api/planning/cards/{cid}/links: {kind, id}.
func (s *Server) linkCard(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := sv.linkCard(r.PathValue("cid"), req.Kind, req.ID); err != nil {
		writeErr(w, err)
		return
	}
	s.answerCard(w, r, sv, r.PathValue("cid"))
}

// linkCard links a card to a task or goal, and moves the card to In
// progress when that work has started.
func (sv *service) linkCard(cardID, kind, id string) error {
	if kind != model.LinkTask && kind != model.LinkGoal {
		return fmt.Errorf("%w: link a task or a goal", errBadRequest)
	}
	if err := sv.writable(); err != nil {
		return err
	}
	c, err := sv.data.GetCard(cardID)
	if err != nil {
		return err
	}
	if err := sv.data.LinkCard(cardID, kind, id); err != nil {
		return err
	}
	started := kind == model.LinkGoal
	if kind == model.LinkTask {
		if t, err := sv.data.GetTodo(id); err == nil {
			started = t.Status != model.StatusDraft
		}
	}
	if started {
		sv.cardStarted(c)
	}
	return nil
}

func (s *Server) unlinkCard(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := sv.writable(); err != nil {
		writeErr(w, err)
		return
	}
	if err := sv.data.UnlinkCard(r.PathValue("cid"), r.PathValue("kind"), r.PathValue("wid")); err != nil {
		writeErr(w, err)
		return
	}
	s.answerCard(w, r, sv, r.PathValue("cid"))
}

// linkedCards handles GET /api/planning/linked/{kind}/{wid}: the cards a
// task or goal is linked to, for its detail's "Link to card".
func (s *Server) linkedCards(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	cards, err := sv.data.CardsLinkedTo(r.PathValue("kind"), r.PathValue("wid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	type row struct {
		ID     string           `json:"id"`
		Title  string           `json:"title"`
		Column model.CardColumn `json:"column"`
	}
	out := []row{}
	for _, c := range cards {
		out = append(out, row{c.ID, c.Title, c.Column})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- the planning tools (mcptools.CardBoard) ----

func cardRow(v cardView) mcptools.CardRow {
	r := mcptools.CardRow{ID: v.ID, Title: v.Title, Column: string(v.Column), Priority: v.Priority, Target: v.Target,
		Project: v.ProjectName, Members: usernames(v.People), Tags: v.Tags, Status: v.Status.Text, Progress: v.Status.Progress, Links: len(v.Links),
		Waiting: v.Waiting}
	for _, p := range v.People {
		if p.ID == v.Owner {
			r.Owner = p.Username
		}
	}
	for _, d := range v.Deps {
		r.DependsOn = append(r.DependsOn, d.ID)
	}
	return r
}

func usernames(ps []cardPerson) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Username)
	}
	return out
}

func (b board) Cards(projectID string) ([]mcptools.CardRow, error) {
	cards, err := b.sv.data.ListCards(projectID)
	if err != nil {
		return nil, err
	}
	vs, err := b.sv.cardViews(context.Background(), cards)
	if err != nil {
		return nil, err
	}
	out := make([]mcptools.CardRow, len(vs))
	for i, v := range vs {
		out[i] = cardRow(v)
	}
	return out, nil
}

func (b board) CreateCard(nc mcptools.NewCard) (mcptools.CardRow, error) {
	req := cardRequest{Title: &nc.Title, Description: &nc.Description, Priority: &nc.Priority, Target: &nc.Target, Tags: &nc.Tags}
	if len(nc.DependsOn) > 0 {
		req.DependsOn = &nc.DependsOn
	}
	if nc.Column != "" {
		req.Column = &nc.Column
	}
	if nc.Project != "" {
		p, err := b.Project(nc.Project)
		if err != nil {
			return mcptools.CardRow{}, err
		}
		req.Project = &p.ID
	}
	c, err := b.sv.createCard(req)
	if err != nil {
		return mcptools.CardRow{}, err
	}
	v, err := b.sv.cardView(context.Background(), c.ID)
	return cardRow(v), err
}

func (b board) LinkCard(cardID, kind, workID string) (mcptools.CardRow, error) {
	if err := b.sv.linkCard(cardID, kind, workID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return mcptools.CardRow{}, fmt.Errorf("no card %q or %s %q (see list_cards)", cardID, kind, workID)
		}
		return mcptools.CardRow{}, err
	}
	v, err := b.sv.cardView(context.Background(), cardID)
	return cardRow(v), err
}

func (b board) Card(id string) (mcptools.CardDetail, error) {
	v, err := b.sv.cardView(context.Background(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return mcptools.CardDetail{}, fmt.Errorf("no card %q (see list_cards)", id)
		}
		return mcptools.CardDetail{}, err
	}
	d := mcptools.CardDetail{CardRow: cardRow(v), Description: v.Description, Work: []mcptools.CardWork{}}
	for _, n := range v.NeededBy {
		d.NeededBy = append(d.NeededBy, n.ID)
	}
	for _, l := range v.Links {
		d.Work = append(d.Work, mcptools.CardWork{Kind: l.Kind, ID: l.ID, Title: l.Title, Project: l.Project, State: l.State})
	}
	ms, err := b.sv.data.CardComments(id)
	if err != nil {
		return d, err
	}
	for _, m := range ms {
		d.Comments = append(d.Comments, mcptools.CardNote{Author: m.AuthorName, Text: m.Text, At: m.CreatedAt})
	}
	return d, nil
}

func (b board) UpdateCard(id string, u mcptools.CardUpdate) (mcptools.CardRow, error) {
	if _, err := b.sv.data.GetCard(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return mcptools.CardRow{}, fmt.Errorf("no card %q (see list_cards)", id)
		}
		return mcptools.CardRow{}, err
	}
	req := cardRequest{Title: u.Title, Description: u.Description, Priority: u.Priority, Target: u.Target, Tags: u.Tags, DependsOn: u.DependsOn}
	if req.Title != nil || req.Description != nil || req.Priority != nil || req.Target != nil || req.Tags != nil || req.DependsOn != nil {
		if err := b.sv.updateCard(id, req); err != nil {
			return mcptools.CardRow{}, err
		}
	}
	if u.Column != nil {
		if err := b.sv.moveCard(id, *u.Column, u.Position); err != nil {
			return mcptools.CardRow{}, err
		}
	}
	v, err := b.sv.cardView(context.Background(), id)
	return cardRow(v), err
}

func (b board) CommentCard(id, text string) (mcptools.CardNote, error) {
	m, err := b.sv.addCardComment(id, text)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return mcptools.CardNote{}, fmt.Errorf("no card %q (see list_cards)", id)
		}
		return mcptools.CardNote{}, err
	}
	return mcptools.CardNote{Author: m.AuthorName, Text: m.Text, At: m.CreatedAt}, nil
}

func (b board) StartCard(id, project, kind string) (mcptools.CardStarted, error) {
	if kind == "" {
		kind = model.LinkTask
	}
	work, err := b.sv.startCard(id, project, kind, 0)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) && work == "" {
			return mcptools.CardStarted{}, fmt.Errorf("no card %q or project %q (see list_cards)", id, project)
		}
		return mcptools.CardStarted{}, err
	}
	v, err := b.sv.cardView(context.Background(), id)
	return mcptools.CardStarted{Card: cardRow(v), Kind: kind, ID: work}, err
}
