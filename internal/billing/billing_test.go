package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/store/storetest"
)

// fakeStripe keeps customers and subscriptions in memory, as Stripe would.
type fakeStripe struct {
	mu        sync.Mutex
	customers []CustomerParams
	checkouts []CheckoutParams
	portals   []string
	subs      map[string]Subscription
	updates   []string // "sub=quantity"
	fail      error
}

func newFake() *fakeStripe { return &fakeStripe{subs: map[string]Subscription{}} }

func (f *fakeStripe) CreateCustomer(_ context.Context, p CustomerParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return "", f.fail
	}
	f.customers = append(f.customers, p)
	return fmt.Sprintf("cus_%d", len(f.customers)), nil
}

func (f *fakeStripe) CreateCheckoutSession(_ context.Context, p CheckoutParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkouts = append(f.checkouts, p)
	return fmt.Sprintf("https://checkout.test/c/%d", len(f.checkouts)), nil
}

func (f *fakeStripe) CreatePortalSession(_ context.Context, customer, returnURL string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.portals = append(f.portals, customer)
	return "https://portal.test/" + customer, nil
}

func (f *fakeStripe) GetSubscription(_ context.Context, id string) (Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.subs[id]
	if !ok {
		return s, errors.New("no such subscription")
	}
	return s, nil
}

func (f *fakeStripe) UpdateQuantity(_ context.Context, sub, item string, q int) (Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return Subscription{}, f.fail
	}
	s, ok := f.subs[sub]
	if !ok {
		return s, errors.New("no such subscription")
	}
	s.Quantity = q
	f.subs[sub] = s
	f.updates = append(f.updates, fmt.Sprintf("%s=%d", sub, q))
	return s, nil
}

const secret = "whsec_test"

type fixture struct {
	t      *testing.T
	acc    *store.Accounts
	owner  int64 // the env user, owner of every org here
	team   int64 // an org with two members
	fake   *fakeStripe
	b      *Service
	now    time.Time
	member int // a counter for new members' names
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	acc, owner := storetest.Accounts(t)
	if _, err := acc.CreateOrg(owner.User, "Team"); err != nil {
		t.Fatal(err)
	}
	ms, err := acc.Memberships(owner.User)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, acc: acc, owner: owner.User, fake: newFake(), now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	for _, m := range ms {
		if m.Name == "Team" {
			f.team = m.ID
		}
	}
	f.b = New(acc, f.fake, Config{SecretKey: "sk_test_x", WebhookSecret: secret, PriceID: "price_seat",
		TrialDays: 14, PublicURL: "https://dash.test/"})
	f.b.SetClock(func() time.Time { return f.now })
	return f
}

// join adds a new member to org, the way an accepted invitation does.
func (f *fixture) join(org int64) int64 {
	f.t.Helper()
	f.member++
	name := fmt.Sprintf("m%d", f.member)
	tok, _, err := f.acc.Invite(f.owner, org, name+"@example.com", store.RoleMember)
	if err != nil {
		f.t.Fatal(err)
	}
	u, _, err := f.acc.RegisterWithInvitation(tok, name, "a-long-password")
	if err != nil {
		f.t.Fatal(err)
	}
	f.b.MembersChanged(org)
	return u.ID
}

func (f *fixture) state(org int64) State {
	f.t.Helper()
	st, err := f.b.State(org)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

// webhook delivers an event to the webhook endpoint, signed, and returns
// the response.
func (f *fixture) webhook(id, typ string, created time.Time, object any) *httptest.ResponseRecorder {
	f.t.Helper()
	obj, _ := json.Marshal(object)
	body := fmt.Sprintf(`{"id":%q,"type":%q,"created":%d,"data":{"object":%s}}`, id, typ, created.Unix(), obj)
	r := httptest.NewRequest("POST", "/api/billing/webhook", strings.NewReader(body))
	r.Header.Set("Stripe-Signature", SignatureHeader([]byte(body), secret, f.now))
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	f.b.Routes(mux)
	mux.ServeHTTP(w, r)
	return w
}

// subObject is a subscription as Stripe's events carry it.
func subObject(id, customer, status string, org int64, quantity int) map[string]any {
	return map[string]any{
		"id": id, "object": "subscription", "customer": customer, "status": status,
		"cancel_at_period_end": false, "trial_end": nil,
		"metadata": map[string]any{"org_id": fmt.Sprint(org)},
		"items": map[string]any{"data": []any{map[string]any{
			"id": "si_" + id, "quantity": quantity, "current_period_end": 1775000000, "price": map[string]any{"id": "price_seat"},
		}}},
	}
}

func TestBillingIsOffByDefault(t *testing.T) {
	if (Config{}).Configured() {
		t.Fatal("an empty config turns billing on")
	}
	var off *Service
	if off.ReadOnly(1) || off.Hold(1) != "" || off.InviteNotice(1) != nil {
		t.Fatal("billing off still holds something back")
	}
	off.MembersChanged(1) // no panic
	if st, err := off.State(1); err != nil || st.Enabled {
		t.Fatalf("state with billing off: %+v %v", st, err)
	}
	mux := http.NewServeMux()
	off.Routes(mux)
	_, owner := storetest.Accounts(t)
	sess := store.Session{User: store.User{ID: owner.User}, Org: &store.Membership{Org: store.Org{ID: owner.Org}, Role: store.RoleOwner}}
	for _, c := range []struct {
		method, path string
		code         int
	}{
		{"GET", fmt.Sprintf("/api/orgs/%d/billing", owner.Org), http.StatusOK},
		{"POST", fmt.Sprintf("/api/orgs/%d/billing/checkout", owner.Org), http.StatusNotFound},
		{"POST", fmt.Sprintf("/api/orgs/%d/billing/portal", owner.Org), http.StatusNotFound},
		{"POST", "/api/billing/webhook", http.StatusNotFound},
	} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader("{}"))
		r = r.WithContext(auth.WithSession(r.Context(), sess))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != c.code {
			t.Errorf("%s %s: %d %s, want %d", c.method, c.path, w.Code, w.Body, c.code)
		}
		if c.code == http.StatusOK && strings.TrimSpace(w.Body.String()) != `{"enabled":false}` {
			t.Errorf("state with billing off: %s", w.Body)
		}
	}
}

func TestConfigCheck(t *testing.T) {
	ok := Config{SecretKey: "sk_test_1", WebhookSecret: "whsec_1", PriceID: "price_1", TrialDays: 14, PublicURL: "https://dash.test"}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Config{
		{SecretKey: "sk_test_1"},
		{SecretKey: "pk_test_1", WebhookSecret: "whsec_1", PriceID: "price_1", PublicURL: "https://dash.test"},
		{SecretKey: "sk_test_1", WebhookSecret: "whsec_1", PriceID: "prod_1", PublicURL: "https://dash.test"},
		{SecretKey: "sk_test_1", WebhookSecret: "whsec_1", PriceID: "price_1"},
		{SecretKey: "sk_test_1", WebhookSecret: "whsec_1", PriceID: "price_1", PublicURL: "https://dash.test", TrialDays: -1},
	} {
		if bad.Check() == nil {
			t.Errorf("%+v passes the check", bad)
		}
	}
}

func TestPersonalWorkspaceAndSoloOrgsAreFree(t *testing.T) {
	f := newFixture(t)
	personal, err := f.acc.EnsurePersonalOrg(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(365 * 24 * time.Hour)
	for _, org := range []int64{personal.ID, f.team} { // Team has only its owner yet
		st := f.state(org)
		if !st.Free || st.Required || st.ReadOnly || f.b.ReadOnly(org) || f.b.Hold(org) != "" {
			t.Errorf("org %d with one member: %+v", org, st)
		}
	}
	if n := f.b.InviteNotice(personal.ID); n != nil {
		t.Errorf("personal workspace gets an invite notice: %v", n)
	}
}

func TestInstanceDefaultOrgIsNeverBilled(t *testing.T) {
	f := newFixture(t)
	def, err := f.acc.DefaultOrg()
	if err != nil {
		t.Fatal(err)
	}
	f.join(def.ID)
	f.join(def.ID)
	f.now = f.now.Add(90 * 24 * time.Hour)
	if st := f.state(def.ID); !st.Free || st.ReadOnly {
		t.Fatalf("default org: %+v", st)
	}
}

func TestUnpaidOrgBecomesReadOnlyAfterGrace(t *testing.T) {
	f := newFixture(t)
	if n := f.b.InviteNotice(f.team); n == nil {
		t.Error("inviting a second member into an unpaid org says nothing about a subscription")
	}
	f.join(f.team)
	st := f.state(f.team)
	if st.Free || !st.Required || st.ReadOnly || st.GraceUntil == nil || !st.GraceUntil.Equal(f.now.Add(DefaultGrace)) {
		t.Fatalf("just became two: %+v", st)
	}
	if !st.CanSubscribe || st.TrialDays != 14 {
		t.Errorf("checkout isn't offered with the trial: %+v", st)
	}
	f.now = f.now.Add(DefaultGrace - time.Minute)
	if f.b.ReadOnly(f.team) {
		t.Fatal("read-only before the grace period ended")
	}
	f.now = f.now.Add(time.Minute)
	if !f.b.ReadOnly(f.team) || !strings.Contains(f.b.Hold(f.team), "subscription") {
		t.Fatal("still writable after the grace period")
	}
	// Leaving makes the org free again at once.
	ms, _ := f.acc.Members(f.team)
	for _, m := range ms {
		if m.ID != f.owner {
			if err := f.acc.RemoveMember(f.owner, f.team, m.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.b.MembersChanged(f.team)
	if st := f.state(f.team); st.ReadOnly || st.Required || !st.Free {
		t.Fatalf("back to one member: %+v", st)
	}
	// Coming back to two starts a new grace period, from now.
	f.join(f.team)
	if st := f.state(f.team); st.ReadOnly || !st.GraceUntil.Equal(f.now.Add(DefaultGrace)) {
		t.Fatalf("two again: %+v", st)
	}
}

func TestCheckoutAndPortal(t *testing.T) {
	f := newFixture(t)
	if _, err := f.b.Portal(context.Background(), f.team); !errors.Is(err, ErrNoCustomer) {
		t.Fatalf("portal without a customer: %v", err)
	}
	f.join(f.team)
	url, err := f.b.Checkout(context.Background(), f.team)
	if err != nil || url == "" {
		t.Fatal(url, err)
	}
	if _, err := f.b.Checkout(context.Background(), f.team); err != nil {
		t.Fatal(err)
	}
	if len(f.fake.customers) != 1 || f.fake.customers[0].Name != "Team" || f.fake.customers[0].OrgID != f.team {
		t.Fatalf("customers: %+v (one per org)", f.fake.customers)
	}
	c := f.fake.checkouts[0]
	if c.Customer != "cus_1" || c.Price != "price_seat" || c.Quantity != 2 || c.TrialDays != 14 || c.OrgID != f.team {
		t.Errorf("checkout: %+v", c)
	}
	if !strings.HasPrefix(c.SuccessURL, "https://dash.test/") || !strings.HasPrefix(c.CancelURL, "https://dash.test/") {
		t.Errorf("checkout returns elsewhere than the public URL: %+v", c)
	}
	if url, err := f.b.Portal(context.Background(), f.team); err != nil || url != "https://portal.test/cus_1" {
		t.Errorf("portal: %q %v", url, err)
	}

	// Subscribed: no second checkout, and no second trial after it ends.
	f.fake.subs["sub_1"] = Subscription{ID: "sub_1", Customer: "cus_1", Status: "trialing", ItemID: "si_1", Quantity: 2, OrgID: f.team}
	if w := f.webhook("evt_1", "checkout.session.completed", f.now, map[string]any{
		"mode": "subscription", "client_reference_id": fmt.Sprint(f.team), "customer": "cus_1", "subscription": "sub_1",
	}); w.Code != http.StatusOK {
		t.Fatalf("webhook: %d %s", w.Code, w.Body)
	}
	if _, err := f.b.Checkout(context.Background(), f.team); !errors.Is(err, ErrSubscribed) {
		t.Fatalf("second checkout: %v", err)
	}
	f.now = f.now.Add(time.Hour)
	f.webhook("evt_2", "customer.subscription.deleted", f.now, subObject("sub_1", "cus_1", "canceled", f.team, 2))
	if _, err := f.b.Checkout(context.Background(), f.team); err != nil {
		t.Fatal(err)
	}
	if c := f.fake.checkouts[len(f.fake.checkouts)-1]; c.TrialDays != 0 || c.Customer != "cus_1" {
		t.Errorf("checkout after a trial: %+v", c)
	}
}

func TestWebhookKeepsTheSubscription(t *testing.T) {
	f := newFixture(t)
	f.join(f.team)
	if _, err := f.b.Checkout(context.Background(), f.team); err != nil {
		t.Fatal(err)
	}
	kicked := 0
	f.b.OnChange = func(int64) { kicked++ }

	t0 := f.now
	if w := f.webhook("evt_a", "customer.subscription.created", t0, subObject("sub_9", "cus_1", "active", f.team, 2)); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	b, _ := f.acc.OrgBilling(f.team)
	if b.SubscriptionID != "sub_9" || b.CustomerID != "cus_1" || b.Status != "active" || b.ItemID != "si_sub_9" ||
		b.Quantity != 2 || b.PeriodEnd.Unix() != 1775000000 || !b.TrialUsed {
		t.Fatalf("billing row: %+v", b)
	}
	if st := f.state(f.team); st.Required || st.ReadOnly || st.Status != "active" {
		t.Fatalf("paid org: %+v", st)
	}
	if kicked != 1 {
		t.Errorf("OnChange called %d times", kicked)
	}

	// The same event again changes nothing.
	w := f.webhook("evt_a", "customer.subscription.created", t0, subObject("sub_9", "cus_1", "past_due", f.team, 2))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "duplicate") {
		t.Fatalf("duplicate: %d %s", w.Code, w.Body)
	}
	if b, _ := f.acc.OrgBilling(f.team); b.Status != "active" {
		t.Fatalf("a repeated event was applied again: %+v", b)
	}

	// A failed payment: past_due starts the grace period.
	f.webhook("evt_b", "customer.subscription.updated", t0.Add(2*time.Hour), subObject("sub_9", "cus_1", "past_due", f.team, 2))
	st := f.state(f.team)
	if !st.Required || st.ReadOnly || st.GraceUntil == nil {
		t.Fatalf("past due: %+v", st)
	}
	// An older event arriving late doesn't undo it.
	f.webhook("evt_c", "customer.subscription.updated", t0.Add(time.Hour), subObject("sub_9", "cus_1", "active", f.team, 2))
	if b, _ := f.acc.OrgBilling(f.team); b.Status != "past_due" {
		t.Fatalf("an out-of-order event was applied: %+v", b)
	}
	f.now = f.now.Add(DefaultGrace)
	if !f.b.ReadOnly(f.team) {
		t.Fatal("past due beyond the grace period is still writable")
	}
	// Paying lifts it.
	f.webhook("evt_d", "customer.subscription.updated", t0.Add(3*time.Hour), subObject("sub_9", "cus_1", "active", f.team, 2))
	if f.b.ReadOnly(f.team) {
		t.Fatal("still read-only after paying")
	}
	if st := f.state(f.team); st.GraceUntil != nil {
		t.Fatalf("grace period not reset: %+v", st)
	}
	// Someone else's customer, or a subscription without an org, is ignored.
	f.webhook("evt_e", "customer.subscription.updated", t0.Add(4*time.Hour), subObject("sub_x", "cus_other", "canceled", f.team, 1))
	f.webhook("evt_f", "customer.subscription.updated", t0.Add(4*time.Hour), subObject("sub_y", "cus_nobody", "canceled", 0, 1))
	if b, _ := f.acc.OrgBilling(f.team); b.Status != "active" || b.SubscriptionID != "sub_9" {
		t.Fatalf("foreign events changed the org: %+v", b)
	}
	// Events Dash doesn't use are acknowledged.
	if w := f.webhook("evt_g", "invoice.paid", f.now, map[string]any{"id": "in_1"}); w.Code != http.StatusOK {
		t.Fatalf("invoice.paid: %d", w.Code)
	}
}

func TestWebhookFailureLetsStripeRetry(t *testing.T) {
	f := newFixture(t)
	f.join(f.team)
	if _, err := f.b.Checkout(context.Background(), f.team); err != nil {
		t.Fatal(err)
	}
	checkout := map[string]any{
		"mode": "subscription", "client_reference_id": fmt.Sprint(f.team), "customer": "cus_1", "subscription": "sub_1",
	}

	// Stripe can't return the subscription yet: the event fails, and isn't
	// recorded as handled.
	w := f.webhook("evt_x", "checkout.session.completed", f.now, checkout)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("failed event: %d %s, want 500", w.Code, w.Body)
	}
	if b, _ := f.acc.OrgBilling(f.team); b.SubscriptionID != "" {
		t.Fatalf("a failed event recorded a subscription: %+v", b)
	}

	// Stripe's retry of the same event is applied, not taken for a duplicate.
	f.fake.subs["sub_1"] = Subscription{ID: "sub_1", Customer: "cus_1", Status: "active", ItemID: "si_1", Quantity: 2, OrgID: f.team}
	w = f.webhook("evt_x", "checkout.session.completed", f.now, checkout)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "duplicate") {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}
	if b, _ := f.acc.OrgBilling(f.team); b.SubscriptionID != "sub_1" || b.Status != "active" {
		t.Fatalf("the retry wasn't applied: %+v", b)
	}
}

func TestOldSubscriptionEndingKeepsTheNewOne(t *testing.T) {
	f := newFixture(t)
	f.join(f.team)
	if _, err := f.b.Checkout(context.Background(), f.team); err != nil {
		t.Fatal(err)
	}
	t0 := f.now
	f.webhook("evt_1", "customer.subscription.created", t0, subObject("sub_1", "cus_1", "active", f.team, 2))
	f.fake.subs["sub_2"] = Subscription{ID: "sub_2", Customer: "cus_1", Status: "active", ItemID: "si_sub_2", Quantity: 2, OrgID: f.team}
	f.webhook("evt_2", "customer.subscription.created", t0.Add(time.Hour), subObject("sub_2", "cus_1", "active", f.team, 2))
	// The old subscription's cancellation arrives after the new one is active.
	if w := f.webhook("evt_3", "customer.subscription.deleted", t0.Add(2*time.Hour), subObject("sub_1", "cus_1", "canceled", f.team, 2)); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if b, _ := f.acc.OrgBilling(f.team); b.SubscriptionID != "sub_2" || b.Status != "active" {
		t.Fatalf("the old subscription's end replaced the new one: %+v", b)
	}
	f.now = f.now.Add(DefaultGrace + time.Hour)
	if f.b.ReadOnly(f.team) {
		t.Fatal("an org paying for its new subscription became read-only")
	}
}

func TestWebhookChecksTheSignature(t *testing.T) {
	f := newFixture(t)
	body := `{"id":"evt_1","type":"customer.subscription.created","created":1,"data":{"object":{}}}`
	mux := http.NewServeMux()
	f.b.Routes(mux)
	for name, header := range map[string]string{
		"none":         "",
		"wrong secret": SignatureHeader([]byte(body), "whsec_other", f.now),
		"too old":      SignatureHeader([]byte(body), secret, f.now.Add(-time.Hour)),
		"other body":   SignatureHeader([]byte(body+" "), secret, f.now),
		"garbage":      "t=abc,v1=zz",
	} {
		r := httptest.NewRequest("POST", "/api/billing/webhook", strings.NewReader(body))
		if header != "" {
			r.Header.Set("Stripe-Signature", header)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	// Several v1 signatures (a secret being rolled): one matching is enough.
	h := SignatureHeader([]byte(body), secret, f.now)
	h = strings.Replace(h, "v1=", "v1=00ff,v1=", 1)
	if err := VerifySignature([]byte(body), h, secret, f.now, SignatureTolerance); err != nil {
		t.Fatal(err)
	}
}

func TestSeatsFollowMembers(t *testing.T) {
	f := newFixture(t)
	f.join(f.team)
	if len(f.fake.updates) != 0 {
		t.Fatalf("seats updated without a subscription: %v", f.fake.updates)
	}
	f.fake.subs["sub_1"] = Subscription{ID: "sub_1", Customer: "cus_9", Status: "active", ItemID: "si_1", Quantity: 2, OrgID: f.team}
	f.webhook("evt_1", "customer.subscription.created", f.now, subObject("sub_1", "cus_9", "active", f.team, 2))

	third := f.join(f.team)
	f.join(f.team)
	if err := f.acc.RemoveMember(f.owner, f.team, third); err != nil {
		t.Fatal(err)
	}
	f.b.MembersChanged(f.team)
	want := []string{"sub_1=3", "sub_1=4", "sub_1=3"}
	if fmt.Sprint(f.fake.updates) != fmt.Sprint(want) {
		t.Fatalf("seat updates %v, want %v", f.fake.updates, want)
	}
	if b, _ := f.acc.OrgBilling(f.team); b.Quantity != 3 {
		t.Fatalf("quantity on record: %d", b.Quantity)
	}
	// Nothing changed: no call.
	f.b.MembersChanged(f.team)
	if len(f.fake.updates) != 3 {
		t.Fatalf("seat update without a change: %v", f.fake.updates)
	}
	// A subscription created with fewer seats than members (someone joined
	// during Checkout) is corrected when its event arrives.
	f.fake.subs["sub_2"] = Subscription{ID: "sub_2", Status: "active"}
	f.webhook("evt_2", "customer.subscription.deleted", f.now.Add(time.Second), subObject("sub_1", "cus_9", "canceled", f.team, 3))
	f.fake.subs["sub_2"] = Subscription{ID: "sub_2", Customer: "cus_9", Status: "active", ItemID: "si_sub_2", Quantity: 1, OrgID: f.team}
	f.webhook("evt_3", "customer.subscription.created", f.now.Add(2*time.Second), subObject("sub_2", "cus_9", "active", f.team, 1))
	if last := f.fake.updates[len(f.fake.updates)-1]; last != "sub_2=3" {
		t.Fatalf("seats not corrected after checkout: %v", f.fake.updates)
	}
	// Stripe failing doesn't fail the membership change.
	f.fake.fail = errors.New("stripe is down")
	f.join(f.team)
}
