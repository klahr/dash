package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// fakePlanner is a SmallClaude whose interview rounds answer from replies,
// in turn (the last one again once they run out), recording what it was given.
type fakePlanner struct {
	fakeSmall
	mu      sync.Mutex
	replies []string
	inputs  []string
	fail    error
}

func (f *fakePlanner) Plan(_ context.Context, in string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, in)
	if f.fail != nil {
		return "", f.fail
	}
	out := f.replies[0]
	if len(f.replies) > 1 {
		f.replies = f.replies[1:]
	}
	return out, nil
}

func (f *fakePlanner) input(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.inputs) {
		return ""
	}
	return f.inputs[i]
}

const (
	planRound1 = "Here is a first brief.\n```json\n" + `{"note":"Let's start with who it's for.",
		"questions":[{"id":"r1q1","topic":"users","text":"Who uses it?","default":"Just me","options":["Just me","A team"]},
			{"id":"r1q2","topic":"data","text":"Where does the data live?","default":"SQLite"},
			{"id":"r1q2","topic":"nonsense","text":"Duplicate id, unknown topic","default":"x"},
			{"id":"r1q4","topic":"auth","text":"  ","default":"dropped: no text"}],
		"topics":[{"id":"users","status":"open"},{"id":"bogus","status":"done"},{"id":"scope","status":"weird"}],
		"brief":"# Recipes\nA recipe box.\n## Platform & stack\nGo and SQLite.\n## How to run and check\n` + "`go test ./...`" + `",
		"open_questions":["Who uses it?"],"verdict":{"ready":false,"summary":"Not yet.","can_wait":[" ","Hosting"]}}` + "\n```"
	planRound2 = `{"note":"You didn't mention login: single user or accounts?",
		"questions":[{"id":"r2q1","topic":"auth","text":"Single user or accounts?","default":"Single user","missing":true}],
		"topics":[{"id":"users","status":"done"},{"id":"data","status":"skipped"}],
		"brief":"# Recipes\nA recipe box for me.\n## Platform & stack\nGo and SQLite.\n## How to run and check\n` + "`go test ./...`" + `",
		"open_questions":[],"verdict":{"ready":true,"summary":"Enough to start.","can_wait":["Hosting"]}}`
)

// planServer is boardServer with a planner here, and handlers signed in as
// its owner and as a second member of the org.
func planServer(t *testing.T) (*Server, *fakePlanner, http.Handler, http.Handler) {
	t.Helper()
	s, _, _ := boardServer(t)
	fp := &fakePlanner{replies: []string{planRound1, planRound2}}
	s.Small = fp
	member(t, s, testOwner(t, s).Org, "bea", store.RoleAdmin)
	return s, fp, loginAs(t, s.Routes(), "u", "p"), loginAs(t, s.Routes(), "bea", "long-enough-bea")
}

// decodePlan reads the plan w answered with code.
func decodePlan(t *testing.T, w *httptest.ResponseRecorder, code int) model.Plan {
	t.Helper()
	var p model.Plan
	if w.Code != code || json.Unmarshal(w.Body.Bytes(), &p) != nil {
		t.Fatalf("status %d, want %d: %s", w.Code, code, w.Body)
	}
	return p
}

// The interview: a plan from an idea, Claude's rounds of questions with
// the brief growing, answers by default, in the user's words or skipped,
// the brief edited by hand, and launching it.
func TestPlanInterview(t *testing.T) {
	_, fp, h, bea := planServer(t)

	p := decodePlan(t, send(h, "POST", "/api/plans", `{"idea":"  A recipe box  ","name":"Recipes"}`), http.StatusCreated)
	if p.ID == "" || p.Idea != "A recipe box" || p.Step != "idea" || len(p.Topics) != len(model.PlanTopics) || p.Topics[0].Status != model.PlanTopicOpen {
		t.Fatalf("created: %+v", p)
	}
	base := "/api/plans/" + p.ID

	// Plans are their user's: another member, an admin, doesn't see it.
	if w := send(bea, "GET", base, ""); w.Code != http.StatusNotFound {
		t.Errorf("another member reads it: %d %s", w.Code, w.Body)
	}
	if w := send(bea, "GET", "/api/plans", ""); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("another member's list: %d %s", w.Code, w.Body)
	}
	if w := send(bea, "POST", base+"/interview", `{}`); w.Code != http.StatusNotFound {
		t.Errorf("another member interviews: %d %s", w.Code, w.Body)
	}
	if len(fp.inputs) != 0 {
		t.Fatal("Claude ran for another member")
	}

	// The first round: from the idea alone.
	p = decodePlan(t, send(h, "POST", base+"/interview", `{}`), http.StatusOK)
	if in := fp.input(0); !strings.Contains(in, "Start the interview") || !strings.Contains(in, `"idea": "A recipe box"`) {
		t.Errorf("first input:\n%s", in)
	}
	if len(p.Rounds) != 1 || p.Step != "interview" || !strings.HasPrefix(p.Brief, "# Recipes") {
		t.Fatalf("after round 1: %+v", p)
	}
	r := p.Rounds[0]
	if len(r.Questions) != 3 || r.Questions[1].ID != "r1q2" || r.Questions[2].ID != "q3" || r.Questions[2].Topic != "" || r.Note == "" {
		t.Errorf("questions: %+v", r.Questions)
	}
	if p.Topics[0].Status != "open" || p.Topics[6].Status != "open" || len(p.Topics) != len(model.PlanTopics) {
		t.Errorf("topics: %+v", p.Topics)
	}
	if p.Verdict.Ready || len(p.Verdict.CanWait) != 1 || len(p.Open) != 1 {
		t.Errorf("verdict %+v, open %v", p.Verdict, p.Open)
	}

	// Answers: nothing at all is refused; one by default, one skipped, one
	// to a question that wasn't asked (dropped), and a message.
	if w := send(h, "POST", base+"/interview", `{"answers":[{"question":"nope","text":"x"}]}`); w.Code != http.StatusBadRequest {
		t.Errorf("no answers: %d %s", w.Code, w.Body)
	}
	p = decodePlan(t, send(h, "POST", base+"/interview", `{"answers":[{"question":"r1q1","text":"Just me","default":true},
		{"question":"r1q2","text":"  "},{"question":"zzz","text":"no"}],"message":"Make it fast"}`), http.StatusOK)
	in := fp.input(1)
	for _, want := range []string{"Update the brief", `"accepted_default": true`, `"skipped": true`, `"person_also_said": "Make it fast"`, "# Recipes"} {
		if !strings.Contains(in, want) {
			t.Errorf("second input lacks %q:\n%s", want, in)
		}
	}
	if strings.Contains(in, `"zzz"`) {
		t.Errorf("an answer to no question was sent:\n%s", in)
	}
	a := p.Rounds[0].Answers
	if len(p.Rounds) != 2 || len(a) != 2 || !a[0].Default || !a[1].Skipped || p.Rounds[0].Message != "Make it fast" || p.Rounds[0].Answered == nil {
		t.Fatalf("after round 2: %+v", p.Rounds)
	}
	if !p.Rounds[1].Questions[0].Missing || p.Topics[0].Status != "done" || p.Topics[2].Status != "skipped" || !p.Verdict.Ready {
		t.Errorf("round 2: %+v %+v %+v", p.Rounds[1], p.Topics, p.Verdict)
	}

	// A failed round keeps nothing: the same answers can be sent again.
	fp.mu.Lock()
	fp.fail = errors.New("claude broke")
	fp.mu.Unlock()
	if w := send(h, "POST", base+"/interview", `{"answers":[{"question":"r2q1","text":"Accounts"}]}`); w.Code != http.StatusInternalServerError {
		t.Errorf("failed round: %d %s", w.Code, w.Body)
	}
	fp.mu.Lock()
	fp.fail, fp.replies = nil, []string{"I can't help with that."}
	fp.mu.Unlock()
	if w := send(h, "POST", base+"/interview", `{"answers":[{"question":"r2q1","text":"Accounts"}]}`); w.Code != http.StatusBadGateway {
		t.Errorf("unreadable reply: %d %s", w.Code, w.Body)
	}
	if got := decodePlan(t, send(h, "GET", base, ""), http.StatusOK); len(got.Rounds) != 2 || got.Rounds[1].Answered != nil {
		t.Errorf("a failed round changed the plan: %+v", got.Rounds)
	}

	// The brief, edited by hand, and the step the user is on.
	brief := "# Recipes\nA recipe box for me.\n## Platform & stack\nGo, SQLite, htmx.\n## How to run and check\n`go vet ./...`\n## Out of scope\nSharing."
	b, _ := json.Marshal(map[string]string{"brief": brief, "step": "repo"})
	p = decodePlan(t, send(h, "PATCH", base, string(b)), http.StatusOK)
	if p.Brief != brief || p.Step != "repo" {
		t.Errorf("patched: %+v", p)
	}
	if w := send(h, "PATCH", base, `{"step":"nowhere"}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown step: %d %s", w.Code, w.Body)
	}
	if w := send(h, "GET", "/api/plans", ""); !strings.Contains(w.Body.String(), p.ID) {
		t.Errorf("drafts: %s", w.Body)
	}

	// What launching commits and starts.
	var lv planLaunchView
	if w := send(h, "GET", base+"/launch", ""); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &lv) != nil {
		t.Fatalf("launch view: %d %s", w.Code, w.Body)
	}
	if lv.Brief != brief+"\n" || !strings.Contains(lv.ClaudeMD, "Go, SQLite, htmx.") || !strings.Contains(lv.ClaudeMD, "`go vet ./...`") ||
		!strings.Contains(lv.ClaudeMD, "BRIEF.md") || lv.Goal.Title != "Build the first version of Recipes" ||
		!strings.Contains(lv.Goal.Description, "Sharing.") || !strings.Contains(lv.Goal.DescriptionNoFiles, "aren't in the repository yet") {
		t.Errorf("launch view: %+v", lv)
	}

	// Launched: out of the drafts, and done with.
	p = decodePlan(t, send(h, "POST", base+"/launched", `{"project_id":"p1","goal_id":"g1"}`), http.StatusOK)
	if p.LaunchedAt == nil || p.ProjectID != "p1" || p.GoalID != "g1" || p.Step != "launch" {
		t.Errorf("launched: %+v", p)
	}
	if w := send(h, "GET", "/api/plans", ""); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("drafts after launching: %s", w.Body)
	}
	for _, x := range []struct{ method, path, body string }{
		{"PATCH", base, `{"idea":"x"}`}, {"POST", base + "/interview", `{"message":"x"}`}, {"POST", base + "/launched", `{"project_id":"p2"}`},
	} {
		if w := send(h, x.method, x.path, x.body); w.Code != http.StatusConflict {
			t.Errorf("%s %s after launching: %d %s", x.method, x.path, w.Code, w.Body)
		}
	}

	// Deleting: theirs only.
	if w := send(bea, "DELETE", base, ""); w.Code != http.StatusNotFound {
		t.Errorf("another member deletes it: %d", w.Code)
	}
	if w := send(h, "DELETE", base, ""); w.Code != http.StatusNoContent {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if w := send(h, "GET", base, ""); w.Code != http.StatusNotFound {
		t.Errorf("deleted plan: %d", w.Code)
	}
}

// A plan needs its idea before the interview starts, and a place to run it.
func TestPlanInterviewNeeds(t *testing.T) {
	s, fp, h, _ := planServer(t)
	p := decodePlan(t, send(h, "POST", "/api/plans", `{"name":"Nameless"}`), http.StatusCreated)
	if w := send(h, "POST", "/api/plans/"+p.ID+"/interview", `{}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "idea") {
		t.Errorf("without an idea: %d %s", w.Code, w.Body)
	}
	if w := send(h, "POST", "/api/plans", `{"idea":"`+strings.Repeat("x", model.MaxPlanIdea+1)+`"}`); w.Code != http.StatusBadRequest {
		t.Errorf("long idea: %d", w.Code)
	}
	if w := send(h, "GET", "/api/plans/"+p.ID+"/launch", ""); w.Code != http.StatusBadRequest {
		t.Errorf("launch without a brief: %d %s", w.Code, w.Body)
	}
	s.Small = nil
	send(h, "PATCH", "/api/plans/"+p.ID, `{"idea":"A thing"}`)
	if w := send(h, "POST", "/api/plans/"+p.ID+"/interview", `{}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("without Claude: %d %s", w.Code, w.Body)
	}
	if len(fp.inputs) != 0 {
		t.Error("Claude ran")
	}
}

// Committing the brief: BRIEF.md and CLAUDE.md on the default branch, each
// its own commit, and a file the branch has already is kept.
func TestCommitBrief(t *testing.T) {
	_, p, h := checklistServer(t, "")
	w := do(t, h, "POST", "/api/projects/"+p.ID+"/brief", `{"brief":"# Recipes\n","claude_md":"# Rules\n"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"committed":["BRIEF.md","CLAUDE.md"]`) {
		t.Fatalf("commit: %d %s", w.Code, w.Body)
	}
	if got := gitIn(t, p.Path, "show", "main:BRIEF.md"); got != "# Recipes" {
		t.Errorf("BRIEF.md %q", got)
	}
	if got := gitIn(t, p.Path, "log", "--format=%s", "-2", "main"); got != "Add CLAUDE.md from the project plan\nAdd BRIEF.md from the project plan" {
		t.Errorf("log %q", got)
	}
	w = do(t, h, "POST", "/api/projects/"+p.ID+"/brief", `{"brief":"# Other\n","claude_md":"# Other\n"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"kept":["BRIEF.md","CLAUDE.md"]`) {
		t.Fatalf("again: %d %s", w.Code, w.Body)
	}
	if got := gitIn(t, p.Path, "show", "main:BRIEF.md"); got != "# Recipes" {
		t.Errorf("BRIEF.md replaced: %q", got)
	}
	if w := do(t, h, "POST", "/api/projects/"+p.ID+"/brief", `{"brief":" "}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty brief: %d %s", w.Code, w.Body)
	}
}

// On a hub, the interview runs on the caller's own runner, never on the
// hub; a member without a runner online is told so.
func TestHubRunsPlanInterviewOnTheUsersRunner(t *testing.T) {
	parallel(t)
	f := newClaudeFleet(t)
	p := decodePlan(t, send(f.owner, "POST", "/api/plans", `{"idea":"A recipe box"}`), http.StatusCreated)
	p = decodePlan(t, send(f.owner, "POST", "/api/plans/"+p.ID+"/interview", `{}`), http.StatusOK)
	if p.Brief != "# Plan from runner" || len(p.Rounds) != 1 || f.small.count("plan") != 1 || f.hubSmall.count("plan") != 0 {
		t.Fatalf("plan %+v; runner %v, hub %v", p, f.small.calls, f.hubSmall.calls)
	}
	q := decodePlan(t, send(f.other, "POST", "/api/plans", `{"idea":"Another"}`), http.StatusCreated)
	if w := send(f.other, "POST", "/api/plans/"+q.ID+"/interview", `{}`); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "no runner of yours") {
		t.Errorf("without a runner: %d %s", w.Code, w.Body)
	}
	if f.small.count("plan") != 1 {
		t.Error("ran on another user's runner")
	}
}

func TestStarterClaudeMD(t *testing.T) {
	parallel(t)
	md := starterClaudeMD("Recipes", "# Recipes\n## Platform & stack\nGo.\n## Auth & accounts\nNone.")
	for _, want := range []string{"# Recipes: rules for Claude", "## Stack\n\nGo.\n", "Add the commands", "## Before you finish"} {
		if !strings.Contains(md, want) {
			t.Errorf("lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "None.") {
		t.Errorf("took another section:\n%s", md)
	}
	if got := planName(model.Plan{Brief: "intro\n# Title here\n"}); got != "Title here" {
		t.Errorf("name from brief %q", got)
	}
	if got := planName(model.Plan{Idea: "A tool to track my plants"}); got != "A tool to track my plants" {
		t.Errorf("name from idea %q", got)
	}
	if got := clipBrief(strings.Repeat("line\n", 100), 50); len(got) > 50 || !strings.HasSuffix(got, "…") {
		t.Errorf("clipped %q", got)
	}
}
