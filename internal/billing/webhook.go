package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/klahr/dash/internal/store"
)

// SignatureTolerance is how old a webhook's signed timestamp may be: older
// deliveries are refused as possible replays.
const SignatureTolerance = 5 * time.Minute

var errSignature = errors.New("bad Stripe-Signature")

// VerifySignature checks a Stripe-Signature header ("t=…,v1=…[,v1=…]")
// against payload: one v1 must be the HMAC-SHA256 of "t.payload" with
// secret, and t within tolerance of now.
func VerifySignature(payload []byte, header, secret string, now time.Time, tolerance time.Duration) error {
	var ts int64
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts, _ = strconv.ParseInt(v, 10, 64)
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if ts == 0 || len(sigs) == 0 {
		return errSignature
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return fmt.Errorf("%w: timestamp too far from now", errSignature)
	}
	want := sign(payload, secret, ts)
	for _, s := range sigs {
		got, err := hex.DecodeString(s)
		if err == nil && hmac.Equal(got, want) {
			return nil
		}
	}
	return errSignature
}

func sign(payload []byte, secret string, ts int64) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(m, "%d.", ts)
	m.Write(payload)
	return m.Sum(nil)
}

// SignatureHeader is the Stripe-Signature header Stripe would send with
// payload at t (for tests and local tools).
func SignatureHeader(payload []byte, secret string, t time.Time) string {
	return fmt.Sprintf("t=%d,v1=%s", t.Unix(), hex.EncodeToString(sign(payload, secret, t.Unix())))
}

// Event is a Stripe webhook event.
type Event struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Created int64  `json:"created"`
	Data    struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

// webhook handles POST /api/billing/webhook: Stripe's events, verified by
// their signature, each applied once.
func (s *Service) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "body too large")
		return
	}
	now := s.now()
	if err := VerifySignature(body, r.Header.Get("Stripe-Signature"), s.cfg.WebhookSecret, now, SignatureTolerance); err != nil {
		slog.Warn("billing webhook refused", "err", err)
		writeError(w, http.StatusBadRequest, "invalid signature")
		return
	}
	var ev Event
	if err := json.Unmarshal(body, &ev); err != nil || ev.ID == "" {
		writeError(w, http.StatusBadRequest, "not a Stripe event")
		return
	}
	fresh, err := s.acc.ClaimStripeEvent(ev.ID, now)
	if err != nil {
		slog.Error("billing webhook", "event", ev.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "something went wrong")
		return
	}
	if !fresh {
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "duplicate": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.HandleEvent(ctx, ev); err != nil {
		_ = s.acc.ReleaseStripeEvent(ev.ID) // Stripe retries; handle it then
		slog.Error("billing webhook", "event", ev.ID, "type", ev.Type, "err", err)
		writeError(w, http.StatusInternalServerError, "something went wrong")
		return
	}
	// Stripe retries for three days; a month of ids is plenty.
	_ = s.acc.PruneStripeEvents(now.Add(-30 * 24 * time.Hour))
	writeJSON(w, http.StatusOK, map[string]any{"received": true})
}

// HandleEvent applies one Stripe event. Events about customers or
// subscriptions that aren't an org's (other products on the same Stripe
// account) are ignored.
func (s *Service) HandleEvent(ctx context.Context, ev Event) error {
	switch ev.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		var cs struct {
			Mode              string          `json:"mode"`
			ClientReferenceID string          `json:"client_reference_id"`
			Customer          json.RawMessage `json:"customer"`
			Subscription      json.RawMessage `json:"subscription"`
		}
		if err := json.Unmarshal(ev.Data.Object, &cs); err != nil {
			return err
		}
		subID := idOf(cs.Subscription)
		if cs.Mode != "subscription" || subID == "" {
			return nil
		}
		org, _ := strconv.ParseInt(cs.ClientReferenceID, 10, 64)
		if !s.ours(org, idOf(cs.Customer)) {
			return nil
		}
		sub, err := s.stripe.GetSubscription(ctx, subID) // its status now
		if err != nil {
			return err
		}
		return s.apply(ctx, org, sub, ev.Created)
	case "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted",
		"customer.subscription.paused", "customer.subscription.resumed", "customer.subscription.trial_will_end":
		var sub Subscription
		if err := json.Unmarshal(ev.Data.Object, &sub); err != nil {
			return err
		}
		org := sub.OrgID
		if org == 0 {
			org, _ = s.acc.OrgByStripeCustomer(sub.Customer)
		}
		if !s.ours(org, sub.Customer) {
			return nil
		}
		return s.apply(ctx, org, sub, ev.Created)
	}
	return nil // invoices and the rest: their subscription's events follow
}

// ours reports whether customer may be org's: org exists, and the customer
// is the one Dash made for it (or it has none on record).
func (s *Service) ours(org int64, customer string) bool {
	if org == 0 {
		return false
	}
	b, err := s.acc.OrgBilling(org)
	if err != nil {
		return false
	}
	if b.CustomerID != "" && customer != "" && b.CustomerID != customer {
		slog.Warn("billing: event for another customer than the org's", "org", org, "customer", customer)
		return false
	}
	if _, err := s.acc.OrgName(org); err != nil {
		return false
	}
	return true
}

var errStale = errors.New("stale")

// apply records sub as org's subscription, unless the event (made at
// created) is older than the last one applied, or it is about an old
// subscription while a newer one is paid for.
func (s *Service) apply(ctx context.Context, org int64, sub Subscription, created int64) error {
	_, err := s.acc.UpdateOrgBilling(org, func(b *store.OrgBilling) error {
		if b.SubscriptionID != "" && b.SubscriptionID != sub.ID && live(b.Status) && !live(sub.Status) {
			return errStale // an old subscription ending
		}
		if b.SubscriptionID == sub.ID && created < b.EventAt {
			return errStale // out of order
		}
		if sub.Customer != "" {
			b.CustomerID = sub.Customer
		}
		b.SubscriptionID, b.Status, b.ItemID, b.Quantity = sub.ID, sub.Status, sub.ItemID, sub.Quantity
		b.PeriodEnd, b.TrialEnd = unix(sub.PeriodEnd), unix(sub.TrialEnd)
		b.CancelAtPeriodEnd = sub.CancelAtPeriodEnd
		b.TrialUsed = true // one trial per org
		b.EventAt = max(b.EventAt, created)
		return nil
	})
	if errors.Is(err, errStale) {
		return nil
	}
	if err != nil {
		return err
	}
	slog.Info("billing: subscription", "org", org, "status", sub.Status, "seats", sub.Quantity)
	if _, err := s.State(org); err != nil { // starts or ends the grace period
		return err
	}
	if s.OnChange != nil {
		s.OnChange(org)
	}
	if live(sub.Status) {
		// Members may have come or gone while the owner was in Checkout.
		if err := s.SyncSeats(ctx, org); err != nil {
			slog.Error("billing: update seats", "org", org, "err", err)
		}
	}
	return nil
}

func unix(s int64) time.Time {
	if s <= 0 {
		return time.Time{}
	}
	return time.Unix(s, 0).UTC()
}
