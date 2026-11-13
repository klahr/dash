package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ---- Web Push ----
//
// The hub's VAPID key and the browsers subscribed to pushes live in the
// database only: they're read when an event goes out.

// PushVAPIDKey returns the hub's VAPID private key, made with gen and saved
// the first time.
func (s *Store) PushVAPIDKey(gen func() (string, error)) (string, error) {
	var key string
	err := s.db.QueryRow("SELECT private_key FROM push_vapid WHERE id = 1").Scan(&key)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if key, err = gen(); err != nil {
		return "", err
	}
	// Another process may have won the race: keep whichever is stored.
	if _, err := s.db.Exec("INSERT INTO push_vapid (id, private_key, created_at) VALUES (1, ?, ?) ON CONFLICT (id) DO NOTHING",
		key, time.Now().Unix()); err != nil {
		return "", err
	}
	err = s.db.QueryRow("SELECT private_key FROM push_vapid WHERE id = 1").Scan(&key)
	return key, err
}

const pushCols = `id, user_id, org_id, endpoint, p256dh, auth, user_agent, created_at, last_at, last_error`

func scanPush(row interface{ Scan(...any) error }) (model.PushSubscription, error) {
	var p model.PushSubscription
	var created int64
	var last sql.NullInt64
	if err := row.Scan(&p.ID, &p.UserID, &p.OrgID, &p.Endpoint, &p.P256dh, &p.Auth, &p.UserAgent, &created, &last, &p.LastError); err != nil {
		return p, err
	}
	p.CreatedAt = time.Unix(created, 0).UTC()
	if last.Valid {
		p.LastAt = time.Unix(last.Int64, 0).UTC()
	}
	return p, nil
}

func (s *Store) queryPush(q string, args ...any) ([]model.PushSubscription, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PushSubscription{}
	for rows.Next() {
		p, err := scanPush(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SavePushSubscription subscribes a browser (p.Endpoint) for p.UserID in
// p.OrgID. A browser already subscribed, by anyone, is taken over: its
// keys, user and org are replaced and its delivery status cleared.
func (s *Store) SavePushSubscription(p model.PushSubscription) (model.PushSubscription, error) {
	now := time.Now().UTC().Truncate(time.Second)
	if len(p.UserAgent) > 300 {
		p.UserAgent = p.UserAgent[:300]
	}
	_, err := s.db.Exec(`INSERT INTO push_subscriptions (id, user_id, org_id, endpoint, p256dh, auth, user_agent, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (endpoint) DO UPDATE SET user_id = excluded.user_id, org_id = excluded.org_id, p256dh = excluded.p256dh,
			auth = excluded.auth, user_agent = excluded.user_agent, created_at = excluded.created_at, last_at = NULL, last_error = ''`,
		NewID(), p.UserID, p.OrgID, p.Endpoint, p.P256dh, p.Auth, p.UserAgent, now.Unix())
	if err != nil {
		return p, err
	}
	return scanPush(s.db.QueryRow("SELECT "+pushCols+" FROM push_subscriptions WHERE endpoint = ?", p.Endpoint))
}

// PushSubscription returns user's subscription of the browser at endpoint.
func (s *Store) PushSubscription(user int64, endpoint string) (model.PushSubscription, error) {
	p, err := scanPush(s.db.QueryRow("SELECT "+pushCols+" FROM push_subscriptions WHERE endpoint = ? AND user_id = ?", endpoint, user))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// PushSubscriptionsForOrg returns the subscriptions made in org by its
// members (someone who left the org keeps theirs, unused, until they
// rejoin or unsubscribe).
func (s *Store) PushSubscriptionsForOrg(org int64) ([]model.PushSubscription, error) {
	return s.queryPush(`SELECT `+pushCols+` FROM push_subscriptions p
		WHERE p.org_id = ? AND EXISTS (SELECT 1 FROM org_memberships m WHERE m.user_id = p.user_id AND m.org_id = p.org_id)
		ORDER BY p.created_at, p.id`, org)
}

// DeleteUserPushSubscription unsubscribes user's browser at endpoint.
func (s *Store) DeleteUserPushSubscription(user int64, endpoint string) error {
	res, err := s.db.Exec("DELETE FROM push_subscriptions WHERE endpoint = ? AND user_id = ?", endpoint, user)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePushSubscription removes a subscription (the browser dropped it).
// A missing one is no error.
func (s *Store) DeletePushSubscription(id string) error {
	_, err := s.db.Exec("DELETE FROM push_subscriptions WHERE id = ?", id)
	return err
}

// RecordPushDelivery notes how the subscription's latest push went (errMsg
// "" = it went through). A deleted subscription is no error.
func (s *Store) RecordPushDelivery(id string, at time.Time, errMsg string) error {
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	_, err := s.db.Exec("UPDATE push_subscriptions SET last_at = ?, last_error = ? WHERE id = ?", at.Unix(), errMsg, id)
	return err
}

// The account side, for the API (it reaches the store through Accounts).

func (a *Accounts) SavePushSubscription(p model.PushSubscription) (model.PushSubscription, error) {
	return a.s.SavePushSubscription(p)
}

func (a *Accounts) PushSubscription(user int64, endpoint string) (model.PushSubscription, error) {
	return a.s.PushSubscription(user, endpoint)
}

func (a *Accounts) DeleteUserPushSubscription(user int64, endpoint string) error {
	return a.s.DeleteUserPushSubscription(user, endpoint)
}
