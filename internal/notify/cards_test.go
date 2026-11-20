package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
)

// planStore is a fakeStore with Planning cards, members' roles and the
// summaries' activity.
type planStore struct {
	*fakeStore
	roles map[int64]string // not in it: a member
	cards []model.Card     // all linking to t1
	acts  []model.Activity
}

func (p *planStore) ProjectOrg(string) (int64, bool) { return 1, true }
func (p *planStore) OrgRole(user, _ int64) (string, error) {
	if r, ok := p.roles[user]; ok {
		return r, nil
	}
	return "member", nil
}
func (p *planStore) TaskCards(id string) ([]model.Card, error) {
	if id != "t1" {
		return nil, nil
	}
	return slices.Clone(p.cards), nil
}
func (p *planStore) RecordActivity(a model.Activity) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.acts = append(p.acts, a)
	return nil
}

// A task a planner started from a card is its developers' (the card's
// members who aren't planners): its news goes to them, and to those who
// chose every task, never to planners. The planners on the card, and the
// planner who started it, hear of it as card news that opens the card.
// CardNews reaches the people it names who want it.
func TestPlannerTaskAndCardNews(t *testing.T) {
	b := newBrowser(t)
	var mu sync.Mutex
	got := map[string][]PushPayload{} // endpoint path → pushes
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p PushPayload
		_ = json.Unmarshal(b.decrypt(t, body), &p)
		mu.Lock()
		got[r.URL.Path] = append(got[r.URL.Path], p)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	key, _ := NewVAPIDPrivateKey()
	vapid, _ := ParseVAPIDKey(key)
	const planner, dev, other, watcher, po2 = 3, 5, 6, 7, 8
	users := []int64{planner, dev, other, watcher, po2}
	var subs []model.PushSubscription
	for _, u := range users {
		s := b.sub(fmt.Sprintf("%s/u%d", srv.URL, u))
		s.ID, s.UserID = fmt.Sprintf("ps%d", u), u
		subs = append(subs, s)
	}
	ps := prefStore{pushStore: &pushStore{subs: subs},
		tasks: map[int64]string{watcher: model.TasksAll, po2: model.TasksAll}}
	n, fs, _ := setup(t)
	st := &planStore{fakeStore: fs, roles: map[int64]string{planner: rolePlanner, po2: rolePlanner}}
	n.Store = st
	n.Push, n.VAPID, n.client = ps, vapid, srv.Client()
	td, _ := fs.GetTodo("t1")
	td.CreatedBy = planner
	fs.setTodo(td)
	card := model.Card{ID: "c1", OrgID: 1, ProjectID: "p1", Title: "Gift cards", Members: []int64{dev, po2}}
	st.cards = []model.Card{card}

	// step runs do, then answers who got which pushes: "u5:task.review …".
	step := func(do func()) string {
		mu.Lock()
		clear(got)
		mu.Unlock()
		do()
		settle(n)
		mu.Lock()
		defer mu.Unlock()
		var out []string
		for _, u := range users {
			for _, p := range got[fmt.Sprintf("/u%d", u)] {
				out = append(out, fmt.Sprintf("u%d:%s", u, p.Type))
			}
		}
		return strings.Join(out, " ")
	}

	if s := step(func() { status(fs, n, model.StatusReview, "") }); s != "u3:card.activity u5:task.review u7:task.review u8:card.activity" {
		t.Errorf("planner's task ready: %s", s)
	}
	mu.Lock()
	if p := got["/u3"]; len(p) != 1 || p[0].URL != "/planning/cards/c1" || p[0].Title != "Ready for review: Gift cards" {
		t.Errorf("card news: %+v", p)
	}
	mu.Unlock()
	st.mu.Lock()
	if len(st.acts) != 1 || st.acts[0].Creator != dev {
		t.Errorf("summary activity: %+v", st.acts)
	}
	st.mu.Unlock()

	if s := step(func() {
		n.Observe(model.Event{Type: model.EvPermission, ProjectID: "p1", Data: perm.Request{ID: "r1", TodoID: "t1", Tool: "Bash"}})
	}); s != "u5:permission.needed u7:permission.needed" {
		t.Errorf("planner's task waits for approval: %s", s)
	}

	// No developer on the card: the task is everyone's (but planners').
	st.cards[0].Members = []int64{po2}
	if s := step(func() { status(fs, n, model.StatusFailed, "boom") }); s != "u3:card.activity u5:task.failed u6:task.failed u7:task.failed u8:card.activity" {
		t.Errorf("planner's task with no developer failed: %s", s)
	}

	// A developer's own task: theirs, as before; the planner on its card
	// hears of it.
	td.CreatedBy = dev
	fs.setTodo(td)
	if s := step(func() { status(fs, n, model.StatusReview, "") }); s != "u5:task.review u7:task.review u8:card.activity" {
		t.Errorf("developer's task ready: %s", s)
	}

	if s := step(func() {
		n.CardNews(context.Background(), 1, card, []int64{dev, po2}, "pia mentioned you", "@dev can you look?")
	}); s != "u5:card.activity u8:card.activity" {
		t.Errorf("mention: %s", s)
	}
	mu.Lock()
	if p := got["/u5"]; len(p) != 1 || p[0].Title != "pia mentioned you: Gift cards" || !strings.Contains(p[0].Body, "@dev can you look?") {
		t.Errorf("mention push: %+v", p)
	}
	mu.Unlock()
}
