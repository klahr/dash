package store

import (
	"database/sql"
	"errors"
	"time"
)

// OrgBilling is an org's Stripe customer and subscription as Dash last heard
// of them (see migrations/0007_billing.sql). The zero value, with OrgID set,
// is an org that has never been through billing.
type OrgBilling struct {
	OrgID             int64
	CustomerID        string
	SubscriptionID    string
	ItemID            string // the subscription's item for the per-seat price
	Status            string // Stripe's subscription status; "" = none yet
	Quantity          int    // seats Stripe bills for
	PeriodEnd         time.Time
	TrialEnd          time.Time
	CancelAtPeriodEnd bool
	TrialUsed         bool
	UnpaidSince       time.Time // zero: paid, or nothing to pay
	EventAt           int64     // created time of the last subscription event applied
}

const orgBillingCols = `SELECT org_id, customer_id, subscription_id, subscription_item_id, status, quantity,
	period_end, trial_end, cancel_at_period_end, trial_used, unpaid_since, event_at FROM org_billing`

func unixOrZero(s int64) time.Time {
	if s <= 0 {
		return time.Time{}
	}
	return time.Unix(s, 0).UTC()
}

func zeroOrUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func orgBilling(q querier, org int64) (OrgBilling, error) {
	b := OrgBilling{OrgID: org}
	var period, trial, unpaid int64
	err := q.QueryRow(orgBillingCols+" WHERE org_id = ?", org).Scan(&b.OrgID, &b.CustomerID, &b.SubscriptionID, &b.ItemID,
		&b.Status, &b.Quantity, &period, &trial, &b.CancelAtPeriodEnd, &b.TrialUsed, &unpaid, &b.EventAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OrgBilling{OrgID: org}, nil
	}
	if err != nil {
		return b, err
	}
	b.PeriodEnd, b.TrialEnd, b.UnpaidSince = unixOrZero(period), unixOrZero(trial), unixOrZero(unpaid)
	return b, nil
}

// OrgBilling is org's billing row, or the zero value for an org that has
// none.
func (a *Accounts) OrgBilling(org int64) (OrgBilling, error) { return orgBilling(a.db, org) }

// UpdateOrgBilling changes org's billing row with fn, creating it if need
// be, and returns the result. fn returning an error leaves it unchanged.
func (a *Accounts) UpdateOrgBilling(org int64, fn func(*OrgBilling) error) (OrgBilling, error) {
	var b OrgBilling
	err := inTx(a.db, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRow("SELECT EXISTS (SELECT 1 FROM organizations WHERE id = ?)", org).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		cur, err := orgBilling(tx, org)
		if err != nil {
			return err
		}
		b = cur
		if err := fn(&b); err != nil {
			return err
		}
		b.OrgID = org
		_, err = tx.Exec(`INSERT INTO org_billing (org_id, customer_id, subscription_id, subscription_item_id, status, quantity,
				period_end, trial_end, cancel_at_period_end, trial_used, unpaid_since, event_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (org_id) DO UPDATE SET customer_id = excluded.customer_id, subscription_id = excluded.subscription_id,
				subscription_item_id = excluded.subscription_item_id, status = excluded.status, quantity = excluded.quantity,
				period_end = excluded.period_end, trial_end = excluded.trial_end, cancel_at_period_end = excluded.cancel_at_period_end,
				trial_used = excluded.trial_used, unpaid_since = excluded.unpaid_since, event_at = excluded.event_at,
				updated_at = excluded.updated_at`,
			org, b.CustomerID, b.SubscriptionID, b.ItemID, b.Status, b.Quantity, zeroOrUnix(b.PeriodEnd), zeroOrUnix(b.TrialEnd),
			b.CancelAtPeriodEnd, b.TrialUsed, zeroOrUnix(b.UnpaidSince), b.EventAt, time.Now().Unix())
		return err
	})
	return b, err
}

// OrgByStripeCustomer is the org whose Stripe customer is customer.
func (a *Accounts) OrgByStripeCustomer(customer string) (int64, error) {
	if customer == "" {
		return 0, ErrNotFound
	}
	var org int64
	err := a.db.QueryRow("SELECT org_id FROM org_billing WHERE customer_id = ?", customer).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return org, err
}

// ClaimStripeEvent records a webhook event as handled and reports whether
// it is new. A handler that fails afterwards calls ReleaseStripeEvent, so
// Stripe's retry is handled again.
func (a *Accounts) ClaimStripeEvent(id string, now time.Time) (bool, error) {
	res, err := a.db.Exec("INSERT INTO stripe_events (id, received_at) VALUES (?, ?) ON CONFLICT (id) DO NOTHING", id, now.Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReleaseStripeEvent forgets a claimed event (see ClaimStripeEvent).
func (a *Accounts) ReleaseStripeEvent(id string) error {
	_, err := a.db.Exec("DELETE FROM stripe_events WHERE id = ?", id)
	return err
}

// PruneStripeEvents forgets events received before cutoff: Stripe stops
// retrying an event after three days.
func (a *Accounts) PruneStripeEvents(cutoff time.Time) error {
	_, err := a.db.Exec("DELETE FROM stripe_events WHERE received_at < ?", cutoff.Unix())
	return err
}

// OrgOwnerEmail is the email address of one of org's owners (the earliest
// to join that has one), for its Stripe customer; "" if none has one.
func (a *Accounts) OrgOwnerEmail(org int64) (string, error) {
	var email string
	err := a.db.QueryRow(`SELECT COALESCE(u.email, '') FROM org_memberships m JOIN users u ON u.id = m.user_id
		WHERE m.org_id = ? AND m.role = 'owner' AND u.email IS NOT NULL ORDER BY m.created_at, u.id LIMIT 1`, org).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return email, err
}

// OrgName is org's name.
func (a *Accounts) OrgName(org int64) (string, error) {
	var name string
	err := a.db.QueryRow("SELECT name FROM organizations WHERE id = ?", org).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}
