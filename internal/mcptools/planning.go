package mcptools

import (
	"errors"
	"strings"
)

// The planning tools: the org's plan (docs/planning.md), cards in the
// columns ideas, planned, in_progress and done, each linked to the tasks and
// goals that carry it out. The chat lists and adds cards, and links work to
// them, so the plan stays in step with what it starts.

// CardRow is a card as the planning tools show it.
type CardRow struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Column   string `json:"column"`
	Priority string `json:"priority,omitempty"`
	Target   string `json:"target,omitempty"`
	Project  string `json:"project,omitempty"`
	// Assignee is the username of the member who owns the card.
	Assignee string `json:"assignee,omitempty"`
	// Status is what its linked work adds up to, e.g. "Waiting for review".
	Status   string `json:"status"`
	Progress string `json:"progress,omitempty"`
	Links    int    `json:"links"`
}

// NewCard is a card to add. Project is a project name or id ("" for none).
type NewCard struct {
	Title, Description, Project, Column, Priority, Target string
}

// CardBoard is a Board with the org's plan: Tools then has PlanningToolNames.
type CardBoard interface {
	// Cards lists the org's cards, of one project (projectID) or all ("").
	Cards(projectID string) ([]CardRow, error)
	CreateCard(c NewCard) (CardRow, error)
	// LinkCard links a card to a task (kind "task") or goal ("goal").
	LinkCard(cardID, kind, workID string) (CardRow, error)
}

// PlanningToolNames are the planning tools; list_cards only looks.
var PlanningToolNames = []string{"list_cards", "create_card", "link_card"}

// PlanningTools returns list_cards, create_card and link_card on cb.
func PlanningTools(b Board, cb CardBoard, o Options) []Tool {
	pProject, project := projectParam(o)
	pProject.Required = false
	pProject.Desc = "Only this project's cards (name or id); default every project's."
	return []Tool{
		{"list_cards", "List the cards of the org's plan (the Planning page): what a product owner wants built, by column " +
			"(ideas, planned, in_progress, done), with each card's priority, target, assignee and the status of the tasks and goals linked to it.",
			[]Param{pProject, {"column", "string", "Only this column: ideas, planned, in_progress or done.", false}},
			func(a Args) (any, error) {
				pid := ""
				if a.Str("project") != "" {
					p, err := project(b, a)
					if err != nil {
						return nil, err
					}
					pid = p.ID
				}
				cs, err := cb.Cards(pid)
				if err != nil {
					return nil, err
				}
				out := []CardRow{}
				for _, c := range cs {
					if col := a.Str("column"); col == "" || c.Column == col {
						out = append(out, c)
					}
				}
				return out, nil
			}},
		{"create_card", "Add a card to the org's plan (the Planning page): something to build, for a product owner to plan. " +
			"It doesn't start any work: start it with create_todo or create_goal and link_card, or from the card.",
			[]Param{
				{"title", "string", "What to build, in a few words.", true},
				{"description", "string", "What it is, who it's for and how you'll know it's done (markdown).", false},
				{"project", "string", "The project it's for (name or id); none if left out.", false},
				{"column", "string", "ideas (the default), planned, in_progress or done.", false},
				{"priority", "string", "high, medium or low; none if left out.", false},
				{"target", "string", "When it should ship: a version like 1.4, or a date.", false},
			},
			func(a Args) (any, error) {
				if strings.TrimSpace(a.Str("title")) == "" {
					return nil, errors.New("title is required")
				}
				return cb.CreateCard(NewCard{Title: a.Str("title"), Description: a.Str("description"), Project: a.Str("project"),
					Column: a.Str("column"), Priority: a.Str("priority"), Target: a.Str("target")})
			}},
		{"link_card", "Link a task or a goal to a card of the plan, so the card shows its status (see list_cards for card ids).",
			[]Param{
				{"card", "string", "Card id (see list_cards).", true},
				{"todo", "string", "Task id to link (or give goal).", false},
				{"goal", "string", "Goal id to link (or give todo).", false},
			},
			func(a Args) (any, error) {
				card := a.Str("card")
				if card == "" {
					return nil, errors.New("card is required")
				}
				switch todo, goal := a.Str("todo"), a.Str("goal"); {
				case todo != "" && goal != "":
					return nil, errors.New("give todo or goal, not both")
				case todo != "":
					return cb.LinkCard(card, "task", todo)
				case goal != "":
					return cb.LinkCard(card, "goal", goal)
				}
				return nil, errors.New("todo or goal is required")
			}},
	}
}
