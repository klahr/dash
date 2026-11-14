package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Plan is a new project being planned: the user's idea, Claude's interview
// about it (rounds of questions with suggested answers), and the brief it
// writes as it goes. Launching it creates the project, commits the brief,
// and starts a goal from it. A plan is one user's, in one org; until it is
// launched it is a draft they can come back to.
type Plan struct {
	ID   string `json:"id"`
	Name string `json:"name"` // the project's name, optional until launch
	Idea string `json:"idea"` // one or two sentences
	// Step is the planner's step the user was last on (PlanSteps), so a
	// draft opens where they left it.
	Step   string      `json:"step"`
	Rounds []PlanRound `json:"rounds"`
	Topics []PlanTopic `json:"topics"`
	// Brief is the project brief (Markdown), Claude's and the user's edits.
	Brief string `json:"brief"`
	// Open are the questions the brief still has open.
	Open    []string    `json:"open"`
	Verdict PlanVerdict `json:"verdict"`
	// Launched: the project and goal it started, once launched.
	ProjectID  string     `json:"project_id,omitempty"`
	GoalID     string     `json:"goal_id,omitempty"`
	LaunchedAt *time.Time `json:"launched_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// PlanSteps are the planner's steps, in order.
var PlanSteps = []string{"idea", "interview", "brief", "repo", "launch"}

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

// PlanTopics are the interview's topics, in the order it goes through them.
var PlanTopics = []PlanTopic{
	{ID: "users", Title: "Users & purpose"},
	{ID: "features", Title: "Core features"},
	{ID: "data", Title: "Data & storage"},
	{ID: "stack", Title: "Platform & stack"},
	{ID: "auth", Title: "Auth & accounts"},
	{ID: "deploy", Title: "Deployment & hosting"},
	{ID: "scope", Title: "Out of scope"},
}

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

// NewPlanTopics are the topics of a new plan, all open.
func NewPlanTopics() []PlanTopic {
	out := slices.Clone(PlanTopics)
	for i := range out {
		out[i].Status = PlanTopicOpen
	}
	return out
}

// CleanPlanDraft trims what the user typed into a plan and checks it.
func CleanPlanDraft(p *Plan) error {
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
		p.Step = PlanSteps[0]
	}
	if !slices.Contains(PlanSteps, p.Step) {
		return fmt.Errorf("unknown step %q", p.Step)
	}
	return nil
}

// CleanPlanTurn keeps an interviewer's answer within what a plan holds:
// questions with text, ids and known topics, at most MaxPlanQuestions;
// the topics the plan has, with known statuses (one it doesn't mention
// keeps prev's); a brief of at most MaxPlanBrief.
func CleanPlanTurn(t PlanTurn, prev []PlanTopic) (PlanTurn, error) {
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
		return slices.ContainsFunc(PlanTopics, func(x PlanTopic) bool { return x.ID == id })
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
	for _, x := range PlanTopics {
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
