package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

// The planner's interviewer: Claude asks about a project idea, a few
// questions at a time, and writes the project's brief as the answers come
// in. Each round is one call without tools: the plan so far in (PlanInput),
// JSON out (ParsePlanTurn). The hub keeps the plan; the call runs on the
// user's runner like the other short calls.

// PlanSystem is the interviewer's system prompt.
var PlanSystem = `You help a person turn a software project idea into a clear project brief, by interviewing them. ` +
	`A coding agent will build the project from the brief, so the brief must settle the decisions the agent would otherwise guess. ` +
	`The person has an idea but has not thought through every decision: your job is to ask about the decisions they haven't thought of, so they don't have to write a long prompt.

How to interview:
- Ask 2 to 4 questions per round, never more than 5. Group them by topic and go through the topics in order: ` + planTopicList + `. Move on once a topic is settled.
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
The ids of the questions are short and new in each round (for example "r2q1"). Topic ids are: ` + planTopicIDs + `.`

var (
	planTopicList = topicList(func(t model.PlanTopic) string { return t.Title })
	planTopicIDs  = topicList(func(t model.PlanTopic) string { return t.ID })
)

func topicList(f func(model.PlanTopic) string) string {
	var out []string
	for _, t := range model.PlanTopics {
		out = append(out, f(t))
	}
	return strings.Join(out, ", ")
}

// MaxPlanInput is how much of a plan Plan reads, in bytes.
const MaxPlanInput = 80000

// planRounds is how many of the latest rounds PlanInput sends in full; the
// brief carries what earlier ones settled.
const planRounds = 8

// PlanInput writes a plan for the interviewer: what to do, then the plan
// so far as JSON (the idea, the brief, the topics and the latest rounds
// with their answers).
func PlanInput(p model.Plan) string {
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
		Name   string            `json:"project_name,omitempty"`
		Idea   string            `json:"idea"`
		Brief  string            `json:"current_brief,omitempty"`
		Topics []model.PlanTopic `json:"topics"`
		Rounds []round           `json:"rounds"`
	}{Name: p.Name, Idea: p.Idea, Brief: p.Brief, Topics: p.Topics, Rounds: []round{}}
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
	b, _ := json.MarshalIndent(in, "", " ")
	do := "Start the interview: write a first brief from the idea and ask the first questions."
	if len(p.Rounds) > 0 {
		do = "Update the brief with the latest answers, then ask the next questions, or none if the brief is enough to start."
	}
	return do + "\n\nThe plan so far:\n" + string(b)
}

// Plan runs one round of the interview: input is PlanInput's, and the
// reply is the interviewer's JSON (see ParsePlanTurn).
func (t Titler) Plan(ctx context.Context, input string) (string, error) {
	if len(input) > MaxPlanInput {
		return "", errors.New("the plan is too long to send to Claude")
	}
	model := t.SplitModel
	if model == "" {
		model = "sonnet"
	}
	return t.run(ctx, model, 4*time.Minute, PlanSystem, input)
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
