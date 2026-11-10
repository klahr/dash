package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/billing"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// stripeStub answers Checkout and the portal; billing's own tests cover the rest.
type stripeStub struct{}

func (stripeStub) CreateCustomer(context.Context, billing.CustomerParams) (string, error) {
	return "cus_1", nil
}
func (stripeStub) CreateCheckoutSession(context.Context, billing.CheckoutParams) (string, error) {
	return "https://checkout.test/1", nil
}
func (stripeStub) CreatePortalSession(context.Context, string, string) (string, error) {
	return "https://portal.test/1", nil
}
func (stripeStub) GetSubscription(context.Context, string) (billing.Subscription, error) {
	return billing.Subscription{}, nil
}
func (stripeStub) UpdateQuantity(context.Context, string, string, int) (billing.Subscription, error) {
	return billing.Subscription{}, nil
}

// An org of two without a subscription, past its grace period: its board
// can be read but not changed, by the API, the MCP tools or the chat; its
// owner can still subscribe, and removing the other member frees it.
func TestUnpaidOrgIsReadOnly(t *testing.T) {
	parallel(t)

	s, _, _ := boardServer(t)
	st := raw(s)
	acc := st.Accounts()
	owner := ownerSession(t, s)
	team, err := acc.CreateOrg(owner.User.ID, "Team")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := acc.Invite(owner.User.ID, team.ID, "m@example.com", store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := acc.RegisterWithInvitation(tok, "m", "m-password")
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProject(model.Project{Name: "teamweb", OrgID: team.ID})
	if err != nil {
		t.Fatal(err)
	}
	td, err := st.CreateTodo(model.Todo{ProjectID: p.ID, Title: "kept", Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	b := billing.New(acc, stripeStub{}, billing.Config{SecretKey: "sk_test", WebhookSecret: "whsec_x", PriceID: "price_x",
		TrialDays: 14, PublicURL: "https://dash.test"})
	b.SetClock(func() time.Time { return now })
	s.Billing = b
	h := s.Routes()
	a := loggedIn(t, h)
	if w := do(t, a, "POST", "/api/auth/switch-org", fmt.Sprintf(`{"org_id":%d}`, team.ID)); w.Code != http.StatusOK {
		t.Fatalf("switch org: %d %s", w.Code, w.Body)
	}
	newTodo := `{"title":"new","prompt":"y"}`

	// Within the grace period everything works.
	if w := do(t, a, "POST", "/api/projects/"+p.ID+"/todos", newTodo); w.Code != http.StatusCreated {
		t.Fatalf("create in grace: %d %s", w.Code, w.Body)
	}
	if b.Hold(team.ID) != "" {
		t.Fatal("tasks held during the grace period")
	}

	now = now.Add(billing.DefaultGrace + time.Minute)
	if w := do(t, a, "POST", "/api/projects/"+p.ID+"/todos", newTodo); w.Code != http.StatusPaymentRequired || !strings.Contains(w.Body.String(), "billing_read_only") {
		t.Fatalf("create when unpaid: %d %s", w.Code, w.Body)
	}
	for _, c := range []struct{ method, path, body string }{
		{"PATCH", "/api/todos/" + td.ID, `{"title":"changed"}`},
		{"POST", "/api/todos/" + td.ID + "/enqueue", ``},
		{"DELETE", "/api/todos/" + td.ID, ``},
		{"PATCH", "/api/projects/" + p.ID, `{"name":"renamed"}`},
	} {
		if w := do(t, a, c.method, c.path, c.body); w.Code != http.StatusPaymentRequired {
			t.Errorf("%s %s when unpaid: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	if got, err := st.GetTodo(td.ID); err != nil || got.Title != "kept" || got.Status != model.StatusDraft {
		t.Fatalf("the task changed or went: %+v %v", got, err)
	}
	// Reading still works.
	if w := do(t, a, "GET", "/api/projects/"+p.ID+"/todos", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "kept") {
		t.Fatalf("list when unpaid: %d %s", w.Code, w.Body)
	}
	// Queued tasks wait, here and on runners.
	if why := b.Hold(team.ID); why == "" {
		t.Fatal("an unpaid org's tasks aren't held")
	}

	// The chat's board tools may look but not change.
	token, release := s.Board.Register(mcptools.Caller{Org: team.ID, User: owner.User.ID}, p.ID)
	defer release()
	if text, isErr := call(t, h, token, "list_todos", `{"project":"`+p.ID+`"}`); isErr || !strings.Contains(text, "kept") {
		t.Errorf("list_todos when unpaid: %v %s", isErr, text)
	}
	for _, c := range []struct{ tool, args string }{
		{"create_todo", `{"project":"` + p.ID + `","title":"via chat","prompt":"z"}`},
		{"update_todo", `{"todo":"` + td.ID + `","title":"via chat"}`},
		{"queue_todo", `{"todo":"` + td.ID + `"}`},
		{"delete_todo", `{"todo":"` + td.ID + `"}`},
		{"report_issue", `{"project":"` + p.ID + `","title":"via chat"}`},
	} {
		if text, isErr := call(t, h, token, c.tool, c.args); !isErr || !strings.Contains(text, "read-only") {
			t.Errorf("%s when unpaid: %v %s", c.tool, isErr, text)
		}
	}

	// The owner sees why, and can subscribe.
	w := do(t, a, "GET", fmt.Sprintf("/api/orgs/%d/billing", team.ID), "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"read_only":true`) || !strings.Contains(w.Body.String(), `"can_manage":true`) {
		t.Fatalf("billing state: %d %s", w.Code, w.Body)
	}
	if w := do(t, a, "POST", fmt.Sprintf("/api/orgs/%d/billing/checkout", team.ID), ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "checkout.test") {
		t.Fatalf("checkout when unpaid: %d %s", w.Code, w.Body)
	}
	// A member sees the state but doesn't manage billing.
	mh := loginAs(t, h, "m", "m-password")
	do(t, mh, "POST", "/api/auth/switch-org", fmt.Sprintf(`{"org_id":%d}`, team.ID))
	if w := do(t, mh, "GET", fmt.Sprintf("/api/orgs/%d/billing", team.ID), ""); !strings.Contains(w.Body.String(), `"can_manage":false`) {
		t.Errorf("member's billing state: %d %s", w.Code, w.Body)
	}
	if w := do(t, mh, "POST", fmt.Sprintf("/api/orgs/%d/billing/checkout", team.ID), ""); w.Code != http.StatusForbidden {
		t.Errorf("member opens checkout: %d %s", w.Code, w.Body)
	}

	// Members can still be removed; one member is free, and writable again.
	if w := do(t, a, "DELETE", fmt.Sprintf("/api/orgs/%d/members/%d", team.ID, m.ID), `{"password":"p"}`); w.Code != http.StatusNoContent {
		t.Fatalf("remove member when unpaid: %d %s", w.Code, w.Body)
	}
	if w := do(t, a, "POST", "/api/projects/"+p.ID+"/todos", newTodo); w.Code != http.StatusCreated {
		t.Fatalf("create after going solo: %d %s", w.Code, w.Body)
	}
}

// With billing off (no Stripe), nothing is ever read-only.
func TestNoBillingNoLimits(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	h := s.Routes()
	a := loggedIn(t, h)
	owner := ownerSession(t, s)
	if w := do(t, a, "GET", fmt.Sprintf("/api/orgs/%d/billing", owner.Org.ID), ""); strings.TrimSpace(w.Body.String()) != `{"enabled":false}` {
		t.Fatalf("billing state: %d %s", w.Code, w.Body)
	}
	if w := do(t, a, "POST", "/api/projects/"+p.ID+"/todos", `{"title":"t","prompt":"p"}`); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
}
