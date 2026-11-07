package billing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// The HTTP client sends what Stripe expects: Checkout with automatic tax,
// tax ID collection and the org in the metadata; prorated seat changes.
func TestHTTPStripeRequests(t *testing.T) {
	var mu sync.Mutex
	got := map[string]url.Values{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, _, ok := r.BasicAuth(); !ok || u != "sk_test_k" {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "bad key"}})
			return
		}
		_ = r.ParseForm()
		mu.Lock()
		got[r.Method+" "+r.URL.Path] = r.PostForm
		mu.Unlock()
		switch {
		case r.URL.Path == "/v1/customers":
			w.Write([]byte(`{"id":"cus_1"}`))
		case r.URL.Path == "/v1/checkout/sessions":
			w.Write([]byte(`{"url":"https://checkout.example/x"}`))
		case r.URL.Path == "/v1/billing_portal/sessions":
			w.Write([]byte(`{"url":"https://portal.example/x"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/subscriptions/"):
			// A newer API version: the period end is on the item.
			w.Write([]byte(`{"id":"sub_1","customer":{"id":"cus_1"},"status":"active","trial_end":null,
				"metadata":{"org_id":"5"},"items":{"data":[{"id":"si_1","quantity":4,"current_period_end":1800000000,"price":{"id":"price_p"}}]}}`))
		}
	}))
	defer srv.Close()
	st := &HTTPStripe{Key: "sk_test_k", Base: srv.URL}
	ctx := context.Background()

	if id, err := st.CreateCustomer(ctx, CustomerParams{OrgID: 5, Name: "Team", Email: "o@example.com"}); err != nil || id != "cus_1" {
		t.Fatal(id, err)
	}
	if f := got["POST /v1/customers"]; f.Get("metadata[org_id]") != "5" || f.Get("name") != "Team" {
		t.Errorf("customer form: %v", f)
	}
	u, err := st.CreateCheckoutSession(ctx, CheckoutParams{OrgID: 5, Customer: "cus_1", Price: "price_p", Quantity: 2, TrialDays: 14,
		SuccessURL: "https://dash.example/?billing=done", CancelURL: "https://dash.example/?billing=canceled"})
	if err != nil || u != "https://checkout.example/x" {
		t.Fatal(u, err)
	}
	f := got["POST /v1/checkout/sessions"]
	for k, v := range map[string]string{
		"mode": "subscription", "customer": "cus_1", "client_reference_id": "5",
		"line_items[0][price]": "price_p", "line_items[0][quantity]": "2",
		"subscription_data[trial_period_days]": "14", "subscription_data[metadata][org_id]": "5",
		"automatic_tax[enabled]": "true", "tax_id_collection[enabled]": "true",
		"customer_update[address]": "auto", "customer_update[name]": "auto",
	} {
		if f.Get(k) != v {
			t.Errorf("checkout %s = %q, want %q", k, f.Get(k), v)
		}
	}
	if _, err := st.CreatePortalSession(ctx, "cus_1", "https://dash.example/"); err != nil {
		t.Fatal(err)
	}
	sub, err := st.UpdateQuantity(ctx, "sub_1", "", 4) // no item on record: looked up first
	if err != nil {
		t.Fatal(err)
	}
	if f := got["POST /v1/subscriptions/sub_1"]; f.Get("items[0][id]") != "si_1" || f.Get("items[0][quantity]") != "4" || f.Get("proration_behavior") != "create_prorations" {
		t.Errorf("update form: %v", f)
	}
	if sub.Customer != "cus_1" || sub.OrgID != 5 || sub.PeriodEnd != 1800000000 || sub.Quantity != 4 || sub.ItemID != "si_1" || sub.TrialEnd != 0 {
		t.Errorf("parsed subscription: %+v", sub)
	}
	bad := &HTTPStripe{Key: "sk_test_wrong", Base: srv.URL}
	if _, err := bad.CreateCustomer(ctx, CustomerParams{}); err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Errorf("error from Stripe: %v", err)
	}
}
