package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Stripe is the part of Stripe's API that billing uses. HTTPStripe calls the
// real one; tests use a fake.
type Stripe interface {
	CreateCustomer(ctx context.Context, p CustomerParams) (id string, err error)
	CreateCheckoutSession(ctx context.Context, p CheckoutParams) (url string, err error)
	CreatePortalSession(ctx context.Context, customer, returnURL string) (url string, err error)
	GetSubscription(ctx context.Context, id string) (Subscription, error)
	// UpdateQuantity sets the seats of a subscription's item, prorated.
	UpdateQuantity(ctx context.Context, subscription, item string, quantity int) (Subscription, error)
}

// CustomerParams describe a new Stripe customer: one per org.
type CustomerParams struct {
	OrgID int64
	Name  string
	Email string
}

// CheckoutParams describe a Checkout session that subscribes an org.
type CheckoutParams struct {
	OrgID      int64
	Customer   string
	Price      string
	Quantity   int
	TrialDays  int // 0: no trial
	SuccessURL string
	CancelURL  string
}

// Subscription is what billing needs of a Stripe subscription.
type Subscription struct {
	ID                string
	Customer          string
	Status            string
	ItemID            string
	Price             string
	Quantity          int
	PeriodEnd         int64
	TrialEnd          int64
	CancelAtPeriodEnd bool
	OrgID             int64 // from its metadata (set by Checkout); 0 if absent
}

// UnmarshalJSON reads a Stripe subscription object. The customer may be
// expanded; the period end is on the subscription in older API versions and
// on its items in newer ones.
func (s *Subscription) UnmarshalJSON(b []byte) error {
	var raw struct {
		ID                string          `json:"id"`
		Customer          json.RawMessage `json:"customer"`
		Status            string          `json:"status"`
		CurrentPeriodEnd  int64           `json:"current_period_end"`
		TrialEnd          *int64          `json:"trial_end"`
		CancelAtPeriodEnd bool            `json:"cancel_at_period_end"`
		Metadata          map[string]any  `json:"metadata"`
		Items             struct {
			Data []struct {
				ID               string `json:"id"`
				Quantity         int    `json:"quantity"`
				CurrentPeriodEnd int64  `json:"current_period_end"`
				Price            struct {
					ID string `json:"id"`
				} `json:"price"`
			} `json:"data"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*s = Subscription{ID: raw.ID, Status: raw.Status, PeriodEnd: raw.CurrentPeriodEnd, CancelAtPeriodEnd: raw.CancelAtPeriodEnd}
	s.Customer = idOf(raw.Customer)
	if raw.TrialEnd != nil {
		s.TrialEnd = *raw.TrialEnd
	}
	if v, ok := raw.Metadata["org_id"].(string); ok {
		s.OrgID, _ = strconv.ParseInt(v, 10, 64)
	}
	if len(raw.Items.Data) > 0 {
		it := raw.Items.Data[0]
		s.ItemID, s.Quantity, s.Price = it.ID, it.Quantity, it.Price.ID
		if s.PeriodEnd == 0 {
			s.PeriodEnd = it.CurrentPeriodEnd
		}
	}
	return nil
}

// idOf is the id of a Stripe reference: a string, or an expanded object.
func idOf(raw json.RawMessage) string {
	var id string
	if json.Unmarshal(raw, &id) == nil {
		return id
	}
	var obj struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &obj)
	return obj.ID
}

// HTTPStripe calls Stripe's REST API with a secret key.
type HTTPStripe struct {
	Key    string
	Base   string // default https://api.stripe.com
	Client *http.Client
}

func (h *HTTPStripe) call(ctx context.Context, method, path string, form url.Values, out any) error {
	base := h.Base
	if base == "" {
		base = "https://api.stripe.com"
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(h.Key, "")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	c := h.Client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("stripe: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("stripe: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Error.Message == "" {
			e.Error.Message = resp.Status
		}
		return fmt.Errorf("stripe %s %s: %s", method, path, e.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

func (h *HTTPStripe) CreateCustomer(ctx context.Context, p CustomerParams) (string, error) {
	f := url.Values{}
	f.Set("name", p.Name)
	if p.Email != "" {
		f.Set("email", p.Email)
	}
	f.Set("metadata[org_id]", strconv.FormatInt(p.OrgID, 10))
	var out struct {
		ID string `json:"id"`
	}
	err := h.call(ctx, http.MethodPost, "/v1/customers", f, &out)
	return out.ID, err
}

func (h *HTTPStripe) CreateCheckoutSession(ctx context.Context, p CheckoutParams) (string, error) {
	org := strconv.FormatInt(p.OrgID, 10)
	f := url.Values{}
	f.Set("mode", "subscription")
	f.Set("customer", p.Customer)
	f.Set("client_reference_id", org)
	f.Set("line_items[0][price]", p.Price)
	f.Set("line_items[0][quantity]", strconv.Itoa(p.Quantity))
	f.Set("subscription_data[metadata][org_id]", org)
	if p.TrialDays > 0 {
		f.Set("subscription_data[trial_period_days]", strconv.Itoa(p.TrialDays))
	}
	// Stripe Tax works out VAT from the address; with a VAT number, EU
	// businesses abroad get reverse charge. Both are saved on the customer.
	f.Set("automatic_tax[enabled]", "true")
	f.Set("tax_id_collection[enabled]", "true")
	f.Set("billing_address_collection", "required")
	f.Set("customer_update[address]", "auto")
	f.Set("customer_update[name]", "auto")
	f.Set("success_url", p.SuccessURL)
	f.Set("cancel_url", p.CancelURL)
	var out struct {
		URL string `json:"url"`
	}
	err := h.call(ctx, http.MethodPost, "/v1/checkout/sessions", f, &out)
	return out.URL, err
}

func (h *HTTPStripe) CreatePortalSession(ctx context.Context, customer, returnURL string) (string, error) {
	f := url.Values{}
	f.Set("customer", customer)
	f.Set("return_url", returnURL)
	var out struct {
		URL string `json:"url"`
	}
	err := h.call(ctx, http.MethodPost, "/v1/billing_portal/sessions", f, &out)
	return out.URL, err
}

func (h *HTTPStripe) GetSubscription(ctx context.Context, id string) (Subscription, error) {
	var s Subscription
	err := h.call(ctx, http.MethodGet, "/v1/subscriptions/"+url.PathEscape(id), nil, &s)
	return s, err
}

func (h *HTTPStripe) UpdateQuantity(ctx context.Context, subscription, item string, quantity int) (Subscription, error) {
	if item == "" {
		cur, err := h.GetSubscription(ctx, subscription)
		if err != nil {
			return cur, err
		}
		item = cur.ItemID
	}
	f := url.Values{}
	f.Set("items[0][id]", item)
	f.Set("items[0][quantity]", strconv.Itoa(quantity))
	f.Set("proration_behavior", "create_prorations")
	var s Subscription
	err := h.call(ctx, http.MethodPost, "/v1/subscriptions/"+url.PathEscape(subscription), f, &s)
	return s, err
}
