package store

// Deleting an account and exporting what Dash keeps about one: the rights
// to erasure and of access (GDPR articles 15, 17 and 20). See
// docs/privacy.md.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

var (
	ErrEnvAccount = errors.New("this is the account DASH_USERNAME describes, which Dash makes again on every start: it can't be deleted")
	ErrLastAdmin  = errors.New("this is the only platform admin: make someone else one first")
	// ErrAccountInUse wraps what keeps an account from being deleted yet.
	ErrAccountInUse = errors.New("this account can't be deleted yet")
)

// DeletedAccount is what DeleteUser did.
type DeletedAccount struct {
	User User
	// Orgs are the orgs they were a member of that remain: their members
	// changed.
	Orgs []int64
	// DeletedOrgs are the orgs deleted with the account, which had no other
	// member (their personal workspace, say).
	DeletedOrgs []Org
	// Tokens are the tokens that were theirs, now gone: close what holds
	// one open (a runner's tunnel).
	Tokens []Token
}

// orgFate is one of a user's memberships, with what deleting them needs.
type orgFate struct {
	Org
	role                      Role
	members, owners, projects int
	billing                   string
	isDefault                 bool
}

// subscribed reports whether billing (org_billing.status) is a Stripe
// subscription that still bills.
func subscribed(status string) bool {
	switch status {
	case "", "canceled", "incomplete_expired":
		return false
	}
	return true
}

// DeleteUser deletes the account uid and everything that is only theirs:
// sessions, tokens, linked identities, push subscriptions, personal
// webhooks, recovery codes, their general and project chats, their project
// plans, and each org they are the only member of (their personal
// workspace). What they made in orgs that
// remain stays, without them: their tasks' and messages' author becomes
// model.DeletedUser, and the sign-in log keeps its entries about them
// without their id, address or details.
//
// It refuses (ErrAccountInUse, saying why) while they are the only owner
// of an org with other members, or the only member of one that still has
// projects or a subscription, or is where records without an org go. The
// env account (ErrEnvAccount) and the last platform admin (ErrLastAdmin)
// can't be deleted.
func (a *Accounts) DeleteUser(uid int64) (DeletedAccount, error) {
	d := DeletedAccount{}
	u, err := a.User(uid)
	if err != nil {
		return d, err
	}
	d.User = u
	if d.Tokens, err = a.Tokens(uid); err != nil {
		return d, err
	}
	// The records (tasks, chats) change beneath the record store: hold its
	// lock throughout and reload it after.
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	err = inTx(a.db, func(tx *sql.Tx) error {
		var env, admin bool
		if err := tx.QueryRow("SELECT env_managed, is_platform_admin FROM users WHERE id = ?", uid).Scan(&env, &admin); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if env {
			return ErrEnvAccount
		}
		if admin {
			var others int
			if err := tx.QueryRow("SELECT COUNT(*) FROM users WHERE is_platform_admin = 1 AND id != ?", uid).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return ErrLastAdmin
			}
		}
		fates, err := orgFates(tx, uid)
		if err != nil {
			return err
		}
		var why []string
		var gone []Org
		for _, f := range fates {
			name := fmt.Sprintf("%q", f.Name)
			if f.Personal {
				name = "your personal workspace"
			}
			switch {
			case f.members > 1 && f.role == RoleOwner && f.owners == 1:
				why = append(why, fmt.Sprintf("you are the only owner of %s, which has other members: make someone else an owner first", name))
			case f.members > 1:
				d.Orgs = append(d.Orgs, f.ID)
			case f.projects > 0:
				why = append(why, fmt.Sprintf("%s still has projects: delete them first (Project menu → Delete project)", name))
			case subscribed(f.billing):
				why = append(why, fmt.Sprintf("%s has a subscription: cancel it first", name))
			case f.isDefault:
				why = append(why, fmt.Sprintf("%s is where this Dash keeps records without an organization: ask a platform admin", name))
			default:
				gone = append(gone, f.Org)
			}
		}
		if len(why) > 0 {
			return fmt.Errorf("%w: %s", ErrAccountInUse, strings.Join(why, "; "))
		}
		for _, o := range gone {
			if err := deleteEmptyOrg(tx, o.ID); err != nil {
				return err
			}
		}
		d.DeletedOrgs = gone
		if err := forgetUser(tx, uid); err != nil {
			return err
		}
		_, err = tx.Exec("DELETE FROM users WHERE id = ?", uid) // the rest cascades
		return err
	})
	if err != nil {
		return DeletedAccount{}, err
	}
	data, err := load(a.db)
	if err != nil {
		return d, err
	}
	a.s.data = data
	a.s.indexOrgsLocked()
	return d, nil
}

// orgFates lists uid's memberships with what deleting uid would mean for
// each org.
func orgFates(tx *sql.Tx, uid int64) ([]orgFate, error) {
	rows, err := tx.Query(`SELECT o.id, o.name, o.slug, o.personal_user_id IS NOT NULL, m.role,
			(SELECT COUNT(*) FROM org_memberships x WHERE x.org_id = o.id),
			(SELECT COUNT(*) FROM org_memberships x WHERE x.org_id = o.id AND x.role = 'owner'),
			(SELECT COUNT(*) FROM projects p WHERE p.org_id = o.id),
			COALESCE((SELECT b.status FROM org_billing b WHERE b.org_id = o.id), ''),
			o.id = COALESCE((SELECT default_org_id FROM instance WHERE id = 1), 0)
		FROM org_memberships m JOIN organizations o ON o.id = m.org_id
		WHERE m.user_id = ? ORDER BY o.id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []orgFate
	for rows.Next() {
		var f orgFate
		if err := rows.Scan(&f.ID, &f.Name, &f.Slug, &f.Personal, &f.role, &f.members, &f.owners, &f.projects, &f.billing, &f.isDefault); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// deleteEmptyOrg deletes an org without projects, with the records and
// log entries it still has (a general chat, say). Its account rows
// cascade.
func deleteEmptyOrg(tx *sql.Tx, orgID int64) error {
	for _, t := range orgTables {
		if _, err := tx.Exec("DELETE FROM "+t+" WHERE org_id = ?", orgID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("DELETE FROM auth_events WHERE org_id = ?", orgID); err != nil {
		return err
	}
	_, err := tx.Exec("DELETE FROM organizations WHERE id = ?", orgID)
	return err
}

// forgetUser takes uid out of what stays after their account: records
// keep their content with model.DeletedUser as author, their own general
// and project chats go, and log entries lose their id and what identifies them.
func forgetUser(tx *sql.Tx, uid int64) error {
	for _, q := range []string{
		`UPDATE todos SET data = json_set(data, '$.created_by', ?2) WHERE json_extract(data, '$.created_by') = ?1`,
		`UPDATE goals SET data = json_set(data, '$.owner', ?2) WHERE json_extract(data, '$.owner') = ?1`,
		`UPDATE chat_messages SET data = json_set(data, '$.created_by', ?2) WHERE kind != 'main' AND json_extract(data, '$.created_by') = ?1`,
		`DELETE FROM chat_messages WHERE kind IN ('main', 'project') AND owner_id LIKE '%:' || ?1`,
		`UPDATE project_events SET user_id = 0 WHERE user_id = ?1`,
		// About them: who, the address and the details (an email address,
		// say) go; that it happened stays in the org's log.
		`UPDATE auth_events SET user_id = NULL, detail = '', ip = '' WHERE user_id = ?1`,
		// By them, about someone else: their address goes.
		`UPDATE auth_events SET actor_id = NULL, ip = '' WHERE actor_id = ?1`,
	} {
		if _, err := tx.Exec(q, uid, model.DeletedUser); err != nil {
			return err
		}
	}
	return nil
}

// PruneAuthEvents deletes sign-in log entries older than before.
func (a *Accounts) PruneAuthEvents(before time.Time) (int64, error) {
	res, err := a.db.Exec("DELETE FROM auth_events WHERE at < ?", before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- export ----

// AccountExport is everything Dash keeps about one account, for them to
// download (Account → Your data). Secrets (password and token hashes, the
// authenticator secret, push keys, webhook secrets) are left out: they
// say nothing about the person, and can't be used anywhere else.
type AccountExport struct {
	ExportedAt time.Time `json:"exported_at"`
	Account    struct {
		ID              int64     `json:"id"`
		Username        string    `json:"username"`
		Email           string    `json:"email,omitempty"`
		PlatformAdmin   bool      `json:"is_platform_admin"`
		TwoFactor       bool      `json:"two_factor"`
		HasPassword     bool      `json:"has_password"`
		Created         time.Time `json:"created"`
		PasswordChanged time.Time `json:"password_changed"`
	} `json:"account"`
	Organizations []ExportMembership `json:"organizations"`
	Identities    []ExportIdentity   `json:"linked_accounts"`
	Sessions      []ExportSession    `json:"sessions"`
	Tokens        []Token            `json:"tokens"`
	Push          []ExportPush       `json:"push_subscriptions"`
	Webhooks      []ExportWebhook    `json:"personal_webhooks"`
	Plans         []ExportPlan       `json:"project_plans"`
	AuthEvents    []AuthEvent        `json:"sign_in_log"`
	Tasks         []ExportTask       `json:"tasks"`
	Messages      []ExportMessage    `json:"chat_messages"`
}

type ExportMembership struct {
	ID     int64     `json:"id"`
	Name   string    `json:"name"`
	Slug   string    `json:"slug"`
	Role   Role      `json:"role"`
	Joined time.Time `json:"joined"`
}

type ExportIdentity struct {
	Identity
	Subject string `json:"subject"`
}

type ExportSession struct {
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"`
	Method   string    `json:"method"`
	OrgID    int64     `json:"org_id,omitempty"`
	SignedIn time.Time `json:"signed_in,omitzero"`
}

type ExportPush struct {
	OrgID     int64     `json:"org_id"`
	UserAgent string    `json:"user_agent,omitempty"`
	Created   time.Time `json:"created"`
	LastAt    time.Time `json:"last_delivery,omitzero"`
}

type ExportWebhook struct {
	OrgID   int64           `json:"org_id"`
	Name    string          `json:"name,omitempty"`
	URL     string          `json:"url"`
	Format  string          `json:"format"`
	Events  json.RawMessage `json:"events"`
	Created time.Time       `json:"created"`
}

// ExportPlan is one of the user's project plans, as the planner keeps it.
type ExportPlan struct {
	OrgID int64           `json:"org_id"`
	Plan  json.RawMessage `json:"plan"`
}

type ExportTask struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Title     string    `json:"title"`
	Prompt    string    `json:"prompt"`
	Status    string    `json:"status"`
	Created   time.Time `json:"created"`
}

type ExportMessage struct {
	// Conversation is "general" (their chat with Claude in OrgID), "task"
	// or "project" (Of is the task's or project's id).
	Conversation string     `json:"conversation"`
	Of           string     `json:"of,omitempty"`
	OrgID        int64      `json:"org_id,omitempty"`
	Content      string     `json:"content"`
	At           *time.Time `json:"at,omitempty"`
}

// ExportUser gathers what Dash keeps about uid.
func (a *Accounts) ExportUser(uid int64, now time.Time) (AccountExport, error) {
	var x AccountExport
	x.ExportedAt = now.UTC()
	acc := &x.Account
	var created, changed int64
	err := a.db.QueryRow(`SELECT id, username, COALESCE(email, ''), is_platform_admin, totp_secret IS NOT NULL, has_password, created_at, password_changed_at
		FROM users WHERE id = ?`, uid).Scan(&acc.ID, &acc.Username, &acc.Email, &acc.PlatformAdmin, &acc.TwoFactor, &acc.HasPassword, &created, &changed)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	acc.Created, acc.PasswordChanged = time.Unix(created, 0).UTC(), time.Unix(0, changed).UTC()

	each := func(q string, scan func(*sql.Rows) error) error {
		rows, err := a.db.Query(q, uid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	x.Organizations = []ExportMembership{}
	if err := each(`SELECT o.id, o.name, o.slug, m.role, m.created_at FROM org_memberships m JOIN organizations o ON o.id = m.org_id
		WHERE m.user_id = ? ORDER BY o.id`, func(r *sql.Rows) error {
		var m ExportMembership
		var at int64
		err := r.Scan(&m.ID, &m.Name, &m.Slug, &m.Role, &at)
		m.Joined = time.Unix(at, 0).UTC()
		x.Organizations = append(x.Organizations, m)
		return err
	}); err != nil {
		return x, err
	}
	x.Identities = []ExportIdentity{}
	if err := each(identityCols+" WHERE user_id = ?", func(r *sql.Rows) error {
		id, err := scanIdentity(r)
		x.Identities = append(x.Identities, ExportIdentity{Identity: id, Subject: id.Subject})
		return err
	}); err != nil {
		return x, err
	}
	x.Sessions = []ExportSession{}
	if err := each(`SELECT created_at, expires_at, method, COALESCE(active_org_id, 0), auth_at FROM sessions
		WHERE user_id = ? ORDER BY created_at`, func(r *sql.Rows) error {
		var s ExportSession
		var c, e, at int64
		err := r.Scan(&c, &e, &s.Method, &s.OrgID, &at)
		s.Created, s.Expires = time.Unix(c, 0).UTC(), time.Unix(e, 0).UTC()
		if at > 0 {
			s.SignedIn = time.Unix(at, 0).UTC()
		}
		x.Sessions = append(x.Sessions, s)
		return err
	}); err != nil {
		return x, err
	}
	if x.Tokens, err = a.Tokens(uid); err != nil {
		return x, err
	}
	x.Push = []ExportPush{}
	if err := each(`SELECT org_id, user_agent, created_at, COALESCE(last_at, 0) FROM push_subscriptions
		WHERE user_id = ? ORDER BY created_at`, func(r *sql.Rows) error {
		var p ExportPush
		var c, last int64
		err := r.Scan(&p.OrgID, &p.UserAgent, &c, &last)
		p.Created = time.Unix(c, 0).UTC()
		if last > 0 {
			p.LastAt = time.Unix(last, 0).UTC()
		}
		x.Push = append(x.Push, p)
		return err
	}); err != nil {
		return x, err
	}
	x.Webhooks = []ExportWebhook{}
	if err := each(`SELECT org_id, name, url, format, events, created_at FROM webhooks
		WHERE user_id = ? ORDER BY created_at`, func(r *sql.Rows) error {
		var w ExportWebhook
		var events string
		var c int64
		err := r.Scan(&w.OrgID, &w.Name, &w.URL, &w.Format, &events, &c)
		w.Events, w.Created = json.RawMessage(events), time.Unix(c, 0).UTC()
		if !json.Valid(w.Events) {
			w.Events = json.RawMessage("[]")
		}
		x.Webhooks = append(x.Webhooks, w)
		return err
	}); err != nil {
		return x, err
	}
	x.Plans = []ExportPlan{}
	if err := each(`SELECT org_id, data FROM plans WHERE user_id = ? ORDER BY updated_at`, func(r *sql.Rows) error {
		var p ExportPlan
		var data string
		err := r.Scan(&p.OrgID, &data)
		p.Plan = json.RawMessage(data)
		if !json.Valid(p.Plan) {
			p.Plan = json.RawMessage("{}")
		}
		x.Plans = append(x.Plans, p)
		return err
	}); err != nil {
		return x, err
	}
	x.AuthEvents = []AuthEvent{}
	if err := each(`SELECT id, at, COALESCE(org_id, 0), COALESCE(user_id, 0), COALESCE(actor_id, 0), kind, detail, ip FROM auth_events
		WHERE user_id = ?1 OR actor_id = ?1 ORDER BY id`, func(r *sql.Rows) error {
		var e AuthEvent
		var at int64
		err := r.Scan(&e.ID, &at, &e.OrgID, &e.UserID, &e.Actor, &e.Kind, &e.Detail, &e.IP)
		e.At = time.Unix(at, 0).UTC()
		x.AuthEvents = append(x.AuthEvents, e)
		return err
	}); err != nil {
		return x, err
	}
	x.Tasks, x.Messages = a.s.authoredBy(uid)
	return x, nil
}

// authoredBy is the tasks uid created and the chat messages they wrote.
func (s *Store) authoredBy(uid int64) ([]ExportTask, []ExportMessage) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tasks := []ExportTask{}
	for _, t := range s.data.Todos {
		if t.CreatedBy == uid {
			tasks = append(tasks, ExportTask{ID: t.ID, ProjectID: t.ProjectID, Title: t.Title, Prompt: t.Prompt, Status: string(t.Status), Created: t.CreatedAt.UTC()})
		}
	}
	msgs := []ExportMessage{}
	add := func(conv, of string, org int64, chat []model.ChatMessage) {
		for _, m := range chat {
			if m.Role == "user" && m.CreatedBy == uid {
				msgs = append(msgs, ExportMessage{Conversation: conv, Of: of, OrgID: org, Content: m.Content, At: m.At})
			}
		}
	}
	for owner, chat := range s.data.MainChats {
		var org, user int64
		if _, err := fmt.Sscanf(owner, "%d:%d", &org, &user); err != nil || user != uid {
			continue
		}
		for _, m := range chat {
			if m.Role == "user" {
				msgs = append(msgs, ExportMessage{Conversation: "general", OrgID: org, Content: m.Content, At: m.At})
			}
		}
	}
	for id, chat := range s.data.TodoChats {
		add("task", id, 0, chat)
	}
	for owner, chat := range s.data.ProjectChats {
		// Their own chats, as for the general chat: what they asked.
		id, user, ok := strings.Cut(owner, ":")
		if !ok || user != strconv.FormatInt(uid, 10) {
			continue
		}
		for _, m := range chat {
			if m.Role == "user" {
				msgs = append(msgs, ExportMessage{Conversation: "project", Of: id, OrgID: s.projectOrgLocked(id), Content: m.Content, At: m.At})
			}
		}
	}
	for id, chat := range s.data.GoalChats {
		add("goal", id, 0, chat)
	}
	slices.SortFunc(tasks, func(a, b ExportTask) int { return a.Created.Compare(b.Created) })
	slices.SortStableFunc(msgs, func(a, b ExportMessage) int {
		if c := strings.Compare(a.Conversation+"\x00"+a.Of, b.Conversation+"\x00"+b.Of); c != 0 || a.At == nil || b.At == nil {
			return c
		}
		return a.At.Compare(*b.At)
	})
	return tasks, msgs
}
