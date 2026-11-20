package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ---- Notification settings, summaries and their activity ----
//
// They live in the database only: read when a summary or a push goes out,
// and when the settings open.

func scanNotify(row scanner) (model.UserNotify, error) {
	var u model.UserNotify
	var raw string
	var next, through, sent int64
	if err := row.Scan(&u.UserID, &u.OrgID, &raw, &next, &through, &sent); err != nil {
		return u, err
	}
	u.Settings = model.DefaultNotifySettings()
	_ = json.Unmarshal([]byte(raw), &u.Settings)
	u.State = model.NotifyState{NextAt: unixOrZero(next), Through: unixOrZero(through), LastSent: unixOrZero(sent)}
	return u, nil
}

const notifyCols = `user_id, org_id, settings, next_at, through, last_sent`

// NotifySettings returns user's settings in org (the defaults if they
// haven't saved any), without webhooks that are gone, and the state of
// their summary.
func (s *Store) NotifySettings(user, org int64) (model.NotifySettings, model.NotifyState, error) {
	u, err := scanNotify(s.db.QueryRow("SELECT "+notifyCols+" FROM notify_settings WHERE user_id = ? AND org_id = ?", user, org))
	if errors.Is(err, sql.ErrNoRows) {
		return model.DefaultNotifySettings(), model.NotifyState{}, nil
	}
	if err != nil {
		return u.Settings, u.State, err
	}
	if u.Settings.Webhooks, err = s.liveWebhooks(user, org, u.Settings.Webhooks); err != nil {
		return u.Settings, u.State, err
	}
	return u.Settings, u.State, nil
}

// liveWebhooks keeps the ids of ids that are user's personal webhooks in org.
func (s *Store) liveWebhooks(user, org int64, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}
	hooks, err := s.ListPersonalWebhooks(user, org)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, id := range ids {
		if slices.ContainsFunc(hooks, func(w model.Webhook) bool { return w.ID == id }) {
			out = append(out, id)
		}
	}
	return out, nil
}

// SaveNotifySettings stores user's settings in org (normalized; the caller
// checked its projects and webhooks are theirs) and schedules their next
// summary after now. Up to when the last summary covered stays: the next
// one starts there.
func (s *Store) SaveNotifySettings(user, org int64, set model.NotifySettings, now time.Time) (model.NotifySettings, model.NotifyState, error) {
	if err := set.Normalize(); err != nil {
		return set, model.NotifyState{}, err
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return set, model.NotifyState{}, err
	}
	next := set.Next(now)
	if _, err := s.db.Exec(`INSERT INTO notify_settings (user_id, org_id, settings, next_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id, org_id) DO UPDATE SET settings = excluded.settings, next_at = excluded.next_at, updated_at = excluded.updated_at`,
		user, org, string(raw), zeroOrUnix(next), now.Unix()); err != nil {
		return set, model.NotifyState{}, err
	}
	return s.NotifySettings(user, org)
}

// TurnOffSummary turns user's summary in org off (the unsubscribe link),
// and reports whether it was on.
func (s *Store) TurnOffSummary(user, org int64, now time.Time) (bool, error) {
	set, _, err := s.NotifySettings(user, org)
	if err != nil {
		return false, err
	}
	was := set.On()
	set.Summary = model.SummaryOff
	if _, _, err := s.SaveNotifySettings(user, org, set, now); err != nil {
		return was, err
	}
	return was, nil
}

// SummariesDue lists the summaries due at now: those whose next time has
// come, settings included.
func (s *Store) SummariesDue(now time.Time) ([]model.UserNotify, error) {
	out, err := queryAll(s.db, scanNotify, "SELECT "+notifyCols+" FROM notify_settings WHERE next_at > 0 AND next_at <= ? ORDER BY next_at", now.Unix())
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Settings.Webhooks, err = s.liveWebhooks(out[i].UserID, out[i].OrgID, out[i].Settings.Webhooks); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ClaimSummary takes the summary of user in org that was due at due: it
// now covers up to through, and the next one goes out at next. It reports
// false when another run took it first (or the settings changed since),
// so a summary is sent once. Claiming before sending means a crash while
// sending loses that summary rather than sending it twice.
func (s *Store) ClaimSummary(user, org int64, due, through, next time.Time) (bool, error) {
	res, err := s.db.Exec("UPDATE notify_settings SET through = ?, next_at = ? WHERE user_id = ? AND org_id = ? AND next_at = ?",
		zeroOrUnix(through), zeroOrUnix(next), user, org, zeroOrUnix(due))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// MarkSummarySent notes that a summary of user in org went out at at.
func (s *Store) MarkSummarySent(user, org int64, at time.Time) error {
	_, err := s.db.Exec("UPDATE notify_settings SET last_sent = ? WHERE user_id = ? AND org_id = ?", at.Unix(), user, org)
	return err
}

// PushSettingsFor returns user's settings in org as a push needs them
// (which events, whose tasks): theirs, or the defaults if they haven't
// saved any.
func (s *Store) PushSettingsFor(user, org int64) (model.NotifySettings, error) {
	set := model.DefaultNotifySettings()
	var raw string
	err := s.db.QueryRow("SELECT settings FROM notify_settings WHERE user_id = ? AND org_id = ?", user, org).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return set, nil
	}
	if err != nil {
		return set, err
	}
	if err := json.Unmarshal([]byte(raw), &set); err != nil {
		return model.DefaultNotifySettings(), nil
	}
	return set, nil
}

// IsOrgMember reports whether user is a member of org.
func (s *Store) IsOrgMember(user, org int64) (bool, error) {
	var one int
	err := s.db.QueryRow("SELECT 1 FROM org_memberships WHERE user_id = ? AND org_id = ?", user, org).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// dropSummaryWebhook removes webhook id from its owner's summary
// destinations (it was deleted).
func dropSummaryWebhook(tx *sql.Tx, user, org int64, id string) error {
	var raw string
	err := tx.QueryRow("SELECT settings FROM notify_settings WHERE user_id = ? AND org_id = ?", user, org).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	set := model.DefaultNotifySettings()
	if err := json.Unmarshal([]byte(raw), &set); err != nil {
		return nil
	}
	if !slices.Contains(set.Webhooks, id) {
		return nil
	}
	set.Webhooks = slices.DeleteFunc(set.Webhooks, func(x string) bool { return x == id })
	b, err := json.Marshal(set)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE notify_settings SET settings = ? WHERE user_id = ? AND org_id = ?", string(b), user, org)
	return err
}

// RecordActivity keeps a moment the summaries may tell about.
func (s *Store) RecordActivity(a model.Activity) error {
	if a.At.IsZero() {
		a.At = time.Now()
	}
	_, err := s.db.Exec("INSERT INTO notify_activity (project_id, at, kind, ref, title, url, severity, user_id, creator) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		a.ProjectID, a.At.Unix(), a.Kind, a.Ref, clip(a.Title, 300), clip(a.URL, 2000), string(a.Severity), a.User, a.Creator)
	return err
}

// ListActivity returns what happened in the projects in (from, to], oldest
// first.
func (s *Store) ListActivity(projects []string, from, to time.Time) ([]model.Activity, error) {
	out := []model.Activity{}
	if len(projects) == 0 {
		return out, nil
	}
	rows, err := s.db.Query("SELECT id, project_id, at, kind, ref, title, url, severity, user_id, creator FROM notify_activity WHERE at > ? AND at <= ? ORDER BY at, id",
		from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a model.Activity
		var at int64
		var sev string
		if err := rows.Scan(&a.ID, &a.ProjectID, &at, &a.Kind, &a.Ref, &a.Title, &a.URL, &sev, &a.User, &a.Creator); err != nil {
			return nil, err
		}
		if !slices.Contains(projects, a.ProjectID) {
			continue
		}
		a.At, a.Severity = time.Unix(at, 0).UTC(), model.Severity(sev)
		out = append(out, a)
	}
	return out, rows.Err()
}

// PruneActivity forgets activity from before before.
func (s *Store) PruneActivity(before time.Time) error {
	_, err := s.db.Exec("DELETE FROM notify_activity WHERE at < ?", before.Unix())
	return err
}

// NotifyKey returns the key that signs unsubscribe links, made with gen
// and saved the first time.
func (s *Store) NotifyKey(gen func() (string, error)) (string, error) {
	var key string
	err := s.db.QueryRow("SELECT key FROM notify_key WHERE id = 1").Scan(&key)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if key, err = gen(); err != nil {
		return "", err
	}
	if _, err := s.db.Exec("INSERT INTO notify_key (id, key, created_at) VALUES (1, ?, ?) ON CONFLICT (id) DO NOTHING", key, time.Now().Unix()); err != nil {
		return "", err
	}
	err = s.db.QueryRow("SELECT key FROM notify_key WHERE id = 1").Scan(&key)
	return key, err
}

// ---- the org's view ----

// NotifySettings returns the user's settings in the org.
func (o *OrgStore) NotifySettings() (model.NotifySettings, model.NotifyState, error) {
	if o.user == 0 || o.org == 0 {
		return model.NotifySettings{}, model.NotifyState{}, ErrNotFound
	}
	return o.s.NotifySettings(o.user, o.org)
}

// SaveNotifySettings saves the user's settings in the org: its projects
// must be the org's, its webhooks the user's personal ones there.
func (o *OrgStore) SaveNotifySettings(set model.NotifySettings, now time.Time) (model.NotifySettings, model.NotifyState, error) {
	if o.user == 0 || o.org == 0 {
		return set, model.NotifyState{}, ErrNotFound
	}
	if err := set.Normalize(); err != nil {
		return set, model.NotifyState{}, err
	}
	for _, p := range set.Projects {
		if !o.ownsProject(p) {
			return set, model.NotifyState{}, ErrNotFound
		}
	}
	live, err := o.s.liveWebhooks(o.user, o.org, set.Webhooks)
	if err != nil {
		return set, model.NotifyState{}, err
	}
	if len(live) != len(set.Webhooks) {
		return set, model.NotifyState{}, ErrNotFound
	}
	return o.s.SaveNotifySettings(o.user, o.org, set, now)
}

// SummaryRecipient returns user's email address and org's name, and
// whether user is still a member of org who sees its tasks (a summary
// only covers the projects of orgs its user is in, and not a planner's).
func (s *Store) SummaryRecipient(user, org int64) (email, orgName string, member bool, err error) {
	acc := s.Accounts()
	m, err := acc.Membership(user, org)
	if errors.Is(err, ErrNotMember) || errors.Is(err, ErrNotFound) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	u, err := acc.User(user)
	if errors.Is(err, ErrNotFound) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	// A planner sees no tasks: no summary for them, and a task of theirs
	// is nobody's in particular.
	return u.Email, m.Org.Name, !m.Role.Planner(), nil
}

// OrgRole is user's role in org ("owner", "admin", "member" or
// "planner"), or "" if they aren't a member.
func (s *Store) OrgRole(user, org int64) (string, error) {
	var role string
	err := s.db.QueryRow("SELECT role FROM org_memberships WHERE user_id = ? AND org_id = ?", user, org).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return role, err
}
