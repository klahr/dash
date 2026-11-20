package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/klahr/dash/internal/model"
)

// The planner's interviewer: Claude asks about an idea for a project (or a
// feature of one), a few questions at a time, and writes the brief as the
// answers come in. Each round is one call without tools: the plan so far in
// (PlanInput; a feature's with its project's files, PlanContext), JSON out
// (ParsePlanTurn). The system prompt is the plan's kind's (PlanSystemFor).
// The hub keeps the plan; the call runs on the user's runner like the
// other short calls.

// PlanSystem is a project interviewer's system prompt.
var PlanSystem = PlanSystemFor(model.PlanKinds[model.PlanProject])

// planSystem is the interviewer's system prompt, the same for every kind
// of brief but for the parts in {{…}}: model.PlanKind's.
var planSystem = template.Must(template.New("plan").Parse(`{{.P.Intro}}

How to interview:
- Ask 2 to 4 questions per round, never more than 5. Group them by topic and go through the topics in order: {{.Topics}}. Move on once a topic is settled.
- Every question has a sensible default the person can accept with one click: the choice you would make for this idea. Prefer the simplest thing that works ({{.P.Simplest}}).
- Give 2 to 4 short options when the answer is a choice; leave options empty for open questions.
- Keep questions short and concrete, one decision each. Don't ask what the idea already answers, and don't ask about details the agent can decide ({{.P.Details}}).
- When the person's answers skip a decision the {{.P.Noun}} needs (for example {{.P.Skips}}), say so in "note" and ask about it, with "missing": true on that question. Example: "{{.P.Example}}"
- An answer marked skipped is left for later: don't ask it again; mark it as an open question in the brief.
- Don't over-ask. When the brief has enough to {{.P.Start}}, say so in the verdict and ask nothing more (an empty questions list), unless something important is still open.

The brief (Markdown) follows this outline. Fill in each section as soon as answers allow and keep what the person wrote or edited in it; write "_Open:_ …" in a section for what is still undecided:
{{.P.Outline}}

Topic statuses: "done" when the brief settles it, "skipped" when the person left it for later, "open" otherwise.

The verdict: "ready" is true once the brief is enough to {{.P.Ready}}. "summary" says in one sentence why it is or isn't. "can_wait" lists the decisions that can be made later without blocking the start.

Reply with only one JSON object, no code fence, no words around it:
{"note": "...", "questions": [{"id": "...", "topic": "{{.First}}", "text": "...", "default": "...", "options": ["...", "..."], "missing": false}], "topics": [{"id": "{{.First}}", "status": "open"}], "brief": "...", "open_questions": ["..."], "verdict": {"ready": false, "summary": "...", "can_wait": ["..."]}}
The ids of the questions are short and new in each round (for example "r2q1"). Topic ids are: {{.IDs}}.`))

// PlanSystemFor is the interviewer's system prompt for a plan of kind k.
func PlanSystemFor(k model.PlanKind) string {
	var titles, ids []string
	for _, t := range k.Topics {
		titles, ids = append(titles, t.Title), append(ids, t.ID)
	}
	var b strings.Builder
	err := planSystem.Execute(&b, map[string]any{"P": k.Prompt, "Topics": strings.Join(titles, ", "), "IDs": strings.Join(ids, ", "), "First": ids[0]})
	if err != nil {
		panic(err) // the template is ours: it can't fail but by a mistake here
	}
	return b.String()
}

// MaxPlanInput is how much of a plan Plan reads, in bytes.
const MaxPlanInput = 80000

// planRounds is how many of the latest rounds PlanInput sends in full; the
// brief carries what earlier ones settled.
const planRounds = 8

// PlanContext is what a feature plan's interviewer reads of its project
// with each round: its CLAUDE.md, README and file tree, as they are on its
// default branch. Each is cut (PlanContext.clip) so the round stays a
// reasonable size.
type PlanContext struct {
	Project  string   `json:"name"`
	ClaudeMD string   `json:"claude_md,omitempty"`
	Readme   string   `json:"readme,omitempty"`
	Files    []string `json:"files,omitempty"`
	// More counts the files left out of Files.
	More int `json:"more_files,omitempty"`
}

// How much of a project PlanInput sends, in bytes: each file, the tree,
// and all of it together (less when the plan itself is large).
const (
	planClaudeMD = 12000
	planReadme   = 8000
	planTree     = 10000
	planContext  = 26000
)

// clip cuts c to at most n bytes in all: the tree first (it says least),
// then the README, then CLAUDE.md.
func (c PlanContext) clip(n int) PlanContext {
	c.ClaudeMD = cutText(c.ClaudeMD, planClaudeMD)
	c.Readme = cutText(c.Readme, planReadme)
	n = min(n, planContext)
	tree := min(planTree, n-len(c.ClaudeMD)-len(c.Readme))
	if tree < 0 {
		c.Readme = cutText(c.Readme, max(0, n-len(c.ClaudeMD)))
		c.ClaudeMD = cutText(c.ClaudeMD, n)
		tree = 0
	}
	size, keep := 0, 0
	for _, f := range c.Files {
		if size+len(f)+4 > tree {
			break
		}
		size += len(f) + 4 // its quotes, comma and space in the JSON
		keep++
	}
	c.More += len(c.Files) - keep
	c.Files = c.Files[:keep]
	return c
}

// cutText cuts s to at most n bytes, at a line when it can, saying so.
func cutText(s string, n int) string {
	const more = "\n… (cut here)"
	if len(s) <= n {
		return s
	}
	if n <= len(more) {
		return ""
	}
	s = s[:n-len(more)]
	if i := strings.LastIndexByte(s, '\n'); i > n/2 {
		s = s[:i]
	}
	return strings.ToValidUTF8(s, "") + more
}

// PlanInput writes a plan for the interviewer: what to do, then the plan
// so far as JSON (the idea, the brief, the topics and the latest rounds
// with their answers). It is PlanInputWith without a project.
func PlanInput(p model.Plan) string { return PlanInputWith(p, nil) }

// PlanInputWith is PlanInput with a feature plan's project (pc; nil: none)
// in the JSON, cut to what fits in MaxPlanInput beside the plan.
func PlanInputWith(p model.Plan, pc *PlanContext) string {
	type answer struct {
		Text    string `json:"text,omitempty"`
		Default bool   `json:"accepted_default,omitempty"`
		Skipped bool   `json:"skipped,omitempty"`
	}
	type question struct {
		ID     string  `json:"id"`
		Topic  string  `json:"topic,omitempty"`
		Text   string  `json:"text"`
		Answer *answer `json:"answer,omitempty"`
	}
	type round struct {
		Note      string     `json:"note,omitempty"`
		Questions []question `json:"questions"`
		Message   string     `json:"person_also_said,omitempty"`
	}
	in := struct {
		Name    string            `json:"project_name,omitempty"`
		Feature string            `json:"feature_name,omitempty"`
		Idea    string            `json:"idea"`
		Brief   string            `json:"current_brief,omitempty"`
		Topics  []model.PlanTopic `json:"topics"`
		Rounds  []round           `json:"rounds"`
		Project *PlanContext      `json:"project,omitempty"`
	}{Name: p.Name, Idea: p.Idea, Brief: p.Brief, Topics: p.Topics, Rounds: []round{}}
	if p.Feature() {
		in.Name, in.Feature = "", p.Name
	}
	rounds := p.Rounds
	if len(rounds) > planRounds {
		rounds = rounds[len(rounds)-planRounds:]
	}
	for _, r := range rounds {
		x := round{Note: r.Note, Message: r.Message}
		for _, q := range r.Questions {
			y := question{ID: q.ID, Topic: q.Topic, Text: q.Text}
			for _, a := range r.Answers {
				if a.Question == q.ID {
					y.Answer = &answer{Text: a.Text, Default: a.Default, Skipped: a.Skipped}
				}
			}
			x.Questions = append(x.Questions, y)
		}
		in.Rounds = append(in.Rounds, x)
	}
	do := "Start the interview: write a first brief from the idea and ask the first questions."
	if len(p.Rounds) > 0 {
		do = "Update the brief with the latest answers, then ask the next questions, or none if the brief is enough to start."
	}
	if pc != nil {
		// What the plan leaves of MaxPlanInput, less a margin for JSON's
		// escapes, goes to the project.
		b, _ := json.MarshalIndent(in, "", " ")
		room := (MaxPlanInput-len(do)-len(b))*3/4 - 200
		c := pc.clip(max(0, room))
		in.Project = &c
	}
	b, _ := json.MarshalIndent(in, "", " ")
	return do + "\n\nThe plan so far:\n" + string(b)
}

// Plan runs one round of the interview of a plan of kind (model.PlanKinds;
// "" is a project's): input is PlanInput's, and the reply is the
// interviewer's JSON (see ParsePlanTurn).
func (t Titler) Plan(ctx context.Context, kind, input string) (string, error) {
	if len(input) > MaxPlanInput {
		return "", errors.New("the plan is too long to send to Claude")
	}
	k, ok := model.PlanKinds[kind]
	if !ok && kind != "" {
		return "", fmt.Errorf("no such kind of brief: %q", kind)
	}
	if !ok {
		k = model.PlanKinds[model.PlanProject]
	}
	return t.run(ctx, PromptRequest{Model: t.SplitModel, Large: true, Timeout: 4 * time.Minute}, PlanSystemFor(k), input)
}

// ParsePlanTurn reads the interviewer's reply: a JSON object, perhaps in a
// code fence or with words around it.
func ParsePlanTurn(s string) (model.PlanTurn, error) {
	var t model.PlanTurn
	i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if i < 0 || j < i {
		return t, errors.New("no plan in Claude's reply")
	}
	if err := json.Unmarshal([]byte(s[i:j+1]), &t); err != nil {
		return t, fmt.Errorf("couldn't read Claude's reply: %w", err)
	}
	return t, nil
}
