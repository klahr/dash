package notify

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// A runner's checkout left behind origin notifies only webhooks that chose
// it, once when it gets stuck, and at most every BehindEvery.
func TestCheckoutBehindIsOptInAndRateLimited(t *testing.T) {
	chosen := newReceiver(t)
	defaults := newReceiver(t)
	n, _, _ := setup(t,
		model.Webhook{ID: "behind", ProjectID: "p1", URL: chosen.URL, Format: model.WebhookJSON, Enabled: true, Events: []model.NotifyEvent{model.NotifyBehind}, Secret: "s"},
		model.Webhook{ID: "new", ProjectID: "p1", URL: defaults.URL, Format: model.WebhookJSON, Enabled: true, Events: model.DefaultNotifyEvents, Secret: "s"},
	)
	var mu sync.Mutex
	now := t0
	n.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	stuck := []model.Checkout{{Runner: "r1", RunnerName: "laptop", Branch: "main", Behind: 3, Stuck: "uncommitted changes in /src/shop"}}
	catching := []model.Checkout{{Runner: "r1", RunnerName: "laptop", Branch: "main", Behind: 3}}
	send := func(cs []model.Checkout) {
		n.Observe(model.Event{Type: model.EvCheckouts, ProjectID: "p1", Data: cs})
		settle(n)
	}
	send(catching) // behind but still following: no news
	send(stuck)
	send(stuck) // the same, reported again
	if got := chosen.events(); len(got) != 1 || got[0] != "checkout.behind" {
		t.Fatalf("events = %v; want one checkout.behind", got)
	}
	var p struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal([]byte(chosen.bodies[0]), &p)
	if !strings.Contains(p.Message, "laptop") || !strings.Contains(p.Message, "3 commits behind") || !strings.Contains(p.Message, "uncommitted") {
		t.Fatalf("message = %q", p.Message)
	}
	// Unstuck and stuck again soon after: rate-limited.
	send(nil)
	send(stuck)
	if len(chosen.events()) != 1 {
		t.Fatalf("notified again within %v: %v", DefaultBehindEvery, chosen.events())
	}
	// Later, it may notify again.
	send(nil)
	advance(DefaultBehindEvery)
	send(stuck)
	if len(chosen.events()) != 2 {
		t.Fatalf("events = %v; want a second after %v", chosen.events(), DefaultBehindEvery)
	}
	if len(defaults.events()) != 0 {
		t.Fatalf("a webhook with the default events got %v", defaults.events())
	}
}
