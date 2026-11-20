package api

import (
	"fmt"
	"net/http"
	"testing"
)

// A card has one owner among its members: an owner who isn't a member
// joins, taking the owner off the members leaves the card with no owner,
// and someone outside the org can't own it.
func TestPlanningCardOwner(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	ph, pat := plannerOf(t, s, h)
	admin, err := s.Auth.Accounts().EnvUser()
	if err != nil {
		t.Fatal(err)
	}
	me := admin.ID

	c := decodeCard(t, do(t, ph, "POST", "/api/planning/cards", fmt.Sprintf(`{"title":"Booking","members":[%d],"owner":%d}`, me, pat.ID)))
	if c.Owner != pat.ID || len(c.Members) != 2 || c.Members[1] != pat.ID {
		t.Fatalf("pat owns the card and joins it: owner %d, members %v", c.Owner, c.Members)
	}
	c = decodeCard(t, do(t, ph, "GET", "/api/planning/cards/"+c.ID, ""))
	if c.Owner != pat.ID {
		t.Errorf("the owner is kept: %d", c.Owner)
	}

	c = decodeCard(t, do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, fmt.Sprintf(`{"owner":%d}`, me)))
	if c.Owner != me || len(c.Members) != 2 {
		t.Errorf("another member owns it: owner %d, members %v", c.Owner, c.Members)
	}
	c = decodeCard(t, do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, fmt.Sprintf(`{"members":[%d]}`, pat.ID)))
	if c.Owner != 0 || len(c.Members) != 1 {
		t.Errorf("the owner taken off: owner %d, members %v", c.Owner, c.Members)
	}
	c = decodeCard(t, do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, fmt.Sprintf(`{"members":[%d],"owner":%d}`, pat.ID, pat.ID)))
	if c.Owner != pat.ID {
		t.Errorf("members and owner at once: owner %d", c.Owner)
	}
	c = decodeCard(t, do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, `{"owner":0}`))
	if c.Owner != 0 || len(c.Members) != 1 {
		t.Errorf("no owner, pat still on it: owner %d, members %v", c.Owner, c.Members)
	}
	if w := do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, `{"owner":999999}`); w.Code != http.StatusBadRequest {
		t.Errorf("a stranger as the owner: %d %s", w.Code, w.Body)
	}
}
