package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/klahr/dash/internal/passhash"
)

// Open signup, email verification, password reset by email and personal
// workspaces. Callers decide whether signup is on; these only keep the
// records.

var (
	// ErrEmailLink covers unknown, used and expired verification and reset
	// links alike.
	ErrEmailLink = errors.New("this link is not valid any more; ask for a new one")
	// ErrUnverified: the account's email address hasn't been confirmed yet.
	ErrUnverified = errors.New("confirm your email address first: open the link we sent you")
)

// How long the links sent by email work.
const (
	VerifyTTL = 24 * time.Hour
	ResetTTL  = time.Hour
)

// EmailPurpose is what an emailed link is for.
type EmailPurpose string

const (
	PurposeVerify EmailPurpose = "verify"
	PurposeReset  EmailPurpose = "reset"
)

// SignupResult is what SignUp did. Exactly one of Created, Pending and
// Existing is set (unless it failed); callers answer the same in every case,
// and tell the address's owner by email instead.
type SignupResult struct {
	User User
	// Token is the verification link's token (Created and Pending).
	Token string
	// Created: a new, unverified account.
	Created bool
	// Pending: an unverified account had the address already; Token is a new
	// link for it, replacing the earlier one.
	Pending bool
	// Existing: a confirmed account has the address.
	Existing bool
}

// CleanEmail trims email and checks that it looks like an address.
func CleanEmail(email string) (string, error) { return cleanEmail(email) }

// SignUp creates an unverified account for email, with a username made
// from the address. It has no password yet: whoever opens the link sent to
// the address chooses it (VerifyEmail), so nobody can sign up for someone
// else's address beforehand and keep a password they know. Until then its
// hash is of a random secret, so no password signs it in. A hash is made
// whatever happens, so the answer takes as long whether or not the address
// has an account. Unverified accounts whose links have expired are deleted.
func (a *Accounts) SignUp(email string, now time.Time) (SignupResult, error) {
	var res SignupResult
	email, err := cleanEmail(email)
	if err != nil {
		return res, err
	}
	hash, err := passhash.Hash(rand.Text())
	if err != nil {
		return res, err
	}
	err = inTx(a.db, func(tx *sql.Tx) error {
		if err := deleteStaleUnverified(tx, now); err != nil {
			return err
		}
		var unverified bool
		var err error
		res.User, err = scanUser(tx.QueryRow(userCols+" WHERE email = ?", email))
		switch {
		case err == nil:
			if err := tx.QueryRow("SELECT unverified FROM users WHERE id = ?", res.User.ID).Scan(&unverified); err != nil {
				return err
			}
			if !unverified {
				res.Existing = true
				return nil
			}
			res.Pending = true
		case errors.Is(err, ErrNotFound):
			if res.User, err = createUnverified(tx, email, hash, now); err != nil {
				return err
			}
			res.Created = true
		default:
			return err
		}
		res.Token, err = newEmailToken(tx, res.User.ID, PurposeVerify, now)
		return err
	})
	if err != nil {
		return SignupResult{}, err
	}
	return res, nil
}

// PruneUnverified deletes signups that were never confirmed and whose links
// have all expired, so an address isn't kept after its last link expires
// just because nobody signed up since.
func (a *Accounts) PruneUnverified(now time.Time) error {
	return inTx(a.db, func(tx *sql.Tx) error { return deleteStaleUnverified(tx, now) })
}

// deleteStaleUnverified deletes the accounts that signed up longer ago than
// a link lasts and have no link that still works (nor any membership).
func deleteStaleUnverified(tx *sql.Tx, now time.Time) error {
	_, err := tx.Exec(`DELETE FROM users WHERE unverified = 1 AND env_managed = 0 AND created_at <= ?
		AND NOT EXISTS (SELECT 1 FROM email_tokens t WHERE t.user_id = users.id AND t.expires_at > ?)
		AND NOT EXISTS (SELECT 1 FROM org_memberships m WHERE m.user_id = users.id)`,
		now.Add(-VerifyTTL).Unix(), now.Unix())
	return err
}

func createUnverified(tx *sql.Tx, email, hash string, now time.Time) (User, error) {
	id, name, err := insertUserWithFreeName(tx, email, hash, "unverified", 1, now)
	if err != nil {
		return User{}, err
	}
	return User{ID: id, Username: name, Email: email}, nil
}

// insertUserWithFreeName adds a user for email, with col (a users column,
// not from input) set to val. Its username is made from email, with a
// number added when another user has it.
func insertUserWithFreeName(tx *sql.Tx, email, hash, col string, val int, now time.Time) (id int64, name string, err error) {
	base := usernameFrom(email)
	name = base
	for i := 2; ; i++ {
		res, err := tx.Exec(`INSERT INTO users (username, email, password_hash, `+col+`, password_changed_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, name, email, hash, val, now.UnixNano(), now.Unix())
		if isUnique(err) && !strings.Contains(err.Error(), "users.email") && i < 10000 {
			name = base + strconv.Itoa(i)
			continue
		}
		if err != nil {
			return 0, "", err
		}
		id, err = res.LastInsertId()
		return id, name, err
	}
}

// usernameFrom is a username for email: its local part without a "+tag",
// with what usernames can't have left out.
func usernameFrom(email string) string {
	local, _, _ := strings.Cut(email, "@")
	local, _, _ = strings.Cut(local, "+") // ann+dash@ is ann
	var b strings.Builder
	for _, r := range local {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-", r) {
			b.WriteRune(r)
		}
		if b.Len() >= 48 {
			break
		}
	}
	if b.Len() == 0 {
		return "user"
	}
	return b.String()
}

// newEmailToken makes a link token for uid, replacing the user's earlier
// links for the same purpose.
func newEmailToken(tx *sql.Tx, uid int64, purpose EmailPurpose, now time.Time) (string, error) {
	ttl := VerifyTTL
	if purpose == PurposeReset {
		ttl = ResetTTL
	}
	if _, err := tx.Exec("DELETE FROM email_tokens WHERE user_id = ? AND purpose = ?", uid, purpose); err != nil {
		return "", err
	}
	if _, err := tx.Exec("DELETE FROM email_tokens WHERE expires_at <= ?", now.Unix()); err != nil {
		return "", err
	}
	tok := rand.Text()
	_, err := tx.Exec("INSERT INTO email_tokens (token_hash, user_id, purpose, created_at, expires_at) VALUES (?, ?, ?, ?, ?)",
		TokenHash(tok), uid, purpose, now.Unix(), now.Add(ttl).Unix())
	return tok, err
}

// useEmailToken redeems tok for purpose: it returns its user and deletes it.
func useEmailToken(tx *sql.Tx, tok string, purpose EmailPurpose, now time.Time) (int64, error) {
	var uid int64
	err := tx.QueryRow("DELETE FROM email_tokens WHERE token_hash = ? AND purpose = ? AND expires_at > ? RETURNING user_id",
		TokenHash(tok), purpose, now.Unix()).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrEmailLink
	}
	return uid, err
}

// checkEmailToken reports whether tok is a link for purpose that still
// works, without using it up.
func (a *Accounts) checkEmailToken(tok string, purpose EmailPurpose, now time.Time) error {
	var ok bool
	err := a.db.QueryRow("SELECT EXISTS (SELECT 1 FROM email_tokens WHERE token_hash = ? AND purpose = ? AND expires_at > ?)",
		TokenHash(tok), purpose, now.Unix()).Scan(&ok)
	if err == nil && !ok {
		return ErrEmailLink
	}
	return err
}

// ResendVerification is a new verification link for the unverified account
// with email; ok is false when there's none (no account, or it's confirmed).
func (a *Accounts) ResendVerification(email string, now time.Time) (u User, tok string, ok bool, err error) {
	email, err = cleanEmail(email)
	if err != nil {
		return u, "", false, err
	}
	err = inTx(a.db, func(tx *sql.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRow(userCols+" WHERE email = ? AND unverified = 1", email))
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		ok = true
		tok, err = newEmailToken(tx, u.ID, PurposeVerify, now)
		return err
	})
	return u, tok, ok && err == nil, err
}

// VerifyEmail confirms the address of the account tok was sent to, sets its
// password (chosen now, by whoever has the link: the address's owner), so
// it can sign in, and gives it its personal workspace.
func (a *Accounts) VerifyEmail(tok, password string, now time.Time) (User, error) {
	if len(password) < MinPassword {
		return User{}, ErrWeakPassword
	}
	if err := a.checkEmailToken(tok, PurposeVerify, now); err != nil { // before the slow hash
		return User{}, err
	}
	hash, err := passhash.Hash(password)
	if err != nil {
		return User{}, err
	}
	var u User
	err = inTx(a.db, func(tx *sql.Tx) error {
		uid, err := useEmailToken(tx, tok, PurposeVerify, now)
		if err != nil {
			return err
		}
		res, err := tx.Exec("UPDATE users SET unverified = 0, password_hash = ?, password_changed_at = ? WHERE id = ? AND unverified = 1 AND env_managed = 0",
			hash, now.UnixNano(), uid)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrEmailLink
		}
		if _, err := tx.Exec("DELETE FROM email_tokens WHERE user_id = ? AND purpose = ?", uid, PurposeVerify); err != nil {
			return err
		}
		if u, err = scanUser(tx.QueryRow(userCols+" WHERE id = ?", uid)); err != nil {
			return err
		}
		_, err = a.ensurePersonalOrg(tx, u, now)
		return err
	})
	return u, err
}

// Unverified reports whether uid signed up and hasn't confirmed their
// address yet.
func (a *Accounts) Unverified(uid int64) (bool, error) {
	var v bool
	err := a.db.QueryRow("SELECT unverified FROM users WHERE id = ?", uid).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return v, err
}

// RequestPasswordReset is a reset link for the account with email; ok is
// false when there's none to send. Unverified accounts get none (they need
// the verification link), nor does the env account, whose password comes
// from DASH_PASSWORD and would be put back at the next start.
func (a *Accounts) RequestPasswordReset(email string, now time.Time) (u User, tok string, ok bool, err error) {
	email, err = cleanEmail(email)
	if err != nil {
		return u, "", false, err
	}
	err = inTx(a.db, func(tx *sql.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRow(userCols+" WHERE email = ? AND unverified = 0 AND env_managed = 0", email))
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		ok = true
		tok, err = newEmailToken(tx, u.ID, PurposeReset, now)
		return err
	})
	return u, tok, ok && err == nil, err
}

// CheckResetToken reports whether tok is a reset link that still works,
// without using it up.
func (a *Accounts) CheckResetToken(tok string, now time.Time) error {
	return a.checkEmailToken(tok, PurposeReset, now)
}

// ResetPassword sets the password of the account a reset link was sent to,
// and signs out its sessions, API tokens and connectors, as a password change
// does. Two-factor sign-in stays on.
//
// It also unlinks the account's Google (and other provider) sign-ins. The
// link is the first proof some accounts get that their owner holds the
// address: one registered through an invitation got an address the inviter
// typed, and whoever registered it could have linked their own Google
// account, which would otherwise outlast the owner taking it over. For the
// same reason it removes the account's personal webhooks and push
// subscriptions. The owner sets up their own again.
func (a *Accounts) ResetPassword(tok, password string, now time.Time) (User, error) {
	if len(password) < MinPassword {
		return User{}, ErrWeakPassword
	}
	if err := a.CheckResetToken(tok, now); err != nil { // before the slow hash
		return User{}, err
	}
	hash, err := passhash.Hash(password)
	if err != nil {
		return User{}, err
	}
	var u User
	err = inTx(a.db, func(tx *sql.Tx) error {
		uid, err := useEmailToken(tx, tok, PurposeReset, now)
		if err != nil {
			return err
		}
		// Asked for a new password by an incident response: not the one
		// that may have leaked.
		var need bool
		var old string
		if err := tx.QueryRow("SELECT "+needsPassword+", u.password_hash FROM users u WHERE u.id = ?", uid).Scan(&need, &old); err != nil {
			return err
		}
		if need {
			if same, _, _ := passhash.Verify(old, password); same {
				return ErrSamePassword
			}
		}
		if _, err := tx.Exec("UPDATE users SET password_hash = ?, has_password = 1, password_changed_at = ? WHERE id = ? AND env_managed = 0",
			hash, now.UnixNano(), uid); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM sessions WHERE user_id = ?", uid); err != nil {
			return err
		}
		// The same goes for where the account's notifications go: its
		// personal webhooks (which get every event of its orgs' projects)
		// and its browsers' push subscriptions.
		for _, q := range []string{
			"DELETE FROM user_identities WHERE user_id = ?",
			"DELETE FROM webhooks WHERE user_id = ? AND project_id = ''",
			"DELETE FROM push_subscriptions WHERE user_id = ?",
		} {
			if _, err := tx.Exec(q, uid); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("DELETE FROM email_tokens WHERE user_id = ? AND purpose = 'reset'", uid); err != nil {
			return err
		}
		// A new password is what an incident response asked for.
		if _, err := tx.Exec("UPDATE user_lockdown SET new_password = 0 WHERE user_id = ?", uid); err != nil {
			return err
		}
		u, err = scanUser(tx.QueryRow(userCols+" WHERE id = ?", uid))
		return err
	})
	return u, err
}

// ---- personal workspaces ----

// EnsurePersonalOrg gives uid a personal workspace if they have none yet:
// a new org with them as its owner and only member. The bootstrapped
// "Default" org is never turned into one.
func (a *Accounts) EnsurePersonalOrg(uid int64) (Org, error) {
	var o Org
	err := inTx(a.db, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRow(userCols+" WHERE id = ?", uid))
		if err != nil {
			return err
		}
		o, err = a.ensurePersonalOrg(tx, u, time.Now())
		return err
	})
	return o, err
}

func (a *Accounts) ensurePersonalOrg(tx *sql.Tx, u User, now time.Time) (Org, error) {
	o, err := scanOrg(tx.QueryRow(orgCols+" WHERE o.personal_user_id = ?", u.ID))
	if err == nil {
		return o, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return o, err
	}
	created, err := a.createOrg(tx, u.ID, u.Username+"'s workspace", now)
	if err != nil {
		return o, err
	}
	if _, err := tx.Exec("UPDATE organizations SET personal_user_id = ? WHERE id = ?", u.ID, created.ID); err != nil {
		return o, err
	}
	created.Personal = true
	return created, nil
}

// ---- per org ----

// ForOrg is the store as org sees it (see Store.ForOrg).
func (a *Accounts) ForOrg(org int64) *OrgStore { return a.s.ForOrg(org) }

// MemberCount is how many members the org has (for billing per seat).
func (o *OrgStore) MemberCount() (int, error) {
	if o.org == 0 {
		return 0, ErrNotFound
	}
	var n int
	err := o.s.db.QueryRow("SELECT COUNT(*) FROM org_memberships WHERE org_id = ?", o.org).Scan(&n)
	return n, err
}

// Personal reports whether the org is someone's personal workspace.
func (o *OrgStore) Personal() (bool, error) {
	var p bool
	err := o.s.db.QueryRow("SELECT personal_user_id IS NOT NULL FROM organizations WHERE id = ?", o.org).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return p, err
}
