package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// TokenKind is what a token is for. Every kind acts as one user in one org.
type TokenKind string

const (
	// TokenAPI is a token a user made in Settings, for the API and /mcp.
	TokenAPI TokenKind = "api"
	// TokenOAuth is the grant an OAuth client got when the user approved it:
	// an access token for /mcp, renewed with a rotating refresh token.
	TokenOAuth TokenKind = "oauth"
	// TokenRunner lets one runner (`dash runner`) connect its tunnel.
	TokenRunner TokenKind = "runner"
)

// Scopes. A token's scopes only narrow what its user may do: read is the
// API's GET requests, write everything else. Runner tokens have ScopeRunner.
const (
	ScopeRead   = "read"
	ScopeWrite  = "write"
	ScopeRunner = "runner"
)

// UserScopes are the scopes of API and OAuth tokens.
var UserScopes = []string{ScopeRead, ScopeWrite}

var (
	ErrNoOrg        = errors.New("a token must be pinned to an organization")
	ErrBadScopes    = errors.New("scopes must be read and/or write")
	ErrBadTokenName = errors.New("a token needs a name (up to 100 characters)")
	// ErrTokenInvalid covers unknown, expired and revoked tokens alike, and
	// those whose user has left the token's org or changed password.
	ErrTokenInvalid = errors.New("unknown, expired or revoked token")
)

// Token is a stored token, as its owner sees it (never the secret).
type Token struct {
	ID     int64     `json:"id"`
	Kind   TokenKind `json:"kind"`
	Name   string    `json:"name"`
	Prefix string    `json:"prefix"`
	UserID int64     `json:"user_id"`
	Org    Org       `json:"org"`
	Scopes []string  `json:"scopes"`
	// Created is when the token (or OAuth grant) was made; a password change
	// after it ends API and OAuth tokens.
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"last_used,omitzero"`
	// Expires is when the token stops working: for an OAuth grant, its
	// refresh token's expiry. Zero: until revoked.
	Expires  time.Time `json:"expires,omitzero"`
	ClientID string    `json:"client_id,omitempty"`
	RunnerID string    `json:"runner_id,omitempty"`
}

// HasScope reports whether t was given scope.
func (t Token) HasScope(scope string) bool { return slices.Contains(t.Scopes, scope) }

// NewToken describes a token to create with CreateToken.
type NewToken struct {
	Kind     TokenKind // TokenAPI or TokenRunner
	Name     string
	UserID   int64
	OrgID    int64 // required: the org the token acts in
	Scopes   []string
	Expires  time.Time // zero: until revoked
	RunnerID string    // TokenRunner
}

// secret prefixes, so a leaked token is recognisable.
const (
	prefixAPI     = "dash_"
	prefixAccess  = "dasho_"
	prefixRefresh = "dashr_"
	prefixRunner  = "dr_"
	prefixShown   = 12 // characters of the secret kept to tell tokens apart
)

func cleanScopes(kind TokenKind, scopes []string) ([]string, error) {
	if kind == TokenRunner {
		return []string{ScopeRunner}, nil
	}
	var out []string
	for _, s := range scopes {
		if !slices.Contains(UserScopes, s) {
			return nil, ErrBadScopes
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, ErrBadScopes
	}
	slices.Sort(out)
	return out, nil
}

func cleanTokenName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return "", ErrBadTokenName
	}
	return name, nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

// CreateToken makes an API or runner token for n.UserID in n.OrgID, which
// they must belong to, and returns its secret: shown only now.
func (a *Accounts) CreateToken(n NewToken, now time.Time) (string, Token, error) {
	if n.OrgID == 0 {
		return "", Token{}, ErrNoOrg
	}
	prefix := prefixAPI
	switch n.Kind {
	case TokenAPI:
	case TokenRunner:
		prefix = prefixRunner
		if n.RunnerID == "" {
			return "", Token{}, errors.New("a runner token needs its runner")
		}
	default:
		return "", Token{}, errors.New("unknown token kind")
	}
	name, err := cleanTokenName(n.Name)
	if err != nil {
		return "", Token{}, err
	}
	scopes, err := cleanScopes(n.Kind, n.Scopes)
	if err != nil {
		return "", Token{}, err
	}
	secret := prefix + newToken()
	var id int64
	err = inTx(a.db, func(tx *sql.Tx) error {
		if _, err := membership(tx, n.UserID, n.OrgID); err != nil {
			return err
		}
		sc, _ := json.Marshal(scopes)
		var runner any
		if n.RunnerID != "" {
			runner = n.RunnerID
		}
		res, err := tx.Exec(`INSERT INTO tokens (kind, user_id, org_id, name, token_hash, token_prefix, scopes, created_at, expires_at, runner_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			n.Kind, n.UserID, n.OrgID, name, hashToken(secret), secret[:prefixShown], string(sc), now.UnixNano(), nullTime(n.Expires), runner)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		return "", Token{}, err
	}
	t, err := a.token(id)
	return secret, t, err
}

const tokenCols = `SELECT t.id, t.kind, t.name, t.token_prefix, t.user_id, o.id, o.name, o.slug, t.scopes, t.created_at,
	t.last_used_at, COALESCE(t.refresh_expires_at, t.expires_at), COALESCE(t.oauth_client_id, ''), COALESCE(t.runner_id, '')
	FROM tokens t JOIN organizations o ON o.id = t.org_id`

func scanToken(sc interface{ Scan(...any) error }) (Token, error) {
	var t Token
	var scopes string
	var created int64
	var used, expires sql.NullInt64
	err := sc.Scan(&t.ID, &t.Kind, &t.Name, &t.Prefix, &t.UserID, &t.Org.ID, &t.Org.Name, &t.Org.Slug, &scopes, &created,
		&used, &expires, &t.ClientID, &t.RunnerID)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	_ = json.Unmarshal([]byte(scopes), &t.Scopes)
	t.Created = time.Unix(0, created)
	if used.Valid {
		t.LastUsed = time.Unix(used.Int64, 0)
	}
	if expires.Valid {
		t.Expires = time.Unix(expires.Int64, 0)
	}
	return t, nil
}

func (a *Accounts) token(id int64) (Token, error) {
	return scanToken(a.db.QueryRow(tokenCols+" WHERE t.id = ?", id))
}

// Authenticate finds the token whose secret is secret, if it is one of
// kinds, and returns it with its user and their membership of the token's
// org. It fails with ErrTokenInvalid if the token is unknown or expired, if
// the user no longer belongs to the org (a token is never moved to another),
// or, for API and OAuth tokens, if they changed password since it was made.
// Success records when the token was last used.
func (a *Accounts) Authenticate(secret string, now time.Time, kinds ...TokenKind) (Token, Session, error) {
	if secret == "" {
		return Token{}, Session{}, ErrTokenInvalid
	}
	return a.AuthenticateHash(hashToken(secret), now, kinds...)
}

// TokenHash is what a secret is stored as, for AuthenticateHash.
func TokenHash(secret string) string { return hashToken(secret) }

// AuthenticateHash is Authenticate for a secret's TokenHash: an open
// connection checks its token again with it.
func (a *Accounts) AuthenticateHash(hash string, now time.Time, kinds ...TokenKind) (Token, Session, error) {
	var s Session
	var role string
	var expires sql.NullInt64
	var changed int64
	var required, orgRequires bool
	t, err := scanToken(&prefixScanner{a.db.QueryRow(`SELECT u.username, COALESCE(u.email, ''), u.is_platform_admin,
			u.totp_secret IS NOT NULL, `+requires2FA+`, o.require_2fa, u.password_changed_at,
			m.role, t.expires_at, `+strings.TrimPrefix(tokenCols, "SELECT ")+`
			JOIN users u ON u.id = t.user_id
			JOIN org_memberships m ON m.user_id = t.user_id AND m.org_id = t.org_id
			WHERE t.token_hash = ?`, hash),
		[]any{&s.User.Username, &s.User.Email, &s.User.PlatformAdmin, &s.User.TOTP, &required, &orgRequires, &changed, &role, &expires}})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			err = ErrTokenInvalid
		}
		return Token{}, s, err
	}
	switch {
	case !slices.Contains(kinds, t.Kind),
		expires.Valid && now.Unix() >= expires.Int64,
		t.Kind != TokenRunner && changed > t.Created.UnixNano():
		return Token{}, s, ErrTokenInvalid
	}
	s.User.ID = t.UserID
	t.Org.Require2FA = orgRequires
	s.Org = &Membership{Org: t.Org, Role: Role(role)}
	// An org of theirs requires two-factor sign-in they haven't set up; the
	// caller decides what that blocks (see auth.tokenIdentity).
	s.Needs2FA = !s.User.TOTP && required
	// Best effort, and at most once a minute per token, so requests don't
	// each take the write lock.
	if now.Sub(t.LastUsed) >= time.Minute {
		if _, err := a.db.Exec("UPDATE tokens SET last_used_at = ?1 WHERE id = ?2 AND (last_used_at IS NULL OR last_used_at < ?1 - 60)",
			now.Unix(), t.ID); err == nil {
			t.LastUsed = now
		}
	}
	return t, s, nil
}

// prefixScanner scans leading columns into pre before handing the rest on.
type prefixScanner struct {
	row *sql.Row
	pre []any
}

func (p *prefixScanner) Scan(dest ...any) error { return p.row.Scan(append(p.pre, dest...)...) }

// Tokens lists uid's tokens of every kind, newest first.
func (a *Accounts) Tokens(uid int64) ([]Token, error) {
	rows, err := a.db.Query(tokenCols+" WHERE t.user_id = ? ORDER BY t.created_at DESC, t.id DESC", uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken deletes uid's token id and returns it (ErrNotFound if it
// isn't theirs).
func (a *Accounts) RevokeToken(uid, id int64) (Token, error) {
	t, err := a.token(id)
	if err != nil {
		return t, err
	}
	if t.UserID != uid {
		return Token{}, ErrNotFound
	}
	_, err = a.db.Exec("DELETE FROM tokens WHERE id = ? AND user_id = ?", id, uid)
	return t, err
}

// DeleteRunnerToken deletes the token of the runner runnerID.
func (a *Accounts) DeleteRunnerToken(runnerID string) error {
	_, err := a.db.Exec("DELETE FROM tokens WHERE kind = 'runner' AND runner_id = ?", runnerID)
	return err
}

// RunnerOwner is whose a runner is: the user who registered it and the org
// its token is pinned to.
type RunnerOwner struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Org      Org    `json:"org"`
}

// RunnerOwners maps runner ids to their owners.
func (a *Accounts) RunnerOwners() (map[string]RunnerOwner, error) {
	rows, err := a.db.Query(`SELECT t.runner_id, u.id, u.username, o.id, o.name, o.slug FROM tokens t
		JOIN users u ON u.id = t.user_id JOIN organizations o ON o.id = t.org_id WHERE t.kind = 'runner'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]RunnerOwner{}
	for rows.Next() {
		var id string
		var o RunnerOwner
		if err := rows.Scan(&id, &o.UserID, &o.Username, &o.Org.ID, &o.Org.Name, &o.Org.Slug); err != nil {
			return nil, err
		}
		out[id] = o
	}
	return out, rows.Err()
}

// PruneTokens deletes expired tokens and OAuth grants.
func (a *Accounts) PruneTokens(now time.Time) error {
	_, err := a.db.Exec(`DELETE FROM tokens WHERE (kind = 'oauth' AND refresh_expires_at <= ?1)
		OR (kind != 'oauth' AND expires_at <= ?1)`, now.Unix())
	return err
}

// ---- OAuth ----

// OAuthClient is a client that registered itself (RFC 7591).
type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs []string
	AuthMethod   string
	SecretHash   string // "" for public clients
	Created      time.Time
}

// CreateOAuthClient stores a newly registered client, first forgetting
// clients registered over an hour ago that never got a grant once there are
// max. It fails with ErrTooManyClients if there are still max.
func (a *Accounts) CreateOAuthClient(c OAuthClient, max int, now time.Time) error {
	return inTx(a.db, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow("SELECT COUNT(*) FROM oauth_clients").Scan(&n); err != nil {
			return err
		}
		if n >= max {
			if _, err := tx.Exec(`DELETE FROM oauth_clients WHERE created_at < ?
				AND NOT EXISTS (SELECT 1 FROM tokens t WHERE t.oauth_client_id = oauth_clients.id)`, now.Add(-time.Hour).Unix()); err != nil {
				return err
			}
			if err := tx.QueryRow("SELECT COUNT(*) FROM oauth_clients").Scan(&n); err != nil {
				return err
			}
			if n >= max {
				return ErrTooManyClients
			}
		}
		uris, _ := json.Marshal(c.RedirectURIs)
		_, err := tx.Exec(`INSERT INTO oauth_clients (id, name, redirect_uris, auth_method, secret_hash, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, c.ID, c.Name, string(uris), c.AuthMethod, c.SecretHash, c.Created.Unix())
		return err
	})
}

// ErrTooManyClients: registration is full of clients in use.
var ErrTooManyClients = errors.New("too many registered clients")

// OAuthClient returns the client id, or ErrNotFound.
func (a *Accounts) OAuthClient(id string) (OAuthClient, error) {
	c := OAuthClient{ID: id}
	var uris string
	var created int64
	err := a.db.QueryRow("SELECT name, redirect_uris, auth_method, secret_hash, created_at FROM oauth_clients WHERE id = ?", id).
		Scan(&c.Name, &uris, &c.AuthMethod, &c.SecretHash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.Created = time.Unix(created, 0)
	return c, json.Unmarshal([]byte(uris), &c.RedirectURIs)
}

// Grant is what a user approved for an OAuth client.
type Grant struct {
	ClientID string
	UserID   int64
	OrgID    int64 // required
	Scopes   []string
}

// TokenPair is an OAuth access token and its refresh token.
type TokenPair struct {
	Access, Refresh string
	Scopes          []string
}

// CreateGrant stores an approved grant as an OAuth token and returns its
// first token pair.
func (a *Accounts) CreateGrant(g Grant, accessTTL, refreshTTL time.Duration, now time.Time) (TokenPair, error) {
	if g.OrgID == 0 {
		return TokenPair{}, ErrNoOrg
	}
	scopes, err := cleanScopes(TokenOAuth, g.Scopes)
	if err != nil {
		return TokenPair{}, err
	}
	p := TokenPair{Access: prefixAccess + newToken(), Refresh: prefixRefresh + newToken(), Scopes: scopes}
	err = inTx(a.db, func(tx *sql.Tx) error {
		m, err := membership(tx, g.UserID, g.OrgID)
		if err != nil {
			return err
		}
		var client string
		if err := tx.QueryRow("SELECT name FROM oauth_clients WHERE id = ?", g.ClientID).Scan(&client); err != nil {
			return err
		}
		if client == "" {
			client = "OAuth client"
		}
		sc, _ := json.Marshal(scopes)
		_, err = tx.Exec(`INSERT INTO tokens (kind, user_id, org_id, name, token_hash, token_prefix, scopes, created_at, expires_at,
				oauth_client_id, refresh_hash, refresh_expires_at)
			VALUES ('oauth', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			g.UserID, g.OrgID, client+" · "+m.Name, hashToken(p.Access), p.Access[:prefixShown], string(sc), now.UnixNano(),
			now.Add(accessTTL).Unix(), g.ClientID, hashToken(p.Refresh), now.Add(refreshTTL).Unix())
		return err
	})
	return p, err
}

// RefreshGrant swaps the refresh token of clientID's grant for a new token
// pair. Each refresh token works once: one that was already swapped is being
// replayed by someone who isn't the client, so the whole grant is revoked.
// A grant whose user left its org or changed password is revoked too.
func (a *Accounts) RefreshGrant(refresh, clientID string, accessTTL, refreshTTL time.Duration, now time.Time) (TokenPair, error) {
	old := hashToken(refresh)
	p := TokenPair{Access: prefixAccess + newToken(), Refresh: prefixRefresh + newToken()}
	invalid := false // set with the revocation, which must commit
	err := inTx(a.db, func(tx *sql.Tx) error {
		var id, created, changed int64
		var scopes string
		var member bool
		err := tx.QueryRow(`SELECT t.id, t.scopes, t.created_at, u.password_changed_at,
				EXISTS (SELECT 1 FROM org_memberships m WHERE m.user_id = t.user_id AND m.org_id = t.org_id)
			FROM tokens t JOIN users u ON u.id = t.user_id
			WHERE t.kind = 'oauth' AND t.refresh_hash = ? AND t.oauth_client_id = ? AND t.refresh_expires_at > ?`,
			old, clientID, now.Unix()).Scan(&id, &scopes, &created, &changed, &member)
		if errors.Is(err, sql.ErrNoRows) {
			invalid = true
			_, err := tx.Exec("DELETE FROM tokens WHERE kind = 'oauth' AND previous_refresh_hash = ? AND oauth_client_id = ?", old, clientID)
			return err
		}
		if err != nil {
			return err
		}
		if !member || changed > created {
			invalid = true
			_, err := tx.Exec("DELETE FROM tokens WHERE id = ?", id)
			return err
		}
		_ = json.Unmarshal([]byte(scopes), &p.Scopes)
		_, err = tx.Exec(`UPDATE tokens SET token_hash = ?, token_prefix = ?, expires_at = ?, refresh_hash = ?,
			refresh_expires_at = ?, previous_refresh_hash = refresh_hash WHERE id = ?`,
			hashToken(p.Access), p.Access[:prefixShown], now.Add(accessTTL).Unix(), hashToken(p.Refresh), now.Add(refreshTTL).Unix(), id)
		return err
	})
	if err == nil && invalid {
		err = ErrTokenInvalid
	}
	if err != nil {
		return TokenPair{}, err
	}
	return p, nil
}

// ConnectedClients counts the OAuth clients with a grant that can still be
// refreshed.
func (a *Accounts) ConnectedClients(now time.Time) int {
	var n int
	_ = a.db.QueryRow("SELECT COUNT(DISTINCT oauth_client_id) FROM tokens WHERE kind = 'oauth' AND refresh_expires_at > ?", now.Unix()).Scan(&n)
	return n
}

// ---- credentials from before tokens had orgs ----

// soleOrg is uid's org if they belong to exactly one: the only org a
// credential made before tokens were pinned can be meant for.
func soleOrg(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, uid int64) (int64, bool) {
	rows, err := q.Query("SELECT org_id FROM org_memberships WHERE user_id = ? LIMIT 2", uid)
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	var orgs []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			orgs = append(orgs, id)
		}
	}
	if len(orgs) != 1 {
		return 0, false
	}
	return orgs[0], true
}

// ErrNoSoleOrg: an old credential's user is in no org or several, so which
// org it is for can't be told; it isn't imported.
var ErrNoSoleOrg = errors.New("the user isn't in exactly one organization")

// ImportOAuthClient keeps a client registered before clients were stored
// here (oauth.json). An existing client of that id wins.
func (a *Accounts) ImportOAuthClient(c OAuthClient) error {
	uris, _ := json.Marshal(c.RedirectURIs)
	_, err := a.db.Exec(`INSERT INTO oauth_clients (id, name, redirect_uris, auth_method, secret_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`, c.ID, c.Name, string(uris), c.AuthMethod, c.SecretHash, c.Created.Unix())
	return err
}

// ImportGrant turns a refresh token from oauth.json (by its hash) into a
// grant of uid's, pinned to their only org. Its access token is not kept:
// the client refreshes.
func (a *Accounts) ImportGrant(clientID string, uid int64, refreshHash string, issued, refreshExpires, now time.Time) error {
	return inTx(a.db, func(tx *sql.Tx) error {
		org, ok := soleOrg(tx, uid)
		if !ok {
			return ErrNoSoleOrg
		}
		var client, orgName string
		if err := tx.QueryRow("SELECT name FROM oauth_clients WHERE id = ?", clientID).Scan(&client); err != nil {
			return err
		}
		if client == "" {
			client = "OAuth client"
		}
		if err := tx.QueryRow("SELECT name FROM organizations WHERE id = ?", org).Scan(&orgName); err != nil {
			return err
		}
		placeholder := prefixAccess + newToken() // never handed out
		sc, _ := json.Marshal(UserScopes)
		_, err := tx.Exec(`INSERT INTO tokens (kind, user_id, org_id, name, token_hash, token_prefix, scopes, created_at, expires_at,
				oauth_client_id, refresh_hash, refresh_expires_at)
			VALUES ('oauth', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (refresh_hash) DO NOTHING`,
			uid, org, client+" · "+orgName, hashToken(placeholder), placeholder[:prefixShown], string(sc), issued.UnixNano(),
			now.Unix(), clientID, refreshHash, refreshExpires.Unix())
		return err
	})
}

// ImportRunnerToken stores the token of a runner registered before runner
// tokens were kept here (by its hash, from runners.json) as the env
// account's, pinned to that account's only org.
func (a *Accounts) ImportRunnerToken(runnerID, name, tokenHash string, created time.Time) error {
	return inTx(a.db, func(tx *sql.Tx) error {
		var uid int64
		if err := tx.QueryRow("SELECT id FROM users WHERE env_managed = 1").Scan(&uid); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		org, ok := soleOrg(tx, uid)
		if !ok {
			return ErrNoSoleOrg
		}
		_, err := tx.Exec(`INSERT INTO tokens (kind, user_id, org_id, name, token_hash, token_prefix, scopes, created_at, runner_id)
			VALUES ('runner', ?, ?, ?, ?, ?, '["runner"]', ?, ?) ON CONFLICT DO NOTHING`,
			uid, org, "Runner "+name, tokenHash, prefixRunner+"…", created.UnixNano(), runnerID)
		return err
	})
}
