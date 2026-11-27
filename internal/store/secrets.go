package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ---- secrets sealed at rest ----
//
// Secrets Dash must use in the clear (authenticator secrets, webhook
// secrets and tokens, GitHub webhook secrets, board-backend credentials and
// webhook secrets, the VAPID and notify keys) are sealed with the hub's
// secret key (auth.Sealer) before they reach the database, and opened when
// read: nothing outside the store sees a sealed value. Each is bound to its
// row (its context), so a sealed value copied elsewhere doesn't open.
//
// Without a key (a runner, a command-line tool, before SetSealer) values
// are written as they are and plaintext ones read as they are, as before
// sealing; a sealed one gives ErrSealedNoKey. SealSecrets seals the
// plaintext rows once a key is set.

// SecretSealer seals and opens secrets: *auth.Sealer.
type SecretSealer interface {
	Seal(plain, context string) (string, error)
	Open(sealed, context string) (string, error)
}

// sealedPrefix starts every sealed value (auth.Sealer's format marker).
const sealedPrefix = "v1:"

// IsSealed reports whether v is a sealed value rather than plaintext.
func IsSealed(v string) bool { return strings.HasPrefix(v, sealedPrefix) }

// ErrSealedNoKey is reading a sealed secret without the key.
var ErrSealedNoKey = errors.New("this secret is encrypted with the hub's secret key, which this Dash wasn't given (DASH_SECRET_KEY or <data>/secret.key)")

// SetSealer seals the secrets written from now on with sl, and opens the
// sealed ones read. Call it before serving, then SealSecrets.
func (s *Store) SetSealer(sl SecretSealer) { s.sealer = sl }

// seal is plain as stored for context: sealed when there's a key.
func (s *Store) seal(plain, context string) (string, error) {
	if s.sealer == nil || plain == "" {
		return plain, nil
	}
	return s.sealer.Seal(plain, context)
}

// open is the plaintext of v, stored for context.
func (s *Store) open(v, context string) (string, error) {
	if !IsSealed(v) {
		return v, nil
	}
	if s.sealer == nil {
		return "", ErrSealedNoKey
	}
	plain, err := s.sealer.Open(v, context)
	if err != nil {
		return "", fmt.Errorf("%s: %w", context, err)
	}
	return plain, nil
}

// The contexts each secret is sealed for: its table, column and row.
func totpContext(uid int64) string           { return fmt.Sprintf("users.totp:%d", uid) }
func webhookSecretContext(id string) string  { return "webhooks.secret:" + id }
func webhookTokenContext(id string) string   { return "webhooks.token:" + id }
func trackerCredsContext(pid string) string  { return "tracker_projects.credentials:" + pid }
func trackerSecretContext(pid string) string { return "tracker_projects.webhook_secret:" + pid }
func githubHookContext(org int64, repo string) string {
	return fmt.Sprintf("github_hooks.secret:%d:%s", org, strings.ToLower(repo))
}
func singletonContext(table, col string) string { return table + "." + col }

// sealColumn is one column of secrets SealSecrets goes through: key selects
// the row's key columns (as text), ctx makes the context from them.
type sealColumn struct {
	table, col, key string
	ctx             func(keys []string) string
}

var sealColumns = []sealColumn{
	{"users", "totp_secret", "CAST(id AS TEXT)", func(k []string) string { return "users.totp:" + k[0] }},
	{"users", "totp_pending", "CAST(id AS TEXT)", func(k []string) string { return "users.totp:" + k[0] }},
	{"webhooks", "secret", "id", func(k []string) string { return webhookSecretContext(k[0]) }},
	{"webhooks", "token", "id", func(k []string) string { return webhookTokenContext(k[0]) }},
	{"github_hooks", "secret", "CAST(org_id AS TEXT), repo", func(k []string) string { return "github_hooks.secret:" + k[0] + ":" + strings.ToLower(k[1]) }},
	{"tracker_projects", "credentials", "project_id", func(k []string) string { return trackerCredsContext(k[0]) }},
	{"tracker_projects", "webhook_secret", "project_id", func(k []string) string { return trackerSecretContext(k[0]) }},
	{"push_vapid", "private_key", "CAST(id AS TEXT)", func([]string) string { return singletonContext("push_vapid", "private_key") }},
	{"notify_key", "key", "CAST(id AS TEXT)", func([]string) string { return singletonContext("notify_key", "key") }},
}

// noCreds is how a board backend without credentials is stored: plaintext,
// there's nothing to hide.
const noCreds = "{}"

// SealSecrets seals the secrets still stored in plaintext (from before
// sealing, or written without a key) and reports how many, and how many
// sealed ones don't open with the key (it changed). Sealed ones are left as
// they are. Without a key it does nothing.
func (s *Store) SealSecrets() (sealed, unreadable int, err error) {
	if s.sealer == nil {
		return 0, 0, nil
	}
	err = inTx(s.db, func(tx *sql.Tx) error {
		for _, c := range sealColumns {
			n, bad, err := s.sealColumn(tx, c)
			if err != nil {
				return fmt.Errorf("seal %s.%s: %w", c.table, c.col, err)
			}
			sealed += n
			unreadable += bad
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return sealed, unreadable, nil
}

// sealColumn seals c's plaintext values, and counts the sealed ones that
// don't open.
func (s *Store) sealColumn(tx *sql.Tx, c sealColumn) (sealed, unreadable int, err error) {
	nkeys := strings.Count(c.key, ",") + 1
	rows, err := tx.Query("SELECT rowid, " + c.key + ", " + c.col + " FROM " + c.table +
		" WHERE " + c.col + " IS NOT NULL AND " + c.col + " NOT IN ('', '" + noCreds + "')")
	if err != nil {
		return 0, 0, err
	}
	type plainRow struct {
		rowid int64
		ctx   string
		plain string
	}
	var todo []plainRow
	for rows.Next() {
		var r plainRow
		keys := make([]string, nkeys)
		dest := []any{&r.rowid}
		for i := range keys {
			dest = append(dest, &keys[i])
		}
		if err := rows.Scan(append(dest, &r.plain)...); err != nil {
			rows.Close()
			return 0, 0, err
		}
		r.ctx = c.ctx(keys)
		if !IsSealed(r.plain) {
			todo = append(todo, r)
		} else if _, err := s.sealer.Open(r.plain, r.ctx); err != nil {
			unreadable++
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	for _, r := range todo {
		v, err := s.sealer.Seal(r.plain, r.ctx)
		if err != nil {
			return 0, 0, err
		}
		if _, err := tx.Exec("UPDATE "+c.table+" SET "+c.col+" = ? WHERE rowid = ?", v, r.rowid); err != nil {
			return 0, 0, err
		}
	}
	return len(todo), unreadable, nil
}
