package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Plan is a brief being written: the user's idea, Claude's interview about
// it (rounds of questions with suggested answers), and the brief it writes
// as it goes. Its Kind (PlanKinds) says what the brief is for. A project
// plan's launch creates the project, commits the brief, and starts a goal
// from it; a feature plan's puts the brief on a Planning card, and may
// start a task or a goal from the card. A plan is one user's, in one org;
// until it is launched it is a draft they can come back to.
type Plan struct {
	ID string `json:"id"`
	// Kind is PlanProject or PlanFeature; "" (a plan from before kinds) is
	// a project's.
	Kind string `json:"kind,omitempty"`
	Name string `json:"name"` // the project's (or feature's) name, optional until launch
	Idea string `json:"idea"` // one or two sentences
	// Step is the planner's step the user was last on (its kind's Steps),
	// so a draft opens where they left it.
	Step   string      `json:"step"`
	Rounds []PlanRound `json:"rounds"`
	Topics []PlanTopic `json:"topics"`
	// Brief is the project brief (Markdown), Claude's and the user's edits.
	Brief string `json:"brief"`
	// Open are the questions the brief still has open.
	Open    []string    `json:"open"`
	Verdict PlanVerdict `json:"verdict"`
	// ProjectID is the project a feature plan is for, from its creation;
	// a project plan's once launched, with the goal it started.
	ProjectID string `json:"project_id,omitempty"`
	// CardID is a feature plan's Planning card, which gets the brief as its
	// description. CardMade: the plan made the card (it was new), so the
	// launch names it after the brief too.
	CardID   string `json:"card_id,omitempty"`
	CardMade bool   `json:"card_made,omitempty"`
	// GoalID or TaskID is the work launching started, if any.
	GoalID     string     `json:"goal_id,omitempty"`
	TaskID     string     `json:"task_id,omitempty"`
	LaunchedAt *time.Time `json:"launched_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Plan kinds: what the brief is for.
const (
	PlanProject = "project" // a new project (Add project → Write a project brief)
	PlanFeature = "feature" // a feature of a project, on a Planning card
)

// PlanKind is everything that differs between the kinds of brief, in one
// place: the planner's steps, the interview's topics, and what the
// interviewer's prompt says (agent.PlanSystemFor). What the last step
// (launch) does is the kind's too: a project's adds the project, commits
// the brief and starts a goal (api/plans.go: planLaunch); a feature's puts
// the brief on its card and starts a task or a goal from it, or only moves
// the card to Planned (api/plans.go: launchFeature). The rest, the
// interview's rules and the reply's JSON, is the same for every kind.
type PlanKind struct {
	ID     string
	Steps  []string    // in order; the first is the idea, the second the interview
	Topics []PlanTopic // in the order the interview goes through them
	Prompt PlanPrompt
}

// PlanPrompt is what the interviewer's prompt says for a kind: the parts
// of agent.PlanSystemFor's template that differ.
type PlanPrompt struct {
	Intro    string // what the interview is for: the prompt's first paragraph
	Simplest string // examples of the simplest thing that works
	Details  string // examples of details the agent decides, not the person
	Noun     string // what is built: "project", "feature"
	Skips    string // examples of decisions people skip
	Example  string // a note asking about one
	Start    string // what the brief is enough for: "start the first version"
	Ready    string // the verdict's: "start building a first version"
	Outline  string // the brief's outline
}

// PlanSteps are a project plan's steps, in order.
var PlanSteps = []string{"idea", "interview", "brief", "repo", "launch"}

// FeatureSteps are a feature plan's steps, in order: no repository (the
// project has one), and Start in place of Launch.
var FeatureSteps = []string{"idea", "interview", "brief", "start"}

// PlanRound is one round of the interview: what Claude asked, and what the
// user answered (empty while it waits for them).
type PlanRound struct {
	// Note is Claude's short message with the questions: what it took
	// from the last answers, decisions the user skipped.
	Note      string         `json:"note,omitempty"`
	Questions []PlanQuestion `json:"questions"`
	Answers   []PlanAnswer   `json:"answers,omitempty"`
	// Message is anything else the user said with their answers.
	Message  string     `json:"message,omitempty"`
	At       time.Time  `json:"at"`
	Answered *time.Time `json:"answered,omitempty"`
}

// PlanQuestion is one of Claude's questions, with the answer it suggests.
type PlanQuestion struct {
	ID    string `json:"id"`
	Topic string `json:"topic"` // a PlanTopics id
	Text  string `json:"text"`
	// Default is the answer Claude suggests, accepted with one click.
	Default string   `json:"default"`
	Options []string `json:"options,omitempty"`
	// Missing marks a decision the user hadn't mentioned at all.
	Missing bool `json:"missing,omitempty"`
}

// PlanAnswer answers one question: the text, and whether it is the
// suggested default as it was. Skipped: left for later.
type PlanAnswer struct {
	Question string `json:"question"`
	Text     string `json:"text"`
	Default  bool   `json:"default,omitempty"`
	Skipped  bool   `json:"skipped,omitempty"`
}

// PlanTopic is how far the interview got with one topic.
type PlanTopic struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // PlanTopicOpen, …
}

// Topic statuses.
const (
	PlanTopicOpen    = "open"    // not settled yet
	PlanTopicDone    = "done"    // decided, in the brief
	PlanTopicSkipped = "skipped" // left for later: the brief says so
)

// PlanTopics are a project interview's topics, in the order it goes
// through them.
var PlanTopics = []PlanTopic{
	{ID: "users", Title: "Users & purpose"},
	{ID: "features", Title: "Core features"},
	{ID: "data", Title: "Data & storage"},
	{ID: "stack", Title: "Platform & stack"},
	{ID: "auth", Title: "Auth & accounts"},
	{ID: "deploy", Title: "Deployment & hosting"},
	{ID: "scope", Title: "Out of scope"},
}

// FeatureTopics are a feature interview's topics, in order.
var FeatureTopics = []PlanTopic{
	{ID: "problem", Title: "Problem & who it's for"},
	{ID: "behaviour", Title: "Behaviour & scope"},
	{ID: "surface", Title: "Where it shows"},
	{ID: "data", Title: "Data changes"},
	{ID: "edges", Title: "Edge cases & errors"},
	{ID: "done", Title: "Done when"},
	{ID: "scope", Title: "Out of scope"},
}

// PlanKinds are the kinds of brief, by id.
var PlanKinds = map[string]PlanKind{
	PlanProject: {
		ID: PlanProject, Steps: PlanSteps, Topics: PlanTopics,
		Prompt: PlanPrompt{
			Intro: "You help a person turn a software project idea into a clear project brief, by interviewing them. " +
				"A coding agent will build the project from the brief, so the brief must settle the decisions the agent would otherwise guess. " +
				"The person has an idea but has not thought through every decision: your job is to ask about the decisions they haven't thought of, so they don't have to write a long prompt.",
			Simplest: "one user before accounts, SQLite before a database server, one deployable before many",
			Details:  "names of files, colors, libraries within a chosen stack",
			Noun:     "project",
			Skips:    "login, where data lives, or how it is deployed",
			Example:  "You didn't mention login: single user or accounts?",
			Start:    "start the first version",
			Ready:    "start building a first version",
			Outline: `# <project name>
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
## Open questions`,
		},
	},
	PlanFeature: {
		ID: PlanFeature, Steps: FeatureSteps, Topics: FeatureTopics,
		Prompt: PlanPrompt{
			Intro: "You help a person turn an idea for a feature of an existing software project into a clear feature brief, by interviewing them. " +
				"A coding agent will build the feature from the brief, in the project's code, so the brief must settle the decisions the agent would otherwise guess. " +
				"The person has an idea but has not thought through every decision: your job is to ask about the decisions they haven't thought of, so they don't have to write a long prompt.\n\n" +
				"The project's CLAUDE.md, README and file tree come with the plan, under \"project\". Read them first: don't ask what they already answer " +
				"(the stack, where things live, how it is built and checked), and use the names the project uses for its screens, commands and data.",
			Simplest: "reuse what the project has before adding to it, one screen before several, no new dependency unless it is needed",
			Details:  "names of files and functions, colors, how to structure the code",
			Noun:     "feature",
			Skips:    "who may use it, what happens on an error, or what it changes in stored data",
			Example:  "You didn't say who may use it: everyone, or admins only?",
			Start:    "build the feature",
			Ready:    "start building the feature",
			Outline: `# <feature name>
<one or two sentences: what it does and for whom>
## Problem & who it's for
## Behaviour & scope
## Where it shows
(the screens, commands or API endpoints it adds or changes, by the names the project uses)
## Data changes
## Edge cases & errors
## Done when
(how to tell it works: the tests to add, and what to try by hand)
## Out of scope
## Open questions`,
		},
	},
}

// KindOf is the kind of plan p (a project's when it says none).
func KindOf(p Plan) PlanKind {
	if k, ok := PlanKinds[p.Kind]; ok {
		return k
	}
	return PlanKinds[PlanProject]
}

// Feature reports whether p is a feature plan.
func (p Plan) Feature() bool { return p.Kind == PlanFeature }

// PlanVerdict is Claude's view on whether the brief is enough to start.
type PlanVerdict struct {
	Ready   bool     `json:"ready"`
	Summary string   `json:"summary,omitempty"`
	CanWait []string `json:"can_wait,omitempty"` // decisions that can be made later
}

// PlanTurn is one answer of the interviewer: the next questions, where the
// topics stand, and the brief as it is now.
type PlanTurn struct {
	Note      string         `json:"note"`
	Questions []PlanQuestion `json:"questions"`
	Topics    []PlanTopic    `json:"topics"`
	Brief     string         `json:"brief"`
	Open      []string       `json:"open_questions"`
	Verdict   PlanVerdict    `json:"verdict"`
}

// Limits on a plan, so one stays a reasonable size to store and to send
// to Claude each round.
const (
	MaxPlanName      = 100
	MaxPlanIdea      = 2000
	MaxPlanBrief     = 30000
	MaxPlanAnswer    = 2000
	MaxPlanRounds    = 30
	MaxPlanQuestions = 6 // per round
)

// NewPlanTopics are the topics of a new project plan, all open.
func NewPlanTopics() []PlanTopic { return PlanKinds[PlanProject].NewTopics() }

// NewTopics are the topics of a new plan of kind k, all open.
func (k PlanKind) NewTopics() []PlanTopic {
	out := slices.Clone(k.Topics)
	for i := range out {
		out[i].Status = PlanTopicOpen
	}
	return out
}

// CleanPlanDraft trims what the user typed into a plan and checks it.
func CleanPlanDraft(p *Plan) error {
	if p.Kind == PlanProject {
		p.Kind = "" // as plans from before kinds are: one way to say it
	}
	if _, ok := PlanKinds[p.Kind]; !ok && p.Kind != "" {
		return fmt.Errorf("unknown kind of brief %q", p.Kind)
	}
	steps := KindOf(*p).Steps
	p.Name = strings.TrimSpace(p.Name)
	p.Idea = strings.TrimSpace(p.Idea)
	switch {
	case len(p.Name) > MaxPlanName:
		return fmt.Errorf("the name is longer than %d characters", MaxPlanName)
	case len(p.Idea) > MaxPlanIdea:
		return fmt.Errorf("the idea is longer than %d characters: keep it to a sentence or two", MaxPlanIdea)
	case len(p.Brief) > MaxPlanBrief:
		return fmt.Errorf("the brief is longer than %d characters", MaxPlanBrief)
	}
	if p.Step == "" {
		p.Step = steps[0]
	}
	if !slices.Contains(steps, p.Step) {
		return fmt.Errorf("unknown step %q", p.Step)
	}
	return nil
}

// CleanPlanTurn is CleanTurn of a project plan.
func CleanPlanTurn(t PlanTurn, prev []PlanTopic) (PlanTurn, error) {
	return PlanKinds[PlanProject].CleanTurn(t, prev)
}

// CleanTurn keeps an interviewer's answer within what a plan of kind k
// holds: questions with text, ids and k's topics, at most
// MaxPlanQuestions; k's topics, with known statuses (one it doesn't
// mention keeps prev's); a brief of at most MaxPlanBrief.
func (k PlanKind) CleanTurn(t PlanTurn, prev []PlanTopic) (PlanTurn, error) {
	// Lists are never null in JSON: the planner reads them as they are.
	out := PlanTurn{Note: strings.TrimSpace(t.Note), Brief: strings.TrimSpace(t.Brief), Verdict: t.Verdict,
		Questions: []PlanQuestion{}, Open: []string{}}
	if out.Brief == "" {
		return out, errors.New("the interviewer wrote no brief")
	}
	if len(out.Brief) > MaxPlanBrief {
		out.Brief = out.Brief[:MaxPlanBrief]
	}
	known := func(id string) bool {
		return slices.ContainsFunc(k.Topics, func(x PlanTopic) bool { return x.ID == id })
	}
	seen := map[string]bool{}
	for i, q := range t.Questions {
		q.Text, q.Default = strings.TrimSpace(q.Text), strings.TrimSpace(q.Default)
		if q.Text == "" || len(out.Questions) == MaxPlanQuestions {
			continue
		}
		if q.ID = strings.TrimSpace(q.ID); q.ID == "" || seen[q.ID] {
			q.ID = fmt.Sprintf("q%d", i+1)
		}
		seen[q.ID] = true
		if !known(q.Topic) {
			q.Topic = ""
		}
		var opts []string
		for _, o := range q.Options {
			if o = strings.TrimSpace(o); o != "" && !slices.Contains(opts, o) && len(opts) < 5 {
				opts = append(opts, o)
			}
		}
		q.Options = opts
		out.Questions = append(out.Questions, q)
	}
	status := map[string]string{}
	for _, x := range prev {
		status[x.ID] = x.Status
	}
	for _, x := range t.Topics {
		switch x.Status {
		case PlanTopicOpen, PlanTopicDone, PlanTopicSkipped:
			status[x.ID] = x.Status
		}
	}
	for _, x := range k.Topics {
		x.Status = status[x.ID]
		if x.Status == "" {
			x.Status = PlanTopicOpen
		}
		out.Topics = append(out.Topics, x)
	}
	for _, q := range t.Open {
		if q = strings.TrimSpace(q); q != "" {
			out.Open = append(out.Open, q)
		}
	}
	out.Verdict.Summary = strings.TrimSpace(out.Verdict.Summary)
	var wait []string
	for _, w := range out.Verdict.CanWait {
		if w = strings.TrimSpace(w); w != "" {
			wait = append(wait, w)
		}
	}
	out.Verdict.CanWait = wait
	return out, nil
}
