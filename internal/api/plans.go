package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

// Plan a new project: the user describes an idea and Claude interviews
// them about it, a few questions at a time, writing the project's brief as
// the answers come in. The plan is kept on this Dash (the hub), one user's
// in one org, so a draft survives a reload and can be picked up later.
//
// Each interview round is one of Claude's short calls (claude.go): on a
// hub it runs on the caller's own runner, the first of theirs online, with
// that runner's `claude` login, like titles and splitting a task. It needs
// no project, so no runner linked to one.
//
// Launching is the browser's: it adds the project the way Add project does
// (the same calls), then commits BRIEF.md and a starter CLAUDE.md
// (POST /api/projects/{pid}/brief), starts a goal from the brief (POST
// /api/goals), and marks the plan launched. GET …/launch gives it the
// files and the goal's text.

func (s *Server) planRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/plans", s.listPlans)
	mux.HandleFunc("POST /api/plans", s.createPlan)
	mux.HandleFunc("GET /api/plans/{id}", s.getPlan)
	mux.HandleFunc("PATCH /api/plans/{id}", s.updatePlan)
	mux.HandleFunc("DELETE /api/plans/{id}", s.deletePlan)
	mux.HandleFunc("POST /api/plans/{id}/interview", s.interviewPlan)
	mux.HandleFunc("GET /api/plans/{id}/launch", s.planLaunch)
	mux.HandleFunc("POST /api/plans/{id}/launched", s.planLaunched)
	mux.HandleFunc("POST /api/projects/{pid}/brief", s.commitBrief)
}

var errNoPlanner = errors.New("planning a project is not available here")

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
	return sv, true
}

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
	var req planRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p := model.Plan{Topics: model.NewPlanTopics(), Rounds: []model.PlanRound{}}
	if err := req.apply(&p); err != nil {
		writeErr(w, err)
		return
	}
	p, err := sv.data.CreatePlan(p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
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
	out, err := sv.planCall(r.Context(), agent.PlanInput(next))
	if errors.Is(err, errNoPlanner) || errors.Is(err, errNoClaudeRunner) || errors.Is(err, tunnel.ErrOffline) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeErr(w, fmt.Errorf("the interview round failed: %w", err))
		return
	}
	turn, err := agent.ParsePlanTurn(out)
	if err == nil {
		turn, err = model.CleanPlanTurn(turn, next.Topics)
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error() + ". Try again."})
		return
	}
	p, err = sv.data.UpdatePlan(id, func(p *model.Plan) error {
		if p.LaunchedAt != nil {
			return errPlanLaunched
		}
		p.Rounds = append(next.Rounds, model.PlanRound{Note: turn.Note, Questions: turn.Questions, At: now()})
		p.Topics, p.Brief, p.Open, p.Verdict = turn.Topics, turn.Brief, turn.Open, turn.Verdict
		if p.Step == model.PlanSteps[0] {
			p.Step = model.PlanSteps[1]
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

// planCall runs one interview round: here, or on the caller's runner.
func (sv *service) planCall(ctx context.Context, input string) (string, error) {
	if !sv.s.hub() {
		if sv.s.Small == nil {
			return "", errNoPlanner
		}
		return sv.s.Small.Plan(ctx, input)
	}
	return sv.smallCall(ctx, "plan", input)
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
	return "the project"
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
// project's default branch (queue.MergeQueue.CommitFile, as the checklist
// is), on a hub by one of its runners. A file the branch has already is
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
