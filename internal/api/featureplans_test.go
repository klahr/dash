package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// featureRound is an interviewer's reply to a feature plan: a question on
// a feature topic, and on a project's (dropped), and the brief.
const featureRound = `{"note":"Who is it for?",
	"questions":[{"id":"r1q1","topic":"problem","text":"Who exports?","default":"Admins"},
		{"id":"r1q2","topic":"deploy","text":"A project's topic","default":"x"}],
	"topics":[{"id":"problem","status":"done"},{"id":"users","status":"done"}],
	"brief":"# CSV export\nExport the board as CSV.\n## Done when\nA test reads the file back.",
	"open_questions":[],"verdict":{"ready":true,"summary":"Enough."}}`

// featureServer is checklistServer (a project with a repository here) with
// a CLAUDE.md and README.md on its branch and a planner whose rounds answer
// featureRound.
func featureServer(t *testing.T) (*Server, model.Project, http.Handler, *fakePlanner) {
	t.Helper()
	s, p, h := checklistServer(t, "")
	for name, text := range map[string]string{"CLAUDE.md": "# Rules\nUse Go.\n", "README.md": "# Web\nThe board.\n", "web/app.js": "x\n"} {
		full := filepath.Join(p.Path, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, p.Path, "add", ".")
	gitIn(t, p.Path, "commit", "-qm", "files")
	fp := &fakePlanner{replies: []string{featureRound}}
	s.Small = fp
	return s, p, h, fp
}

// The interview of a feature plan: its kind's steps and topics, the
// project's files with each round, and a feature's prompt.
func TestFeaturePlanInterview(t *testing.T) {
	_, p, h, fp := featureServer(t)

	if w := send(h, "POST", "/api/plans", `{"kind":"feature","idea":"Export the board"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "project") {
		t.Errorf("without a project: %d %s", w.Code, w.Body)
	}
	if w := send(h, "POST", "/api/plans", `{"kind":"song","idea":"x"}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown kind: %d %s", w.Code, w.Body)
	}
	if w := send(h, "POST", "/api/plans", `{"idea":"x","project":"`+p.ID+`"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a project's brief for a project: %d %s", w.Code, w.Body)
	}

	// Without a card, the plan makes one: in Ideas, the idea its description.
	pl := decodePlan(t, send(h, "POST", "/api/plans", `{"kind":"feature","project":"`+p.ID+`","idea":"Export the board as CSV"}`), http.StatusCreated)
	if pl.Kind != model.PlanFeature || pl.ProjectID != p.ID || pl.CardID == "" || !pl.CardMade || pl.Step != "idea" ||
		len(pl.Topics) != len(model.FeatureTopics) || pl.Topics[0].ID != "problem" {
		t.Fatalf("created: %+v", pl)
	}
	c := decodeCard(t, send(h, "GET", "/api/planning/cards/"+pl.CardID, ""))
	if c.Column != model.CardIdeas || c.ProjectID != p.ID || c.Description != "Export the board as CSV" || c.Title != "Export the board as CSV" {
		t.Errorf("its card: %+v", c.Card)
	}
	if w := send(h, "PATCH", "/api/plans/"+pl.ID, `{"step":"repo"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a project's step: %d %s", w.Code, w.Body)
	}

	pl = decodePlan(t, send(h, "POST", "/api/plans/"+pl.ID+"/interview", `{}`), http.StatusOK)
	in := fp.input(0)
	for _, want := range []string{`"project": {`, `"name": "web"`, "Use Go.", "The board.", `"web/app.js"`, `"idea": "Export the board as CSV"`} {
		if !strings.Contains(in, want) {
			t.Errorf("the round lacks %q:\n%s", want, in)
		}
	}
	if pl.Step != "interview" || len(pl.Rounds) != 1 || len(pl.Rounds[0].Questions) != 2 || pl.Rounds[0].Questions[1].Topic != "" {
		t.Errorf("after the round: %+v", pl.Rounds)
	}
	if pl.Topics[0].Status != model.PlanTopicDone || len(pl.Topics) != len(model.FeatureTopics) {
		t.Errorf("topics: %+v", pl.Topics)
	}
	// A feature's launch isn't a project's.
	if w := send(h, "GET", "/api/plans/"+pl.ID+"/launch", ""); w.Code != http.StatusBadRequest {
		t.Errorf("project launch view: %d %s", w.Code, w.Body)
	}
	if w := send(h, "POST", "/api/plans/"+pl.ID+"/launched", `{"project_id":"p"}`); w.Code != http.StatusBadRequest {
		t.Errorf("launched as a project: %d %s", w.Code, w.Body)
	}
}

// A brief for an existing card: its title is the name, its description the
// idea, or the brief to refine when it is longer than an idea.
func TestFeaturePlanFromCard(t *testing.T) {
	_, p, h, _ := featureServer(t)
	card := func(body string) cardView {
		t.Helper()
		return decodeCard(t, send(h, "POST", "/api/planning/cards", body))
	}
	short := card(`{"title":"Dark mode","description":"Follow the system's theme."}`)
	pl := decodePlan(t, send(h, "POST", "/api/plans", `{"kind":"feature","card":"`+short.ID+`","project":"`+p.ID+`"}`), http.StatusCreated)
	if pl.CardID != short.ID || pl.CardMade || pl.Name != "Dark mode" || pl.Idea != "Follow the system's theme." || pl.Brief != "" || pl.ProjectID != p.ID {
		t.Errorf("from a card: %+v", pl)
	}
	// A card of no project needs one named.
	if w := send(h, "POST", "/api/plans", `{"kind":"feature","card":"`+short.ID+`"}`); w.Code != http.StatusBadRequest {
		t.Errorf("no project: %d %s", w.Code, w.Body)
	}
	long := strings.Repeat("A long description. ", 150)
	b, _ := json.Marshal(map[string]string{"title": "Search", "description": long, "project": p.ID})
	lc := card(string(b))
	pl = decodePlan(t, send(h, "POST", "/api/plans", `{"kind":"feature","card":"`+lc.ID+`"}`), http.StatusCreated)
	if pl.ProjectID != p.ID || pl.Idea != "Search" || pl.Brief != strings.TrimSpace(long) {
		t.Errorf("from a long card: idea %q, brief %d bytes, project %q", pl.Idea, len(pl.Brief), pl.ProjectID)
	}
	if w := send(h, "POST", "/api/plans", `{"kind":"feature","card":"nope","project":"`+p.ID+`"}`); w.Code != http.StatusNotFound {
		t.Errorf("no such card: %d %s", w.Code, w.Body)
	}
}

// Launching a feature brief: onto the card, then a task or a goal from the
// card (linked to it), or the card to Planned. Nothing is committed.
func TestFeaturePlanLaunch(t *testing.T) {
	s, p, h, _ := featureServer(t)
	head := gitIn(t, p.Path, "rev-parse", "main")
	ready := func(body string) model.Plan {
		t.Helper()
		pl := decodePlan(t, send(h, "POST", "/api/plans", body), http.StatusCreated)
		return decodePlan(t, send(h, "POST", "/api/plans/"+pl.ID+"/interview", `{}`), http.StatusOK)
	}
	type launched struct {
		Plan model.Plan `json:"plan"`
		Card cardView   `json:"card"`
	}
	launch := func(pl model.Plan, start string, code int) launched {
		t.Helper()
		w := send(h, "POST", "/api/plans/"+pl.ID+"/feature", `{"start":"`+start+`"}`)
		var out launched
		if w.Code != code || (code == http.StatusOK && json.Unmarshal(w.Body.Bytes(), &out) != nil) {
			t.Fatalf("launch %s: %d %s", start, w.Code, w.Body)
		}
		return out
	}

	// A task: the card has the brief (and the plan made it, so the brief's
	// title), and the task its title and the brief.
	pl := ready(`{"kind":"feature","project":"` + p.ID + `","idea":"Export"}`)
	launch(pl, "nothing", http.StatusBadRequest)
	out := launch(pl, "task", http.StatusOK)
	c := out.Card
	if c.Title != "CSV export" || !strings.HasPrefix(c.Description, "# CSV export") || c.Column != model.CardInProgress ||
		len(c.Links) != 1 || c.Links[0].Kind != model.LinkTask || c.Links[0].ID != out.Plan.TaskID {
		t.Errorf("card after a task: %+v %+v", c.Card, c.Links)
	}
	td, err := raw(s).GetTodo(out.Plan.TaskID)
	if err != nil || td.ProjectID != p.ID || !strings.Contains(td.Prompt, "A test reads the file back.") {
		t.Errorf("the task: %+v %v", td, err)
	}
	if out.Plan.LaunchedAt == nil || out.Plan.Step != "start" {
		t.Errorf("plan: %+v", out.Plan)
	}
	launch(pl, "task", http.StatusConflict)
	if w := send(h, "GET", "/api/plans", ""); strings.Contains(w.Body.String(), pl.ID) {
		t.Errorf("a launched plan in the drafts: %s", w.Body)
	}

	// A goal, for an existing card: its title stays.
	ec := decodeCard(t, send(h, "POST", "/api/planning/cards", `{"title":"Exports","description":"Export things","project":"`+p.ID+`"}`))
	pl = ready(`{"kind":"feature","card":"` + ec.ID + `"}`)
	out = launch(pl, "goal", http.StatusOK)
	c = out.Card
	if c.ID != ec.ID || c.Title != "Exports" || !strings.HasPrefix(c.Description, "# CSV export") || len(c.Links) != 1 || c.Links[0].Kind != model.LinkGoal || out.Plan.GoalID == "" {
		t.Errorf("card after a goal: %+v %+v", c.Card, c.Links)
	}
	if g, err := raw(s).GetGoal(out.Plan.GoalID); err != nil || g.ProjectID != p.ID || !strings.Contains(g.Description, "CSV export") {
		t.Errorf("the goal: %+v %v", g, err)
	}

	// Planned: the card has the brief, moves on from Ideas, and has no work.
	pl = ready(`{"kind":"feature","project":"` + p.ID + `","idea":"Export again"}`)
	out = launch(pl, "planned", http.StatusOK)
	if c := out.Card; c.Column != model.CardPlanned || len(c.Links) != 0 || !strings.HasPrefix(c.Description, "# CSV export") || out.Plan.TaskID != "" || out.Plan.GoalID != "" {
		t.Errorf("planned: %+v %+v", c.Card, out.Plan)
	}

	// A card deleted meanwhile: the brief goes onto a new one.
	pl = ready(`{"kind":"feature","project":"` + p.ID + `","idea":"Export once more"}`)
	if w := send(h, "DELETE", "/api/planning/cards/"+pl.CardID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete card: %d %s", w.Code, w.Body)
	}
	out = launch(pl, "planned", http.StatusOK)
	if out.Card.ID == pl.CardID || out.Plan.CardID != out.Card.ID || out.Card.ProjectID != p.ID {
		t.Errorf("after the card went: %+v, plan card %q", out.Card.Card, out.Plan.CardID)
	}

	if got := gitIn(t, p.Path, "rev-parse", "main"); got != head {
		t.Error("launching a feature brief committed")
	}
	// A project's plan doesn't launch as a feature.
	pp := decodePlan(t, send(h, "POST", "/api/plans", `{"idea":"A project"}`), http.StatusCreated)
	launch(pp, "task", http.StatusBadRequest)
}

// A planner's guide runs on their own runner, so on a Dash that isn't a
// hub (which runs Claude with its own login) it is refused them, and
// nothing runs. A project's brief launches a project, which planners can't
// add: refused them anywhere (see TestPlannerRunsFeatureBriefsOnTheirRunner
// for a hub).
func TestFeaturePlanRefusesPlanners(t *testing.T) {
	s, p, _, fp := featureServer(t)
	h := s.Routes()
	ph, _ := plannerOf(t, s, h)
	for _, x := range []struct{ method, path, body string }{
		{"GET", "/api/plans", ""},
		{"POST", "/api/plans", `{"kind":"feature","project":"` + p.ID + `","idea":"x"}`},
		{"POST", "/api/plans/x/interview", `{}`},
		{"POST", "/api/plans/x/feature", `{"start":"task"}`},
	} {
		if w := send(ph, x.method, x.path, x.body); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "hub") {
			t.Errorf("planner %s %s: %d %s", x.method, x.path, w.Code, w.Body)
		}
	}
	for _, x := range []struct{ method, path string }{{"GET", "/api/plans/x/launch"}, {"POST", "/api/plans/x/launched"}, {"POST", "/api/projects/" + p.ID + "/brief"}} {
		if w := send(ph, x.method, x.path, "{}"); w.Code != http.StatusForbidden {
			t.Errorf("planner %s %s: %d %s", x.method, x.path, w.Code, w.Body)
		}
	}
	if len(fp.inputs) != 0 {
		t.Error("Claude ran for a planner")
	}
	cards, err := s.Store.ForOrg(ownerSession(t, s).Org.ID).ListCards("")
	if err != nil || len(cards) != 0 {
		t.Errorf("a planner's brief made a card: %v %v", cards, err)
	}
}

// A plan as stored before kinds (no "kind": the store's test reads one
// such) is a project's: its steps, topics, input and launch are as before.
func TestPlanFromBeforeKinds(t *testing.T) {
	_, fp, h, _ := planServer(t)
	p := decodePlan(t, send(h, "POST", "/api/plans", `{"idea":"A recipe box","kind":"project"}`), http.StatusCreated)
	if w := send(h, "GET", "/api/plans/"+p.ID, ""); strings.Contains(w.Body.String(), `"kind"`) || strings.Contains(w.Body.String(), `"card_id"`) {
		t.Errorf("a project plan isn't stored as before kinds: %s", w.Body)
	}
	if p.Feature() || model.KindOf(p).ID != model.PlanProject {
		t.Fatalf("kind: %+v", p)
	}
	p = decodePlan(t, send(h, "PATCH", "/api/plans/"+p.ID, `{"step":"repo","brief":"# Recipes"}`), http.StatusOK)
	p = decodePlan(t, send(h, "POST", "/api/plans/"+p.ID+"/interview", `{}`), http.StatusOK)
	if len(p.Rounds) != 1 || len(p.Topics) != len(model.PlanTopics) || p.CardID != "" || p.ProjectID != "" {
		t.Errorf("round: %+v", p)
	}
	if in := fp.input(0); strings.Contains(in, `"project": {`) || strings.Contains(in, "feature_name") || !strings.Contains(in, `"idea": "A recipe box"`) {
		t.Errorf("input:\n%s", in)
	}
	if w := send(h, "GET", "/api/plans/"+p.ID+"/launch", ""); w.Code != http.StatusOK {
		t.Errorf("launch view: %d %s", w.Code, w.Body)
	}
}

// On a hub, a feature plan's round runs on the caller's own runner too, as
// its own call (plan-feature: a feature's prompt), with what could be read
// of the project; a project's stays "plan".
func TestHubRunsFeatureInterviewOnTheUsersRunner(t *testing.T) {
	parallel(t)
	f := newClaudeFleet(t)
	p := decodePlan(t, send(f.owner, "POST", "/api/plans", `{"kind":"feature","project":"`+f.project.ID+`","idea":"Export the board"}`), http.StatusCreated)
	p = decodePlan(t, send(f.owner, "POST", "/api/plans/"+p.ID+"/interview", `{}`), http.StatusOK)
	if len(p.Rounds) != 1 || f.small.count("plan-feature") != 1 || f.small.count("plan") != 0 || f.hubSmall.count("plan-feature") != 0 {
		t.Fatalf("plan %+v; runner %v, hub %v", p, f.small.calls, f.hubSmall.calls)
	}
}
