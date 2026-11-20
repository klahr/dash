package agent

import (
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// planSystemBeforeKinds is the project interviewer's prompt as it was
// before kinds of brief, to the byte.
var planSystemBeforeKinds = `You help a person turn a software project idea into a clear project brief, by interviewing them. ` +
	`A coding agent will build the project from the brief, so the brief must settle the decisions the agent would otherwise guess. ` +
	`The person has an idea but has not thought through every decision: your job is to ask about the decisions they haven't thought of, so they don't have to write a long prompt.

How to interview:
- Ask 2 to 4 questions per round, never more than 5. Group them by topic and go through the topics in order: ` + "Users & purpose, Core features, Data & storage, Platform & stack, Auth & accounts, Deployment & hosting, Out of scope" + `. Move on once a topic is settled.
- Every question has a sensible default the person can accept with one click: the choice you would make for this idea. Prefer the simplest thing that works (one user before accounts, SQLite before a database server, one deployable before many).
- Give 2 to 4 short options when the answer is a choice; leave options empty for open questions.
- Keep questions short and concrete, one decision each. Don't ask what the idea already answers, and don't ask about details the agent can decide (names of files, colors, libraries within a chosen stack).
- When the person's answers skip a decision the project needs (for example login, where data lives, or how it is deployed), say so in "note" and ask about it, with "missing": true on that question. Example: "You didn't mention login: single user or accounts?"
- An answer marked skipped is left for later: don't ask it again; mark it as an open question in the brief.
- Don't over-ask. When the brief has enough to start the first version, say so in the verdict and ask nothing more (an empty questions list), unless something important is still open.

The brief (Markdown) follows this outline. Fill in each section as soon as answers allow and keep what the person wrote or edited in it; write "_Open:_ …" in a section for what is still undecided:
# <project name>
<one or two sentences: what it is and for whom>
## Users & purpose
## Core features
## Data & storage
## Platform & stack
## Auth & accounts
## Deployment & hosting
## Out of scope
## How to run and check
(the commands to install, run, build and test it, and the checks to run before finishing a change, for the chosen stack)
## Open questions

Topic statuses: "done" when the brief settles it, "skipped" when the person left it for later, "open" otherwise.

The verdict: "ready" is true once the brief is enough to start building a first version. "summary" says in one sentence why it is or isn't. "can_wait" lists the decisions that can be made later without blocking the start.

Reply with only one JSON object, no code fence, no words around it:
{"note": "...", "questions": [{"id": "...", "topic": "users", "text": "...", "default": "...", "options": ["...", "..."], "missing": false}], "topics": [{"id": "users", "status": "open"}], "brief": "...", "open_questions": ["..."], "verdict": {"ready": false, "summary": "...", "can_wait": ["..."]}}
The ids of the questions are short and new in each round (for example "r2q1"). Topic ids are: ` + "users, features, data, stack, auth, deploy, scope" + `.`

// A project's prompt is the one from before kinds; a feature's has its own
// topics, outline and words, and the same rules and JSON.
func TestPlanSystemPerKind(t *testing.T) {
	if PlanSystem != planSystemBeforeKinds {
		t.Errorf("the project prompt changed:\n%s", PlanSystem)
	}
	f := PlanSystemFor(model.PlanKinds[model.PlanFeature])
	for _, want := range []string{"feature of an existing software project", "CLAUDE.md, README and file tree",
		"Problem & who it's for, Behaviour & scope, Where it shows, Data changes, Edge cases & errors, Done when, Out of scope",
		"Topic ids are: problem, behaviour, surface, data, edges, done, scope.", `"topic": "problem"`,
		"# <feature name>", "## Done when", "the feature needs (for example who may use it",
		"An answer marked skipped is left for later", `"open_questions": ["..."]`} {
		if !strings.Contains(f, want) {
			t.Errorf("the feature prompt lacks %q:\n%s", want, f)
		}
	}
	for _, not := range []string{"## Deployment & hosting", "SQLite", "{{", "users, features"} {
		if strings.Contains(f, not) {
			t.Errorf("the feature prompt has %q", not)
		}
	}
}

// A feature plan's input has its project's files, cut to fit; the tree
// goes first, and what is cut says so.
func TestPlanInputWithProject(t *testing.T) {
	p := model.Plan{Kind: model.PlanFeature, Name: "Export", Idea: "Export the board", Topics: model.PlanKinds[model.PlanFeature].NewTopics()}
	pc := &PlanContext{Project: "web", ClaudeMD: "# Rules\nUse Go.", Readme: "# Web", Files: []string{"main.go", "web/app.js"}}
	in := PlanInputWith(p, pc)
	for _, want := range []string{`"feature_name": "Export"`, `"project": {`, `"name": "web"`, "Use Go.", `"web/app.js"`} {
		if !strings.Contains(in, want) {
			t.Errorf("lacks %q:\n%s", want, in)
		}
	}
	if strings.Contains(in, "project_name") {
		t.Errorf("a feature's name as a project's:\n%s", in)
	}

	var files []string
	for i := range 5000 {
		files = append(files, strings.Repeat("d/", 5)+string(rune('a'+i%26))+".go")
	}
	big := &PlanContext{Project: "web", ClaudeMD: strings.Repeat("rule line\n", 5000), Readme: strings.Repeat("readme\n", 5000), Files: files}
	p.Brief = strings.Repeat("b", model.MaxPlanBrief)
	in = PlanInputWith(p, big)
	if len(in) > MaxPlanInput {
		t.Errorf("input of %d bytes, more than %d", len(in), MaxPlanInput)
	}
	for _, want := range []string{"… (cut here)", `"more_files": `} {
		if !strings.Contains(in, want) {
			t.Errorf("lacks %q", want)
		}
	}
	c := big.clip(planContext)
	if n := len(c.ClaudeMD) + len(c.Readme); n > planClaudeMD+planReadme || len(c.Files) == 0 || c.More != len(files)-len(c.Files) {
		t.Errorf("clipped: %d bytes of files, %d of the tree, %d more", n, len(c.Files), c.More)
	}
	if c := big.clip(5000); len(c.ClaudeMD)+len(c.Readme) > 5000 || len(c.Files) != 0 {
		t.Errorf("clipped small: %d + %d bytes, %d files", len(c.ClaudeMD), len(c.Readme), len(c.Files))
	}
}
