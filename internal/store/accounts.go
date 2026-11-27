package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/passhash"
)

// Accounts are Dash's users, their organizations and memberships, pending
// invitations and browser sessions. Unlike the records, nothing here is
// cached: every call reads the database, so a removed member loses access on
// their next request.
type Accounts struct {
	db *sql.DB
	s  *Store // reloaded when Bootstrap moves records between orgs
	// Quiet logs the first account's creation at debug level: a runner's
	// made-up account, which nobody set up or signs in with.
	Quiet bool
}

// Accounts returns the store's accounts.
func (s *Store) Accounts() *Accounts { return &Accounts{db: s.db, s: s} }

// Role is what a member may do in an org: owners and admins manage members
// and invitations; only owners grant or change the owner role.
//
// RolePlanner is apart from that ranking: a planner (a product owner) uses
// Planning and their own account, and nothing else, whatever a rank would
// say. The API enforces it in one place, by route (internal/api's planner
// gate), so don't read Planner as "less than Member" anywhere else. It
// ranks lowest only so that admins may grant it and change it.
type Role string

const (
	RoleOwner   Role = "owner"
	RoleAdmin   Role = "admin"
	RoleMember  Role = "member"
	RolePlanner Role = "planner"
)

func (r Role) rank() int {
	switch r {
	case RoleOwner:
		return 4
	case RoleAdmin:
		return 3
	case RoleMember:
		return 2
	case RolePlanner:
		return 1
	}
	return 0
}

// Valid reports whether r is one of the four roles.
func (r Role) Valid() bool { return r.rank() > 0 }

// Planner reports whether r is the planner role: Planning only.
func (r Role) Planner() bool { return r == RolePlanner }

// AtLeast reports whether r grants everything other does. A planner grants
// nothing a member does (see RolePlanner).
func (r Role) AtLeast(other Role) bool { return r.rank() >= other.rank() }

var (
	// ErrNoAccount: there are no users yet and no DASH_USERNAME / DASH_PASSWORD to create the first.
	ErrNoAccount     = errors.New("no accounts yet: set DASH_USERNAME and DASH_PASSWORD (see .env.example)")
	ErrNotMember     = errors.New("not a member of that organization")
	ErrForbidden     = errors.New("your role in this organization doesn't allow that")
	ErrLastOwner     = errors.New("an organization needs at least one owner")
	ErrUsernameTaken = errors.New("that username is taken")
	ErrBadUsername   = errors.New("a username is 1 to 64 letters, digits or . _ - @, without spaces")
	ErrWeakPassword  = errors.New("a password needs at least 8 characters")
	ErrBadEmail      = errors.New("that doesn't look like an email address")
	ErrBadRole       = errors.New("role must be owner, admin, member or planner")
	ErrBadOrgName    = errors.New("an organization needs a name (up to 100 characters)")
	// ErrInvitation covers unknown, used and expired invitation links alike.
	ErrInvitation = errors.New("this invitation link is not valid any more; ask for a new one")
	// ErrPersonalOrg: a personal workspace has its owner as its only member.
	ErrPersonalOrg = errors.New("a personal workspace is just for you: create an organization to invite people")
)

// MinPassword is the shortest password Dash accepts for a new account.
const MinPassword = 8

type User struct {
	ID            int64  `json:"id"`
	Username      string `json:"username"`
	Email         string `json:"email,omitempty"`
	PlatformAdmin bool   `json:"is_platform_admin"`
	TOTP          bool   `json:"totp_enabled"` // signs in with a code from an authenticator app too
	// Avatar is the version of their profile picture (0: none), for its
	// URL: /api/users/{id}/avatar?v={avatar}.
	Avatar int64 `json:"avatar,omitempty"`
}

type Org struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	// Require2FA: members must sign in with two factors.
	Require2FA bool `json:"require_2fa"`
	// Personal: the org is its only member's personal workspace (see
	// Accounts.EnsurePersonalOrg); it can't invite anyone.
	Personal bool `json:"personal,omitempty"`
	// Folder is the org's folder under the projects root. Unlike the slug
	// it never changes.
	Folder string `json:"-"`
}

// ProjectFolder is the name of o's folder under a projects root: its
// Folder (its slug, for an org loaded without one) made safe by FolderSlug.
// The hub's own projects of o go there, and so do runners' clones of them.
func (o Org) ProjectFolder() string {
	if o.Folder != "" {
		return FolderSlug(o.Folder)
	}
	return FolderSlug(o.Slug)
}

var folderSlugRe = regexp.MustCompile(`[^a-z0-9-]+`)

// FolderSlug makes s a folder name for an org: lower-case letters, digits
// and '-', not starting or ending with '-' ("" when nothing is left).
func FolderSlug(s string) string {
	return strings.Trim(folderSlugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// Membership is one org a user belongs to and their role there.
type Membership struct {
	Org
	Role Role `json:"role"`
}

// Member is one user of an org.
type Member struct {
	User
	Role   Role      `json:"role"`
	Joined time.Time `json:"joined"`
}

type Invitation struct {
	ID        int64     `json:"id"`
	Org       Org       `json:"org"`
	Email     string    `json:"email"`
	Role      Role      `json:"role"`
	InvitedBy string    `json:"invited_by,omitempty"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires"`
	// AnyEmail: whoever has the link may accept it, even by signing in
	// with Google as another address. Off, a Google sign-in must be the
	// invited address (a password account accepts with the link either way).
	AnyEmail bool `json:"any_email"`
}

// Session is who a browser session belongs to and the org they are working
// in. Org is nil when the user belongs to no org. Needs2FA is set when an org
// of theirs requires two-factor sign-in and they haven't set it up.
type Session struct {
	User     User
	Org      *Membership
	Needs2FA bool
	// Family is shared by the sessions one sign-in made on the base domain
	// and the org subdomains it was handed to ("" for older sessions).
	Family string
	// Method is how the sign-in was made: MethodPassword or MethodGoogle.
	Method string
	// AuthAt is when the sign-in was made (a handed-off session keeps its
	// family's); zero for sessions from before it was recorded.
	AuthAt time.Time
}

// Sign-in methods (Session.Method).
const (
	MethodPassword = "password"
	MethodGoogle   = "google"
)

// ---- validation ----

// ValidUsername reports whether name can be a username.
func ValidUsername(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-@", r)) {
			return false
		}
	}
	return true
}

func cleanEmail(email string) (string, error) {
	email = strings.TrimSpace(email)
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || domain == "" || strings.ContainsAny(email, " \t\r\n<>,;") || len(email) > 254 {
		return "", ErrBadEmail
	}
	return email, nil
}

// slugify makes a URL-friendly name: lower-case letters and digits joined by '-'.
func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	if b.Len() == 0 {
		return "org"
	}
	return b.String()
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// ---- the first account ----

// Bootstrap makes sure the account DASH_USERNAME / DASH_PASSWORD describe
// exists. On first start (no users) it creates it as a platform admin and
// owner of a "Default" org, and that org becomes the one existing and new
// records belong to. Later starts keep its name and password in step with
// the environment; changing either signs its sessions out. With no users
// and no credentials it returns ErrNoAccount. Records without an org are
// assigned to the default org every time.
func (a *Accounts) Bootstrap(username, password string) error {
	var n int
	if err := a.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n); err != nil {
		return err
	}
	now := time.Now()
	switch {
	case n == 0:
		if username == "" || password == "" {
			return ErrNoAccount
		}
		hash, err := passhash.Hash(password)
		if err != nil {
			return err
		}
		err = inTx(a.db, func(tx *sql.Tx) error {
			res, err := tx.Exec(`INSERT INTO users (username, password_hash, is_platform_admin, env_managed, password_changed_at, created_at)
				VALUES (?, ?, 1, 1, ?, ?)`, username, hash, now.UnixNano(), now.Unix())
			if err != nil {
				return err
			}
			uid, _ := res.LastInsertId()
			org, err := a.createOrg(tx, uid, "Default", now)
			if err != nil {
				return err
			}
			_, err = tx.Exec("UPDATE instance SET default_org_id = ? WHERE id = 1 AND default_org_id IS NULL", org.ID)
			return err
		})
		if err != nil {
			return fmt.Errorf("create the first account: %w", err)
		}
		level := slog.LevelInfo
		if a.Quiet {
			level = slog.LevelDebug
		}
		slog.Log(context.Background(), level, "created the first account from DASH_USERNAME", "username", username)
	case username != "" && password != "":
		if err := a.syncEnvUser(username, password, now); err != nil {
			return err
		}
	}
	if err := a.assignRecords(); err != nil {
		return err
	}
	if a.s != nil {
		return a.s.reload() // the records' orgs (and the general chat's owner) may have changed
	}
	return nil
}

func (a *Accounts) syncEnvUser(username, password string, now time.Time) error {
	var id int64
	var name, hash string
	err := a.db.QueryRow("SELECT id, username, password_hash FROM users WHERE env_managed = 1").Scan(&id, &name, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	ok, rehash, _ := passhash.Verify(hash, password)
	if ok && name == username && !rehash {
		return nil
	}
	if !ok || rehash {
		if hash, err = passhash.Hash(password); err != nil {
			return err
		}
	}
	return inTx(a.db, func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE users SET username = ?, password_hash = ? WHERE id = ?", username, hash, id); err != nil {
			if isUnique(err) {
				return fmt.Errorf("DASH_USERNAME %q is already another account's username", username)
			}
			return err
		}
		if ok && strings.EqualFold(name, username) {
			return nil // only a rehash, or the name's case
		}
		if _, err := tx.Exec("UPDATE users SET password_changed_at = ? WHERE id = ?", now.UnixNano(), id); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM sessions WHERE user_id = ?", id)
		return err
	})
}

var orgTables = []string{"projects", "todos", "scan_runs", "scan_settings", "scan_templates", "chat_messages", "goals", "past_chats"}

// assignRecords puts records that have no org (those from before orgs) in
// the default org, choosing the oldest org as default if none is set.
func (a *Accounts) assignRecords() error {
	return inTx(a.db, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE instance SET default_org_id = (SELECT MIN(id) FROM organizations)
			WHERE id = 1 AND default_org_id IS NULL`); err != nil {
			return err
		}
		// Records go with their project's org; projects (and records of
		// projects that are gone) without one go in the default org.
		if _, err := tx.Exec("UPDATE projects SET org_id = " + defaultOrg + " WHERE org_id IS NULL"); err != nil {
			return err
		}
		for _, t := range orgTables[1:] {
			q := "UPDATE " + t + " SET org_id = COALESCE((SELECT p.org_id FROM projects p WHERE p.id = " + t + ".project_id), " + defaultOrg + ") WHERE org_id IS NULL"
			if t == "chat_messages" {
				q = "UPDATE chat_messages SET org_id = " + defaultOrg + " WHERE org_id IS NULL"
			}
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		// The general chat from before users becomes the first account's
		// (the env account's, or else the oldest), in the default org.
		_, err := tx.Exec(`UPDATE chat_messages SET owner_id = ` + defaultOrg + ` || ':' || COALESCE(
				(SELECT id FROM users WHERE env_managed = 1), (SELECT MIN(id) FROM users)),
				org_id = ` + defaultOrg + `
			WHERE kind = 'main' AND owner_id = '' AND EXISTS (SELECT 1 FROM users) AND ` + defaultOrg + ` IS NOT NULL`)
		return err
	})
}

// DefaultOrg is the org records are assigned to for now.
func (a *Accounts) DefaultOrg() (Org, error) {
	o, err := scanOrg(a.db.QueryRow(`SELECT ` + orgFields + ` FROM instance i JOIN organizations o ON o.id = i.default_org_id
		WHERE i.id = 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	return o, err
}

// EnvUser is the account DASH_USERNAME describes, if any.
func (a *Accounts) EnvUser() (User, error) {
	return scanUser(a.db.QueryRow(userCols + " WHERE env_managed = 1"))
}

// orgFields are the organizations columns (as o) scanOrg reads, in its order.
const orgFields = "o.id, o.name, o.slug, o.require_2fa, o.personal_user_id IS NOT NULL, COALESCE(o.folder, o.slug)"

// orgCols selects orgFields; append a WHERE clause.
const orgCols = "SELECT " + orgFields + " FROM organizations o"

// scanOrg reads a row selected by orgCols (or orgFields followed by columns
// scanned into extra). Unlike scanUser it returns sql.ErrNoRows as it is:
// callers map a missing row to their own error.
func scanOrg(sc scanner, extra ...any) (Org, error) {
	var o Org
	err := sc.Scan(append([]any{&o.ID, &o.Name, &o.Slug, &o.Require2FA, &o.Personal, &o.Folder}, extra...)...)
	return o, err
}

// ---- users and passwords ----

// userFields are the users columns scanUser reads, in its order.
const userFields = "id, username, COALESCE(email, ''), is_platform_admin, totp_secret IS NOT NULL, avatar_at"

// userCols selects userFields; append a WHERE clause.
const userCols = "SELECT " + userFields + " FROM users"

// scanUser reads a row selected by userCols (or userFields followed by
// columns scanned into extra). No row is ErrNotFound.
func scanUser(sc scanner, extra ...any) (User, error) {
	var u User
	err := sc.Scan(append([]any{&u.ID, &u.Username, &u.Email, &u.PlatformAdmin, &u.TOTP, &u.Avatar}, extra...)...)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// Credentials returns the user whose username or email is login, and their
// password hash. A username wins over another account's email.
func (a *Accounts) Credentials(login string) (User, string, error) {
	var hash string
	u, err := scanUser(a.db.QueryRow(`SELECT `+userFields+`, password_hash FROM users
		WHERE username = ?1 OR email = ?1 ORDER BY username = ?1 DESC LIMIT 1`, login), &hash)
	if err != nil {
		return u, "", err
	}
	return u, hash, nil
}

// PasswordHash is the user's stored password hash.
func (a *Accounts) PasswordHash(uid int64) (string, error) {
	var hash string
	err := a.db.QueryRow("SELECT password_hash FROM users WHERE id = ?", uid).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

// Rehash replaces the user's hash of the same password with a stronger one.
func (a *Accounts) Rehash(uid int64, hash string) error {
	_, err := a.db.Exec("UPDATE users SET password_hash = ? WHERE id = ?", hash, uid)
	return err
}

// ---- sessions ----

// CreateSession stores a session for uid under key (a hash of the cookie),
// working in the user's first org.
func (a *Accounts) CreateSession(key string, uid int64, now, expires time.Time) error {
	return a.CreateSessionIn(key, uid, 0, "", now, expires)
}

// CreateSessionIn stores a session for uid under key working in org (0:
// the user's first org), in family (see Session.Family; "" for none).
func (a *Accounts) CreateSessionIn(key string, uid, org int64, family string, now, expires time.Time) error {
	return a.CreateSessionVia(key, uid, org, family, "", now, expires)
}

// CreateSessionVia is CreateSessionIn for a sign-in made with method
// (MethodPassword, MethodGoogle). With method "", the session is another
// host's of family's sign-in: it takes the method and time of the
// family's first session (MethodPassword and now without one).
func (a *Accounts) CreateSessionVia(key string, uid, org int64, family, method string, now, expires time.Time) error {
	var fam, meth any
	if family != "" {
		fam = family
	}
	if method != "" {
		meth = method
	}
	// A disabled account gets no session (ErrAccountDisabled), however it
	// signed in.
	return inTx(a.db, func(tx *sql.Tx) error {
		err := execOne(tx, ErrAccountDisabled, `INSERT INTO sessions (key, user_id, active_org_id, family, created_at, expires_at, method, auth_at)
			SELECT ?1, ?2, COALESCE((SELECT org_id FROM org_memberships WHERE user_id = ?2 AND org_id = ?3),
				(SELECT org_id FROM org_memberships WHERE user_id = ?2 ORDER BY created_at, org_id LIMIT 1)), ?4, ?5, ?6,
				COALESCE(?7, (SELECT method FROM sessions WHERE family = ?4 AND user_id = ?2 ORDER BY created_at LIMIT 1), 'password'),
				CASE WHEN ?7 IS NULL THEN COALESCE((SELECT MIN(auth_at) FROM sessions WHERE family = ?4 AND user_id = ?2 AND auth_at > 0), ?5) ELSE ?5 END
			WHERE EXISTS (SELECT 1 FROM users WHERE id = ?2) AND `+enabled("?2"),
			key, uid, org, fam, now.Unix(), expires.Unix(), meth)
		if err != nil {
			return err
		}
		if method == "" {
			return nil // another host of a sign-in already counted
		}
		_, err = tx.Exec(`INSERT INTO user_status (user_id, last_login_at) VALUES (?, ?)
			ON CONFLICT (user_id) DO UPDATE SET last_login_at = excluded.last_login_at`, uid, now.Unix())
		return err
	})
}

// Session returns the session stored under key, checking the user's
// membership of its org now. If they have left that org, the session moves
// to another of theirs, or to none. Unknown and expired sessions are
// ErrNotFound (and expired ones are deleted).
func (a *Accounts) Session(key string, now time.Time) (Session, error) {
	var s Session
	var expires, authAt int64
	var active sql.NullInt64
	var orgID sql.NullInt64
	var name, slug, folder, role, family sql.NullString
	var require2FA, personal, required sql.NullBool
	err := a.db.QueryRow(`SELECT u.id, u.username, COALESCE(u.email, ''), u.is_platform_admin, u.totp_secret IS NOT NULL, u.avatar_at,
			s.expires_at, s.active_org_id, o.id, o.name, o.slug, COALESCE(o.folder, o.slug), o.require_2fa, o.personal_user_id IS NOT NULL, m.role, s.family, s.method, s.auth_at, `+requires2FA+`
		FROM sessions s JOIN users u ON u.id = s.user_id
		LEFT JOIN org_memberships m ON m.user_id = s.user_id AND m.org_id = s.active_org_id
		LEFT JOIN organizations o ON o.id = m.org_id
		WHERE s.key = ? AND `+enabled("u.id"), key).Scan(&s.User.ID, &s.User.Username, &s.User.Email, &s.User.PlatformAdmin, &s.User.TOTP, &s.User.Avatar,
		&expires, &active, &orgID, &name, &slug, &folder, &require2FA, &personal, &role, &family, &s.Method, &authAt, &required)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	if now.Unix() >= expires {
		_, _ = a.db.Exec("DELETE FROM sessions WHERE key = ?", key)
		return s, ErrNotFound
	}
	s.Needs2FA = !s.User.TOTP && required.Bool
	s.Family = family.String
	s.AuthAt = unixOrZero(authAt)
	if role.Valid {
		s.Org = &Membership{Org: Org{ID: orgID.Int64, Name: name.String, Slug: slug.String, Require2FA: require2FA.Bool, Personal: personal.Bool, Folder: folder.String}, Role: Role(role.String)}
		return s, nil
	}
	// No membership of the active org (removed, or never had one): fall
	// back to the user's first org so they needn't sign in again.
	ms, err := a.Memberships(s.User.ID)
	if err != nil {
		return s, err
	}
	var next any // NULL
	if len(ms) > 0 {
		s.Org = &ms[0]
		next = ms[0].ID
	}
	if len(ms) > 0 || active.Valid {
		if _, err := a.db.Exec("UPDATE sessions SET active_org_id = ? WHERE key = ?", next, key); err != nil {
			return s, err
		}
	}
	return s, nil
}

// EnsureSessionFamily gives the session under key family unless it has
// one, and returns the one it has.
func (a *Accounts) EnsureSessionFamily(key, family string) (string, error) {
	var f string
	err := a.db.QueryRow("UPDATE sessions SET family = COALESCE(family, ?) WHERE key = ? RETURNING family", family, key).Scan(&f)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return f, err
}

// DeleteSessionFamily signs a session out together with the rest of its
// family: the same sign-in on the base domain and the org subdomains.
func (a *Accounts) DeleteSessionFamily(key string) error {
	_, err := a.db.Exec(`DELETE FROM sessions WHERE key = ?1
		OR family = (SELECT family FROM sessions WHERE key = ?1 AND family IS NOT NULL)`, key)
	return err
}

// PruneSessions deletes expired sessions.
func (a *Accounts) PruneSessions(now time.Time) error {
	_, err := a.db.Exec("DELETE FROM sessions WHERE expires_at <= ?", now.Unix())
	return err
}

// SwitchOrg makes orgID the session's active org, if its user is a member.
func (a *Accounts) SwitchOrg(key string, uid, orgID int64) (Membership, error) {
	m, err := a.Membership(uid, orgID)
	if err != nil {
		return m, err
	}
	_, err = a.db.Exec("UPDATE sessions SET active_org_id = ? WHERE key = ? AND user_id = ?", orgID, key, uid)
	return m, err
}

// ---- orgs and members ----

// Memberships lists the orgs uid belongs to, oldest membership first.
func (a *Accounts) Memberships(uid int64) ([]Membership, error) {
	return queryAll(a.db, func(sc scanner) (m Membership, err error) {
		m.Org, err = scanOrg(sc, &m.Role)
		return m, err
	}, `SELECT `+orgFields+`, m.role FROM org_memberships m
		JOIN organizations o ON o.id = m.org_id WHERE m.user_id = ? ORDER BY m.created_at, o.id`, uid)
}

// Membership is uid's membership of orgID, or ErrNotMember.
func (a *Accounts) Membership(uid, orgID int64) (Membership, error) {
	return membership(a.db, uid, orgID)
}

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func membership(q querier, uid, orgID int64) (Membership, error) {
	var m Membership
	var err error
	m.Org, err = scanOrg(q.QueryRow(`SELECT `+orgFields+`, m.role FROM org_memberships m
		JOIN organizations o ON o.id = m.org_id
		WHERE m.user_id = ? AND m.org_id = ?`, uid, orgID), &m.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotMember
	}
	return m, err
}

// CreateOrg creates an org named name with uid as its owner.
func (a *Accounts) CreateOrg(uid int64, name string) (Org, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return Org{}, ErrBadOrgName
	}
	var o Org
	err := inTx(a.db, func(tx *sql.Tx) (err error) {
		o, err = a.createOrg(tx, uid, name, time.Now())
		return err
	})
	return o, err
}

// createOrg makes an org named name owned by uid. Its slug comes from the
// name (see slugBase) and is one no org holds; its folder is the same and
// stays when the slug is renamed.
func (a *Accounts) createOrg(tx *sql.Tx, uid int64, name string, now time.Time) (Org, error) {
	slug, err := a.freeSlug(tx, a.slugBase(name), 0, now)
	if err != nil {
		return Org{}, err
	}
	o := Org{Name: name, Slug: slug, Folder: slug}
	res, err := tx.Exec("INSERT INTO organizations (name, slug, folder, created_at) VALUES (?, ?, ?, ?)", name, slug, slug, now.Unix())
	if err != nil {
		return o, err
	}
	o.ID, _ = res.LastInsertId()
	_, err = tx.Exec("INSERT INTO org_memberships (user_id, org_id, role, created_at) VALUES (?, ?, 'owner', ?)", uid, o.ID, now.Unix())
	return o, err
}

// Members lists an org's members, owners first.
func (a *Accounts) Members(orgID int64) ([]Member, error) {
	return queryAll(a.db, func(sc scanner) (m Member, err error) {
		var joined int64
		err = sc.Scan(&m.ID, &m.Username, &m.Email, &m.PlatformAdmin, &m.TOTP, &m.Avatar, &m.Role, &joined)
		m.Joined = time.Unix(joined, 0).UTC()
		return m, err
	}, `SELECT u.id, u.username, COALESCE(u.email, ''), u.is_platform_admin, u.totp_secret IS NOT NULL, u.avatar_at, m.role, m.created_at
		FROM org_memberships m JOIN users u ON u.id = m.user_id WHERE m.org_id = ?
		ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END, u.username`, orgID)
}

// changeMemberAs checks that actor may change target's membership of orgID
// from its current role to role ("" = remove), in tx. Members may remove
// themselves; otherwise it takes an admin, and only owners touch owners.
// The last owner can't be demoted or removed.
// A platform admin (platform) needn't be a member: only the last owner
// rule applies.
func changeMemberAs(tx *sql.Tx, actor int64, platform bool, orgID, target int64, role Role) error {
	cur, err := membership(tx, target, orgID)
	if err != nil {
		return err
	}
	if !platform && !(actor == target && role == "") { // leaving needs no role
		mine, err := membership(tx, actor, orgID)
		if err != nil {
			return err
		}
		if !mine.Role.AtLeast(RoleAdmin) || !mine.Role.AtLeast(cur.Role) || !mine.Role.AtLeast(role) {
			return ErrForbidden
		}
	}
	if cur.Role == RoleOwner && role != RoleOwner {
		var owners int
		if err := tx.QueryRow("SELECT COUNT(*) FROM org_memberships WHERE org_id = ? AND role = 'owner'", orgID).Scan(&owners); err != nil {
			return err
		}
		if owners <= 1 {
			return ErrLastOwner
		}
	}
	return nil
}

// SetRole changes target's role in orgID, as actor.
func (a *Accounts) SetRole(actor, orgID, target int64, role Role) error {
	return a.setRole(actor, false, orgID, target, role)
}

func (a *Accounts) setRole(actor int64, platform bool, orgID, target int64, role Role) error {
	if !role.Valid() {
		return ErrBadRole
	}
	return inTx(a.db, func(tx *sql.Tx) error {
		if err := changeMemberAs(tx, actor, platform, orgID, target, role); err != nil {
			return err
		}
		_, err := tx.Exec("UPDATE org_memberships SET role = ? WHERE user_id = ? AND org_id = ?", role, target, orgID)
		return err
	})
}

// RemoveMember takes target out of orgID, as actor (who may be target,
// leaving), with their general chat in the org, their own chats about
// the org's projects and their plans there. Their sessions in that org move on at their next
// request. Their queued tasks there go back to Drafts, as no runner would take them any more
// (see orphanedQueued); Dash's own tasks that were theirs stay queued, for the next member
// (DashWorkFor).
func (a *Accounts) RemoveMember(actor, orgID, target int64) error {
	return a.removeMember(actor, false, orgID, target)
}

func (a *Accounts) removeMember(actor int64, platform bool, orgID, target int64) error {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	orphans := a.s.orphanedQueuedLocked(orgID, target)
	err := inTx(a.db, func(tx *sql.Tx) error {
		for _, t := range orphans {
			if err := putTodo(tx, t); err != nil {
				return err
			}
		}
		if err := changeMemberAs(tx, actor, platform, orgID, target, ""); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM org_memberships WHERE user_id = ? AND org_id = ?", target, orgID); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM chat_messages WHERE kind = ? AND owner_id = ?", chatMain, mainChatOwner(orgID, target)); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM plans WHERE org_id = ? AND user_id = ?", orgID, target); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM chat_messages WHERE kind = ? AND org_id = ? AND owner_id LIKE '%:' || ?", chatProject, orgID, target); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM past_chats WHERE org_id = ? AND user_id = ?", orgID, target)
		return err
	})
	if err != nil {
		return err
	}
	delete(a.s.data.MainChats, mainChatOwner(orgID, target))
	suffix := fmt.Sprintf(":%d", target)
	for owner := range a.s.data.ProjectChats {
		if pid, ok := strings.CutSuffix(owner, suffix); ok && a.s.projectOrgLocked(pid) == orgID {
			delete(a.s.data.ProjectChats, owner)
		}
	}
	for _, t := range orphans {
		a.s.data.Todos[t.ID] = t
		a.s.emit(model.EvTodoUpdated, t.ProjectID, t.ID, *t)
	}
	return nil
}

// orphanedQueuedLocked is, changed but not stored, the queued tasks of
// orgID's projects that only target's runners (or shared ones) would take:
// those target made, or started a run of. Removing target from the org
// would leave them waiting for good, so they go back to Drafts.
// Dash's own tasks given to target (DashWorkFor) stay queued, given to
// nobody, so the next member takes them. The caller holds the lock.
func (s *Store) orphanedQueuedLocked(orgID, target int64) []*model.Todo {
	if target <= 0 {
		return nil
	}
	var out []*model.Todo
	now := time.Now()
	for _, t := range s.data.Todos {
		if t.Status != model.StatusQueued || s.projectOrgLocked(t.ProjectID) != orgID {
			continue
		}
		if t.CreatedBy != target && t.RunBy != target {
			continue
		}
		cp := clone(*t)
		cp.UpdatedAt = now
		if cp.CreatedBy == 0 {
			cp.RunBy = 0 // Dash's own: DashWorkFor gives it to someone else
		} else {
			cp.Status, cp.Error = model.StatusDraft, ""
			cp.Paused, cp.Waiting, cp.StartNow, cp.Rejoin = false, "", false, false
		}
		out = append(out, &cp)
	}
	return out
}

// ---- invitations ----

// InvitationTTL is how long an invitation link works.
const InvitationTTL = 7 * 24 * time.Hour

// InviteWith invites email to orgID with role, as actor (an admin or owner,
// who can't grant more than their own role), and returns the link's token.
// anyEmail says whether a Google sign-in accepting it may be another address
// than email (see Invitation.AnyEmail). It doesn't look at whether email has
// an account. Inviting the same address again replaces the earlier
// invitation, whose link stops working.
func (a *Accounts) InviteWith(actor, orgID int64, email string, role Role, anyEmail bool) (string, Invitation, error) {
	var inv Invitation
	email, err := cleanEmail(email)
	if err != nil {
		return "", inv, err
	}
	if !role.Valid() {
		return "", inv, ErrBadRole
	}
	tok := rand.Text()
	now := time.Now()
	err = inTx(a.db, func(tx *sql.Tx) error {
		mine, err := membership(tx, actor, orgID)
		if err != nil {
			return err
		}
		if !mine.Role.AtLeast(RoleAdmin) || !mine.Role.AtLeast(role) {
			return ErrForbidden
		}
		if mine.Personal {
			return ErrPersonalOrg
		}
		inv.Org = mine.Org
		return tx.QueryRow(`INSERT INTO org_invitations (org_id, email, role, token_hash, invited_by, created_at, expires_at, any_email)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (org_id, email) DO UPDATE SET role = excluded.role, token_hash = excluded.token_hash,
				invited_by = excluded.invited_by, created_at = excluded.created_at, expires_at = excluded.expires_at,
				any_email = excluded.any_email
			RETURNING id`, orgID, email, role, TokenHash(tok), actor, now.Unix(), now.Add(InvitationTTL).Unix(), anyEmail).Scan(&inv.ID)
	})
	if err != nil {
		return "", inv, err
	}
	inv.Email, inv.Role, inv.AnyEmail = email, role, anyEmail
	inv.Created, inv.Expires = time.Unix(now.Unix(), 0).UTC(), time.Unix(now.Add(InvitationTTL).Unix(), 0).UTC()
	return tok, inv, nil
}

const invitationCols = `SELECT i.id, o.id, o.name, o.slug, i.email, i.role, COALESCE(u.username, ''), i.created_at, i.expires_at, i.any_email
	FROM org_invitations i JOIN organizations o ON o.id = i.org_id LEFT JOIN users u ON u.id = i.invited_by`

func scanInvitation(sc scanner) (Invitation, error) {
	var inv Invitation
	var created, expires int64
	err := sc.Scan(&inv.ID, &inv.Org.ID, &inv.Org.Name, &inv.Org.Slug, &inv.Email, &inv.Role, &inv.InvitedBy, &created, &expires, &inv.AnyEmail)
	inv.Created, inv.Expires = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC()
	return inv, err
}

// Invitations lists an org's pending invitations, newest first. Expired ones
// are deleted.
func (a *Accounts) Invitations(orgID int64) ([]Invitation, error) {
	if _, err := a.db.Exec("DELETE FROM org_invitations WHERE expires_at <= ?", time.Now().Unix()); err != nil {
		return nil, err
	}
	return queryAll(a.db, scanInvitation, invitationCols+" WHERE i.org_id = ? ORDER BY i.created_at DESC, i.id DESC", orgID)
}

// RevokeInvitation deletes a pending invitation of orgID, as actor (an admin
// or owner).
func (a *Accounts) RevokeInvitation(actor, orgID, id int64) error {
	return inTx(a.db, func(tx *sql.Tx) error {
		mine, err := membership(tx, actor, orgID)
		if err != nil {
			return err
		}
		if !mine.Role.AtLeast(RoleAdmin) {
			return ErrForbidden
		}
		return execOne(tx, ErrNotFound, "DELETE FROM org_invitations WHERE id = ? AND org_id = ?", id, orgID)
	})
}

func invitationByToken(q querier, tok string, now time.Time) (Invitation, error) {
	inv, err := scanInvitation(q.QueryRow(invitationCols+" WHERE i.token_hash = ?", TokenHash(tok)))
	if errors.Is(err, sql.ErrNoRows) || err == nil && !now.Before(inv.Expires) {
		return inv, ErrInvitation
	}
	return inv, err
}

// InvitationByToken is the pending invitation a link's token stands for.
func (a *Accounts) InvitationByToken(tok string) (Invitation, error) {
	return invitationByToken(a.db, tok, time.Now())
}

// AcceptInvitation makes uid a member of the invitation's org and uses the
// invitation up. Someone who already is a member keeps their role.
func (a *Accounts) AcceptInvitation(tok string, uid int64) (Membership, error) {
	var m Membership
	err := inTx(a.db, func(tx *sql.Tx) error {
		inv, err := invitationByToken(tx, tok, time.Now())
		if err != nil {
			return err
		}
		if m, err = accept(tx, inv, uid); err != nil {
			return err
		}
		return nil
	})
	return m, err
}

func accept(tx *sql.Tx, inv Invitation, uid int64) (Membership, error) {
	if _, err := tx.Exec(`INSERT INTO org_memberships (user_id, org_id, role, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, org_id) DO NOTHING`, uid, inv.Org.ID, inv.Role, time.Now().Unix()); err != nil {
		return Membership{}, err
	}
	if _, err := tx.Exec("DELETE FROM org_invitations WHERE id = ?", inv.ID); err != nil {
		return Membership{}, err
	}
	return membership(tx, uid, inv.Org.ID)
}

// RegisterWithInvitation creates an account and accepts the invitation with
// it. The account gets the invitation's email unless another account has
// that address already, which it doesn't say.
func (a *Accounts) RegisterWithInvitation(tok, username, password string) (User, Membership, error) {
	username = strings.TrimSpace(username)
	if !ValidUsername(username) {
		return User{}, Membership{}, ErrBadUsername
	}
	if len(password) < MinPassword {
		return User{}, Membership{}, ErrWeakPassword
	}
	if _, err := a.InvitationByToken(tok); err != nil { // before the slow hash
		return User{}, Membership{}, err
	}
	hash, err := passhash.Hash(password)
	if err != nil {
		return User{}, Membership{}, err
	}
	var u User
	var m Membership
	err = inTx(a.db, func(tx *sql.Tx) error {
		inv, err := invitationByToken(tx, tok, time.Now())
		if err != nil {
			return err
		}
		var email any = inv.Email
		var taken bool
		if err := tx.QueryRow("SELECT EXISTS (SELECT 1 FROM users WHERE email = ?)", inv.Email).Scan(&taken); err != nil {
			return err
		}
		if taken {
			email = nil
		}
		now := time.Now()
		res, err := tx.Exec(`INSERT INTO users (username, email, password_hash, password_changed_at, created_at) VALUES (?, ?, ?, ?, ?)`,
			username, email, hash, now.UnixNano(), now.Unix())
		if isUnique(err) {
			return ErrUsernameTaken
		}
		if err != nil {
			return err
		}
		u.ID, _ = res.LastInsertId()
		u.Username = username
		if !taken {
			u.Email = inv.Email
		}
		m, err = accept(tx, inv, u.ID)
		return err
	})
	if err != nil {
		return User{}, Membership{}, err
	}
	return u, m, nil
}

// DeclineInvitation deletes the invitation a link's token stands for.
func (a *Accounts) DeclineInvitation(tok string) error {
	return execOne(a.db, ErrInvitation, "DELETE FROM org_invitations WHERE token_hash = ?", TokenHash(tok))
}

// PlannerContext reports whether org's planners' feature brief interviews
// are sent their project's context (CLAUDE.md, README, file tree): on
// unless an owner turned it off (SetPlannerContext).
func (a *Accounts) PlannerContext(orgID int64) (bool, error) {
	var off bool
	err := a.db.QueryRow("SELECT no_project_context FROM org_planner_settings WHERE org_id = ?", orgID).Scan(&off)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return !off, err
}

// SetPlannerContext turns sending org's planners' feature brief interviews
// their project's context on or off, as actor, who must be an owner.
func (a *Accounts) SetPlannerContext(actor, orgID int64, on bool) error {
	return inTx(a.db, func(tx *sql.Tx) error {
		mine, err := membership(tx, actor, orgID)
		if err != nil {
			return err
		}
		if mine.Role != RoleOwner {
			return ErrForbidden
		}
		_, err = tx.Exec(`INSERT INTO org_planner_settings (org_id, no_project_context) VALUES (?, ?)
			ON CONFLICT (org_id) DO UPDATE SET no_project_context = excluded.no_project_context`, orgID, !on)
		return err
	})
}
