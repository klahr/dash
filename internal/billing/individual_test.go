package billing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/store"
)

// newIndividualFixture is newFixture with an individual price.
func newIndividualFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.b = New(f.acc, f.fake, Config{SecretKey: "sk_test_x", WebhookSecret: secret, PriceID: "price_seat",
		IndividualPriceID: "price_solo", IndividualPrice: "€5 / month", TrialDays: 14, PublicURL: "https://dash.test/"})
	f.b.SetClock(func() time.Time { return f.now })
	return f
}

// soloSub is an individual subscription as Stripe's events carry it.
func soloSub(id, customer, status string, org int64) map[string]any {
	o := subObject(id, customer, status, org, 1)
	o["items"].(map[string]any)["data"].([]any)[0].(map[string]any)["price"] = map[string]any{"id": "price_solo"}
	return o
}

// neverHeldBack checks that nothing holds org back, whatever it pays.
func (f *fixture) neverHeldBack(org int64, what string) State {
	f.t.Helper()
	st := f.state(org)
	if !st.Free || st.Required || st.ReadOnly || st.GraceUntil != nil || f.b.ReadOnly(org) || f.b.Hold(org) != "" {
		f.t.Fatalf("%s: held back: %+v", what, st)
	}
	return st
}

func TestConfigCheckIndividualPrice(t *testing.T) {
	t.Parallel()

	ok := Config{SecretKey: "sk_test_1", WebhookSecret: "whsec_1", PriceID: "price_1", PublicURL: "https://dash.test"}
	ok.IndividualPriceID = "price_2"
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"prod_2", "price_1"} {
		c := ok
		c.IndividualPriceID = bad
		if c.Check() == nil {
			t.Errorf("individual price %q passes the check", bad)
		}
	}
	if !(Config{IndividualPriceID: "price_2"}).Configured() {
		t.Error("an individual price alone doesn't count as configuring billing (so its check never says what's missing)")
	}
}

// Without an individual price nothing changes: individuals are free and
// never asked.
func TestIndividualsFreeWithoutIndividualPrice(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	personal, err := f.acc.EnsurePersonalOrg(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, org := range []int64{personal.ID, f.team} {
		st := f.neverHeldBack(org, "no individual price")
		if st.Individual || st.Ask || st.IndividualPrice != "" {
			t.Errorf("org %d is asked to pay without an individual price: %+v", org, st)
		}
		if _, err := f.b.ChooseFree(org); !errors.Is(err, ErrNotIndividual) {
			t.Errorf("choosing free without an individual price: %v", err)
		}
	}
	if _, err := f.b.Checkout(context.Background(), f.team); err != nil {
		t.Fatal(err)
	}
	if c := f.fake.checkouts[0]; c.Price != "price_seat" || c.Quantity != 1 || c.TrialDays != 14 {
		t.Errorf("checkout of a solo org without an individual price: %+v", c)
	}
}

// An individual who hasn't answered is asked, and nothing is held back.
func TestIndividualIsAskedNotBlocked(t *testing.T) {
	t.Parallel()

	f := newIndividualFixture(t)
	personal, err := f.acc.EnsurePersonalOrg(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(365 * 24 * time.Hour)
	for _, org := range []int64{personal.ID, f.team} { // Team has only its owner
		st := f.neverHeldBack(org, "an individual who hasn't answered")
		if !st.Individual || !st.Ask || st.ChoseFree || st.IndividualPrice != "€5 / month" || !st.CanSubscribe || st.Plan != "" {
			t.Errorf("org %d isn't asked to pay: %+v", org, st)
		}
		if n := f.b.InviteNotice(org); org == personal.ID && n != nil {
			t.Errorf("personal workspace gets an invite notice: %v", n)
		}
	}
	// The instance's own org, and one an admin made free, aren't asked.
	def, err := f.acc.DefaultOrg()
	if err != nil {
		t.Fatal(err)
	}
	if st := f.state(def.ID); st.Individual || st.Ask {
		t.Errorf("the instance's own org is asked to pay: %+v", st)
	}
	if _, err := f.b.SetFree(f.team, true, time.Time{}, "", "root"); err != nil {
		t.Fatal(err)
	}
	if st := f.state(f.team); st.Individual || st.Ask {
		t.Errorf("an org made free is asked to pay: %+v", st)
	}
}

// "I can't pay right now" stops the asking and keeps them free; they can
// still subscribe.
func TestCantPayStopsTheAsk(t *testing.T) {
	t.Parallel()

	f := newIndividualFixture(t)
	mux := http.NewServeMux()
	f.b.Routes(mux)
	call := func(role store.Role) *httptest.ResponseRecorder {
		t.Helper()
		sess := store.Session{User: store.User{ID: f.owner}, Org: &store.Membership{Org: store.Org{ID: f.team}, Role: role}}
		r := httptest.NewRequest("POST", fmt.Sprintf("/api/orgs/%d/billing/free", f.team), nil)
		r = r.WithContext(auth.WithSession(r.Context(), sess))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := call(store.RoleOwner)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"chose_free":true`) || strings.Contains(w.Body.String(), `"ask":true`) {
		t.Fatalf("can't pay: %d %s", w.Code, w.Body)
	}
	f.now = f.now.Add(365 * 24 * time.Hour)
	st := f.neverHeldBack(f.team, "chose free")
	if st.Ask || !st.ChoseFree || !st.Individual || !st.CanSubscribe {
		t.Fatalf("after choosing free: %+v", st)
	}
	// Again changes nothing.
	b0, _ := f.acc.OrgBilling(f.team)
	if w := call(store.RoleOwner); w.Code != http.StatusOK {
		t.Fatalf("again: %d %s", w.Code, w.Body)
	}
	if b, _ := f.acc.OrgBilling(f.team); !b.ChoseFreeAt.Equal(b0.ChoseFreeAt) {
		t.Errorf("choosing again moved the date: %v, was %v", b.ChoseFreeAt, b0.ChoseFreeAt)
	}
	// They change their mind: Checkout at the individual price.
	if _, err := f.b.Checkout(context.Background(), f.team); err != nil {
		t.Fatal(err)
	}
	if c := f.fake.checkouts[0]; c.Price != "price_solo" || c.Quantity != 1 || c.TrialDays != 0 {
		t.Errorf("individual checkout: %+v", c)
	}
	// A team can't choose free.
	f.join(f.team)
	if w := call(store.RoleOwner); w.Code != http.StatusConflict {
		t.Errorf("a team choosing free: %d %s", w.Code, w.Body)
	}
}

// A subscribed individual pays for one, isn't seat-synced, and after
// cancelling is free again without being asked afresh.
func TestSubscribedIndividual(t *testing.T) {
	t.Parallel()

	f := newIndividualFixture(t)
	personal, err := f.acc.EnsurePersonalOrg(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.Checkout(context.Background(), personal.ID); err != nil {
		t.Fatal(err)
	}
	if c := f.fake.checkouts[0]; c.Price != "price_solo" || c.Quantity != 1 || c.TrialDays != 0 {
		t.Errorf("individual checkout: %+v", c)
	}
	if w := f.webhook("evt_1", "customer.subscription.created", f.now, soloSub("sub_s", "cus_1", "active", personal.ID)); w.Code != http.StatusOK {
		t.Fatalf("webhook: %d %s", w.Code, w.Body)
	}
	st := f.neverHeldBack(personal.ID, "subscribed")
	if st.Plan != "individual" || st.Ask || st.CanSubscribe || !st.HasAccount || st.Status != "active" {
		t.Fatalf("subscribed individual: %+v", st)
	}
	b, _ := f.acc.OrgBilling(personal.ID)
	if b.PriceID != "price_solo" || b.TrialUsed {
		t.Errorf("billing row: %+v (an individual subscription has no trial to use up)", b)
	}
	f.b.MembersChanged(personal.ID)
	if err := f.b.SyncSeats(context.Background(), personal.ID); err != nil || len(f.fake.updates) != 0 {
		t.Errorf("an individual subscription was seat-synced: %v %v", f.fake.updates, err)
	}
	if url, err := f.b.Portal(context.Background(), personal.ID); err != nil || url == "" {
		t.Errorf("portal: %q %v", url, err)
	}
	// Cancelled in the portal: free, not asked again, can subscribe again.
	f.now = f.now.Add(time.Hour)
	f.webhook("evt_2", "customer.subscription.deleted", f.now, soloSub("sub_s", "cus_1", "canceled", personal.ID))
	st = f.neverHeldBack(personal.ID, "cancelled")
	if st.Ask || st.Plan != "" || !st.CanSubscribe || !st.Individual {
		t.Fatalf("after cancelling: %+v", st)
	}
}

// Growing to two members: an individual subscription moves to the per-seat
// price; an individual who chose free gets the usual team billing.
func TestIndividualGrowsIntoATeam(t *testing.T) {
	t.Parallel()

	t.Run("subscribed", func(t *testing.T) {
		t.Parallel()
		f := newIndividualFixture(t)
		f.fake.subs["sub_s"] = Subscription{ID: "sub_s", Customer: "cus_1", Status: "active", ItemID: "si_sub_s", Price: "price_solo", Quantity: 1, OrgID: f.team}
		f.webhook("evt_1", "customer.subscription.created", f.now, soloSub("sub_s", "cus_1", "active", f.team))
		f.join(f.team)
		if !slices.Equal(f.fake.updates, []string{"sub_s:price_seat", "sub_s=2"}) {
			t.Fatalf("stripe updates: %v", f.fake.updates)
		}
		b, _ := f.acc.OrgBilling(f.team)
		if b.PriceID != "price_seat" || b.Quantity != 2 {
			t.Fatalf("billing row: %+v", b)
		}
		st := f.state(f.team)
		if st.Individual || st.Ask || st.Required || st.Plan != "team" || st.Seats != 2 {
			t.Fatalf("a team of two on the moved subscription: %+v", st)
		}
		// From now on seats follow the members.
		f.join(f.team)
		if f.fake.updates[len(f.fake.updates)-1] != "sub_s=3" {
			t.Errorf("seats don't follow the members: %v", f.fake.updates)
		}
	})
	t.Run("chose free", func(t *testing.T) {
		t.Parallel()
		f := newIndividualFixture(t)
		if _, err := f.b.ChooseFree(f.team); err != nil {
			t.Fatal(err)
		}
		if n := f.b.InviteNotice(f.team); n == nil {
			t.Error("inviting a second member says nothing about a subscription")
		}
		f.join(f.team)
		st := f.state(f.team)
		if st.Individual || st.Ask || st.Free || !st.Required || st.GraceUntil == nil || !st.GraceUntil.Equal(f.now.Add(DefaultGrace)) {
			t.Fatalf("a team of two that chose free as an individual: %+v", st)
		}
		if _, err := f.b.Checkout(context.Background(), f.team); err != nil {
			t.Fatal(err)
		}
		if c := f.fake.checkouts[0]; c.Price != "price_seat" || c.Quantity != 2 || c.TrialDays != 14 {
			t.Errorf("team checkout: %+v", c)
		}
	})
}
