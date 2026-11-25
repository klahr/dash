// Package billing charges organizations per seat with a Stripe subscription.
// It is off unless Stripe is configured (see docs/billing.md): a nil
// *Service is billing off, and every method of it answers as if nothing
// needed paying.
//
// The rule: a personal workspace (one member) is free; an org with two or
// more members needs an active or trialing subscription, whose quantity is
// its member count. Without one, after a grace period, its board becomes
// read-only and its tasks don't start. Nothing is ever deleted.
//
// With an individual price set, individuals (a personal workspace or a
// one-member org) are asked to pay, at a single price, but never made to:
// "I can't pay right now" keeps them free, and nothing is ever locked.
package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/klahr/dash/internal/auth"
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
	ErrReadOnly = errors.New("this organization's board is read-only until its subscription is active: an owner can subscribe in Organization → Billing & usage")
	// ErrSubscribed: the org has a subscription; change it in the portal.
	ErrSubscribed = errors.New("this organization already has a subscription: manage it in the billing portal")
	// ErrNoCustomer: the org has never subscribed, so there's no portal yet.
	ErrNoCustomer = errors.New("this organization has no billing account yet: subscribe first")
	// ErrNotIndividual: only individuals choose to use Dash for free.
	ErrNotIndividual = errors.New("only a personal workspace or a one-member organization can choose to use Dash for free")
)

// Config is Stripe's side of billing.
type Config struct {
	SecretKey     string // STRIPE_SECRET_KEY
	WebhookSecret string // STRIPE_WEBHOOK_SECRET
	PriceID       string // STRIPE_PRICE_ID: a recurring per-seat price
	// IndividualPriceID (STRIPE_INDIVIDUAL_PRICE_ID, optional): a recurring
	// price individuals are asked, not made, to pay. "": individuals are free.
	IndividualPriceID string
	// IndividualPrice is that price as shown (DASH_INDIVIDUAL_PRICE), e.g.
	// "€5 / month" (optional: Checkout shows the amount anyway).
	IndividualPrice string
	TrialDays       int // a first subscription's trial; 0 = none
	Grace           time.Duration
	PublicURL       string // where Checkout and the portal send people back to
	// TermsURL and PrivacyURL are the published terms and privacy policy,
	// absolute (optional): Checkout asks customers to accept the terms.
	TermsURL, PrivacyURL string
}

// Configured reports whether any of Stripe's settings are present: if none
// are, billing is simply off.
func (c Config) Configured() bool {
	return c.SecretKey != "" || c.WebhookSecret != "" || c.PriceID != "" || c.IndividualPriceID != ""
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
	if c.IndividualPriceID != "" && !strings.HasPrefix(c.IndividualPriceID, "price_") {
		return errors.New("STRIPE_INDIVIDUAL_PRICE_ID should be a price id (price_…), not a product id")
	}
	if c.IndividualPriceID != "" && c.IndividualPriceID == c.PriceID {
		return errors.New("STRIPE_INDIVIDUAL_PRICE_ID should be another price than STRIPE_PRICE_ID")
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
	// member, the instance's own org, or a platform admin's override).
	Free bool `json:"free"`
	// Exempt: the instance's own Default org, which is never billed.
	Exempt bool `json:"exempt,omitempty"`
	// FreeForever and FreeUntil: a platform admin made the org free, for
	// good or until then. Only set while the override holds.
	FreeForever bool       `json:"free_forever,omitempty"`
	FreeUntil   *time.Time `json:"free_until,omitempty"`
	Members     int        `json:"members"`
	Required    bool       `json:"required"` // it needs a subscription and has none that's paid for
	Status      string     `json:"status,omitempty"`
	ReadOnly    bool       `json:"read_only"`
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

	// Individual: the org is an individual's (a personal workspace or one
	// member) and the instance has an individual price: it is asked to pay,
	// never made to. Free stays true and Required false.
	Individual      bool   `json:"individual,omitempty"`
	IndividualPrice string `json:"individual_price,omitempty"` // the price as shown, if set
	// Ask: an individual that has never subscribed nor chosen to use Dash
	// for free: offer Subscribe and "I can't pay right now".
	Ask bool `json:"ask,omitempty"`
	// ChoseFree: they clicked "I can't pay right now".
	ChoseFree bool `json:"chose_free,omitempty"`
	// Plan is what the live subscription is on: "individual" or "team".
	Plan string `json:"plan,omitempty"`
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

// individualPrice reports whether price is the instance's individual price.
func (s *Service) individualPrice(price string) bool {
	return s.cfg.IndividualPriceID != "" && price == s.cfg.IndividualPriceID
}

// freeByOverride reports whether a platform admin's override makes b's org
// free at now.
func freeByOverride(b store.OrgBilling, now time.Time) bool {
	return b.FreeForever || now.Before(b.FreeUntil)
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
	now := s.now()
	override := freeByOverride(b, now)
	exempt := s.exempt(org)
	st := State{
		Enabled: true, Members: members, Status: b.Status, Exempt: exempt,
		Free:      personal || members < 2 || exempt || override,
		PeriodEnd: timePtr(b.PeriodEnd), TrialEnd: timePtr(b.TrialEnd), CancelAtPeriodEnd: b.CancelAtPeriodEnd,
		CanSubscribe: !live(b.Status), HasAccount: b.CustomerID != "",
	}
	if live(b.Status) {
		st.Seats = b.Quantity
		st.Plan = "team"
		if s.individualPrice(b.PriceID) {
			st.Plan = "individual"
		}
	}
	// Individuals are asked to pay, never made to: this touches nothing
	// below (Free stays, so Required stays false).
	if s.cfg.IndividualPriceID != "" && (personal || members < 2) && !exempt && !override {
		st.Individual, st.IndividualPrice = true, s.cfg.IndividualPrice
		st.ChoseFree = !b.ChoseFreeAt.IsZero()
		// Having subscribed once (even if since cancelled) is an answer too.
		st.Ask = !st.ChoseFree && b.SubscriptionID == ""
	}
	if !b.TrialUsed {
		st.TrialDays = s.cfg.TrialDays
	}
	if override {
		st.FreeForever = b.FreeForever
		if !b.FreeForever {
			st.FreeUntil = timePtr(b.FreeUntil)
		}
	}
	st.Required = !st.Free && !good(b.Status)
	// The grace period counts from when the org came to need a
	// subscription: never from before an admin's override ended.
	stale := func(x store.OrgBilling) bool { return x.UnpaidSince.IsZero() || x.UnpaidSince.Before(x.FreeUntil) }
	switch {
	case st.Required && stale(b):
		b, err = s.acc.UpdateOrgBilling(org, func(x *store.OrgBilling) error {
			if stale(*x) {
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
		return "waiting for the organization's subscription: an owner can subscribe in Organization → Billing & usage"
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
	if err != nil || s.exempt(org) || good(st.Status) || st.FreeForever || st.FreeUntil != nil {
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
// count (at least 1), with proration. An individual subscription isn't
// seat-synced: it stays at one while the org has one member, and moves to
// the per-seat price, with a seat per member, once it has two or more.
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
	price := "" // keep it
	if s.individualPrice(b.PriceID) {
		if n < 2 {
			return nil
		}
		price = s.cfg.PriceID // a team now: per seat
	}
	if n == b.Quantity && price == "" {
		return nil
	}
	sub, err := s.stripe.UpdateItem(ctx, b.SubscriptionID, b.ItemID, price, n)
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
		if price != "" {
			x.PriceID = price
		}
		return nil
	})
	if err == nil {
		slog.Info("billing: seats updated", "org", org, "seats", n, "to_team_price", price != "")
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
// members as seats, and returns its URL. An individual subscribes at the
// individual price instead: one, with no trial.
func (s *Service) Checkout(ctx context.Context, org int64) (string, error) {
	st, err := s.State(org)
	if err != nil {
		return "", err
	}
	if !st.CanSubscribe {
		return "", ErrSubscribed
	}
	cust, err := s.customer(ctx, org)
	if err != nil {
		return "", err
	}
	price, n, trial := s.cfg.PriceID, max(st.Members, 1), st.TrialDays
	if st.Individual {
		price, n, trial = s.cfg.IndividualPriceID, 1, 0
	}
	return s.stripe.CreateCheckoutSession(ctx, CheckoutParams{
		OrgID: org, Customer: cust, Price: price, Quantity: n, TrialDays: trial,
		SuccessURL: s.cfg.PublicURL + auth.BillingReturnPath + "?to=done",
		CancelURL:  s.cfg.PublicURL + auth.BillingReturnPath + "?to=canceled",
		TermsURL:   s.cfg.TermsURL, PrivacyURL: s.cfg.PrivacyURL,
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
	return s.stripe.CreatePortalSession(ctx, b.CustomerID, s.cfg.PublicURL+auth.BillingReturnPath+"?to=portal")
}

// ChooseFree records that an individual can't pay right now: Dash stops
// asking them, and they keep using it for free. They can subscribe later.
func (s *Service) ChooseFree(org int64) (State, error) {
	st, err := s.State(org)
	if err != nil {
		return st, err
	}
	if !st.Individual {
		return st, ErrNotIndividual
	}
	if !st.ChoseFree {
		if _, err := s.acc.UpdateOrgBilling(org, func(x *store.OrgBilling) error {
			if x.ChoseFreeAt.IsZero() {
				x.ChoseFreeAt = s.now().UTC().Truncate(time.Second)
			}
			return nil
		}); err != nil {
			return st, err
		}
		slog.Info("billing: individual chose to use Dash for free", "org", org)
	}
	return s.State(org)
}

// ErrFreeOverride is a free override that can't be set as asked.
var ErrFreeOverride = errors.New("make the organization free either forever or until a date in the future")

// Override is a platform admin's billing override of an org, as admins see
// it.
type Override struct {
	OrgID   int64  `json:"org_id"`
	OrgName string `json:"org_name"`
	// Active: the override makes the org free now.
	Active      bool       `json:"active"`
	FreeForever bool       `json:"free_forever"`
	FreeUntil   *time.Time `json:"free_until,omitempty"`
	Note        string     `json:"note,omitempty"`
	SetBy       string     `json:"set_by,omitempty"`
	SetAt       *time.Time `json:"set_at,omitempty"`
}

// maxNote is how long an override's note may be, in bytes.
const maxNote = 500

// Override is org's billing override.
func (s *Service) Override(org int64) (Override, error) {
	name, err := s.acc.OrgName(org)
	if err != nil {
		return Override{}, err
	}
	b, err := s.acc.OrgBilling(org)
	if err != nil {
		return Override{}, err
	}
	return Override{
		OrgID: org, OrgName: name, Active: freeByOverride(b, s.now()),
		FreeForever: b.FreeForever, FreeUntil: timePtr(b.FreeUntil),
		Note: b.FreeNote, SetBy: b.FreeSetBy, SetAt: timePtr(b.FreeSetAt),
	}, nil
}

// SetFree makes org free forever, or until until (which must be in the
// future), whatever its members; by is the admin who does it. A
// subscription it has is left alone: its owners cancel it in the portal if
// they want, and its seats keep following the members.
func (s *Service) SetFree(org int64, forever bool, until time.Time, note, by string) (Override, error) {
	now := s.now()
	note = strings.TrimSpace(note)
	if forever == !until.IsZero() || !forever && !now.Before(until) || len(note) > maxNote {
		return Override{}, ErrFreeOverride
	}
	if forever {
		until = time.Time{}
	}
	if _, err := s.acc.UpdateOrgBilling(org, func(x *store.OrgBilling) error {
		x.FreeForever, x.FreeUntil, x.FreeNote = forever, until.UTC().Truncate(time.Second), note
		if !x.FreeUntil.IsZero() && !now.Before(x.FreeUntil) {
			return ErrFreeOverride // under a second away
		}
		x.FreeSetBy, x.FreeSetAt = by, now.UTC().Truncate(time.Second)
		x.UnpaidSince = time.Time{} // free now: a grace period starts afresh after
		return nil
	}); err != nil {
		return Override{}, err
	}
	s.changed(org)
	slog.Info("billing: org made free", "org", org, "forever", forever, "until", until, "by", by)
	return s.Override(org)
}

// ClearFree ends org's override: the usual rule applies again, and an org
// that then needs a subscription gets a full grace period from now.
func (s *Service) ClearFree(org int64, by string) (Override, error) {
	if _, err := s.acc.UpdateOrgBilling(org, func(x *store.OrgBilling) error {
		if freeByOverride(*x, s.now()) {
			x.UnpaidSince = time.Time{}
		}
		x.FreeForever, x.FreeUntil, x.FreeNote, x.FreeSetBy, x.FreeSetAt = false, time.Time{}, "", "", time.Time{}
		return nil
	}); err != nil {
		return Override{}, err
	}
	s.changed(org)
	slog.Info("billing: org's free override cleared", "org", org, "by", by)
	return s.Override(org)
}

// changed brings org's grace period in line and tells OnChange: its tasks
// may start now.
func (s *Service) changed(org int64) {
	if _, err := s.State(org); err != nil {
		slog.Error("billing state", "org", org, "err", err)
	}
	if s.OnChange != nil {
		s.OnChange(org)
	}
}
