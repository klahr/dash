package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/store"
)

// withBearer sends a request with an Authorization: Bearer header.
func withBearer(h http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// newToken makes an API token through the API, as c, and returns its secret.
func (c *client) newToken(orgID int64, scopes, password string) (string, store.Token) {
	c.t.Helper()
	w := c.do("POST", "/api/tokens", fmt.Sprintf(`{"name":"ci","org_id":%d,"scopes":%s,"password":%q}`, orgID, scopes, password))
	if w.Code != http.StatusCreated {
		c.t.Fatalf("create token: %d %s", w.Code, w.Body)
	}
	var res struct {
		Secret string      `json:"secret"`
		Token  store.Token `json:"token"`
	}
	decode(c.t, w, &res)
	return res.Secret, res.Token
}

func TestAPITokenActsInItsOrg(t *testing.T) {
	a, h := newServer(t)
	c := signIn(t, h, "admin", "s3cret")
	me := c.me()
	two, err := a.acc.CreateOrg(me.User.ID, "Two")
	if err != nil {
		t.Fatal(err)
	}

	// The org is required, must be the user's, and the password is asked again.
	if w := c.do("POST", "/api/tokens", `{"name":"ci","scopes":["read"],"password":"s3cret"}`); w.Code != http.StatusBadRequest {
		t.Errorf("token without an org: %d %s", w.Code, w.Body)
	}
	if w := c.do("POST", "/api/tokens", `{"name":"ci","org_id":999,"scopes":["read"],"password":"s3cret"}`); w.Code != http.StatusNotFound {
		t.Errorf("token for another org: %d %s", w.Code, w.Body)
	}
	if w := c.do("POST", "/api/tokens", fmt.Sprintf(`{"name":"ci","org_id":%d,"scopes":["read"],"password":"nope"}`, two.ID)); w.Code != http.StatusForbidden {
		t.Errorf("token with a wrong password: %d %s", w.Code, w.Body)
	}

	// Pinned to the second org, though the session is working in the first.
	secret, tok := c.newToken(two.ID, `["read","write"]`, "s3cret")
	if !strings.HasPrefix(secret, "dash_") || tok.Org.ID != two.ID {
		t.Fatalf("token %q %+v", secret, tok)
	}
	w := withBearer(h, secret, "GET", "/api/me", "")
	var tm meResp
	decode(t, w, &tm)
	if w.Code != http.StatusOK || tm.Org == nil || tm.Org.ID != two.ID || len(tm.Orgs) != 1 || tm.User.ID != me.User.ID {
		t.Fatalf("me with token: %d %s", w.Code, w.Body)
	}
	// Nor does it see the user's other orgs or their members.
	var orgs []store.Membership
	w = withBearer(h, secret, "GET", "/api/orgs", "")
	decode(t, w, &orgs)
	if w.Code != http.StatusOK || len(orgs) != 1 || orgs[0].ID != two.ID {
		t.Errorf("orgs with token: %d %s", w.Code, w.Body)
	}
	if w := withBearer(h, secret, "GET", fmt.Sprintf("/api/orgs/%d/members", me.Org.ID), ""); w.Code != http.StatusNotFound {
		t.Errorf("another org's members with token: %d %s", w.Code, w.Body)
	}
	if w := withBearer(h, secret, "GET", fmt.Sprintf("/api/orgs/%d/members", two.ID), ""); w.Code != http.StatusOK {
		t.Errorf("own org's members with token: %d %s", w.Code, w.Body)
	}
	if w := withBearer(h, secret, "GET", "/api/projects", ""); w.Code != http.StatusOK || w.Body.String() != "private" {
		t.Errorf("API with token: %d %s", w.Code, w.Body)
	}
	// Pages aren't the API.
	if w := withBearer(h, secret, "GET", "/", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("page with token: %d", w.Code)
	}
	if w := withBearer(h, "dash_nope", "GET", "/api/projects", ""); w.Code != http.StatusUnauthorized ||
		!strings.Contains(w.Header().Get("WWW-Authenticate"), "invalid_token") {
		t.Errorf("unknown token: %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}

	// Leaving the org ends it: it never falls back to the user's other org.
	other := addUser(t, a, two.ID, "other", store.RoleOwner)
	if err := a.acc.RemoveMember(other, two.ID, me.User.ID); err != nil {
		t.Fatal(err)
	}
	if w := withBearer(h, secret, "GET", "/api/me", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("token after leaving its org: %d %s", w.Code, w.Body)
	}
}

func TestAPITokenScopes(t *testing.T) {
	_, h := newServer(t)
	c := signIn(t, h, "admin", "s3cret")
	org := c.me().Org.ID
	ro, _ := c.newToken(org, `["read"]`, "s3cret")
	rw, _ := c.newToken(org, `["read","write"]`, "s3cret")

	for _, tc := range []struct {
		token, method, path string
		want                int
	}{
		{ro, "GET", "/api/projects", http.StatusOK},
		{ro, "POST", "/api/projects", http.StatusForbidden},
		{ro, "DELETE", "/api/todos/x", http.StatusForbidden},
		{ro, "GET", "/ws/chat", http.StatusForbidden}, // sends: write
		{ro, "GET", "/ws/events", http.StatusOK},
		{rw, "POST", "/api/projects", http.StatusOK},
		{rw, "GET", "/api/projects", http.StatusOK},
	} {
		w := withBearer(h, tc.token, tc.method, tc.path, "{}")
		if w.Code != tc.want {
			t.Errorf("%s %s with %s token: %d, want %d (%s)", tc.method, tc.path, map[bool]string{true: "read", false: "write"}[tc.token == ro], w.Code, tc.want, w.Body)
		}
		if tc.want == http.StatusForbidden && !strings.Contains(w.Header().Get("WWW-Authenticate"), "insufficient_scope") {
			t.Errorf("%s %s: no insufficient_scope", tc.method, tc.path)
		}
	}
}

// Tokens can read but not manage members, invitations or credentials.
func TestRequireSession(t *testing.T) {
	a, h := newServer(t)
	c := signIn(t, h, "admin", "s3cret")
	org := c.me().Org.ID
	rw, tok := c.newToken(org, `["read","write"]`, "s3cret")
	member := addUser(t, a, org, "member", store.RoleMember)

	for _, req := range []struct{ method, path, body string }{
		{"POST", "/api/tokens", fmt.Sprintf(`{"name":"more","org_id":%d,"scopes":["read"],"password":"s3cret"}`, org)},
		{"GET", "/api/tokens", ""},
		{"DELETE", fmt.Sprintf("/api/tokens/%d", tok.ID), ""},
		{"POST", fmt.Sprintf("/api/orgs/%d/invitations", org), `{"email":"x@example.com","password":"s3cret"}`},
		{"PATCH", fmt.Sprintf("/api/orgs/%d/members/%d", org, member), `{"role":"admin","password":"s3cret"}`},
		{"DELETE", fmt.Sprintf("/api/orgs/%d/members/%d", org, member), `{"password":"s3cret"}`},
		{"POST", "/api/auth/switch-org", fmt.Sprintf(`{"org_id":%d}`, org)},
		{"POST", "/api/orgs", `{"name":"Mine"}`},
		{"POST", "/api/invitations/accept", `{"token":"x"}`},
	} {
		if w := withBearer(h, rw, req.method, req.path, req.body); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "token") {
			t.Errorf("%s %s with a token: %d %s", req.method, req.path, w.Code, w.Body)
		}
	}
	// Reading members is fine.
	if w := withBearer(h, rw, "GET", fmt.Sprintf("/api/orgs/%d/members", org), ""); w.Code != http.StatusOK {
		t.Errorf("members with a token: %d %s", w.Code, w.Body)
	}
	// Nothing changed: the member is still a member, and the token still works.
	if m, err := a.acc.Membership(member, org); err != nil || m.Role != store.RoleMember {
		t.Errorf("member changed: %+v %v", m, err)
	}
	// A token can't confirm a password either.
	r := httptest.NewRequest("DELETE", "/api/projects/p", nil)
	id, err := a.tokenIdentity(rw, store.TokenAPI)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ConfirmPassword(r.WithContext(withIdentity(r.Context(), id)), "s3cret"); err != ErrSessionRequired {
		t.Errorf("ConfirmPassword with a token: %v", err)
	}
}

func TestRevokeToken(t *testing.T) {
	a, h := newServer(t)
	c := signIn(t, h, "admin", "s3cret")
	org := c.me().Org.ID
	secret, tok := c.newToken(org, `["read"]`, "s3cret")
	var revoked []store.Token
	a.OnRevoke = func(t store.Token) { revoked = append(revoked, t) }

	var list []store.Token
	decode(t, c.do("GET", "/api/tokens", ""), &list)
	if len(list) != 1 || list[0].ID != tok.ID || list[0].Prefix != secret[:12] || strings.Contains(fmt.Sprint(list), secret) {
		t.Fatalf("list %+v", list)
	}
	// Someone else's token isn't theirs to revoke.
	addUser(t, a, org, "bob", store.RoleOwner)
	bob := signIn(t, h, "bob", "pw-bob-long")
	if w := bob.do("DELETE", fmt.Sprintf("/api/tokens/%d", tok.ID), ""); w.Code != http.StatusNotFound {
		t.Errorf("revoked by another user: %d", w.Code)
	}
	if w := c.do("DELETE", fmt.Sprintf("/api/tokens/%d", tok.ID), ""); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if len(revoked) != 1 || revoked[0].ID != tok.ID {
		t.Errorf("OnRevoke got %+v", revoked)
	}
	if w := withBearer(h, secret, "GET", "/api/me", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked token: %d", w.Code)
	}
}

// Someone in several orgs picks the one the OAuth client acts in; the grant
// is pinned to it, listed with their tokens, and revocable.
func TestOAuthConsentPicksOrg(t *testing.T) {
	a, _, h := newOAuthServer(t)
	u, _ := a.acc.EnvUser()
	def, _ := a.acc.DefaultOrg()
	two, err := a.acc.CreateOrg(u.ID, "Two")
	if err != nil {
		t.Fatal(err)
	}
	c := register(t, h, `{"client_name":"Claude","redirect_uris":["`+callback+`"]}`)
	id := c["client_id"].(string)
	q := authParams(id)
	q.Set("scope", "read")

	// The password is right, but which org? The page asks, with a ticket.
	w, _ := login(t, h, q, "s3cret")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `name="org_id"`) ||
		!strings.Contains(w.Body.String(), ">Two<") || !strings.Contains(w.Body.String(), ">Default<") {
		t.Fatalf("org picker: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), `name="password"`) {
		t.Error("the org picker asks for the password again")
	}
	ticket := between(w.Body.String(), `name="ticket" value="`, `"`)
	if ticket == "" {
		t.Fatal("no ticket")
	}
	pick := func(ticket, org string, q url.Values) *httptest.ResponseRecorder {
		v := url.Values{}
		for k, vs := range q {
			v[k] = vs
		}
		v.Set("ticket", ticket)
		v.Set("org_id", org)
		v.Set("action", "allow")
		return form(h, "/authorize", v)
	}
	// A ticket is bound to its request: another client's can't use it.
	other := authParams(register(t, h, `{"redirect_uris":["`+callback+`"]}`)["client_id"].(string))
	if w := pick(ticket, fmt.Sprint(two.ID), other); w.Code != http.StatusBadRequest {
		t.Errorf("ticket for another client: %d", w.Code)
	}
	// …and it was spent trying. Start again; an org that isn't theirs is refused.
	w, _ = login(t, h, q, "s3cret")
	ticket = between(w.Body.String(), `name="ticket" value="`, `"`)
	if w := pick(ticket, "999", q); w.Code != http.StatusForbidden {
		t.Errorf("someone else's org: %d %s", w.Code, w.Body)
	}
	w, _ = login(t, h, q, "s3cret")
	ticket = between(w.Body.String(), `name="ticket" value="`, `"`)
	w = pick(ticket, fmt.Sprint(two.ID), q)
	if w.Code != http.StatusFound {
		t.Fatalf("pick org: %d %s", w.Code, w.Body)
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	code := loc.Query().Get("code")
	status, tok := tokens(t, h, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {id},
		"redirect_uri": {callback}, "code_verifier": {verifier}})
	if status != http.StatusOK || tok["scope"] != "read" {
		t.Fatalf("token: %d %v", status, tok)
	}
	access := tok["access_token"].(string)
	r := httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))
	r.Header.Set("Authorization", "Bearer "+access)
	var seen *Identity
	o := a.oauth
	o.Bearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen, _ = From(r.Context()) })).ServeHTTP(httptest.NewRecorder(), r)
	if seen == nil || seen.Org.ID != two.ID || seen.Token == nil || seen.Can(store.ScopeWrite) || !seen.Can(store.ScopeRead) {
		t.Fatalf("identity behind the access token: %+v", seen)
	}
	if seen.Org.ID == def.ID {
		t.Fatal("fell back to the first org")
	}

	// Listed as the user's, and revoking it disconnects the client.
	ts, _ := a.acc.Tokens(u.ID)
	if len(ts) != 1 || ts[0].Kind != store.TokenOAuth || ts[0].Name != "Claude · Two" {
		t.Fatalf("tokens %+v", ts)
	}
	if _, err := a.acc.RevokeToken(u.ID, ts[0].ID); err != nil {
		t.Fatal(err)
	}
	if w := callMCP(h, access); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked grant: %d", w.Code)
	}
	if status, _ := tokens(t, h, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}, "client_id": {id}}); status != http.StatusBadRequest {
		t.Errorf("refresh of a revoked grant: %d", status)
	}
}

// API tokens work on /mcp too; a bad scope parameter goes back to the client.
func TestOAuthBearerTakesAPITokens(t *testing.T) {
	a, _, h := newOAuthServer(t)
	u, _ := a.acc.EnvUser()
	org, _ := a.acc.DefaultOrg()
	secret, _, err := a.acc.CreateToken(store.NewToken{Kind: store.TokenAPI, Name: "x", UserID: u.ID, OrgID: org.ID, Scopes: []string{"read"}}, a.now())
	if err != nil {
		t.Fatal(err)
	}
	if w := callMCP(h, secret); w.Code != http.StatusOK {
		t.Errorf("/mcp with an API token: %d", w.Code)
	}
	id := register(t, h, `{"redirect_uris":["`+callback+`"]}`)["client_id"].(string)
	q := authParams(id)
	q.Set("scope", "admin")
	w := do(h, "GET", "/authorize?"+q.Encode(), "")
	if loc, _ := url.Parse(w.Header().Get("Location")); w.Code != http.StatusFound || loc.Query().Get("error") != "invalid_scope" {
		t.Errorf("bad scope: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func between(s, start, end string) string {
	_, after, ok := strings.Cut(s, start)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(after, end)
	return v
}

// A token is no way around an org's two-factor requirement, nor into the
// second-factor settings.
func TestTokensAndTwoFactor(t *testing.T) {
	a, h := newServer(t)
	admin, _ := a.acc.EnvUser()
	org, _ := a.acc.DefaultOrg()
	if err := a.acc.BeginTOTP(admin.ID, "JBSWY3DPEHPK3PXP"); err != nil {
		t.Fatal(err)
	}
	if err := a.acc.ConfirmTOTP(admin.ID, "JBSWY3DPEHPK3PXP", 1, []string{"recovery-1"}); err != nil {
		t.Fatal(err)
	}
	bob := addUser(t, a, org.ID, "bob", store.RoleMember)
	mk := func(uid int64, kind store.TokenKind) string {
		n := store.NewToken{Kind: kind, Name: "x", UserID: uid, OrgID: org.ID, Scopes: store.UserScopes}
		if kind == store.TokenRunner {
			n.RunnerID = fmt.Sprint("r", uid)
		}
		secret, _, err := a.acc.CreateToken(n, a.now())
		if err != nil {
			t.Fatal(err)
		}
		return secret
	}
	bobs, bobsRunner, admins := mk(bob, store.TokenAPI), mk(bob, store.TokenRunner), mk(admin.ID, store.TokenAPI)
	if w := withBearer(h, bobs, "GET", "/api/projects", ""); w.Code != http.StatusOK {
		t.Fatalf("before the requirement: %d", w.Code)
	}
	if _, err := a.acc.SetRequire2FA(admin.ID, org.ID, true); err != nil {
		t.Fatal(err)
	}
	if w := withBearer(h, bobs, "GET", "/api/projects", ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "2fa_required") {
		t.Errorf("token of a member without 2FA: %d %s", w.Code, w.Body)
	}
	if _, _, err := a.acc.Authenticate(bobsRunner, a.now(), store.TokenRunner); err != nil {
		t.Errorf("runner of a member without 2FA: %v", err)
	}
	if w := withBearer(h, admins, "GET", "/api/projects", ""); w.Code != http.StatusOK {
		t.Errorf("token of a member with 2FA: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/api/account/2fa", "/api/account/2fa/disable"} {
		method := map[bool]string{true: "GET", false: "POST"}[path == "/api/account/2fa"]
		if w := withBearer(h, admins, method, path, `{"password":"s3cret"}`); w.Code != http.StatusForbidden {
			t.Errorf("%s %s with a token: %d %s", method, path, w.Code, w.Body)
		}
	}
	if w := withBearer(h, admins, "PATCH", fmt.Sprintf("/api/orgs/%d", org.ID), `{"require_2fa":false,"password":"s3cret"}`); w.Code != http.StatusForbidden {
		t.Errorf("org settings with a token: %d %s", w.Code, w.Body)
	}
}
