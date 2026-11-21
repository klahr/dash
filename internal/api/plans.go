package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// Write a project brief (once "Plan a new project"; the API keeps
// /api/plans): the user describes an idea and Claude interviews
// them about it, a few questions at a time, writing the project's brief as
// the answers come in. The plan is kept on this Dash (the hub), one user's
// in one org, so a draft survives a reload and can be picked up later.
//
// The same guide writes a feature brief (a plan of kind feature,
// model.PlanKinds): for a project, onto a Planning card. What differs by
// kind is in model.PlanKinds; the routes, the interview and its JSON are
// the same.
//
// Each interview round is one of Claude's short calls (claude.go): on a
// hub it runs on the caller's own runner, the first of theirs online, with
// that runner's `claude` login, like titles and splitting a task. A project
// plan needs no project, so no runner linked to one; a feature plan's round
// also sends its project's CLAUDE.md, README and file tree (featureContext),
// read from one of the project's runners.
//
// Launching a project plan is the browser's: it adds the project the way
// Add project does (the same calls), then commits BRIEF.md and a starter
// CLAUDE.md (POST /api/projects/{pid}/brief), starts a goal from the brief
// (POST /api/goals), and marks the plan launched. GET …/launch gives it the
// files and the goal's text. A feature plan launches here (POST …/feature):
// the brief becomes its card's description, and the card's Start makes a
// task or a goal from it, or the card waits in Planned. Nothing is
// committed.
//
// Planners write feature briefs too, on their own runner (plannerRoutes
// has the routes a feature brief uses): planService refuses them a hub
// that isn't one, and createPlan a project's brief, which only members
// launch (planners can't add projects). Their rounds are sent the
// project's context unless an owner turned that off (Organization →
// Planners; store.Accounts.PlannerContext), when they get only the card
// and the project's name.

func (s *Server) planRoutes(mux routeMux) {
	mux.HandleFunc("GET /api/plans", s.listPlans)
	mux.HandleFunc("POST /api/plans", s.createPlan)
	mux.HandleFunc("GET /api/plans/{id}", s.getPlan)
	mux.HandleFunc("PATCH /api/plans/{id}", s.updatePlan)
	mux.HandleFunc("DELETE /api/plans/{id}", s.deletePlan)
	mux.HandleFunc("POST /api/plans/{id}/interview", s.interviewPlan)
	mux.HandleFunc("GET /api/plans/{id}/launch", s.planLaunch)
	mux.HandleFunc("POST /api/plans/{id}/launched", s.planLaunched)
	mux.HandleFunc("POST /api/plans/{id}/feature", s.launchFeature)
	mux.HandleFunc("POST /api/projects/{pid}/brief", s.commitBrief)
}

var errNoPlanner = errors.New("writing a project brief is not available here")

// planService is the caller's service for plans, or answers r: plans are
// a person's.
func (s *Server) planService(w http.ResponseWriter, r *http.Request) (*service, bool) {
	sv, ok := s.service(w, r)
	if !ok {
		return nil, false
	}
	if sv.c.User == 0 {
		writeErr(w, fmt.Errorf("%w: %w", errBadRequest, store.ErrNoUser))
		return nil, false
	}
	if sv.c.Planner && !s.hub() {
		writeError(w, http.StatusServiceUnavailable, errPlannerNoHub.Error())
		return nil, false
	}
	return sv, true
}

// errPlannerNoHub: a planner's interview runs on their own runner, which
// needs a hub to connect to.
var errPlannerNoHub = errors.New("a planner's feature brief runs on their own runner, which connects to a hub; this Dash isn't one")

// errPlannerProjectBrief: planners can't add projects.
var errPlannerProjectBrief = fmt.Errorf("%w: a project's brief is for members, who add projects; write a feature brief from a Planning card", errBadRequest)

func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.planService(w, r)
	if !ok {
		return
	}
	plans, err := sv.data.ListPlans()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plans)
}

// planRequest is what POST /api/plans and PATCH /api/plans/{id} take.
type planRequest struct {
	Name  *string `json:"name"`
	Idea  *string `json:"idea"`
	Brief *string `json:"brief"`
	Step  *string `json:"step"`
}

func (req planRequest) apply(p *model.Plan) error {
	set(&p.Name, req.Name)
	set(&p.Idea, req.Idea)
	set(&p.Brief, req.Brief)
	set(&p.Step, req.Step)
	if err := model.CleanPlanDraft(p); err != nil {
		return fmt.Errorf("%w: %v", errBadRequest, err)
	}
	return nil
}

func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.planService(w, r)
	if !ok {
		return
	}
	if err := sv.writable(); err != nil {
		writeErr(w, err)
		return
	}
	// A plan's kind, and a feature plan's project and card, are set once,
	// here.
	var req struct {
		planRequest
		Kind    string `json:"kind"`
		Project string `json:"project"`
		Card    string `json:"card"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p := model.Plan{Kind: req.Kind, Rounds: []model.PlanRound{}}
	if sv.c.Planner && p.Kind != model.PlanFeature {
		writeErr(w, errPlannerProjectBrief)
		return
	}
	if p.Kind == model.PlanFeature {
		if err := sv.fromCard(&p, req.Card); err != nil {
			writeErr(w, err)
			return
		}
	} else if req.Project != "" || req.Card != "" {
		writeErr(w, fmt.Errorf("%w: only a feature's brief is for a project or a card", errBadRequest))
		return
	}
	if err := req.apply(&p); err != nil {
		writeErr(w, err)
		return
	}
	p.Topics = model.KindOf(p).NewTopics()
	if p.Feature() {
		if err := sv.featureCard(&p, req.Project); err != nil {
			writeErr(w, err)
			return
		}
	}
	p, err := sv.data.CreatePlan(p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// fromCard starts feature plan p from the card id ("" for none): its title
// is the feature's name, and its description the idea, or the brief to
// refine when it is longer than an idea. Given name and idea (applied
// after) take their place.
func (sv *service) fromCard(p *model.Plan, id string) error {
	if id == "" {
		return nil
	}
	c, err := sv.data.GetCard(id)
	if err != nil {
		return err
	}
	p.CardID, p.ProjectID = c.ID, c.ProjectID
	p.Name = clipBrief(strings.TrimSpace(c.Title), model.MaxPlanName)
	switch desc := strings.TrimSpace(c.Description); {
	case desc == "":
		p.Idea = c.Title
	case len(desc) <= model.MaxPlanIdea:
		p.Idea = desc
	default:
		p.Idea = clipBrief(c.Title, model.MaxPlanIdea)
		p.Brief = clipBrief(desc, model.MaxPlanBrief)
	}
	return nil
}

// featureCard sets feature plan p's project (project, or its card's) and
// makes its card when it has none: in Ideas, named after the plan, with the
// idea as its description, until the brief takes its place.
func (sv *service) featureCard(p *model.Plan, project string) error {
	if project == "" {
		project = p.ProjectID
	}
	if project == "" {
		return fmt.Errorf("%w: choose the project the feature is for", errBadRequest)
	}
	pr, err := sv.data.GetProject(project)
	if err != nil {
		return err
	}
	p.ProjectID = pr.ID
	if p.CardID != "" {
		return nil
	}
	title := featureTitle(*p)
	if title == "" {
		return fmt.Errorf("%w: describe the idea first", errBadRequest)
	}
	col := string(model.CardIdeas)
	c, err := sv.createCard(cardRequest{Title: &title, Description: &p.Idea, Project: &p.ProjectID, Column: &col})
	if err != nil {
		return err
	}
	p.CardID, p.CardMade = c.ID, true
	return nil
}

// featureTitle is a card's title for feature plan p: its name, its brief's
// title or its idea's start ("" for none of them).
func featureTitle(p model.Plan) string {
	t := planName(p)
	if p.Name == "" && p.Brief == "" && strings.TrimSpace(p.Idea) == "" {
		t = ""
	}
	return clipBrief(t, model.MaxCardTitle)
}

func (s *Server) getPlan(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.planService(w, r)
	if !ok {
		return
	}
	p, err := sv.data.GetPlan(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// errPlanLaunched: a launched plan is a record of what was started.
var errPlanLaunched = fmt.Errorf("%w: this plan is launched already", errConflict)

func (s *Server) updatePlan(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.planService(w, r)
	if !ok {
		return
	}
	var req planRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := sv.data.UpdatePlan(r.PathValue("id"), func(p *model.Plan) error {
		if p.LaunchedAt != nil {
			return errPlanLaunched
		}
		return req.apply(p)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deletePlan(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.planService(w, r)
	if !ok {
		return
	}
	if err := sv.data.DeletePlan(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// interviewing holds the plans whose interview round is running: one at a
// time per plan (a second would answer the same questions twice).
var interviewing sync.Map

// interviewPlan handles POST /api/plans/{id}/interview: the answers to the
// latest round's questions (and anything else the user said), then Claude's
// next round. The first call, on a plan without rounds, starts the
// interview from the idea. The answers are kept only with Claude's reply,
// so after a failure the same answers can be sent again.
func (s *Server) interviewPlan(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.planService(w, r)
	if !ok {
		return
	}
	if err := sv.writable(); err != nil {
		writeErr(w, err)
		return
	}
	var req struct {
		Answers []model.PlanAnswer `json:"answers"`
		Message string             `json:"message"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	id := r.PathValue("id")
	if _, busy := interviewing.LoadOrStore(id, true); busy {
		writeErr(w, fmt.Errorf("%w: Claude is still on the last answers", errConflict))
		return
	}
	defer interviewing.Delete(id)
	p, err := sv.data.GetPlan(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	next, err := answerRound(p, req.Answers, req.Message)
	if err != nil {
		writeErr(w, err)
		return
	}
	kind := model.KindOf(next)
	if sv.c.Planner && !next.Feature() {
		writeErr(w, errPlannerProjectBrief)
		return
	}
	var pc *agent.PlanContext
	if next.Feature() {
		pc = sv.featureContext(r.Context(), next.ProjectID)
	}
	out, err := sv.planCall(r.Context(), kind.ID, agent.PlanInputWith(next, pc))
	if errors.Is(err, errNoPlanner) || errors.Is(err, errNoClaudeRunner) || errors.Is(err, tunnel.ErrOffline) || errors.Is(err, errOldRunner) {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		writeErr(w, fmt.Errorf("the interview round failed: %w", err))
		return
	}
	turn, err := agent.ParsePlanTurn(out)
	if err == nil {
		turn, err = kind.CleanTurn(turn, next.Topics)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error()+". Try again.")
		return
	}
	p, err = sv.data.UpdatePlan(id, func(p *model.Plan) error {
		if p.LaunchedAt != nil {
			return errPlanLaunched
		}
		p.Rounds = append(next.Rounds, model.PlanRound{Note: turn.Note, Questions: turn.Questions, At: now()})
		p.Topics, p.Brief, p.Open, p.Verdict = turn.Topics, turn.Brief, turn.Open, turn.Verdict
		if steps := model.KindOf(*p).Steps; p.Step == steps[0] {
			p.Step = steps[1]
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// answerRound is p with its latest round answered: answers to that round's
// questions only (the last answer to a question counts), and message.
// A plan without rounds is returned as it is, to start the interview.
func answerRound(p model.Plan, answers []model.PlanAnswer, message string) (model.Plan, error) {
	switch {
	case p.LaunchedAt != nil:
		return p, errPlanLaunched
	case strings.TrimSpace(p.Idea) == "":
		return p, fmt.Errorf("%w: describe the idea first", errBadRequest)
	case len(p.Rounds) == 0:
		return p, nil
	case len(p.Rounds) >= model.MaxPlanRounds:
		return p, fmt.Errorf("%w: that's %d rounds of questions: edit the brief yourself, or launch it", errBadRequest, model.MaxPlanRounds)
	}
	p.Rounds = slices.Clone(p.Rounds)
	last := &p.Rounds[len(p.Rounds)-1]
	var kept []model.PlanAnswer
	for _, a := range answers {
		a.Text = strings.TrimSpace(a.Text)
		if !slices.ContainsFunc(last.Questions, func(q model.PlanQuestion) bool { return q.ID == a.Question }) {
			continue
		}
		if len(a.Text) > model.MaxPlanAnswer {
			return p, fmt.Errorf("%w: an answer is longer than %d characters", errBadRequest, model.MaxPlanAnswer)
		}
		if a.Text == "" {
			a.Skipped, a.Default = true, false
		}
		kept = slices.DeleteFunc(kept, func(x model.PlanAnswer) bool { return x.Question == a.Question })
		kept = append(kept, a)
	}
	message = strings.TrimSpace(message)
	if len(message) > model.MaxPlanAnswer {
		return p, fmt.Errorf("%w: the message is longer than %d characters", errBadRequest, model.MaxPlanAnswer)
	}
	if len(kept) == 0 && message == "" {
		return p, fmt.Errorf("%w: answer a question, or say what to change", errBadRequest)
	}
	at := now()
	last.Answers, last.Message, last.Answered = kept, message, &at
	return p, nil
}

// errOldRunner: the caller's runner is from before feature briefs.
var errOldRunner = errors.New("your runner is from before feature briefs: update Dash on it to write one")

// planCall runs one interview round of a plan of kind: here, or on the
// caller's runner (POST /api/hub/claude/plan for a project's, as before
// kinds; plan-<kind> for another kind's).
func (sv *service) planCall(ctx context.Context, kind, input string) (string, error) {
	if !sv.s.hub() {
		if sv.s.Small == nil {
			return "", errNoPlanner
		}
		return sv.s.Small.Plan(ctx, kind, input)
	}
	call := "plan"
	if kind != model.PlanProject {
		call += "-" + kind
	}
	out, err := sv.smallCall(ctx, call, input)
	var ce *tunnel.CallError
	if call != "plan" && errors.As(err, &ce) && ce.Status == http.StatusNotFound {
		return "", errOldRunner
	}
	return out, err
}

// featureContext is what a feature plan's interviewer reads of its project
// (agent.PlanContext): CLAUDE.md, the README and the files on its default
// branch, here or, on a hub, read on one of the project's runners. What
// can't be read is left out, and the round goes on without it.
func (sv *service) featureContext(ctx context.Context, projectID string) *agent.PlanContext {
	p, err := sv.data.GetProject(projectID)
	if err != nil {
		return nil
	}
	pc := &agent.PlanContext{Project: p.Name}
	if sv.c.Planner && !sv.plannerContext() {
		return pc // the owners keep the project's files from planners' rounds
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	read := func(name string) string {
		var f fileContent
		var err error
		if sv.s.hub() {
			err = sv.callRunner(ctx, p, http.MethodGet, "/file?path="+url.QueryEscape(name), nil, &f)
		} else {
			f, _, err = sv.s.branchFile(ctx, p, name)
		}
		if err != nil || f.Content == nil {
			return ""
		}
		return *f.Content
	}
	if sv.s.hub() {
		var out struct {
			Files []string `json:"files"`
		}
		if err := sv.callRunner(ctx, p, http.MethodGet, "/files", nil, &out); err != nil {
			slog.Debug("feature brief: the project's files", "project", p.ID, "err", err)
			return pc
		}
		pc.Files = out.Files
	} else if sv.s.Git != nil {
		if pc.Files, err = sv.s.Git.Files(ctx, p.ID, p.Path, p.Base()); err != nil {
			return pc
		}
	}
	for _, f := range pc.Files {
		switch base := strings.ToLower(f); {
		case f == "CLAUDE.md":
			pc.ClaudeMD = read(f)
		case pc.Readme == "" && (base == "readme" || strings.HasPrefix(base, "readme.")):
			pc.Readme = read(f)
		}
	}
	return pc
}

// plannerContext reports whether the caller's org sends its planners'
// feature brief rounds the project's context (on unless an owner turned it
// off; off if the setting can't be read, which may be the owners' "off").
func (sv *service) plannerContext() bool {
	on, err := sv.s.Store.Accounts().PlannerContext(sv.c.Org)
	if err != nil {
		slog.Warn("planner context setting", "org", sv.c.Org, "err", err)
		return false
	}
	return on
}

// planLaunchView is what launching a plan commits and starts: BRIEF.md and
// CLAUDE.md, and the goal (its description for when the files are
// committed, and for when they couldn't be).
type planLaunchView struct {
	Brief    string `json:"brief"`
	ClaudeMD string `json:"claude_md"`
	Goal     struct {
		Title              string `json:"title"`
		Description        string `json:"description"`
		DescriptionNoFiles string `json:"description_no_files"`
	} `json:"goal"`
}

// planLaunch handles GET /api/plans/{id}/launch.
func (s *Server) planLaunch(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.planService(w, r)
	if !ok {
		return
	}
	p, err := sv.data.GetPlan(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if p.Feature() {
		writeErr(w, errFeatureLaunch)
		return
	}
	if strings.TrimSpace(p.Brief) == "" {
		writeErr(w, fmt.Errorf("%w: the plan has no brief yet", errBadRequest))
		return
	}
	writeJSON(w, http.StatusOK, launchView(p))
}

func launchView(p model.Plan) planLaunchView {
	var v planLaunchView
	name := planName(p)
	v.Brief = strings.TrimSpace(p.Brief) + "\n"
	v.ClaudeMD = starterClaudeMD(name, p.Brief)
	v.Goal.Title = clipBrief("Build the first version of "+name, model.MaxGoalTitle)
	lead := "Build the first version of " + name + ", as BRIEF.md at the root of the repository describes it; CLAUDE.md has its stack, conventions and checks.\n\n" +
		"Plan the first tasks: start with the project's skeleton (the stack, a way to run it, and the checks under \"How to run and check\"), " +
		"then the core features, one task each. Leave what the brief puts out of scope, and ask me about its open questions when a task depends on one."
	v.Goal.Description = clipBrief(lead+"\n\nThe brief, as committed:\n\n"+v.Brief, model.MaxGoalDescription)
	noFiles := lead + "\n\nBRIEF.md and CLAUDE.md aren't in the repository yet: make adding them the first task, with the brief below as BRIEF.md " +
		"and the starter CLAUDE.md after it.\n\n## BRIEF.md\n\n" + v.Brief + "\n## CLAUDE.md\n\n```markdown\n" + v.ClaudeMD + "```\n"
	v.Goal.DescriptionNoFiles = clipBrief(noFiles, model.MaxGoalDescription)
	return v
}

// clipBrief cuts s to at most n bytes, at a line when it can.
func clipBrief(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n-len("\n…")]
	if i := strings.LastIndexByte(s, '\n'); i > n/2 {
		s = s[:i]
	}
	return strings.ToValidUTF8(s, "") + "\n…"
}

// planName is the project's name: the plan's, or its brief's title, or
// the idea's start.
func planName(p model.Plan) string {
	if p.Name != "" {
		return p.Name
	}
	for l := range strings.SplitSeq(p.Brief, "\n") {
		if t, ok := strings.CutPrefix(strings.TrimSpace(l), "# "); ok && strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t)
		}
	}
	if t := plainTitle(p.Idea); t != "" {
		return t
	}
	return "the " + model.KindOf(p).Prompt.Noun
}

// briefSection is the body of the brief's "## title" section ("" if it
// has none), trimmed.
func briefSection(brief, title string) string {
	var out []string
	in := false
	for l := range strings.SplitSeq(brief, "\n") {
		if h, ok := strings.CutPrefix(strings.TrimSpace(l), "## "); ok {
			in = strings.EqualFold(strings.TrimSpace(h), title)
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(l), "# ") {
			in = false
			continue
		}
		if in {
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// starterClaudeMD is the CLAUDE.md a planned project starts with, from
// its brief: what it is, its stack and checks, and the conventions every
// task follows. Made here, not by Claude, so it says the same as the
// brief the user saw.
func starterClaudeMD(name, brief string) string {
	or := func(s, none string) string {
		if s == "" {
			return none
		}
		return s
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: rules for Claude\n\n", name)
	b.WriteString("[BRIEF.md](BRIEF.md) describes what this project is, for whom, and what is in and out of scope. " +
		"Read it before you plan a change, and update it in the same change when a decision in it changes.\n\n")
	b.WriteString("## Stack\n\n")
	b.WriteString(or(briefSection(brief, "Platform & stack"), "Not decided yet: see the open questions in BRIEF.md.") + "\n\n")
	b.WriteString("## How to run and check\n\n")
	b.WriteString(or(briefSection(brief, "How to run and check"), "Add the commands to install, run and test the project here once it has them.") + "\n\n")
	b.WriteString("## Conventions\n\n" +
		"- Keep each change small and focused, and leave the project working after it.\n" +
		"- Match the style of the code around you; prefer the simplest code that works.\n" +
		"- Add or update tests with the code they test.\n" +
		"- Don't build what BRIEF.md puts out of scope, and don't settle its open questions on your own: ask.\n" +
		"- Keep the README's setup steps true when you change how to run the project.\n\n")
	b.WriteString("## Before you finish\n\nRun the checks under \"How to run and check\" and fix what they report. Say in your summary which ones you ran.\n")
	return b.String()
}

// planLaunched handles POST /api/plans/{id}/launched: the browser launched
// the plan as project_id (on prefix's Dash) with goal goal_id. The plan
// leaves the drafts.
func (s *Server) planLaunched(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.planService(w, r)
	if !ok {
		return
	}
	var req struct {
		ProjectID string `json:"project_id"`
		GoalID    string `json:"goal_id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.ProjectID == "" {
		writeErr(w, fmt.Errorf("%w: project_id is required", errBadRequest))
		return
	}
	p, err := sv.data.UpdatePlan(r.PathValue("id"), func(p *model.Plan) error {
		if p.LaunchedAt != nil {
			return errPlanLaunched
		}
		if p.Feature() {
			return errFeatureLaunch
		}
		at := now()
		p.ProjectID, p.GoalID, p.LaunchedAt, p.Step = req.ProjectID, req.GoalID, &at, model.PlanSteps[len(model.PlanSteps)-1]
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// briefFiles are the files a planned project starts with.
var briefFiles = []string{"BRIEF.md", "CLAUDE.md"}

// commitBrief handles POST /api/projects/{pid}/brief: {"brief",
// "claude_md"} committed as BRIEF.md and CLAUDE.md straight onto the
// project's default branch (queue.MergeQueue.CommitFile), on a hub by one of its runners. A file the branch has already is
// kept as it is. A project that lands changes through pull requests gets
// nothing committed (400): the goal adds the files then.
func (s *Server) commitBrief(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.project(w, r)
	if !ok {
		return
	}
	if err := sv.writable(); err != nil {
		writeErr(w, err)
		return
	}
	var req struct {
		Brief    string `json:"brief"`
		ClaudeMD string `json:"claude_md"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if strings.TrimSpace(req.Brief) == "" || len(req.Brief) > model.MaxPlanBrief || len(req.ClaudeMD) > model.MaxPlanBrief {
		writeErr(w, fmt.Errorf("%w: a brief of 1 to %d characters is required", errBadRequest, model.MaxPlanBrief))
		return
	}
	type result struct {
		Committed []string `json:"committed"`
		Kept      []string `json:"kept"`
	}
	out := result{Committed: []string{}, Kept: []string{}}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if s.hub() {
		if p.UsesPR() {
			writeErr(w, fmt.Errorf("%w: %s lands changes through pull requests", errBadRequest, p.Name))
			return
		}
		if err := sv.callRunner(ctx, p, http.MethodPost, "/brief", req, &out); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if s.Merges == nil {
		writeErr(w, fmt.Errorf("%w: this Dash doesn't commit files", errBadRequest))
		return
	}
	contents := map[string]string{"BRIEF.md": req.Brief, "CLAUDE.md": req.ClaudeMD}
	for _, name := range briefFiles {
		if strings.TrimSpace(contents[name]) == "" {
			continue
		}
		_, _, err := s.Merges.CommitFile(ctx, p, name, "", contents[name], "Add "+name+" from the project plan")
		switch {
		case errors.Is(err, queue.ErrFileChanged):
			out.Kept = append(out.Kept, name) // there already: theirs stays
		case errors.Is(err, queue.ErrUsesPR):
			writeErr(w, fmt.Errorf("%w: %w", errBadRequest, err))
			return
		case err != nil:
			writeErr(w, err)
			return
		default:
			out.Committed = append(out.Committed, name)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// errFeatureLaunch: a feature plan launches with POST …/feature.
var errFeatureLaunch = fmt.Errorf("%w: a feature brief goes onto its card: POST /api/plans/{id}/feature", errBadRequest)

// launching holds the plans being launched: one launch at a time per plan
// (a second would start the same work twice).
var launching sync.Map

// launchFeature handles POST /api/plans/{id}/feature: {"start": "task",
// "goal" or "planned"}. The brief becomes the plan's card's description (a
// new card in Ideas if the card is gone), and then the card's Start makes a
// task or a goal from it in the plan's project (startCard: linked to the
// card, which moves to In progress), or, for "planned", the card moves to
// Planned. The plan leaves the drafts. It answers {"plan", "card"}.
func (s *Server) launchFeature(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.planService(w, r)
	if !ok {
		return
	}
	if err := sv.writable(); err != nil {
		writeErr(w, err)
		return
	}
	var req struct {
		Start string `json:"start"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Start != model.LinkTask && req.Start != model.LinkGoal && req.Start != string(model.CardPlanned) {
		writeErr(w, fmt.Errorf("%w: start a task or a goal, or save the card to Planned", errBadRequest))
		return
	}
	if sv.c.Planner && req.Start == model.LinkGoal {
		// A planner's goal needs its owner, a developer: Start… on the card asks for one.
		writeErr(w, fmt.Errorf("%w: start a task, or save the card to Planned and start a goal from the card's Start…, where you choose its owner", errBadRequest))
		return
	}
	id := r.PathValue("id")
	if _, busy := launching.LoadOrStore(id, true); busy {
		writeErr(w, fmt.Errorf("%w: this brief is being started already", errConflict))
		return
	}
	defer launching.Delete(id)
	p, err := sv.data.GetPlan(id)
	switch {
	case err != nil:
	case !p.Feature():
		err = fmt.Errorf("%w: this is a project's brief: launch it from Add project", errBadRequest)
	case p.LaunchedAt != nil:
		err = errPlanLaunched
	case strings.TrimSpace(p.Brief) == "":
		err = fmt.Errorf("%w: the plan has no brief yet", errBadRequest)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	cardID, err := sv.briefOnCard(p)
	if err != nil {
		writeErr(w, err)
		return
	}
	var work string
	if req.Start == string(model.CardPlanned) {
		err = sv.planCard(cardID)
	} else {
		work, err = sv.startCard(cardID, p.ProjectID, req.Start, 0, 0)
	}
	if err != nil && work == "" {
		// The card has the brief: the plan stays a draft, to start it again.
		_, _ = sv.data.UpdatePlan(id, func(x *model.Plan) error { x.CardID = cardID; return nil })
		writeErr(w, err)
		return
	}
	p, err = sv.data.UpdatePlan(id, func(x *model.Plan) error {
		at := now()
		x.CardID, x.LaunchedAt, x.Step = cardID, &at, model.KindOf(*x).Steps[len(model.KindOf(*x).Steps)-1]
		switch req.Start {
		case model.LinkTask:
			x.TaskID = work
		case model.LinkGoal:
			x.GoalID = work
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	v, err := sv.cardView(r.Context(), cardID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": p, "card": v})
}

// briefOnCard puts feature plan p's brief on its card as the description,
// and returns the card's id: p's card, or a new one in Ideas when p has
// none or it was deleted since. A card the plan made is named after the
// brief too, and a card of no project gets the plan's.
func (sv *service) briefOnCard(p model.Plan) (string, error) {
	desc := clipBrief(strings.TrimSpace(p.Brief), model.MaxCardDescription)
	title := featureTitle(p)
	if p.CardID != "" {
		c, err := sv.data.GetCard(p.CardID)
		if err == nil {
			req := cardRequest{Description: &desc}
			if p.CardMade && title != "" {
				req.Title = &title
			}
			if c.ProjectID == "" {
				req.Project = &p.ProjectID
			}
			return c.ID, sv.updateCard(c.ID, req)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}
	col := string(model.CardIdeas)
	c, err := sv.createCard(cardRequest{Title: &title, Description: &desc, Project: &p.ProjectID, Column: &col})
	return c.ID, err
}

// planCard moves a card in Ideas to Planned: its brief is ready, for
// someone to start later. A card further on stays where it is.
func (sv *service) planCard(id string) error {
	c, err := sv.data.GetCard(id)
	if err != nil || c.Column != model.CardIdeas {
		return err
	}
	return sv.moveCard(id, string(model.CardPlanned), nil)
}
