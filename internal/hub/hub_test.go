package hub

import (
	"testing"

	"github.com/klahr/dash/internal/model"
)

// An event for one user (their own project chat, say) reaches only their
// dashboards in its org: not another member's, not another org's, not
// another Dash's that receives every org's events.
func TestPublishToOneUser(t *testing.T) {
	h := New()
	sub := func(s Subscriber) *client {
		c := &client{send: make(chan model.Event, 4), sub: s}
		h.clients[c] = struct{}{}
		return c
	}
	ann1, ann2 := sub(Subscriber{Org: 1, User: 10}), sub(Subscriber{Org: 1, User: 10})
	bob := sub(Subscriber{Org: 1, User: 11})
	annElsewhere := sub(Subscriber{Org: 2, User: 10})
	machine := sub(Subscriber{All: true})

	h.Publish(model.Event{Type: model.EvProjectChat, ProjectID: "p", OrgID: 1, UserID: 10})
	h.Publish(model.Event{Type: model.EvProjectUpdated, ProjectID: "p", OrgID: 1})

	for name, c := range map[string]*client{"ann's first device": ann1, "ann's second device": ann2} {
		if n := len(c.send); n != 2 {
			t.Errorf("%s got %d events, want both", name, n)
		}
	}
	for name, c := range map[string]*client{"bob": bob, "another Dash": machine} {
		if n := len(c.send); n != 1 || (<-c.send).Type != model.EvProjectUpdated {
			t.Errorf("%s got %d events, want only the org's", name, n)
		}
	}
	if n := len(annElsewhere.send); n != 0 {
		t.Errorf("ann in another org got %d events", n)
	}
}
