package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Planning is the org's plan, for a product owner: cards in fixed columns,
// each linked to the tasks and goals that carry it out (see docs/planning.md).

// CardColumn is where a card is on the planning board.
type CardColumn string

const (
	CardIdeas      CardColumn = "ideas"
	CardPlanned    CardColumn = "planned"
	CardInProgress CardColumn = "in_progress"
	CardDone       CardColumn = "done"
)

// CardColumns are the board's columns, left to right.
var CardColumns = []CardColumn{CardIdeas, CardPlanned, CardInProgress, CardDone}

// Valid reports whether c is one of CardColumns.
func (c CardColumn) Valid() bool {
	for _, x := range CardColumns {
		if c == x {
			return true
		}
	}
	return false
}

// ParseCardColumn reads a column's name as an API user may write it: any
// case, with spaces around it ("Planned" is planned). It doesn't check it:
// see Valid.
func ParseCardColumn(s string) CardColumn {
	return CardColumn(strings.ToLower(strings.TrimSpace(s)))
}

// Card priorities ("" is none).
const (
	CardHigh   = "high"
	CardMedium = "medium"
	CardLow    = "low"
)

// Limits on a card's text, its members and its dependencies.
const (
	MaxCardTitle       = 200
	MaxCardDescription = 20000
	MaxCardTarget      = 60
	MaxCardComment     = 10000
	MaxCardMembers     = 50
	MaxCardDeps        = 50
	MaxCardTags        = 20
	MaxCardTag         = 40
	MaxCardFiles       = 30
)

// CardBoard is one of an org's planning boards (one per product or team,
// say): its cards in the fixed columns. Position orders the org's boards,
// from 0. Every org has one default board, which can't be deleted: a card
// given no board goes on it, and the cards of a board that is deleted move
// to it.
type CardBoard struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Position  int       `json:"position"`
	Default   bool      `json:"default"`
	CreatedAt time.Time `json:"created_at"`
}

// MaxCardBoardName is how long a planning board's name may be.
const MaxCardBoardName = 60

// CleanCardBoardName trims a board's name and checks it.
func CleanCardBoardName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	switch {
	case name == "":
		return "", errors.New("a board needs a name")
	case len(name) > MaxCardBoardName:
		return "", fmt.Errorf("the board's name is longer than %d characters", MaxCardBoardName)
	}
	return name, nil
}

// Card is one item of the plan. ProjectID is "" for a card of no project in
// particular. BoardID is the org's board it is on. Position orders a
// column's cards on its board, from 0.
type Card struct {
	ID          string     `json:"id"`
	OrgID       int64      `json:"-"`
	BoardID     string     `json:"board_id"`
	ProjectID   string     `json:"project_id"`
	Title       string     `json:"title"`
	Description string     `json:"description"` // markdown
	Column      CardColumn `json:"column"`
	Position    int        `json:"position"`
	Priority    string     `json:"priority"`
	// Target is when it should ship: free text, a version ("1.4") or a date.
	Target string `json:"target"`
	// Members are the people in the org on the card (user ids), in the
	// order they were added.
	Members []int64 `json:"members"`
	// Owner is the one member who answers for the card (a user id in
	// Members; 0: nobody).
	Owner int64 `json:"owner"`
	// DependsOn are the org's other cards that must be done (in the Done
	// column) before this one, in the order they were added.
	DependsOn []string `json:"depends_on"`
	// Tags are short labels on the card ("backend", "Q3"), in the order
	// they were added; each once, whatever its case.
	Tags []string `json:"tags"`
	// Files are the files attached to the card, in the order they were added.
	Files     []CardFile `json:"files"`
	CreatedBy int64      `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Version names the state of the card's fields a PATCH changes (title,
// description, project, priority, target, members, owner, dependencies and
// tags): it changes whenever one of them does, and not when the card only
// moves or gets a file. An edit that sends the version it started from is
// refused when it no longer matches, so one planner's edit doesn't silently
// replace another's.
func (c Card) Version() string {
	// nil and empty lists are the same card.
	b, _ := json.Marshal([]any{c.Title, c.Description, c.ProjectID, c.Priority, c.Target,
		append([]int64{}, c.Members...), c.Owner, append([]string{}, c.DependsOn...), append([]string{}, c.Tags...)})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// CardFile is a file attached to a card: an upload (its ID is the upload's
// id), as anyone who sees the card may see it. Where it is stored is the
// server's, not the card's.
type CardFile struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Type    string    `json:"type"`
	Size    int64     `json:"size"`
	AddedBy int64     `json:"added_by"`
	AddedAt time.Time `json:"added_at"`
}

// CardComment is a comment on a card.
type CardComment struct {
	ID     int64  `json:"id"`
	CardID string `json:"card_id"`
	Author int64  `json:"author"`
	// AuthorName is the author's username ("" once their account is gone).
	AuthorName string    `json:"author_name"`
	Text       string    `json:"text"`
	CreatedAt  time.Time `json:"created_at"`
}

// What a card links to (CardLink.Kind).
const (
	LinkTask = "task"
	LinkGoal = "goal"
)

// CardLink links a card to a task or a goal that carries it out.
type CardLink struct {
	CardID    string    `json:"card_id"`
	Kind      string    `json:"kind"` // LinkTask or LinkGoal
	WorkID    string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

// titleFromText takes a card's title from the first line of its text and
// returns it with the rest of the text. A line too long for the title stays
// in the text, as the title holds only its start.
func titleFromText(text string, max int) (title, rest string) {
	title = firstLine(text, max)
	if title == "" || title != firstLine(text, len(text)) {
		return title, text // cut short: the text keeps the whole line
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if firstLine(l, len(l)) != "" {
			return title, strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
	}
	return title, text
}

// CleanCard trims a card's text and checks it, and its column and priority.
func CleanCard(c *Card) error {
	c.Title = strings.TrimSpace(c.Title)
	c.Description = strings.TrimSpace(c.Description)
	c.Target = strings.TrimSpace(c.Target)
	c.Priority = strings.ToLower(strings.TrimSpace(c.Priority))
	if c.Priority == "none" {
		c.Priority = ""
	}
	c.Column = ParseCardColumn(string(c.Column))
	switch {
	case c.Title == "" && c.Description == "":
		return errors.New("a card needs a title")
	case len(c.Title) > MaxCardTitle:
		return fmt.Errorf("the title is longer than %d characters", MaxCardTitle)
	case len(c.Description) > MaxCardDescription:
		return fmt.Errorf("the description is longer than %d characters", MaxCardDescription)
	case len(c.Target) > MaxCardTarget:
		return fmt.Errorf("the target is longer than %d characters", MaxCardTarget)
	case strings.ContainsAny(c.Target, "\r\n"):
		return errors.New("the target is one line")
	}
	if c.Title == "" {
		c.Title, c.Description = titleFromText(c.Description, 80)
	}
	if c.Column == "" {
		c.Column = CardIdeas
	}
	if !c.Column.Valid() {
		return fmt.Errorf("unknown column %q: use ideas, planned, in_progress or done", c.Column)
	}
	switch c.Priority {
	case "", CardHigh, CardMedium, CardLow:
	default:
		return fmt.Errorf("unknown priority %q: use high, medium, low or none", c.Priority)
	}
	// Each member once, in the order given.
	seen := map[int64]bool{}
	members := []int64{}
	for _, id := range c.Members {
		if id > 0 && !seen[id] {
			seen[id] = true
			members = append(members, id)
		}
	}
	if len(members) > MaxCardMembers {
		return fmt.Errorf("a card has at most %d members", MaxCardMembers)
	}
	c.Members = members
	// The owner is one of the members: an owner who isn't one yet joins.
	if c.Owner < 0 {
		c.Owner = 0
	}
	if c.Owner != 0 && !slices.Contains(c.Members, c.Owner) {
		if len(c.Members) >= MaxCardMembers {
			return fmt.Errorf("a card has at most %d members", MaxCardMembers)
		}
		c.Members = append(c.Members, c.Owner)
	}
	// Each dependency once, in the order given.
	seenDep := map[string]bool{}
	deps := []string{}
	for _, id := range c.DependsOn {
		if id = strings.TrimSpace(id); id != "" && !seenDep[id] {
			seenDep[id] = true
			deps = append(deps, id)
		}
	}
	if len(deps) > MaxCardDeps {
		return fmt.Errorf("a card depends on at most %d cards", MaxCardDeps)
	}
	c.DependsOn = deps
	tags, err := CleanTags(c.Tags)
	if err != nil {
		return err
	}
	c.Tags = tags
	return nil
}

// CleanTags trims each tag and squeezes its spaces, drops empty ones and
// repeats (the first of those that differ only in case stays), and checks
// their number and length.
func CleanTags(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range in {
		t = strings.Join(strings.Fields(t), " ")
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		if len(t) > MaxCardTag {
			return nil, fmt.Errorf("the tag %q is longer than %d characters", t, MaxCardTag)
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	if len(out) > MaxCardTags {
		return nil, fmt.Errorf("a card has at most %d tags", MaxCardTags)
	}
	return out, nil
}

// CleanComment trims a comment and checks its length.
func CleanComment(text string) (string, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "", errors.New("a comment needs text")
	case len(text) > MaxCardComment:
		return "", fmt.Errorf("the comment is longer than %d characters", MaxCardComment)
	}
	return text, nil
}
