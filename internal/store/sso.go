package store

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/klahr/dash/internal/passhash"
)

// Single sign-on: identities at OpenID providers (Google) linked to
// accounts, each org's SSO settings and Google Workspace domains, and the
// audit log of sign-ins and their settings. Client secrets reach this file
// sealed (auth.Sealer): it never sees them in the clear.

// ProviderGoogle is Google's name in user_identities.
const ProviderGoogle = "google"

var (
	ErrIdentityTaken  = errors.New("that Google account is linked to another account")
	ErrIdentityLinked = errors.New("your account is linked to a Google account already; unlink it first")
	ErrNoPassword     = errors.New("your account has no password yet: choose one with “Forgot password?” on the sign-in page (which unlinks Google), or you couldn't sign in")
	ErrBadDomain      = errors.New("that doesn't look like a domain name, e.g. example.com")
	ErrDomainTaken    = errors.New("another organization has that domain already")
	ErrBadJoinRole    = errors.New("people who join by their domain are members or admins, never owners")
)

// Identity is an account's identity at a provider.
type Identity struct {
	UserID    int64     `json:"-"`
	Provider  string    `json:"provider"`
	Subject   string    `json:"-"` // the provider's id for the person: never shown
	Email     string    `json:"email"`
	HD        string    `json:"hd,omitempty"` // Google Workspace hosted domain
	Created   time.Time `json:"created"`
	LastLogin time.Time `json:"last_login,omitzero"`
}

const identityCols = "SELECT user_id, provider, subject, email, hd, created_at, COALESCE(last_login_at, 0) FROM user_identities"

func scanIdentity(sc interface{ Scan(...any) error }) (Identity, error) {
	var id Identity
	var created, last int64
	err := sc.Scan(&id.UserID, &id.Provider, &id.Subject, &id.Email, &id.HD, &created, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return id, ErrNotFound
	}
	id.Created = time.Unix(created, 0).UTC()
	if last > 0 {
		id.LastLogin = time.Unix(last, 0).UTC()
	}
	return id, err
}

// IdentityBySubject is the identity provider knows as subject.
func (a *Accounts) IdentityBySubject(provider, subject string) (Identity, error) {
	return scanIdentity(a.db.QueryRow(identityCols+" WHERE provider = ? AND subject = ?", provider, subject))
}

// UserIdentity is uid's identity at provider.
func (a *Accounts) UserIdentity(uid int64, provider string) (Identity, error) {
	return scanIdentity(a.db.QueryRow(identityCols+" WHERE user_id = ? AND provider = ?", uid, provider))
}

// LinkIdentity links id to its user.
func (a *Accounts) LinkIdentity(id Identity, now time.Time) error {
	return inTx(a.db, func(tx *sql.Tx) error { return linkIdentity(tx, id, now) })
}

func linkIdentity(tx *sql.Tx, id Identity, now time.Time) error {
	var owner int64
	err := tx.QueryRow("SELECT user_id FROM user_identities WHERE provider = ? AND subject = ?", id.Provider, id.Subject).Scan(&owner)
	switch {
	case err == nil && owner == id.UserID:
		return nil
	case err == nil:
		return ErrIdentityTaken
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	_, err = tx.Exec(`INSERT INTO user_identities (user_id, provider, subject, email, hd, created_at, last_login_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id.UserID, id.Provider, id.Subject, id.Email, id.HD, now.Unix(), now.Unix())
	if isUnique(err) {
		return ErrIdentityLinked
	}
	return err
}

// TouchIdentity records a sign-in with an identity and what the provider
// said about it this time.
func (a *Accounts) TouchIdentity(provider, subject, email, hd string, now time.Time) error {
	_, err := a.db.Exec("UPDATE user_identities SET email = ?, hd = ?, last_login_at = ? WHERE provider = ? AND subject = ?",
		email, hd, now.Unix(), provider, subject)
	return err
}

// UnlinkIdentity removes uid's identity at provider, unless it is the
// only way they sign in (ErrNoPassword).
func (a *Accounts) UnlinkIdentity(uid int64, provider string) (Identity, error) {
	var id Identity
	err := inTx(a.db, func(tx *sql.Tx) error {
		var err error
		if id, err = scanIdentity(tx.QueryRow(identityCols+" WHERE user_id = ? AND provider = ?", uid, provider)); err != nil {
			return err
		}
		var has bool
		if err := tx.QueryRow("SELECT has_password FROM users WHERE id = ?", uid).Scan(&has); err != nil {
			return err
		}
		if !has {
			return ErrNoPassword
		}
		_, err = tx.Exec("DELETE FROM user_identities WHERE user_id = ? AND provider = ?", uid, provider)
		return err
	})
	return id, err
}

// HasPassword reports whether uid has a password they chose (accounts made
// by signing in with Google don't, until they reset it).
func (a *Accounts) HasPassword(uid int64) (bool, error) {
	var has bool
	err := a.db.QueryRow("SELECT has_password FROM users WHERE id = ?", uid).Scan(&has)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return has, err
}

// UserByEmail is the account with email, and whether it still waits for
// its signup link.
func (a *Accounts) UserByEmail(email string) (User, bool, error) {
	var unverified bool
	u, err := scanUser(a.db.QueryRow("SELECT "+userFields+", unverified FROM users WHERE email = ?", email), &unverified)
	if err != nil {
		return u, false, err
	}
	return u, unverified, nil
}

// User is the account uid.
func (a *Accounts) User(uid int64) (User, error) {
	return scanUser(a.db.QueryRow(userCols+" WHERE id = ?", uid))
}

// Join is what a new account made by a provider sign-in joins: an
// invitation (by its token) or an org with a role, or nothing.
type Join struct {
	InviteToken string
	Org         int64
	Role        Role
}

// CreateIdentityUser makes an account for someone who signed in with a
// provider: email is theirs (the provider verified it), without a password
// (has_password 0; a hash of a random secret), with id linked. An account
// that signed up with email and never confirmed it is taken over instead,
// since nobody knows its password: its signup link stops working. It then
// joins what join says, in the same transaction.
func (a *Accounts) CreateIdentityUser(email string, id Identity, join Join, now time.Time) (User, *Membership, error) {
	email, err := cleanEmail(email)
	if err != nil {
		return User{}, nil, err
	}
	hash, err := passhash.Hash(newToken())
	if err != nil {
		return User{}, nil, err
	}
	var u User
	var m *Membership
	err = inTx(a.db, func(tx *sql.Tx) error {
		var uid int64
		var unverified bool
		err := tx.QueryRow("SELECT id, unverified FROM users WHERE email = ?", email).Scan(&uid, &unverified)
		switch {
		case err == nil && unverified:
			if _, err := tx.Exec("UPDATE users SET unverified = 0, has_password = 0, password_hash = ?, password_changed_at = ? WHERE id = ?",
				hash, now.UnixNano(), uid); err != nil {
				return err
			}
			if _, err := tx.Exec("DELETE FROM email_tokens WHERE user_id = ?", uid); err != nil {
				return err
			}
		case err == nil:
			return ErrUsernameTaken // a confirmed account has the address: it must link instead
		case errors.Is(err, sql.ErrNoRows):
			if uid, err = createIdentityUser(tx, email, hash, now); err != nil {
				return err
			}
		default:
			return err
		}
		if u, err = scanUser(tx.QueryRow(userCols+" WHERE id = ?", uid)); err != nil {
			return err
		}
		id.UserID = uid
		if err := linkIdentity(tx, id, now); err != nil {
			return err
		}
		m, err = joinTx(tx, uid, join, now)
		return err
	})
	if err != nil {
		return User{}, nil, err
	}
	return u, m, nil
}

func createIdentityUser(tx *sql.Tx, email, hash string, now time.Time) (int64, error) {
	base := usernameFrom(email)
	name := base
	for i := 2; ; i++ {
		res, err := tx.Exec(`INSERT INTO users (username, email, password_hash, has_password, password_changed_at, created_at)
			VALUES (?, ?, ?, 0, ?, ?)`, name, email, hash, now.UnixNano(), now.Unix())
		if isUnique(err) && !strings.Contains(err.Error(), "users.email") && i < 10000 {
			name = base + strconv.Itoa(i)
			continue
		}
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
}

// JoinOrg makes uid a member of what join says (see Join), keeping the role
// of a membership they have. It returns nil when join is empty.
func (a *Accounts) JoinOrg(uid int64, join Join, now time.Time) (*Membership, error) {
	var m *Membership
	err := inTx(a.db, func(tx *sql.Tx) (err error) {
		m, err = joinTx(tx, uid, join, now)
		return err
	})
	return m, err
}

func joinTx(tx *sql.Tx, uid int64, join Join, now time.Time) (*Membership, error) {
	switch {
	case join.InviteToken != "":
		inv, err := invitationByToken(tx, join.InviteToken, now)
		if err != nil {
			return nil, err
		}
		m, err := accept(tx, inv, uid)
		return &m, err
	case join.Org != 0:
		if join.Role != RoleMember && join.Role != RoleAdmin {
			return nil, ErrBadJoinRole // never owner: signing in mustn't make anyone one
		}
		var personal bool
		if err := tx.QueryRow("SELECT personal_user_id IS NOT NULL FROM organizations WHERE id = ?", join.Org).Scan(&personal); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if personal {
			return nil, ErrPersonalOrg
		}
		if _, err := tx.Exec(`INSERT INTO org_memberships (user_id, org_id, role, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (user_id, org_id) DO NOTHING`, uid, join.Org, join.Role, now.Unix()); err != nil {
			return nil, err
		}
		m, err := membership(tx, uid, join.Org)
		return &m, err
	}
	return nil, nil
}

// ---- org settings ----

// OrgSSO is an org's single sign-on settings. GoogleSecret is sealed.
type OrgSSO struct {
	OrgID         int64
	GoogleClient  string
	GoogleSecret  string // sealed (auth.Sealer): never the secret itself
	AutoJoin      bool
	JoinRole      Role
	RequireGoogle bool
	Google2FA     bool
	Domains       []SSODomain
}

// SSODomain is a Google Workspace domain an org claims.
type SSODomain struct {
	OrgID      int64     `json:"org_id"`
	OrgName    string    `json:"org,omitempty"`
	Domain     string    `json:"domain"`
	Verified   bool      `json:"verified"`
	How        string    `json:"how,omitempty"` // "google": an owner's Google sign-in; "admin": a platform admin
	Created    time.Time `json:"created"`
	VerifiedAt time.Time `json:"verified_at,omitzero"`
}

// VerifiedDomain reports whether d is one of the verified domains.
func (s OrgSSO) VerifiedDomain(d string) bool {
	for _, x := range s.Domains {
		if x.Verified && strings.EqualFold(x.Domain, d) {
			return true
		}
	}
	return false
}

// OrgSSO is orgID's settings (the defaults if it has none).
func (a *Accounts) OrgSSO(orgID int64) (OrgSSO, error) {
	s := OrgSSO{OrgID: orgID, JoinRole: RoleMember}
	err := a.db.QueryRow(`SELECT google_client_id, google_client_secret, auto_join, join_role, require_google, google_2fa
		FROM org_sso WHERE org_id = ?`, orgID).Scan(&s.GoogleClient, &s.GoogleSecret, &s.AutoJoin, &s.JoinRole, &s.RequireGoogle, &s.Google2FA)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	s.Domains, err = a.domains("WHERE d.org_id = ?", orgID)
	return s, err
}

// PutOrgSSO stores s (but not its domains: see AddSSODomain).
func (a *Accounts) PutOrgSSO(s OrgSSO, now time.Time) error {
	if s.JoinRole == "" {
		s.JoinRole = RoleMember
	}
	if s.JoinRole != RoleMember && s.JoinRole != RoleAdmin {
		return ErrBadJoinRole
	}
	_, err := a.db.Exec(`INSERT INTO org_sso (org_id, google_client_id, google_client_secret, auto_join, join_role, require_google, google_2fa, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (org_id) DO UPDATE SET google_client_id = excluded.google_client_id, google_client_secret = excluded.google_client_secret,
			auto_join = excluded.auto_join, join_role = excluded.join_role, require_google = excluded.require_google,
			google_2fa = excluded.google_2fa, updated_at = excluded.updated_at`,
		s.OrgID, s.GoogleClient, s.GoogleSecret, s.AutoJoin, s.JoinRole, s.RequireGoogle, s.Google2FA, now.Unix())
	return err
}

func (a *Accounts) domains(where string, args ...any) ([]SSODomain, error) {
	rows, err := a.db.Query(`SELECT d.org_id, o.name, d.domain, d.verified, d.how, d.created_at, COALESCE(d.verified_at, 0)
		FROM org_sso_domains d JOIN organizations o ON o.id = d.org_id `+where+` ORDER BY d.domain`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SSODomain{}
	for rows.Next() {
		var d SSODomain
		var created, verified int64
		if err := rows.Scan(&d.OrgID, &d.OrgName, &d.Domain, &d.Verified, &d.How, &created, &verified); err != nil {
			return nil, err
		}
		d.Created = time.Unix(created, 0).UTC()
		if verified > 0 {
			d.VerifiedAt = time.Unix(verified, 0).UTC()
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// CleanDomain lower-cases d and checks it looks like a domain name.
func CleanDomain(d string) (string, error) {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimPrefix(d, "@")
	if len(d) < 3 || len(d) > 253 || !strings.Contains(d, ".") || strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") || strings.Contains(d, "..") {
		return "", ErrBadDomain
	}
	for _, r := range d {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return "", ErrBadDomain
		}
	}
	return d, nil
}

// AddSSODomain claims domain for orgID, verified (how: "google" or
// "admin", by uid) or not (how ""). A domain another org has verified is
// ErrDomainTaken.
func (a *Accounts) AddSSODomain(orgID int64, domain, how string, by int64, now time.Time) (SSODomain, error) {
	domain, err := CleanDomain(domain)
	if err != nil {
		return SSODomain{}, err
	}
	err = inTx(a.db, func(tx *sql.Tx) error {
		var other int64
		err := tx.QueryRow("SELECT org_id FROM org_sso_domains WHERE domain = ? AND verified = 1 AND org_id != ?", domain, orgID).Scan(&other)
		if err == nil {
			return ErrDomainTaken
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO org_sso_domains (org_id, domain, created_at) VALUES (?, ?, ?)
			ON CONFLICT (org_id, domain) DO NOTHING`, orgID, domain, now.Unix()); err != nil {
			return err
		}
		if how == "" {
			return nil
		}
		return verifyDomain(tx, orgID, domain, how, by, now)
	})
	if err != nil {
		return SSODomain{}, err
	}
	ds, err := a.domains("WHERE d.org_id = ? AND d.domain = ?", orgID, domain)
	if err != nil || len(ds) == 0 {
		return SSODomain{}, errors.Join(err, ErrNotFound)
	}
	return ds[0], nil
}

func verifyDomain(tx *sql.Tx, orgID int64, domain, how string, by int64, now time.Time) error {
	var byID any
	if by != 0 {
		byID = by
	}
	res, err := tx.Exec(`UPDATE org_sso_domains SET verified = 1, how = ?, verified_by = ?, verified_at = ?
		WHERE org_id = ? AND domain = ? AND verified = 0`, how, byID, now.Unix(), orgID, domain)
	if isUnique(err) {
		return ErrDomainTaken
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		// Other orgs' claims of it can never be verified now.
		_, err = tx.Exec("DELETE FROM org_sso_domains WHERE domain = ? AND org_id != ? AND verified = 0", domain, orgID)
	}
	return err
}

// VerifySSODomain verifies orgID's claim of domain (how: "google" or
// "admin", by uid). An unknown claim is ErrNotFound.
func (a *Accounts) VerifySSODomain(orgID int64, domain, how string, by int64, now time.Time) error {
	return inTx(a.db, func(tx *sql.Tx) error {
		var verified bool
		err := tx.QueryRow("SELECT verified FROM org_sso_domains WHERE org_id = ? AND domain = ?", orgID, domain).Scan(&verified)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil || verified {
			return err
		}
		return verifyDomain(tx, orgID, domain, how, by, now)
	})
}

// RemoveSSODomain drops orgID's claim of domain. Unless verifiedToo, only
// a claim still waiting for verification goes (else ErrNotFound).
func (a *Accounts) RemoveSSODomain(orgID int64, domain string, verifiedToo bool) error {
	q := "DELETE FROM org_sso_domains WHERE org_id = ? AND domain = ?"
	if !verifiedToo {
		q += " AND verified = 0"
	}
	res, err := a.db.Exec(q, orgID, strings.ToLower(strings.TrimSpace(domain)))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PendingSSODomains are the claims waiting for verification, every org's.
func (a *Accounts) PendingSSODomains() ([]SSODomain, error) {
	return a.domains("WHERE d.verified = 0")
}

// DomainOrg is the org that verified domain, with its settings.
func (a *Accounts) DomainOrg(domain string) (OrgSSO, error) {
	var org int64
	err := a.db.QueryRow("SELECT org_id FROM org_sso_domains WHERE domain = ? AND verified = 1", domain).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return OrgSSO{}, ErrNotFound
	}
	if err != nil {
		return OrgSSO{}, err
	}
	return a.OrgSSO(org)
}

// ---- the instance's client ----

// InstanceGoogle is the Google client a platform admin set (secret sealed).
func (a *Accounts) InstanceGoogle() (client, sealedSecret string, err error) {
	err = a.db.QueryRow("SELECT google_client_id, google_client_secret FROM instance_sso WHERE id = 1").Scan(&client, &sealedSecret)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return client, sealedSecret, err
}

// PutInstanceGoogle stores the instance's Google client (secret sealed).
func (a *Accounts) PutInstanceGoogle(client, sealedSecret string, now time.Time) error {
	_, err := a.db.Exec(`INSERT INTO instance_sso (id, google_client_id, google_client_secret, updated_at) VALUES (1, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET google_client_id = excluded.google_client_id,
			google_client_secret = excluded.google_client_secret, updated_at = excluded.updated_at`, client, sealedSecret, now.Unix())
	return err
}

// ---- sessions ----

// SignOutMember signs uid out of orgID: every session working in it, and
// the rest of those sessions' families (the same sign-in on the base
// domain and other hosts). It returns how many sessions ended.
func (a *Accounts) SignOutMember(uid, orgID int64) (int, error) {
	res, err := a.db.Exec(`DELETE FROM sessions WHERE user_id = ?1 AND (active_org_id = ?2
		OR family IN (SELECT family FROM sessions WHERE user_id = ?1 AND active_org_id = ?2 AND family IS NOT NULL))`, uid, orgID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ---- audit ----

// AuthEvent is one entry of the audit log.
type AuthEvent struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	OrgID  int64     `json:"-"`
	UserID int64     `json:"-"`
	Actor  int64     `json:"-"`
	User   string    `json:"user,omitempty"`  // the username, when listed
	ActorN string    `json:"actor,omitempty"` // the actor's username, when listed
	Kind   string    `json:"kind"`
	Detail string    `json:"detail,omitempty"`
	IP     string    `json:"ip,omitempty"`
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// Audit adds e to the log.
func (a *Accounts) Audit(e AuthEvent) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	_, err := a.db.Exec("INSERT INTO auth_events (at, org_id, user_id, actor_id, kind, detail, ip) VALUES (?, ?, ?, ?, ?, ?, ?)",
		e.At.Unix(), nullID(e.OrgID), nullID(e.UserID), nullID(e.Actor), e.Kind, e.Detail, e.IP)
	return err
}

// AuthEvents is orgID's latest log entries, newest first, at most limit.
func (a *Accounts) AuthEvents(orgID int64, limit int) ([]AuthEvent, error) {
	rows, err := a.db.Query(`SELECT e.id, e.at, COALESCE(e.user_id, 0), COALESCE(e.actor_id, 0), COALESCE(u.username, ''), COALESCE(x.username, ''), e.kind, e.detail, e.ip
		FROM auth_events e LEFT JOIN users u ON u.id = e.user_id LEFT JOIN users x ON x.id = e.actor_id
		WHERE e.org_id = ? ORDER BY e.id DESC LIMIT ?`, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuthEvent{}
	for rows.Next() {
		var e AuthEvent
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.UserID, &e.Actor, &e.User, &e.ActorN, &e.Kind, &e.Detail, &e.IP); err != nil {
			return nil, err
		}
		e.At, e.OrgID = time.Unix(at, 0).UTC(), orgID
		out = append(out, e)
	}
	return out, rows.Err()
}

// AnyOrgGoogleClient reports whether some org has its own Google client.
func (a *Accounts) AnyOrgGoogleClient() (bool, error) {
	var ok bool
	err := a.db.QueryRow("SELECT EXISTS (SELECT 1 FROM org_sso WHERE google_client_id != '' AND google_client_secret != '')").Scan(&ok)
	return ok, err
}
