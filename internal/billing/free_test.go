package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/store"
)

// unpaid makes the fixture's team two members without a subscription,
// past its grace period.
func (f *fixture) unpaid() {
	f.t.Helper()
	f.join(f.team)
	f.now = f.now.Add(DefaultGrace + time.Hour)
	if !f.b.ReadOnly(f.team) {
		f.t.Fatal("an unpaid team of two is writable after its grace period")
	}
}

// writable checks that the team is free by override and nothing holds it back.
func (f *fixture) writable(what string) State {
	f.t.Helper()
	st := f.state(f.team)
	if !st.Free || st.Required || st.ReadOnly || st.GraceUntil != nil || f.b.ReadOnly(f.team) || f.b.Hold(f.team) != "" {
		f.t.Fatalf("%s: %+v", what, st)
	}
	if n := f.b.InviteNotice(f.team); n != nil {
		f.t.Fatalf("%s: inviting someone asks for a subscription: %v", what, n)
	}
	return st
}

func TestFreeForever(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.unpaid()
	o, err := f.b.SetFree(f.team, true, time.Time{}, " a partner ", "root")
	if err != nil {
		t.Fatal(err)
	}
	if !o.Active || !o.FreeForever || o.FreeUntil != nil || o.Note != "a partner" || o.SetBy != "root" || o.SetAt == nil {
		t.Fatalf("override: %+v", o)
	}
	st := f.writable("free forever")
	if !st.FreeForever || st.FreeUntil != nil {
		t.Fatalf("state doesn't say free forever: %+v", st)
	}
	f.join(f.team)
	f.now = f.now.Add(10 * 365 * 24 * time.Hour)
	f.writable("ten years on")
}

func TestFreeUntilADateKeepsTheOrgWritable(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.join(f.team)
	until := f.now.Add(30 * 24 * time.Hour)
	if _, err := f.b.SetFree(f.team, false, until, "", "root"); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(DefaultGrace + 24*time.Hour) // well past the grace period
	st := f.writable("before the date")
	if st.FreeForever || st.FreeUntil == nil || !st.FreeUntil.Equal(until) {
		t.Fatalf("state doesn't say free until %v: %+v", until, st)
	}
	f.now = until.Add(-time.Second)
	f.writable("a second before the date")
}

func TestExpiredFreeDateStartsTheGracePeriodThen(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.unpaid() // the grace period ran out before the override
	until := f.now.Add(10 * 24 * time.Hour)
	if _, err := f.b.SetFree(f.team, false, until, "", "root"); err != nil {
		t.Fatal(err)
	}
	f.now = until
	st := f.state(f.team)
	if st.Free || !st.Required || st.ReadOnly || st.FreeUntil != nil || st.GraceUntil == nil || !st.GraceUntil.Equal(until.Add(DefaultGrace)) {
		t.Fatalf("at the date: %+v", st)
	}
	if n := f.b.InviteNotice(f.team); n == nil {
		t.Error("after the date, inviting doesn't mention the subscription")
	}
	f.now = until.Add(DefaultGrace - time.Second)
	if f.b.ReadOnly(f.team) {
		t.Fatal("read-only before a full grace period after the date")
	}
	f.now = until.Add(DefaultGrace)
	if !f.b.ReadOnly(f.team) || f.b.Hold(f.team) == "" {
		t.Fatal("writable after the grace period that followed the date")
	}
	if o, err := f.b.Override(f.team); err != nil || o.Active || o.FreeUntil == nil {
		t.Fatalf("the override after its date: %+v %v", o, err)
	}
}

// Nobody looked at the org while it was free: the grace period it had
// before still doesn't count once the date has passed.
func TestExpiredFreeDateIgnoresAnOldGracePeriod(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.unpaid()
	until := f.now.Add(24 * time.Hour)
	if _, err := f.acc.UpdateOrgBilling(f.team, func(b *store.OrgBilling) error {
		b.FreeUntil = until // set behind the service's back: unpaid_since stays
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.now = until.Add(time.Hour)
	if st := f.state(f.team); st.ReadOnly || st.GraceUntil == nil || !st.GraceUntil.Equal(f.now.Add(DefaultGrace)) {
		t.Fatalf("after the date: %+v", st)
	}
}

func TestClearFree(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.unpaid()
	if _, err := f.b.SetFree(f.team, true, time.Time{}, "trial run", "root"); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(5 * DefaultGrace)
	o, err := f.b.ClearFree(f.team, "root")
	if err != nil {
		t.Fatal(err)
	}
	if o.Active || o.FreeForever || o.FreeUntil != nil || o.Note != "" || o.SetBy != "" {
		t.Fatalf("cleared override: %+v", o)
	}
	st := f.state(f.team)
	if st.Free || !st.Required || st.ReadOnly || st.FreeForever || !st.GraceUntil.Equal(f.now.Add(DefaultGrace)) {
		t.Fatalf("after clearing: %+v", st)
	}
	f.now = f.now.Add(DefaultGrace)
	if !f.b.ReadOnly(f.team) {
		t.Fatal("writable after the grace period that followed clearing")
	}
}

func TestSetFreeChecksItsArguments(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	for _, c := range []struct {
		forever bool
		until   time.Time
		note    string
	}{
		{false, time.Time{}, ""},                      // neither
		{true, f.now.Add(time.Hour), ""},              // both
		{false, f.now.Add(-time.Hour), ""},            // in the past
		{false, f.now, ""},                            // now
		{true, time.Time{}, strings.Repeat("x", 501)}, // too long a note
	} {
		if _, err := f.b.SetFree(f.team, c.forever, c.until, c.note, "root"); !errors.Is(err, ErrFreeOverride) {
			t.Errorf("SetFree(%v, %v, %d bytes): %v", c.forever, c.until, len(c.note), err)
		}
	}
	if _, err := f.b.SetFree(999999, true, time.Time{}, "", "root"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetFree of no org: %v", err)
	}
}

// A subscription the org has stays as it is, and its seats keep following
// the members.
func TestFreeLeavesTheSubscriptionAlone(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.join(f.team)
	f.fake.subs["sub_1"] = Subscription{ID: "sub_1", Status: "active", Quantity: 2}
	if _, err := f.acc.UpdateOrgBilling(f.team, func(b *store.OrgBilling) error {
		b.CustomerID, b.SubscriptionID, b.ItemID, b.Status, b.Quantity = "cus_1", "sub_1", "si_1", "active", 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.SetFree(f.team, true, time.Time{}, "", "root"); err != nil {
		t.Fatal(err)
	}
	f.join(f.team)
	b, err := f.acc.OrgBilling(f.team)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "active" || b.SubscriptionID != "sub_1" || b.Quantity != 3 || len(f.fake.updates) != 1 || f.fake.updates[0] != "sub_1=3" {
		t.Fatalf("subscription after the override: %+v, updates %v", b, f.fake.updates)
	}
}

// Only platform admins see and change an org's override; the org's own
// owners can't.
func TestFreeOverrideAPI(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.unpaid()
	mux := http.NewServeMux()
	f.b.Routes(mux)
	admin := store.Session{User: store.User{ID: f.owner, Username: "root", PlatformAdmin: true}}
	owner := store.Session{User: store.User{ID: f.owner, Username: "root"},
		Org: &store.Membership{Org: store.Org{ID: f.team}, Role: store.RoleOwner}}
	call := func(sess store.Session, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(auth.WithSession(r.Context(), sess))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	path := fmt.Sprintf("/api/admin/orgs/%d/billing", f.team)

	for _, m := range []string{"GET", "PUT", "DELETE"} {
		if w := call(owner, m, path, `{"free_forever":true}`); w.Code != http.StatusForbidden {
			t.Errorf("an owner who isn't a platform admin: %s %d %s", m, w.Code, w.Body)
		}
	}
	if !f.b.ReadOnly(f.team) {
		t.Fatal("a non-admin's PUT made the org free")
	}

	past := f.now.Add(-time.Hour).Format(time.RFC3339)
	if w := call(admin, "PUT", path, `{"free_until":"`+past+`"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a date in the past: %d %s", w.Code, w.Body)
	}
	if w := call(admin, "PUT", "/api/admin/orgs/999999/billing", `{"free_forever":true}`); w.Code != http.StatusNotFound {
		t.Errorf("no such org: %d %s", w.Code, w.Body)
	}

	until := f.now.Add(7 * 24 * time.Hour).UTC().Truncate(time.Second)
	w := call(admin, "PUT", path, `{"free_until":"`+until.Format(time.RFC3339)+`","note":"pilot"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	var v struct {
		Active    bool       `json:"active"`
		FreeUntil *time.Time `json:"free_until"`
		Note      string     `json:"note"`
		SetBy     string     `json:"set_by"`
		State     State      `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if !v.Active || v.FreeUntil == nil || !v.FreeUntil.Equal(until) || v.Note != "pilot" || v.SetBy != "root" ||
		!v.State.Free || v.State.ReadOnly || v.State.FreeUntil == nil {
		t.Fatalf("set: %s", w.Body)
	}
	if f.b.ReadOnly(f.team) {
		t.Fatal("still read-only once free")
	}
	// Members see it in the org's billing state, without the note.
	if w := call(owner, "GET", fmt.Sprintf("/api/orgs/%d/billing", f.team), ""); !strings.Contains(w.Body.String(), `"free_until":"`) ||
		strings.Contains(w.Body.String(), "pilot") {
		t.Errorf("member's state: %d %s", w.Code, w.Body)
	}
	if w := call(admin, "GET", path, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"note":"pilot"`) {
		t.Errorf("get: %d %s", w.Code, w.Body)
	}
	if w := call(admin, "DELETE", path, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"active":false`) {
		t.Errorf("clear: %d %s", w.Code, w.Body)
	}
	if st := f.state(f.team); st.Free || st.FreeUntil != nil {
		t.Errorf("after clearing: %+v", st)
	}
}
