// Package billing charges organizations per seat with a Stripe subscription.
// It is off unless Stripe is configured (see docs/billing.md): a nil
// *Service is billing off, and every method of it answers as if nothing
// needed paying.
//
// The rule: a personal workspace (one member) is free; an org with two or
// more members needs an active or trialing subscription, whose quantity is
// its member count. Without one, after a grace period, its board becomes
// read-only and its tasks don't start. Nothing is ever deleted.
package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/klahr/dash/internal/store"
)

// DefaultTrialDays is the trial Checkout offers an org's first subscription.
const DefaultTrialDays = 14

// DefaultGrace is how long an org that needs a subscription may go without
// one (a failed payment Stripe is retrying, a trial that ended) before its
// board becomes read-only.
const DefaultGrace = 3 * 24 * time.Hour

var (
	// ErrReadOnly is what a change to an unpaid org's board gets.
	ErrReadOnly = errors.New("this organization's board is read-only until its subscription is active: an owner can subscribe in Organization → Billing")
	// ErrSubscribed: the org has a subscription; change it in the portal.
	ErrSubscribed = errors.New("this organization already has a subscription: manage it in the billing portal")
	// ErrNoCustomer: the org has never subscribed, so there's no portal yet.
	ErrNoCustomer = errors.New("this organization has no billing account yet: subscribe first")
)

// Config is Stripe's side of billing.
type Config struct {
	SecretKey     string // STRIPE_SECRET_KEY
	WebhookSecret string // STRIPE_WEBHOOK_SECRET
	PriceID       string // STRIPE_PRICE_ID: a recurring per-seat price
	TrialDays     int    // a first subscription's trial; 0 = none
	Grace         time.Duration
	PublicURL     string // where Checkout and the portal send people back to
}

// Configured reports whether any of Stripe's settings are present: if none
// are, billing is simply off.
func (c Config) Configured() bool {
	return c.SecretKey != "" || c.WebhookSecret != "" || c.PriceID != ""
}

// Check says what is missing or wrong for billing to work.
func (c Config) Check() error {
	var missing []string
	if c.SecretKey == "" {
		missing = append(missing, "STRIPE_SECRET_KEY")
	}
	if c.WebhookSecret == "" {
		missing = append(missing, "STRIPE_WEBHOOK_SECRET")
	}
	if c.PriceID == "" {
		missing = append(missing, "STRIPE_PRICE_ID")
	}
	if c.PublicURL == "" {
		missing = append(missing, "DASH_PUBLIC_URL (Checkout sends people back there)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing %s", strings.Join(missing, ", "))
	}
	if !strings.HasPrefix(c.SecretKey, "sk_") && !strings.HasPrefix(c.SecretKey, "rk_") {
		return errors.New("STRIPE_SECRET_KEY should start with sk_ (or rk_ for a restricted key)")
	}
	if !strings.HasPrefix(c.WebhookSecret, "whsec_") {
		return errors.New("STRIPE_WEBHOOK_SECRET should start with whsec_ (the endpoint's signing secret)")
	}
	if !strings.HasPrefix(c.PriceID, "price_") {
		return errors.New("STRIPE_PRICE_ID should be a price id (price_…), not a product id")
	}
	if c.TrialDays < 0 || c.TrialDays > 730 {
		return errors.New("the trial is 0 to 730 days")
	}
	if !strings.HasPrefix(c.PublicURL, "https://") && !strings.HasPrefix(c.PublicURL, "http://") {
		return errors.New("DASH_PUBLIC_URL should be a URL like https://dash.example.com")
	}
	return nil
}

// Service is billing on. Its zero value isn't usable: see New.
type Service struct {
	// OnChange is told when an org's subscription changed (optional): its
	// tasks may start again.
	OnChange func(org int64)

	acc    *store.Accounts
	stripe Stripe
	cfg    Config
	now    func() time.Time
}

// New turns billing on with cfg (already checked), calling Stripe through st.
func New(acc *store.Accounts, st Stripe, cfg Config) *Service {
	if cfg.Grace <= 0 {
		cfg.Grace = DefaultGrace
	}
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	return &Service{acc: acc, stripe: st, cfg: cfg, now: time.Now}
}

// Grace is how long an unpaid org keeps a writable board.
func (s *Service) Grace() time.Duration { return s.cfg.Grace }

// SetClock replaces time.Now, for tests.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// good reports whether a subscription in Stripe status st is paid for.
func good(st string) bool { return st == "active" || st == "trialing" }

// live reports whether a subscription in status st still exists to change
// (Stripe can't change a canceled one, nor one that never started).
func live(st string) bool {
	switch st {
	case "", "canceled", "incomplete_expired":
		return false
	}
	return true
}

// State is an org's billing as its members see it.
type State struct {
	Enabled bool `json:"enabled"`
	// Free: the org needs no subscription (a personal workspace, one
	// member, or the instance's own org).
	Free     bool   `json:"free"`
	Members  int    `json:"members"`
	Required bool   `json:"required"` // it needs a subscription and has none that's paid for
	Status   string `json:"status,omitempty"`
	ReadOnly bool   `json:"read_only"`
	// GraceUntil is when the board becomes read-only if nothing is paid.
	GraceUntil        *time.Time `json:"grace_until,omitempty"`
	PeriodEnd         *time.Time `json:"period_end,omitempty"`
	TrialEnd          *time.Time `json:"trial_end,omitempty"`
	CancelAtPeriodEnd bool       `json:"cancel_at_period_end,omitempty"`
	Seats             int        `json:"seats,omitempty"` // what Stripe bills for
	// CanSubscribe: Checkout is open (there's no live subscription).
	CanSubscribe bool `json:"can_subscribe"`
	// HasAccount: there's a Stripe customer, so the portal works.
	HasAccount bool `json:"has_account"`
	TrialDays  int  `json:"trial_days,omitempty"` // the trial Checkout offers
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// exempt reports whether org is the instance's own: the Default org of the
// DASH_USERNAME account is never billed.
func (s *Service) exempt(org int64) bool {
	d, err := s.acc.DefaultOrg()
	return err == nil && d.ID == org
}

// State is org's billing now. It also starts (or ends) the grace period of
// an org that needs a subscription it doesn't have.
func (s *Service) State(org int64) (State, error) {
	if s == nil {
		return State{}, nil
	}
	members, err := s.acc.ForOrg(org).MemberCount()
	if err != nil {
		return State{}, err
	}
	personal, err := s.acc.ForOrg(org).Personal()
	if err != nil {
		return State{}, err
	}
	b, err := s.acc.OrgBilling(org)
	if err != nil {
		return State{}, err
	}
	st := State{
		Enabled: true, Members: members, Status: b.Status,
		Free:      personal || members < 2 || s.exempt(org),
		PeriodEnd: timePtr(b.PeriodEnd), TrialEnd: timePtr(b.TrialEnd), CancelAtPeriodEnd: b.CancelAtPeriodEnd,
		CanSubscribe: !live(b.Status), HasAccount: b.CustomerID != "",
	}
	if live(b.Status) {
		st.Seats = b.Quantity
	}
	if !b.TrialUsed {
		st.TrialDays = s.cfg.TrialDays
	}
	st.Required = !st.Free && !good(b.Status)
	now := s.now()
	switch {
	case st.Required && b.UnpaidSince.IsZero():
		b, err = s.acc.UpdateOrgBilling(org, func(x *store.OrgBilling) error {
			if x.UnpaidSince.IsZero() {
				x.UnpaidSince = now.UTC().Truncate(time.Second)
			}
			return nil
		})
		if err != nil {
			return st, err
		}
	case !st.Required && !b.UnpaidSince.IsZero():
		if _, err := s.acc.UpdateOrgBilling(org, func(x *store.OrgBilling) error {
			x.UnpaidSince = time.Time{}
			return nil
		}); err != nil {
			return st, err
		}
	}
	if st.Required {
		until := b.UnpaidSince.Add(s.cfg.Grace)
		st.GraceUntil = &until
		st.ReadOnly = !now.Before(until)
	}
	return st, nil
}

// ReadOnly reports whether org's board may only be looked at. If its state
// can't be read, it isn't held back: billing never locks anyone out by
// mistake.
func (s *Service) ReadOnly(org int64) bool {
	if s == nil || org == 0 {
		return false
	}
	st, err := s.State(org)
	if err != nil {
		slog.Error("billing state", "org", org, "err", err)
		return false
	}
	return st.ReadOnly
}

// Hold is why org's queued tasks and scheduled scans don't start, or "".
func (s *Service) Hold(org int64) string {
	if s.ReadOnly(org) {
		return "waiting for the organization's subscription: an owner can subscribe in Organization → Billing"
	}
	return ""
}

// InviteNotice is what the inviting owner should hear when an invitation
// would make org need a subscription it doesn't have, or nil.
func (s *Service) InviteNotice(org int64) any {
	if s == nil {
		return nil
	}
	st, err := s.State(org)
	if err != nil || s.exempt(org) || good(st.Status) {
		return nil
	}
	if p, _ := s.acc.ForOrg(org).Personal(); p {
		return nil
	}
	return map[string]any{
		"subscription_needed": true,
		"trial_days":          st.TrialDays,
		"message":             "An organization with two or more members needs a subscription, billed per member.",
	}
}

// MembersChanged brings org's subscription's seats in line with its
// members, after someone joined or left. Failures are logged; the next
// change, or Stripe's next webhook, tries again.
func (s *Service) MembersChanged(org int64) {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.SyncSeats(ctx, org); err != nil {
		slog.Error("billing: update seats", "org", org, "err", err)
	}
	if _, err := s.State(org); err != nil { // starts or ends the grace period
		slog.Error("billing state", "org", org, "err", err)
	}
}

// SyncSeats sets the quantity of org's live subscription to its member
// count (at least 1), with proration.
func (s *Service) SyncSeats(ctx context.Context, org int64) error {
	if s == nil {
		return nil
	}
	b, err := s.acc.OrgBilling(org)
	if err != nil || b.SubscriptionID == "" || !live(b.Status) {
		return err
	}
	n, err := s.acc.ForOrg(org).MemberCount()
	if err != nil {
		return err
	}
	n = max(n, 1)
	if n == b.Quantity {
		return nil
	}
	sub, err := s.stripe.UpdateQuantity(ctx, b.SubscriptionID, b.ItemID, n)
	if err != nil {
		return err
	}
	_, err = s.acc.UpdateOrgBilling(org, func(x *store.OrgBilling) error {
		if x.SubscriptionID != b.SubscriptionID {
			return nil // replaced meanwhile
		}
		x.Quantity = n
		if sub.ItemID != "" {
			x.ItemID = sub.ItemID
		}
		return nil
	})
	if err == nil {
		slog.Info("billing: seats updated", "org", org, "seats", n)
	}
	return err
}

// customer is org's Stripe customer, created if it has none yet.
func (s *Service) customer(ctx context.Context, org int64) (string, error) {
	b, err := s.acc.OrgBilling(org)
	if err != nil {
		return "", err
	}
	if b.CustomerID != "" {
		return b.CustomerID, nil
	}
	name, err := s.acc.OrgName(org)
	if err != nil {
		return "", err
	}
	email, err := s.acc.OrgOwnerEmail(org)
	if err != nil {
		return "", err
	}
	id, err := s.stripe.CreateCustomer(ctx, CustomerParams{OrgID: org, Name: name, Email: email})
	if err != nil {
		return "", err
	}
	var kept string
	_, err = s.acc.UpdateOrgBilling(org, func(x *store.OrgBilling) error {
		if x.CustomerID == "" { // another request may have made one first
			x.CustomerID = id
		}
		kept = x.CustomerID
		return nil
	})
	return kept, err
}

// Checkout opens a Stripe Checkout session that subscribes org, with its
// members as seats, and returns its URL.
func (s *Service) Checkout(ctx context.Context, org int64) (string, error) {
	b, err := s.acc.OrgBilling(org)
	if err != nil {
		return "", err
	}
	if live(b.Status) {
		return "", ErrSubscribed
	}
	n, err := s.acc.ForOrg(org).MemberCount()
	if err != nil {
		return "", err
	}
	cust, err := s.customer(ctx, org)
	if err != nil {
		return "", err
	}
	trial := s.cfg.TrialDays
	if b.TrialUsed {
		trial = 0
	}
	return s.stripe.CreateCheckoutSession(ctx, CheckoutParams{
		OrgID: org, Customer: cust, Price: s.cfg.PriceID, Quantity: max(n, 1), TrialDays: trial,
		SuccessURL: s.cfg.PublicURL + "/?billing=done",
		CancelURL:  s.cfg.PublicURL + "/?billing=canceled",
	})
}

// Portal opens the Stripe customer portal for org (card, invoices,
// cancelling) and returns its URL.
func (s *Service) Portal(ctx context.Context, org int64) (string, error) {
	b, err := s.acc.OrgBilling(org)
	if err != nil {
		return "", err
	}
	if b.CustomerID == "" {
		return "", ErrNoCustomer
	}
	return s.stripe.CreatePortalSession(ctx, b.CustomerID, s.cfg.PublicURL+"/?billing=portal")
}
