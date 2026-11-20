package api

import (
	"context"
	"slices"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/textutil"
)

// Card news (notify.CardNews): the people a change to a Planning card
// concerns are pushed what happened, planners too. Someone added to a
// card's members hears of it, and so does someone @mentioned in a card's
// comment (by their username). The caller never hears of their own doing.
// The news of a card's work (ready for review, failed, merged) comes from
// the notifier, which watches the tasks.

// cardNews pushes news of card c to users, but not to the caller.
func (sv *service) cardNews(c model.Card, users []int64, head, message string) {
	if !sv.s.Notify.PushEnabled() || sv.c.User == 0 {
		return
	}
	users = slices.DeleteFunc(slices.Clone(users), func(u int64) bool { return u == sv.c.User })
	if len(users) == 0 {
		return
	}
	// Not the request's context: the pushes go on after the answer.
	sv.s.Notify.CardNews(context.Background(), sv.c.Org, c, users, head, message)
}

// callerName is the caller's username ("Someone" if it can't be had).
func (sv *service) callerName() string {
	if u, err := sv.s.Store.Accounts().User(sv.c.User); err == nil && u.Username != "" {
		return u.Username
	}
	return "Someone"
}

// membersAdded tells the people added to card c (now as after, before it
// was before: nil for a new card) that they were.
func (sv *service) membersAdded(c model.Card, before []int64) {
	var added []int64
	for _, u := range c.Members {
		if !slices.Contains(before, u) {
			added = append(added, u)
		}
	}
	if len(added) > 0 {
		sv.cardNews(c, added, sv.callerName()+" added you to a card", "")
	}
}

// mentionedIn tells the org's members whom comment m on card cid mentions.
func (sv *service) mentionedIn(cid string, m model.CardComment) {
	if !sv.s.Notify.PushEnabled() {
		return
	}
	ms, err := sv.s.Store.Accounts().Members(sv.c.Org)
	if err != nil {
		return
	}
	var users []int64
	for _, p := range ms {
		if textutil.Mentioned(m.Text, p.Username) {
			users = append(users, p.ID)
		}
	}
	if len(users) == 0 {
		return
	}
	c, err := sv.data.GetCard(cid)
	if err != nil {
		return
	}
	sv.cardNews(c, users, sv.callerName()+" mentioned you", m.Text)
}
