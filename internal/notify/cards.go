package notify

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/klahr/dash/internal/applink"
	"github.com/klahr/dash/internal/model"
)

// Planning cards and the people on them. A planner (a product owner) sees
// Planning only, not tasks, so:
//
//   - a task a planner created (Start on a card) is answered for by the
//     card's members who can open tasks: they get its news as if they had
//     created it (taskOwners), and the summaries count it as theirs;
//   - the planners on a card (and the planner who started the task) get its
//     news as card.activity, which opens the card;
//   - CardNews tells people they were mentioned in a card's comment or added
//     to a card (internal/api/planning.go).
//
// card.activity is pushed only, to the people it names, if they want it
// (model.NotifySettings.Cards).

// planningStore is a Store that knows the Planning cards that link to a
// task, and members' roles.
type planningStore interface {
	roleStore
	ProjectOrg(projectID string) (int64, bool)
	TaskCards(todoID string) ([]model.Card, error)
}

// cardNote is card news for some users.
type cardNote struct {
	org   int64
	note  Notification
	users map[int64]bool
}

// taskPlanning looks at the Planning cards that link to note's task: it
// hands a planner's task to the cards' other members (note.byPlanner,
// note.owners), and returns the card news for the planners who follow it.
func (n *Notifier) taskPlanning(note *Notification) []cardNote {
	if note.Task == nil || !model.TaskEvent(note.Type) {
		return nil
	}
	ps, ok := n.Store.(planningStore)
	if !ok {
		return nil
	}
	org, ok := ps.ProjectOrg(note.Project.ID)
	if !ok {
		return nil
	}
	cards, err := ps.TaskCards(note.Task.ID)
	if err != nil {
		slog.Error("notifications: a task's planning cards", "task", note.Task.ID, "err", err)
	}
	cards = slices.DeleteFunc(cards, func(c model.Card) bool { return c.OrgID != org })
	rs := n.roles(org)
	if note.creator > 0 && rs.planner(note.creator) {
		note.byPlanner = true
		for _, c := range cards {
			for _, u := range c.Members {
				if r := rs.of(u); r != "" && r != rolePlanner && !slices.Contains(note.owners, u) {
					note.owners = append(note.owners, u)
				}
			}
		}
	}
	if note.Type == model.NotifyPermission || len(cards) == 0 {
		return nil // a planner can't answer for a tool
	}
	// Each planner once, about the first card they are on.
	byCard := map[string]map[int64]bool{}
	told := map[int64]bool{}
	tell := func(c model.Card, u int64) {
		if told[u] || !rs.planner(u) {
			return
		}
		told[u] = true
		if byCard[c.ID] == nil {
			byCard[c.ID] = map[int64]bool{}
		}
		byCard[c.ID][u] = true
	}
	for _, c := range cards {
		for _, u := range c.Members {
			tell(c, u)
		}
	}
	if note.byPlanner {
		tell(cards[0], note.creator)
	}
	var out []cardNote
	for _, c := range cards {
		users := byCard[c.ID]
		if len(users) == 0 {
			continue
		}
		cn := n.cardNotification(c, note.Head(), note.Message)
		cn.Project = note.Project
		out = append(out, cardNote{org: org, note: cn, users: users})
	}
	return out
}

// cardNotification is card.activity news of card c: head says what
// happened, message more (a comment, why the work failed).
func (n *Notifier) cardNotification(c model.Card, head, message string) Notification {
	note := Notification{ID: newEventID(), Type: model.NotifyCard, Time: n.now().UTC().Truncate(time.Second),
		Card: &CardRef{ID: c.ID, Title: c.Title, URL: n.cardURL(c.ProjectID, c.ID)}, Message: message, head: head}
	if c.ProjectID != "" {
		if p, err := n.Store.GetProject(c.ProjectID); err == nil {
			note.Project = ProjectRef{ID: p.ID, Name: p.Name}
		}
	}
	return note
}

// cardURL opens the card on Planning.
func (n *Notifier) cardURL(projectID, cardID string) string {
	o := n.origin(projectID)
	if o == "" {
		return ""
	}
	return o + applink.Card(cardID)
}

// CardNews pushes news of card c, in org, to users (those of them who
// want card news): head says what happened ("Ana mentioned you"), message
// more. A no-op on a Dash that doesn't push.
func (n *Notifier) CardNews(ctx context.Context, org int64, c model.Card, users []int64, head, message string) {
	if !n.PushEnabled() || len(users) == 0 {
		return
	}
	n.init()
	only := map[int64]bool{}
	for _, u := range users {
		only[u] = true
	}
	n.pushTo(ctx, n.cardNotification(c, head, message), org, only)
}
