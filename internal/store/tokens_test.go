package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// tokenAccounts is a database whose env user "admin" owns the default org
// and also belongs to a second org, "Two".
func tokenAccounts(t *testing.T) (s *Store, acc *Accounts, uid, org1, org2 int64) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	acc = s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-password"); err != nil {
		t.Fatal(err)
	}
	u, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	two, err := acc.CreateOrg(u.ID, "Two")
	if err != nil {
		t.Fatal(err)
	}
	return s, acc, u.ID, def.ID, two.ID
}

func TestTokenIsPinnedToItsOrg(t *testing.T) {
	_, acc, uid, org1, org2 := tokenAccounts(t)
	now := time.Now()

	// No org, no token: there is no default to fall back on.
	if _, _, err := acc.CreateToken(NewToken{Kind: TokenAPI, Name: "ci", UserID: uid, Scopes: UserScopes}, now); !errors.Is(err, ErrNoOrg) {
		t.Fatalf("token without an org: %v", err)
	}
	if _, err := acc.CreateGrant(Grant{ClientID: "c", UserID: uid, Scopes: UserScopes}, time.Hour, time.Hour, now); !errors.Is(err, ErrNoOrg) {
		t.Fatalf("grant without an org: %v", err)
	}
	// Nor in an org the user isn't in.
	stranger := addMember(t, acc, uid, org1, "stranger", RoleMember)
	theirs, err := acc.CreateOrg(stranger, "Three")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := acc.CreateToken(NewToken{Kind: TokenAPI, Name: "ci", UserID: uid, OrgID: theirs.ID, Scopes: UserScopes}, now); !errors.Is(err, ErrNotMember) {
		t.Fatalf("token in someone else's org: %v", err)
	}
	if _, err := acc.CreateGrant(Grant{ClientID: "c", UserID: uid, OrgID: theirs.ID, Scopes: UserScopes}, time.Hour, time.Hour, now); !errors.Is(err, ErrNotMember) {
		t.Fatalf("grant in someone else's org: %v", err)
	}

	secret, tok, err := acc.CreateToken(NewToken{Kind: TokenAPI, Name: " ci ", UserID: uid, OrgID: org2, Scopes: []string{"write", "read", "read"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Org.ID != org2 || tok.Name != "ci" || len(tok.Scopes) != 2 || tok.Prefix != secret[:12] || tok.Kind != TokenAPI {
		t.Fatalf("token %+v", tok)
	}
	got, s, err := acc.Authenticate(secret, now, TokenAPI)
	if err != nil || got.ID != tok.ID || s.User.ID != uid || s.Org == nil || s.Org.ID != org2 || s.Org.Role != RoleOwner {
		t.Fatalf("authenticate: %+v %+v %v", got, s, err)
	}
	if got.LastUsed.IsZero() {
		t.Error("last use not recorded")
	}
	// Only as the kinds asked for.
	if _, _, err := acc.Authenticate(secret, now, TokenOAuth, TokenRunner); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("api token as another kind: %v", err)
	}

	// Leaving the org ends the token; it doesn't move to the user's other org.
	second := addMember(t, acc, uid, org2, "second", RoleOwner)
	if err := acc.RemoveMember(second, org2, uid); err != nil {
		t.Fatal(err)
	}
	if _, _, err := acc.Authenticate(secret, now, TokenAPI); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("token after leaving its org: %v", err)
	}
	if _, err := acc.Membership(uid, org1); err != nil {
		t.Fatalf("still in the first org: %v", err)
	}
}

// addMember invites name into org and has them accept.
func addMember(t *testing.T, acc *Accounts, inviter, org int64, name string, role Role) int64 {
	t.Helper()
	tok, _, err := acc.Invite(inviter, org, name+"@example.com", role)
	if err != nil {
		t.Fatal(err)
	}
	u, _, err := acc.RegisterWithInvitation(tok, name, "long-enough")
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestTokenScopesAndNames(t *testing.T) {
	_, acc, uid, org1, _ := tokenAccounts(t)
	now := time.Now()
	for _, sc := range [][]string{nil, {}, {"admin"}, {"read", "runner"}} {
		if _, _, err := acc.CreateToken(NewToken{Kind: TokenAPI, Name: "x", UserID: uid, OrgID: org1, Scopes: sc}, now); !errors.Is(err, ErrBadScopes) {
			t.Errorf("scopes %q: %v", sc, err)
		}
	}
	if _, _, err := acc.CreateToken(NewToken{Kind: TokenAPI, Name: "  ", UserID: uid, OrgID: org1, Scopes: UserScopes}, now); !errors.Is(err, ErrBadTokenName) {
		t.Errorf("blank name: %v", err)
	}
	_, ro, err := acc.CreateToken(NewToken{Kind: TokenAPI, Name: "ro", UserID: uid, OrgID: org1, Scopes: []string{"read"}}, now)
	if err != nil || !ro.HasScope(ScopeRead) || ro.HasScope(ScopeWrite) {
		t.Fatalf("read-only token: %+v %v", ro, err)
	}
	// A runner's token has the runner scope whatever is asked.
	_, rt, err := acc.CreateToken(NewToken{Kind: TokenRunner, Name: "Runner work", UserID: uid, OrgID: org1, RunnerID: "r1", Scopes: UserScopes}, now)
	if err != nil || len(rt.Scopes) != 1 || !rt.HasScope(ScopeRunner) || rt.RunnerID != "r1" {
		t.Fatalf("runner token: %+v %v", rt, err)
	}
}

func TestTokenExpiryPasswordAndRevocation(t *testing.T) {
	s, acc, uid, org1, _ := tokenAccounts(t)
	now := time.Now()
	secret, tok, err := acc.CreateToken(NewToken{Kind: TokenAPI, Name: "x", UserID: uid, OrgID: org1, Scopes: UserScopes, Expires: now.Add(time.Hour)}, now)
	if err != nil {
		t.Fatal(err)
	}
	runner, _, err := acc.CreateToken(NewToken{Kind: TokenRunner, Name: "Runner w", UserID: uid, OrgID: org1, RunnerID: "w"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := acc.Authenticate(secret, now.Add(2*time.Hour), TokenAPI); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expired token: %v", err)
	}

	// A password change ends API tokens, not runners'.
	if _, err := s.db.Exec("UPDATE users SET password_changed_at = ? WHERE id = ?", now.Add(time.Second).UnixNano(), uid); err != nil {
		t.Fatal(err)
	}
	if _, _, err := acc.Authenticate(secret, now, TokenAPI); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("token after a password change: %v", err)
	}
	if _, _, err := acc.Authenticate(runner, now, TokenRunner); err != nil {
		t.Errorf("runner token after a password change: %v", err)
	}

	// Only the owner revokes, and then it's gone.
	other := addMember(t, acc, uid, org1, "other", RoleAdmin)
	if _, err := acc.RevokeToken(other, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked by someone else: %v", err)
	}
	if ts, _ := acc.Tokens(uid); len(ts) != 2 {
		t.Fatalf("tokens %+v", ts)
	}
	if _, err := acc.RevokeToken(uid, tok.ID); err != nil {
		t.Fatal(err)
	}
	if ts, _ := acc.Tokens(uid); len(ts) != 1 || ts[0].Kind != TokenRunner {
		t.Errorf("after revoking: %+v", ts)
	}
	if err := acc.DeleteRunnerToken("w"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := acc.Authenticate(runner, now, TokenRunner); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("deleted runner token: %v", err)
	}
}

func TestGrantRefreshRotationAndReplay(t *testing.T) {
	_, acc, uid, _, org2 := tokenAccounts(t)
	now := time.Now()
	if err := acc.CreateOAuthClient(OAuthClient{ID: "c1", Name: "Claude", RedirectURIs: []string{"https://claude.ai/cb"}, AuthMethod: "none", Created: now}, 10, now); err != nil {
		t.Fatal(err)
	}
	p, err := acc.CreateGrant(Grant{ClientID: "c1", UserID: uid, OrgID: org2, Scopes: []string{"read"}}, time.Hour, 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	tok, s, err := acc.Authenticate(p.Access, now, TokenOAuth)
	if err != nil || tok.Org.ID != org2 || s.Org.ID != org2 || tok.Name != "Claude · Two" || tok.ClientID != "c1" || tok.HasScope(ScopeWrite) {
		t.Fatalf("grant %+v %v", tok, err)
	}
	if _, _, err := acc.Authenticate(p.Refresh, now, TokenOAuth); !errors.Is(err, ErrTokenInvalid) {
		t.Error("refresh token works as an access token")
	}
	if n := acc.ConnectedClients(now); n != 1 {
		t.Errorf("connected %d", n)
	}

	p2, err := acc.RefreshGrant(p.Refresh, "c1", time.Hour, 24*time.Hour, now)
	if err != nil || len(p2.Scopes) != 1 || p2.Scopes[0] != ScopeRead {
		t.Fatalf("refresh: %+v %v", p2, err)
	}
	if _, _, err := acc.Authenticate(p.Access, now, TokenOAuth); !errors.Is(err, ErrTokenInvalid) {
		t.Error("old access token still works")
	}
	if _, _, err := acc.Authenticate(p2.Access, now, TokenOAuth); err != nil {
		t.Errorf("new access token: %v", err)
	}
	if _, err := acc.RefreshGrant(p2.Refresh, "other-client", time.Hour, 24*time.Hour, now); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("refreshed by another client: %v", err)
	}
	// Replaying the spent refresh token revokes the grant.
	if _, err := acc.RefreshGrant(p.Refresh, "c1", time.Hour, 24*time.Hour, now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("replayed refresh token: %v", err)
	}
	if _, _, err := acc.Authenticate(p2.Access, now, TokenOAuth); !errors.Is(err, ErrTokenInvalid) {
		t.Error("grant survived a replayed refresh token")
	}
	if _, err := acc.RefreshGrant(p2.Refresh, "c1", time.Hour, 24*time.Hour, now); !errors.Is(err, ErrTokenInvalid) {
		t.Error("revoked grant refreshed")
	}
}

func TestOAuthClientLimit(t *testing.T) {
	_, acc, uid, org1, _ := tokenAccounts(t)
	now := time.Now()
	c := func(id string, at time.Time) OAuthClient {
		return OAuthClient{ID: id, RedirectURIs: []string{"https://claude.ai/cb"}, AuthMethod: "none", Created: at}
	}
	old := now.Add(-2 * time.Hour)
	for _, id := range []string{"used", "idle"} {
		if err := acc.CreateOAuthClient(c(id, old), 2, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := acc.CreateGrant(Grant{ClientID: "used", UserID: uid, OrgID: org1, Scopes: UserScopes}, time.Hour, time.Hour, now); err != nil {
		t.Fatal(err)
	}
	// Full: the old client without a grant makes room, the one with a grant stays.
	if err := acc.CreateOAuthClient(c("new", now), 2, now); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.OAuthClient("idle"); !errors.Is(err, ErrNotFound) {
		t.Errorf("idle client kept: %v", err)
	}
	if _, err := acc.OAuthClient("used"); err != nil {
		t.Errorf("client in use dropped: %v", err)
	}
	if err := acc.CreateOAuthClient(c("newer", now), 2, now); !errors.Is(err, ErrTooManyClients) {
		t.Errorf("over the limit: %v", err)
	}
}

// Credentials from before tokens had orgs are imported only when the org is
// unambiguous: the user's only one.
func TestImportNeedsSoleOrg(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	acc := s.Accounts()
	acc.Bootstrap("admin", "s3cret-password")
	u, _ := acc.EnvUser()
	now := time.Now()
	if err := acc.ImportRunnerToken("r1", "work", hashToken("dr_old"), now); err != nil {
		t.Fatal(err)
	}
	tok, _, err := acc.Authenticate("dr_old", now, TokenRunner)
	if err != nil || tok.RunnerID != "r1" {
		t.Fatalf("imported runner token: %+v %v", tok, err)
	}
	if owners, _ := acc.RunnerOwners(); owners["r1"].Username != "admin" {
		t.Errorf("owners %+v", owners)
	}
	if _, err := acc.CreateOrg(u.ID, "Two"); err != nil {
		t.Fatal(err)
	}
	if err := acc.ImportRunnerToken("r2", "home", hashToken("dr_older"), now); !errors.Is(err, ErrNoSoleOrg) {
		t.Errorf("runner of a user in two orgs: %v", err)
	}
}
