package mcptools

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// pCard is the card id parameter of the planning tools.
var pCard = Param{"card", "string", "Card id (see list_cards).", true}

// The planning tools: the org's plan (docs/planning.md), cards in the
// columns ideas, planned, in_progress and done, each linked to the tasks and
// goals that carry it out. The chats, dash-mcp and the claude.ai connector
// list, read, add, change, comment on and start cards, and link work to them,
// so the plan stays in step with what they start.

// CardRow is a card as the planning tools show it.
type CardRow struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Column   string `json:"column"`
	Priority string `json:"priority,omitempty"`
	Target   string `json:"target,omitempty"`
	Project  string `json:"project,omitempty"`
	// Members are the usernames of the people on the card.
	Members []string `json:"members,omitempty"`
	// Owner is the username of the member who owns the card ("": nobody).
	Owner string   `json:"owner,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	// Status is what its linked work adds up to, e.g. "Waiting for review".
	Status   string `json:"status"`
	Progress string `json:"progress,omitempty"`
	Links    int    `json:"links"`
	// DependsOn are the ids of the cards to be done before this one;
	// Waiting counts those not done yet.
	DependsOn []string `json:"depends_on,omitempty"`
	Waiting   int      `json:"waiting,omitempty"`
}

// NewCard is a card to add. Project is a project name or id ("" for none).
// DependsOn are ids of cards to be done before it.
type NewCard struct {
	Title, Description, Project, Column, Priority, Target string
	Tags                                                  []string
	DependsOn                                             []string
}

// CardDetail is one card with what list_cards leaves out: its description,
// the cards that depend on it, its linked work and its comments.
type CardDetail struct {
	CardRow
	Description string     `json:"description,omitempty"`
	NeededBy    []string   `json:"needed_by,omitempty"`
	Work        []CardWork `json:"work"`
	Comments    []CardNote `json:"comments,omitempty"`
}

// CardWork is a task or goal linked to a card, with its state in owner's
// words ("Waiting for review").
type CardWork struct {
	Kind    string `json:"kind"` // task or goal
	ID      string `json:"id"`
	Title   string `json:"title"`
	Project string `json:"project,omitempty"`
	State   string `json:"state"`
}

// CardNote is a comment on a card.
type CardNote struct {
	Author string    `json:"author,omitempty"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

// CardUpdate changes a card; nil fields stay as they are. Column moves it
// (to the end of that column, or to Position: 0 is the top).
type CardUpdate struct {
	Title, Description, Priority, Target *string
	Tags, DependsOn                      *[]string
	Column                               *string
	Position                             *int
}

// CardStarted is a card that start_card made work for: the new task's or
// goal's kind and id.
type CardStarted struct {
	Card CardRow `json:"card"`
	Kind string  `json:"kind"`
	ID   string  `json:"id"`
}

// CardBoard is a Board with the org's plan: Tools then has the planning tools
// (see PlanningTools).
type CardBoard interface {
	// Cards lists the org's cards, of one project (projectID) or all ("").
	Cards(projectID string) ([]CardRow, error)
	// Card is one card, with its description, work and comments.
	Card(id string) (CardDetail, error)
	CreateCard(c NewCard) (CardRow, error)
	// UpdateCard changes a card's fields, and moves it when u.Column is set.
	UpdateCard(id string, u CardUpdate) (CardRow, error)
	// CommentCard adds the caller's comment to a card.
	CommentCard(id, text string) (CardNote, error)
	// StartCard makes a task (kind "task", queued) or goal ("goal") for a
	// card in a project ("": the card's), links it and moves the card to In
	// progress, as Start on the Planning page does.
	StartCard(id, project, kind string) (CardStarted, error)
	// LinkCard links a card to a task (kind "task") or goal ("goal").
	LinkCard(cardID, kind, workID string) (CardRow, error)
}

// PlanningToolNames are the PlanningTools.
var PlanningToolNames = []string{"list_cards", "get_card", "create_card", "update_card", "comment_card", "start_card", "link_card"}

// PlanningTools returns list_cards, get_card, create_card, update_card,
// comment_card, start_card and link_card on cb.
func PlanningTools(b Board, cb CardBoard, o Options) []Tool {
	pProject, project := projectParam(o)
	pProject.Required = false
	pProject.Desc = "Only this project's cards (name or id); default every project's."
	return []Tool{
		{"list_cards", "List the cards of the org's plan (the Planning page): what a product owner wants built, by column " +
			"(ideas, planned, in_progress, done), with each card's priority, target, members, owner, tags, the cards it depends on " +
			"(depends_on; waiting counts those not done yet) and the status of the tasks and goals linked to it.",
			[]Param{pProject, {"column", "string", "Only this column: ideas, planned, in_progress or done.", false},
				{"tag", "string", "Only the cards with this tag (any case).", false}},
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
					if col := a.Str("column"); col != "" && c.Column != col {
						continue
					}
					if tag := a.Str("tag"); tag != "" && !slices.ContainsFunc(c.Tags, func(t string) bool { return strings.EqualFold(t, tag) }) {
						continue
					}
					out = append(out, c)
				}
				return out, nil
			}},
		{"get_card", "Show one card of the plan: its fields and description, the cards it depends on and that depend on it, " +
			"the tasks and goals linked to it with their state, and its comments.",
			[]Param{pCard},
			func(a Args) (any, error) {
				if a.Str("card") == "" {
					return nil, errors.New("card is required")
				}
				return cb.Card(a.Str("card"))
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
				{"tags", "array", "Short labels for the card, e.g. [\"backend\", \"Q3\"].", false},
				{"depends_on", "array", "Ids of cards (see list_cards) that must be done before this one.", false},
			},
			func(a Args) (any, error) {
				if strings.TrimSpace(a.Str("title")) == "" {
					return nil, errors.New("title is required")
				}
				return cb.CreateCard(NewCard{Title: a.Str("title"), Description: a.Str("description"), Project: a.Str("project"),
					Column: a.Str("column"), Priority: a.Str("priority"), Target: a.Str("target"), Tags: a.List("tags"),
					DependsOn: a.List("depends_on")})
			}},
		{"update_card", "Change a card of the plan, or move it to another column; only the given fields change. " +
			"Moving a card to done is for when the user says it is done (the card's status says when its work is).",
			[]Param{pCard,
				{"title", "string", "New title.", false},
				{"description", "string", "New description (markdown); replaces the old one.", false},
				{"priority", "string", "high, medium or low; none (or an empty string) for none.", false},
				{"target", "string", "When it should ship: a version like 1.4, or a date; an empty string for none.", false},
				{"tags", "array", "The card's tags; replaces them, an empty array clears them.", false},
				{"depends_on", "array", "Ids of the cards to be done before this one; replaces the list, an empty array clears it.", false},
				{"column", "string", "Move it to this column: ideas, planned, in_progress or done.", false},
				{"position", "integer", "Where in the column to put it, from 0 (the top); default the end. Needs column.", false},
			},
			func(a Args) (any, error) {
				if a.Str("card") == "" {
					return nil, errors.New("card is required")
				}
				u := CardUpdate{Title: a.OptStr("title"), Description: a.OptStr("description"), Priority: a.OptStr("priority"),
					Target: a.OptStr("target"), Tags: a.OptList("tags"), DependsOn: a.OptList("depends_on"), Column: a.OptStr("column"),
					Position: a.OptNum("position")}
				if u.Position != nil && u.Column == nil {
					return nil, errors.New("position needs column")
				}
				if u.Title == nil && u.Description == nil && u.Priority == nil && u.Target == nil && u.Tags == nil && u.DependsOn == nil && u.Column == nil {
					return nil, errors.New("nothing to change: pass title, description, priority, target, tags, depends_on or column")
				}
				return cb.UpdateCard(a.Str("card"), u)
			}},
		{"comment_card", "Comment on a card of the plan, as the user, for the people who plan it to read.",
			[]Param{pCard, {"text", "string", "The comment (markdown).", true}},
			func(a Args) (any, error) {
				if a.Str("card") == "" {
					return nil, errors.New("card is required")
				}
				if a.Str("text") == "" {
					return nil, errors.New("text is required")
				}
				return cb.CommentCard(a.Str("card"), a.Str("text"))
			}},
		{"start_card", "Start the work for a card, like Start on the Planning page: a task (queued to run now, its prompt the card's title " +
			"and description) or a goal (a coordinator plans it into tasks), linked to the card, which moves to in_progress. " +
			"Only when the user asks to start the card.",
			[]Param{pCard,
				{"kind", "string", "task (the default) or goal.", false},
				{"project", "string", "The project to start it in (name or id); default the card's project.", false},
			},
			func(a Args) (any, error) {
				if a.Str("card") == "" {
					return nil, errors.New("card is required")
				}
				kind := a.Str("kind")
				switch kind {
				case "", "task", "goal":
				default:
					return nil, fmt.Errorf("unknown kind %q: start a task or a goal", kind)
				}
				pid := ""
				if a.Str("project") != "" {
					p, err := b.Project(a.Str("project"))
					if err != nil {
						return nil, err
					}
					pid = p.ID
				}
				return cb.StartCard(a.Str("card"), pid, kind)
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

// ---- the fleet's plan ----

// planBoard is the computer that keeps the org's plan: the first reachable
// one that has it, which is the one the client logged in to (the hub lists
// itself first).
func (f Fleet) planBoard() (CardBoard, error) {
	ms, err := f.machines()
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		if cb, ok := m.Board.(CardBoard); ok && m.OK {
			return cb, nil
		}
	}
	return nil, errors.New("this Dash has no plan")
}

func (f Fleet) Cards(projectID string) ([]CardRow, error) {
	cb, err := f.planBoard()
	if err != nil {
		return nil, err
	}
	return cb.Cards(projectID)
}

func (f Fleet) Card(id string) (CardDetail, error) {
	cb, err := f.planBoard()
	if err != nil {
		return CardDetail{}, err
	}
	return cb.Card(id)
}

func (f Fleet) CreateCard(c NewCard) (CardRow, error) {
	cb, err := f.planBoard()
	if err != nil {
		return CardRow{}, err
	}
	if c.Project != "" {
		p, err := f.Project(c.Project)
		if err != nil {
			return CardRow{}, err
		}
		c.Project = p.ID
	}
	return cb.CreateCard(c)
}

func (f Fleet) UpdateCard(id string, u CardUpdate) (CardRow, error) {
	cb, err := f.planBoard()
	if err != nil {
		return CardRow{}, err
	}
	return cb.UpdateCard(id, u)
}

func (f Fleet) CommentCard(id, text string) (CardNote, error) {
	cb, err := f.planBoard()
	if err != nil {
		return CardNote{}, err
	}
	return cb.CommentCard(id, text)
}

func (f Fleet) StartCard(id, project, kind string) (CardStarted, error) {
	cb, err := f.planBoard()
	if err != nil {
		return CardStarted{}, err
	}
	return cb.StartCard(id, project, kind)
}

func (f Fleet) LinkCard(cardID, kind, workID string) (CardRow, error) {
	cb, err := f.planBoard()
	if err != nil {
		return CardRow{}, err
	}
	return cb.LinkCard(cardID, kind, workID)
}

// ---- over the REST API (/api/planning) ----

// restCard is a card as GET /api/planning/cards answers it.
type restCard struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Column      string   `json:"column"`
	Priority    string   `json:"priority"`
	Target      string   `json:"target"`
	ProjectName string   `json:"project_name"`
	Tags        []string `json:"tags"`
	Owner       int64    `json:"owner"`
	People      []struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"people"`
	Status struct {
		Text     string `json:"text"`
		Progress string `json:"progress"`
	} `json:"status"`
	Links []struct {
		Kind    string `json:"kind"`
		ID      string `json:"id"`
		Title   string `json:"title"`
		Project string `json:"project"`
		State   string `json:"state"`
	} `json:"links"`
	Deps     []struct{ ID string } `json:"deps"`
	NeededBy []struct{ ID string } `json:"needed_by"`
	Waiting  int                   `json:"waiting"`
	// Discussion is the card's comments (GET of one card).
	Discussion []struct {
		AuthorName string    `json:"author_name"`
		Text       string    `json:"text"`
		CreatedAt  time.Time `json:"created_at"`
	} `json:"discussion"`
	// Started is the work start_card made (POST …/start).
	Started *struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"started"`
}

func (c restCard) row() CardRow {
	r := CardRow{ID: c.ID, Title: c.Title, Column: c.Column, Priority: c.Priority, Target: c.Target, Project: c.ProjectName,
		Tags: c.Tags, Status: c.Status.Text, Progress: c.Status.Progress, Links: len(c.Links), Waiting: c.Waiting}
	for _, p := range c.People {
		r.Members = append(r.Members, p.Username)
		if p.ID != 0 && p.ID == c.Owner {
			r.Owner = p.Username
		}
	}
	for _, d := range c.Deps {
		r.DependsOn = append(r.DependsOn, d.ID)
	}
	return r
}

func (c restCard) detail() CardDetail {
	d := CardDetail{CardRow: c.row(), Description: c.Description, Work: []CardWork{}}
	for _, n := range c.NeededBy {
		d.NeededBy = append(d.NeededBy, n.ID)
	}
	for _, l := range c.Links {
		d.Work = append(d.Work, CardWork{l.Kind, l.ID, l.Title, l.Project, l.State})
	}
	for _, m := range c.Discussion {
		d.Comments = append(d.Comments, CardNote{m.AuthorName, m.Text, m.CreatedAt})
	}
	return d
}

func (c RESTComputer) cardPath(id string, rest ...string) string {
	return c.Prefix + "/api/planning/cards/" + url.PathEscape(id) + strings.Join(rest, "")
}

func (c RESTComputer) Cards(projectID string) ([]CardRow, error) {
	path := c.Prefix + "/api/planning/cards"
	if projectID != "" {
		path += "?project=" + url.QueryEscape(projectID)
	}
	var cs []restCard
	if err := c.API.Call("GET", path, nil, &cs); err != nil {
		return nil, err
	}
	out := make([]CardRow, len(cs))
	for i, x := range cs {
		out[i] = x.row()
	}
	return out, nil
}

func (c RESTComputer) Card(id string) (CardDetail, error) {
	var out restCard
	if err := c.API.Call("GET", c.cardPath(id), nil, &out); err != nil {
		return CardDetail{}, err
	}
	return out.detail(), nil
}

func (c RESTComputer) CreateCard(nc NewCard) (CardRow, error) {
	body := map[string]any{"title": nc.Title, "description": nc.Description, "project": nc.Project, "priority": nc.Priority,
		"target": nc.Target}
	if len(nc.Tags) > 0 {
		body["tags"] = nc.Tags
	}
	if nc.Column != "" {
		body["column"] = nc.Column
	}
	if len(nc.DependsOn) > 0 {
		body["depends_on"] = nc.DependsOn
	}
	var out restCard
	if err := c.API.Call("POST", c.Prefix+"/api/planning/cards", body, &out); err != nil {
		return CardRow{}, err
	}
	return out.row(), nil
}

func (c RESTComputer) UpdateCard(id string, u CardUpdate) (CardRow, error) {
	changes := map[string]any{}
	for k, v := range map[string]*string{"title": u.Title, "description": u.Description, "priority": u.Priority, "target": u.Target} {
		if v != nil {
			changes[k] = *v
		}
	}
	for k, v := range map[string]*[]string{"tags": u.Tags, "depends_on": u.DependsOn} {
		if v != nil {
			l := *v
			if l == nil {
				l = []string{} // [] clears the list; null would leave it
			}
			changes[k] = l
		}
	}
	var out restCard
	if len(changes) > 0 {
		if err := c.API.Call("PATCH", c.cardPath(id), changes, &out); err != nil {
			return CardRow{}, err
		}
	}
	if u.Column != nil {
		body := map[string]any{"column": *u.Column}
		if u.Position != nil {
			body["position"] = *u.Position
		}
		if err := c.API.Call("POST", c.cardPath(id, "/move"), body, &out); err != nil {
			return CardRow{}, err
		}
	}
	return out.row(), nil
}

func (c RESTComputer) CommentCard(id, text string) (CardNote, error) {
	var out struct {
		AuthorName string    `json:"author_name"`
		Text       string    `json:"text"`
		CreatedAt  time.Time `json:"created_at"`
	}
	if err := c.API.Call("POST", c.cardPath(id, "/comments"), map[string]any{"text": text}, &out); err != nil {
		return CardNote{}, err
	}
	return CardNote{out.AuthorName, out.Text, out.CreatedAt}, nil
}

func (c RESTComputer) StartCard(id, project, kind string) (CardStarted, error) {
	var out restCard
	if err := c.API.Call("POST", c.cardPath(id, "/start"), map[string]any{"project": project, "kind": kind}, &out); err != nil {
		return CardStarted{}, err
	}
	s := CardStarted{Card: out.row()}
	if out.Started != nil {
		s.Kind, s.ID = out.Started.Kind, out.Started.ID
	}
	return s, nil
}

func (c RESTComputer) LinkCard(cardID, kind, workID string) (CardRow, error) {
	var out restCard
	if err := c.API.Call("POST", c.cardPath(cardID, "/links"), map[string]any{"kind": kind, "id": workID}, &out); err != nil {
		return CardRow{}, err
	}
	return out.row(), nil
}
