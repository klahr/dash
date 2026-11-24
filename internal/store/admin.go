package store

// Managing accounts for someone else: platform admins for every account and
// org on the hub (the Admin sheet), and org owners and admins for their
// members (Organization → Members). Callers check who may; the rule for
// org admins is MemberAccess.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrAccountDisabled: a platform admin disabled the account.
	ErrAccountDisabled = errors.New("this account is disabled: ask an administrator of this Dash")
	// ErrEnvPassword: the env account's password comes from DASH_PASSWORD.
	ErrEnvPassword = errors.New("this account's password comes from DASH_PASSWORD on the server: change it there")
	// ErrSSOOnly: the account signs in only with Google and has no password to reset.
	ErrSSOOnly = errors.New("this account signs in only with Google and has no password to reset")
	// ErrAlreadyMember: the account is in the org already.
	ErrAlreadyMember = errors.New("already a member of that organization")
	// ErrOrgInUse wraps what keeps an org from being deleted yet.
	ErrOrgInUse = errors.New("this organization can't be deleted yet")
)

// enabled is an SQL condition: the account whose id is the expression uid
// isn't disabled.
func enabled(uid string) string {
	return "NOT EXISTS (SELECT 1 FROM user_status ds WHERE ds.user_id = " + uid + " AND ds.disabled_at IS NOT NULL)"
}

// forbidden is a refusal that says why; errors.Is matches ErrForbidden.
type forbidden string

func (e forbidden) Error() string        { return string(e) }
func (e forbidden) Is(target error) bool { return target == ErrForbidden }

// Why an org admin may not manage a member's account (MemberAccess).
const (
	WhySelf          = forbidden("that's you: use Account for your own account")
	WhyPlatformAdmin = forbidden("a platform admin of this Dash: ask another platform admin")
	WhyOtherOrg      = forbidden("also a member of another organization: ask a platform admin")
	WhyOwner         = forbidden("an owner: only owners can do this for an owner")
	WhyNotAdmin      = forbidden("only owners and admins manage members' accounts")
)

// AdminUser is an account as platform admins see it.
type AdminUser struct {
	User
	Orgs        []Membership `json:"orgs"`
	HasPassword bool         `json:"has_password"`
	SSO         bool         `json:"sso"` // a linked Google (or other provider) sign-in
	Disabled    bool         `json:"disabled"`
	Unverified  bool         `json:"unverified,omitempty"` // signed up, link not opened yet
	EnvAccount  bool         `json:"env_account,omitempty"`
	Created     time.Time    `json:"created"`
	LastLogin   time.Time    `json:"last_login,omitzero"`
}

const adminUserCols = `SELECT ` + userFields + `, has_password,
	EXISTS (SELECT 1 FROM user_identities i WHERE i.user_id = users.id),
	EXISTS (SELECT 1 FROM user_status ds WHERE ds.user_id = users.id AND ds.disabled_at IS NOT NULL), unverified, env_managed, created_at,
	COALESCE((SELECT st.last_login_at FROM user_status st WHERE st.user_id = users.id), 0) FROM users`

func scanAdminUser(sc scanner) (AdminUser, error) {
	var u AdminUser
	var created, last int64
	var err error
	u.User, err = scanUser(sc, &u.HasPassword, &u.SSO, &u.Disabled, &u.Unverified, &u.EnvAccount, &created, &last)
	u.Created = time.Unix(created, 0).UTC()
	if last > 0 {
		u.LastLogin = time.Unix(last, 0).UTC()
	}
	u.Orgs = []Membership{}
	return u, err
}

// AdminUserLimit is the most accounts AdminUsers returns.
const AdminUserLimit = 500

// likePattern is a LIKE pattern matching s anywhere, with '\' as escape.
func likePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// AdminUsers lists the accounts whose username or email contains query
// (all for ""), by username, at most AdminUserLimit, with their orgs. total
// is how many accounts the hub has, matched how many match.
func (a *Accounts) AdminUsers(query string) (users []AdminUser, total, matched int, err error) {
	query = strings.TrimSpace(query)
	if err = a.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&total); err != nil {
		return nil, 0, 0, err
	}
	where := ` WHERE ?1 = '' OR username LIKE ?2 ESCAPE '\' OR email LIKE ?2 ESCAPE '\'`
	if err = a.db.QueryRow("SELECT COUNT(*) FROM users"+where, query, likePattern(query)).Scan(&matched); err != nil {
		return nil, 0, 0, err
	}
	users, err = queryAll(a.db, scanAdminUser, adminUserCols+where+" ORDER BY username COLLATE NOCASE LIMIT ?3", query, likePattern(query), AdminUserLimit)
	if err != nil {
		return nil, 0, 0, err
	}
	if err = a.attachOrgs(users); err != nil {
		return nil, 0, 0, err
	}
	return users, total, matched, nil
}

// AdminUser is the account uid as platform admins see it.
func (a *Accounts) AdminUser(uid int64) (AdminUser, error) {
	u, err := scanAdminUser(a.db.QueryRow(adminUserCols+" WHERE id = ?", uid))
	if err != nil {
		return u, err
	}
	us := []AdminUser{u}
	if err := a.attachOrgs(us); err != nil {
		return u, err
	}
	return us[0], nil
}

func (a *Accounts) attachOrgs(users []AdminUser) error {
	if len(users) == 0 {
		return nil
	}
	at := make(map[int64]int, len(users))
	for i, u := range users {
		at[u.ID] = i
	}
	// Every membership: fewer round trips than one query per account.
	byUser, err := groupBy(a.db, func(sc scanner) (uid int64, m Membership, err error) {
		m.Org, err = scanOrg(sc, &m.Role, &uid)
		return uid, m, err
	}, `SELECT `+orgFields+`, m.role, m.user_id FROM org_memberships m
		JOIN organizations o ON o.id = m.org_id ORDER BY m.created_at, o.id`)
	if err != nil {
		return err
	}
	for uid, ms := range byUser {
		if i, ok := at[uid]; ok {
			users[i].Orgs = append(users[i].Orgs, ms...)
		}
	}
	return nil
}

// AdminOrg is an org as platform admins see it.
type AdminOrg struct {
	Org
	Members  int       `json:"member_count"`
	Projects int       `json:"project_count"`
	Created  time.Time `json:"created"`
	// Default: records without an org go here; it can't be deleted.
	Default bool `json:"default,omitempty"`
	// Subscribed: it has a subscription that still bills.
	Subscribed bool `json:"subscribed,omitempty"`
}

const adminOrgCols = `SELECT ` + orgFields + `, o.created_at,
	(SELECT COUNT(*) FROM org_memberships x WHERE x.org_id = o.id),
	(SELECT COUNT(*) FROM projects p WHERE p.org_id = o.id),
	o.id = COALESCE((SELECT default_org_id FROM instance WHERE id = 1), 0),
	COALESCE((SELECT b.status FROM org_billing b WHERE b.org_id = o.id), '')
	FROM organizations o`

func scanAdminOrg(sc scanner) (AdminOrg, error) {
	var o AdminOrg
	var created int64
	var billing string
	var err error
	o.Org, err = scanOrg(sc, &created, &o.Members, &o.Projects, &o.Default, &billing)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	o.Created = time.Unix(created, 0).UTC()
	o.Subscribed = subscribed(billing)
	return o, err
}

// AdminOrgs lists the orgs whose name or slug contains query (all for ""),
// by name.
func (a *Accounts) AdminOrgs(query string) ([]AdminOrg, error) {
	query = strings.TrimSpace(query)
	return queryAll(a.db, scanAdminOrg, adminOrgCols+` WHERE ?1 = '' OR o.name LIKE ?2 ESCAPE '\' OR o.slug LIKE ?2 ESCAPE '\'
		ORDER BY o.name COLLATE NOCASE, o.id`, query, likePattern(query))
}

// AdminOrg is the org id as platform admins see it.
func (a *Accounts) AdminOrg(id int64) (AdminOrg, error) {
	return scanAdminOrg(a.db.QueryRow(adminOrgCols+" WHERE o.id = ?", id))
}

// ---- what an org admin may do ----

type orgRole struct {
	org  int64
	role Role
}

// rolesOf is each of uids' memberships.
func (a *Accounts) rolesOf(uids ...int64) (map[int64][]orgRole, error) {
	out := map[int64][]orgRole{}
	if len(uids) == 0 {
		return out, nil
	}
	args := make([]any, len(uids))
	for i, id := range uids {
		args[i] = id
	}
	return groupBy(a.db, func(sc scanner) (uid int64, r orgRole, err error) {
		err = sc.Scan(&uid, &r.org, &r.role)
		return uid, r, err
	}, "SELECT user_id, org_id, role FROM org_memberships WHERE user_id IN (?"+strings.Repeat(", ?", len(uids)-1)+")", args...)
}

// manageWhy is why actor (with roles mine, platform admin or not) may not
// manage target's account with its memberships theirs; nil if they may.
func manageWhy(actor int64, platform bool, mine []orgRole, target User, theirs []orgRole) error {
	if target.ID == actor {
		return WhySelf
	}
	if platform {
		return nil
	}
	if target.PlatformAdmin {
		return WhyPlatformAdmin
	}
	roles := map[int64]Role{}
	for _, r := range mine {
		roles[r.org] = r.role
	}
	var owner bool
	for _, r := range theirs {
		my, ok := roles[r.org]
		if !ok || !my.AtLeast(RoleAdmin) {
			return WhyOtherOrg
		}
		if r.role == RoleOwner && my != RoleOwner {
			owner = true
		}
	}
	if owner {
		return WhyOwner
	}
	return nil
}

// MemberAccess says whether actor, an owner or admin of orgID (a platform
// admin if platform), may manage the account of target, a member of
// orgID: reset its password, turn off its two-factor sign-in, sign it out
// everywhere. It's nil when they may, or a refusal saying why (errors.Is
// ErrForbidden). An org admin may only for an account whose every
// membership is in an org where they are an owner or admin, or else an
// admin of one org could take over an account that's also in another;
// never for a platform admin or themselves; and only owners for an owner
// (in any of those orgs). Platform admins may for anyone but themselves.
func (a *Accounts) MemberAccess(actor int64, platform bool, orgID, target int64) error {
	mine, err := membership(a.db, actor, orgID)
	if err != nil {
		return err
	}
	if !mine.Role.AtLeast(RoleAdmin) && !platform {
		return WhyNotAdmin
	}
	if _, err := membership(a.db, target, orgID); err != nil {
		return err
	}
	u, err := a.User(target)
	if err != nil {
		return err
	}
	roles, err := a.rolesOf(actor, target)
	if err != nil {
		return err
	}
	return manageWhy(actor, platform, roles[actor], u, roles[target])
}

// MemberAccount is what an org admin may do with a member's account.
type MemberAccount struct {
	Can bool   `json:"can"`
	Why string `json:"why,omitempty"` // why not
	// HasPassword: false for an account that signs in only with Google,
	// which has no password to reset.
	HasPassword bool `json:"has_password"`
}

// MemberAccessAll is MemberAccess for each of members (of one org), by
// user id, with whether each has a password.
func (a *Accounts) MemberAccessAll(actor int64, platform bool, members []Member) (map[int64]MemberAccount, error) {
	uids := []int64{actor}
	for _, m := range members {
		uids = append(uids, m.ID)
	}
	roles, err := a.rolesOf(uids...)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]MemberAccount, len(members))
	for _, m := range members {
		has, err := a.HasPassword(m.ID)
		if err != nil {
			return nil, err
		}
		acc := MemberAccount{Can: true, HasPassword: has}
		if err := manageWhy(actor, platform, roles[actor], m.User, roles[m.ID]); err != nil {
			acc.Can, acc.Why = false, err.Error()
		}
		out[m.ID] = acc
	}
	return out, nil
}

// ---- actions ----

// Disabled reports whether uid's account is disabled.
func (a *Accounts) Disabled(uid int64) (bool, error) {
	var d bool
	err := a.db.QueryRow("SELECT NOT "+enabled("users.id")+" FROM users WHERE id = ?", uid).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return d, err
}

// lastAdmin reports whether uid is a platform admin and no other enabled
// account is one.
func lastAdmin(tx *sql.Tx, uid int64) (bool, error) {
	var admin bool
	var others int
	err := tx.QueryRow(`SELECT is_platform_admin, (SELECT COUNT(*) FROM users o WHERE o.is_platform_admin = 1 AND `+enabled("o.id")+` AND o.id != ?1)
		FROM users WHERE id = ?1`, uid).Scan(&admin, &others)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return admin && others == 0, err
}

// SetDisabled disables uid's account (on) or enables it again. Disabling
// ends its sessions (returning how many) and stops its tokens and runners
// working until it's enabled again; it can't sign in. The last platform
// admin can't be disabled (ErrLastAdmin).
func (a *Accounts) SetDisabled(uid int64, on bool, now time.Time) (int, error) {
	var n int64
	err := inTx(a.db, func(tx *sql.Tx) error {
		if _, err := scanUser(tx.QueryRow(userCols+" WHERE id = ?", uid)); err != nil {
			return err
		}
		if !on {
			_, err := tx.Exec("UPDATE user_status SET disabled_at = NULL WHERE user_id = ?", uid)
			return err
		}
		last, err := lastAdmin(tx, uid)
		if err != nil {
			return err
		}
		if last {
			return ErrLastAdmin
		}
		if _, err := tx.Exec(`INSERT INTO user_status (user_id, disabled_at) VALUES (?1, ?2)
			ON CONFLICT (user_id) DO UPDATE SET disabled_at = COALESCE(disabled_at, ?2)`, uid, now.Unix()); err != nil {
			return err
		}
		res, err := tx.Exec("DELETE FROM sessions WHERE user_id = ?", uid)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return int(n), err
}

// SetPlatformAdmin makes uid a platform admin, or not. The last (enabled)
// platform admin stays one (ErrLastAdmin).
func (a *Accounts) SetPlatformAdmin(uid int64, on bool) error {
	return inTx(a.db, func(tx *sql.Tx) error {
		if !on {
			last, err := lastAdmin(tx, uid)
			if err != nil {
				return err
			}
			if last {
				return ErrLastAdmin
			}
		}
		return execOne(tx, ErrNotFound, "UPDATE users SET is_platform_admin = ? WHERE id = ?", on, uid)
	})
}

// SignOutEverywhere ends all of uid's sessions and deletes their API tokens
// and connected apps (OAuth grants), returning how many sessions ended and
// the tokens deleted. Their runners keep their tokens, as after a password
// change.
func (a *Accounts) SignOutEverywhere(uid int64) (int, []Token, error) {
	var n int64
	var toks []Token
	err := inTx(a.db, func(tx *sql.Tx) error {
		var err error
		if toks, err = queryAll(tx, scanToken, tokenCols+" WHERE t.user_id = ? AND t.kind IN ('api', 'oauth')", uid); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM tokens WHERE user_id = ? AND kind IN ('api', 'oauth')", uid); err != nil {
			return err
		}
		res, err := tx.Exec("DELETE FROM sessions WHERE user_id = ?", uid)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return int(n), toks, err
}

// SignOutMemberTokens ends uid's sessions working in orgID (SignOutMember)
// and deletes their API tokens and connected apps that act in orgID,
// returning how many of each.
func (a *Accounts) SignOutMemberTokens(uid, orgID int64) (int, []Token, error) {
	toks, err := queryAll(a.db, scanToken, tokenCols+" WHERE t.user_id = ? AND t.org_id = ? AND t.kind IN ('api', 'oauth')", uid, orgID)
	if err != nil {
		return 0, nil, err
	}
	if _, err := a.db.Exec("DELETE FROM tokens WHERE user_id = ? AND org_id = ? AND kind IN ('api', 'oauth')", uid, orgID); err != nil {
		return 0, nil, err
	}
	n, err := a.SignOutMember(uid, orgID)
	return n, toks, err
}

// AdminDisableTOTP turns uid's two-factor sign-in off for someone who lost
// both their phone and their recovery codes, whatever their orgs require
// (an org that requires it has them set it up again at their next sign-in).
func (a *Accounts) AdminDisableTOTP(uid int64) (User, error) {
	var u User
	err := inTx(a.db, func(tx *sql.Tx) error {
		var err error
		if u, err = scanUser(tx.QueryRow(userCols+" WHERE id = ?", uid)); err != nil {
			return err
		}
		if !u.TOTP {
			return ErrTOTPNotSetUp
		}
		u.TOTP = false
		return resetTOTP(tx, uid)
	})
	return u, err
}

// AdminResetLink makes a reset link for uid, as "Forgot password?" sends
// (it works once and for ResetTTL, replacing their earlier one), for an
// admin to hand on. The account must have a password (ErrSSOOnly), have
// confirmed its address (ErrUnverified) and not be the env account
// (ErrEnvPassword).
func (a *Accounts) AdminResetLink(uid int64, now time.Time) (User, string, time.Time, error) {
	var u User
	var tok string
	err := inTx(a.db, func(tx *sql.Tx) error {
		var has, unverified, env bool
		var err error
		u, err = scanUser(tx.QueryRow("SELECT "+userFields+", has_password, unverified, env_managed FROM users WHERE id = ?", uid), &has, &unverified, &env)
		if err != nil {
			return err
		}
		switch {
		case env:
			return ErrEnvPassword
		case unverified:
			return ErrUnverified
		case !has:
			return ErrSSOOnly
		}
		tok, err = newEmailToken(tx, uid, PurposeReset, now)
		return err
	})
	if err != nil {
		return User{}, "", time.Time{}, err
	}
	return u, tok, time.Unix(now.Add(ResetTTL).Unix(), 0).UTC(), nil
}

// AdminSetMember adds uid to orgID with role, or changes their role there,
// as a platform admin (who needn't be a member). An org keeps an owner
// (ErrLastOwner); a personal workspace takes nobody else (ErrPersonalOrg).
// added says whether they joined.
func (a *Accounts) AdminSetMember(orgID, uid int64, role Role) (added bool, err error) {
	if !role.Valid() {
		return false, ErrBadRole
	}
	if _, err := a.Membership(uid, orgID); err == nil {
		return false, a.setRole(0, true, orgID, uid, role)
	} else if !errors.Is(err, ErrNotMember) {
		return false, err
	}
	err = inTx(a.db, func(tx *sql.Tx) error {
		o, err := orgIn(tx, orgID)
		if err != nil {
			return err
		}
		if o.Personal {
			return ErrPersonalOrg
		}
		if _, err := scanUser(tx.QueryRow(userCols+" WHERE id = ?", uid)); err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO org_memberships (user_id, org_id, role, created_at) VALUES (?, ?, ?, ?)", uid, orgID, role, time.Now().Unix())
		if isUnique(err) {
			return ErrAlreadyMember
		}
		return err
	})
	return err == nil, err
}

// AdminRemoveMember takes uid out of orgID as a platform admin, as
// RemoveMember does. The last owner stays (ErrLastOwner).
func (a *Accounts) AdminRemoveMember(orgID, uid int64) error {
	return a.removeMember(0, true, orgID, uid)
}

// DeletedOrg is what DeleteOrg did.
type DeletedOrg struct {
	Org Org
	// Members were its members, who lost access.
	Members []int64
	// Tokens were pinned to it, now gone: close what holds one open.
	Tokens []Token
}

// DeleteOrg deletes orgID with its members' access, invitations, chats,
// planning boards, settings, tokens and sign-in log. It refuses
// (ErrOrgInUse, saying why) while it has projects, a subscription that
// still bills, or is where records without an org go.
func (a *Accounts) DeleteOrg(orgID int64) (DeletedOrg, error) {
	var d DeletedOrg
	o, err := a.AdminOrg(orgID)
	if err != nil {
		return d, err
	}
	d.Org = o.Org
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	err = inTx(a.db, func(tx *sql.Tx) error {
		o, err := scanAdminOrg(tx.QueryRow(adminOrgCols+" WHERE o.id = ?", orgID))
		if err != nil {
			return err
		}
		var why []string
		if o.Projects > 0 {
			why = append(why, fmt.Sprintf("it has %d projects: delete them first (Project menu → Delete project)", o.Projects))
		}
		if o.Subscribed {
			why = append(why, "it has a subscription: cancel it first")
		}
		if o.Default {
			why = append(why, "it's where this Dash keeps records without an organization")
		}
		if len(why) > 0 {
			return fmt.Errorf("%w: %s", ErrOrgInUse, strings.Join(why, "; "))
		}
		if d.Members, err = queryAll(tx, scanOne[int64], "SELECT user_id FROM org_memberships WHERE org_id = ?", orgID); err != nil {
			return err
		}
		if d.Tokens, err = queryAll(tx, scanToken, tokenCols+" WHERE t.org_id = ?", orgID); err != nil {
			return err
		}
		return deleteEmptyOrg(tx, orgID)
	})
	if err != nil {
		return DeletedOrg{}, err
	}
	data, err := load(a.db)
	if err != nil {
		return d, err
	}
	a.s.data = data
	a.s.indexOrgsLocked()
	return d, nil
}

// UserAuthEvents is the latest sign-in log entries about uid, in any org,
// newest first, at most limit.
func (a *Accounts) UserAuthEvents(uid int64, limit int) ([]AuthEvent, error) {
	return queryAll(a.db, func(sc scanner) (AuthEvent, error) {
		var e AuthEvent
		var at int64
		err := sc.Scan(&e.ID, &at, &e.OrgID, &e.User, &e.ActorN, &e.Kind, &e.Detail, &e.IP)
		e.At, e.UserID = time.Unix(at, 0).UTC(), uid
		return e, err
	}, `SELECT e.id, e.at, COALESCE(e.org_id, 0), COALESCE(u.username, ''), COALESCE(x.username, ''), e.kind, e.detail, e.ip
		FROM auth_events e LEFT JOIN users u ON u.id = e.user_id LEFT JOIN users x ON x.id = e.actor_id
		WHERE e.user_id = ? ORDER BY e.id DESC LIMIT ?`, uid, limit)
}
