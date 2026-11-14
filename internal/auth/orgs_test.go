package auth

import (
	"bufio"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/store"
)

// addUser creates an account name (password "pw-"+name) that is a member of
// orgID with role, invited by the env user.
func addUser(t *testing.T, a *Auth, orgID int64, name string, role store.Role) int64 {
	t.Helper()
	env, err := a.acc.EnvUser()
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := a.acc.Invite(env.ID, orgID, name+"@example.com", role)
	if err != nil {
		t.Fatal(err)
	}
	u, _, err := a.acc.RegisterWithInvitation(tok, name, "pw-"+name+"-long")
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// client is a browser: it keeps the session cookie between requests.
type client struct {
	t *testing.T
	h http.Handler
	c *http.Cookie
}

func (c *client) do(method, path, body string) *httptest.ResponseRecorder {
	c.t.Helper()
	var cookies []*http.Cookie
	if c.c != nil {
		cookies = append(cookies, c.c)
	}
	w := do(c.h, method, path, body, cookies...)
	for _, k := range w.Result().Cookies() {
		if k.Name == PlainCookieName {
			c.c = k
		}
	}
	return w
}

func signIn(t *testing.T, h http.Handler, username, password string) *client {
	t.Helper()
	c := &client{t: t, h: h}
	if w := c.do("POST", "/api/login", fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)); w.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", username, w.Code, w.Body)
	}
	return c
}

type meResp struct {
	Username string            `json:"username"`
	User     store.User        `json:"user"`
	Org      *store.Membership `json:"org"`
	Orgs     []store.Membership
}

func (c *client) me() meResp {
	c.t.Helper()
	w := c.do("GET", "/api/me", "")
	if w.Code != http.StatusOK {
		c.t.Fatalf("me: %d %s", w.Code, w.Body)
	}
	var m meResp
	decode(c.t, w, &m)
	return m
}

func TestMeShowsUserAndOrg(t *testing.T) {
	t.Parallel()

	_, h := newServer(t)
	m := signIn(t, h, "admin", "s3cret").me()
	if m.Username != "admin" || !m.User.PlatformAdmin || m.Org == nil || m.Org.Slug != "default" || m.Org.Role != store.RoleOwner || len(m.Orgs) != 1 {
		t.Fatalf("me = %+v", m)
	}
}

func TestLoginWithEmailAndUnknownNames(t *testing.T) {
	t.Parallel()

	a, h := newServer(t)
	org, _ := a.acc.DefaultOrg()
	addUser(t, a, org.ID, "bea", store.RoleMember)
	signIn(t, h, "bea@example.com", "pw-bea-long") // email works as the login
	signIn(t, h, "BEA", "pw-bea-long")             // usernames ignore case

	// An unknown name and a wrong password look the same.
	unknown := do(h, "POST", "/api/login", `{"username":"nobody","password":"pw-bea-long"}`)
	wrong := do(h, "POST", "/api/login", `{"username":"bea","password":"nope"}`)
	if unknown.Code != http.StatusUnauthorized || unknown.Code != wrong.Code || unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("unknown: %d %s; wrong password: %d %s", unknown.Code, unknown.Body, wrong.Code, wrong.Body)
	}
}

func TestInviteRegisterAndAccept(t *testing.T) {
	t.Parallel()

	a, h := newServer(t)
	owner := signIn(t, h, "admin", "s3cret")
	org := owner.me().Org

	invite := func(email, role string) (int, string) {
		w := owner.do("POST", fmt.Sprintf("/api/orgs/%d/invitations", org.ID),
			fmt.Sprintf(`{"email":%q,"role":%q,"password":"s3cret"}`, email, role))
		var r struct {
			Token string `json:"token"`
			Path  string `json:"path"`
		}
		if w.Code == http.StatusCreated {
			decode(t, w, &r)
			if r.Path != "/invite.html#"+r.Token {
				t.Fatalf("path %q", r.Path)
			}
		}
		return w.Code, r.Token
	}
	if code, _ := invite("new@example.com", "member"); code != http.StatusCreated {
		t.Fatalf("invite: %d", code)
	}
	code, tok := invite("new@example.com", "admin") // again: replaces the first
	if code != http.StatusCreated || tok == "" {
		t.Fatalf("re-invite: %d", code)
	}

	// The link works without a session: what it's for, then a new account.
	anon := &client{t: t, h: h}
	w := anon.do("POST", "/api/invitations/lookup", `{"token":"`+tok+`"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"role":"admin"`) || !strings.Contains(w.Body.String(), `"name":"Default"`) {
		t.Fatalf("lookup: %d %s", w.Code, w.Body)
	}
	if w := anon.do("POST", "/api/invitations/register", `{"token":"`+tok+`","username":"admin","password":"long-enough"}`); w.Code != http.StatusConflict {
		t.Fatalf("taken username: %d %s", w.Code, w.Body)
	}
	if w := anon.do("POST", "/api/invitations/register", `{"token":"`+tok+`","username":"newbie","password":"short"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("short password: %d %s", w.Code, w.Body)
	}
	if w := anon.do("POST", "/api/invitations/register", `{"token":"`+tok+`","username":"newbie","password":"long-enough"}`); w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	if m := anon.me(); m.Username != "newbie" || m.User.Email != "new@example.com" || m.Org == nil || m.Org.ID != org.ID || m.Org.Role != store.RoleAdmin {
		t.Fatalf("registered: %+v", m)
	}
	if w := anon.do("POST", "/api/invitations/lookup", `{"token":"`+tok+`"}`); w.Code != http.StatusNotFound {
		t.Fatalf("used link: %d", w.Code)
	}

	// An existing account accepts with its session; until then it isn't a member.
	other, _ := a.acc.CreateOrg(mustEnv(t, a), "Other")
	u2 := addUser(t, a, other.ID, "sam", store.RoleMember)
	_, tok2 := invite("sam@example.com", "member")
	sam := signIn(t, h, "sam", "pw-sam-long")
	if _, err := a.acc.Membership(u2, org.ID); err != store.ErrNotMember {
		t.Fatalf("member before accepting: %v", err)
	}
	if w := sam.do("POST", "/api/invitations/accept", `{"token":"`+tok2+`"}`); w.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", w.Code, w.Body)
	}
	if m := sam.me(); m.Org == nil || m.Org.ID != org.ID || len(m.Orgs) != 2 {
		t.Fatalf("after accepting: %+v", m)
	}

	// Declining uses the link up.
	_, tok3 := invite("nope@example.com", "member")
	if w := anon.do("POST", "/api/invitations/decline", `{"token":"`+tok3+`"}`); w.Code != http.StatusNoContent {
		t.Fatalf("decline: %d", w.Code)
	}
	if w := anon.do("POST", "/api/invitations/register", `{"token":"`+tok3+`","username":"late","password":"long-enough"}`); w.Code != http.StatusNotFound {
		t.Fatalf("declined link: %d", w.Code)
	}
}

func mustEnv(t *testing.T, a *Auth) int64 {
	t.Helper()
	u, err := a.acc.EnvUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// Inviting answers the same for an address with an account and one without,
// and so does registering with a link to an address that has one.
func TestInvitationDoesNotRevealAccounts(t *testing.T) {
	t.Parallel()

	a, h := newServer(t)
	org, _ := a.acc.DefaultOrg()
	addUser(t, a, org.ID, "known", store.RoleMember) // known@example.com has an account
	owner := signIn(t, h, "admin", "s3cret")
	shape := func(email string) string {
		w := owner.do("POST", fmt.Sprintf("/api/orgs/%d/invitations", org.ID), `{"email":"`+email+`","password":"s3cret"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("invite %s: %d %s", email, w.Code, w.Body)
		}
		var r map[string]any
		decode(t, w, &r)
		inv := r["invitation"].(map[string]any)
		keys := []string{}
		for k := range r {
			keys = append(keys, k)
		}
		for k := range inv {
			keys = append(keys, "invitation."+k)
		}
		return fmt.Sprint(len(keys), inv["role"], inv["org"])
	}
	if a, b := shape("known@example.com"), shape("unknown@example.com"); a != b {
		t.Fatalf("responses differ: %s vs %s", a, b)
	}

	// Registering with the link of an address that has an account still
	// works, just without the address.
	tok, _, _ := a.acc.Invite(mustEnv(t, a), org.ID, "known@example.com", store.RoleMember)
	w := do(h, "POST", "/api/invitations/register", `{"token":"`+tok+`","username":"second","password":"long-enough"}`)
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), "known@example.com") {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
}

func TestRoleChecks(t *testing.T) {
	t.Parallel()

	a, h := newServer(t)
	org, _ := a.acc.DefaultOrg()
	ownerID := mustEnv(t, a)
	adminID := addUser(t, a, org.ID, "ada", store.RoleAdmin)
	memberID := addUser(t, a, org.ID, "max", store.RoleMember)
	owner, admin, member := signIn(t, h, "admin", "s3cret"), signIn(t, h, "ada", "pw-ada-long"), signIn(t, h, "max", "pw-max-long")
	members := fmt.Sprintf("/api/orgs/%d/members/", org.ID)
	invitations := fmt.Sprintf("/api/orgs/%d/invitations", org.ID)

	for _, tc := range []struct {
		name       string
		c          *client
		method     string
		path, body string
		want       int
	}{
		{"member lists members", member, "GET", members[:len(members)-1], "", http.StatusOK},
		{"member can't invite", member, "POST", invitations, `{"email":"x@example.com","password":"pw-max-long"}`, http.StatusForbidden},
		{"member can't change roles", member, "PATCH", members + fmt.Sprint(adminID), `{"role":"member","password":"pw-max-long"}`, http.StatusForbidden},
		{"member can't remove others", member, "DELETE", members + fmt.Sprint(adminID), `{"password":"pw-max-long"}`, http.StatusForbidden},
		{"admin can't invite owners", admin, "POST", invitations, `{"email":"x@example.com","role":"owner","password":"pw-ada-long"}`, http.StatusForbidden},
		{"admin can't make owners", admin, "PATCH", members + fmt.Sprint(memberID), `{"role":"owner","password":"pw-ada-long"}`, http.StatusForbidden},
		{"admin can't demote owners", admin, "PATCH", members + fmt.Sprint(ownerID), `{"role":"member","password":"pw-ada-long"}`, http.StatusForbidden},
		{"admin can't remove owners", admin, "DELETE", members + fmt.Sprint(ownerID), `{"password":"pw-ada-long"}`, http.StatusForbidden},
		{"admin needs the password", admin, "PATCH", members + fmt.Sprint(memberID), `{"role":"admin","password":"wrong"}`, http.StatusForbidden},
		{"bad role", admin, "PATCH", members + fmt.Sprint(memberID), `{"role":"boss","password":"pw-ada-long"}`, http.StatusBadRequest},
		{"admin promotes a member", admin, "PATCH", members + fmt.Sprint(memberID), `{"role":"admin","password":"pw-ada-long"}`, http.StatusNoContent},
		{"admin invites", admin, "POST", invitations, `{"email":"x@example.com","role":"admin","password":"pw-ada-long"}`, http.StatusCreated},
		{"last owner can't step down", owner, "PATCH", members + fmt.Sprint(ownerID), `{"role":"admin","password":"s3cret"}`, http.StatusConflict},
		{"last owner can't leave", owner, "DELETE", members + fmt.Sprint(ownerID), `{"password":"s3cret"}`, http.StatusConflict},
		{"owner makes an owner", owner, "PATCH", members + fmt.Sprint(adminID), `{"role":"owner","password":"s3cret"}`, http.StatusNoContent},
		{"now the first owner can step down", owner, "PATCH", members + fmt.Sprint(ownerID), `{"role":"admin","password":"s3cret"}`, http.StatusNoContent},
		{"a member leaves", member, "DELETE", members + fmt.Sprint(memberID), `{"password":"pw-max-long"}`, http.StatusNoContent},
		{"outsiders see nothing", member, "GET", members[:len(members)-1], "", http.StatusForbidden}, // no org left: no_org
	} {
		if w := tc.c.do(tc.method, tc.path, tc.body); w.Code != tc.want {
			t.Errorf("%s: want %d, got %d %s", tc.name, tc.want, w.Code, w.Body)
		}
	}

	// Only admins see the invitations.
	var r map[string]any
	decode(t, admin.do("GET", members[:len(members)-1], ""), &r)
	if _, ok := r["invitations"]; !ok {
		t.Error("admin doesn't see invitations")
	}
	// Only platform admins create orgs.
	if w := admin.do("POST", "/api/orgs", `{"name":"Mine"}`); w.Code != http.StatusForbidden {
		t.Errorf("non-platform admin created an org: %d", w.Code)
	}
	if w := owner.do("POST", "/api/orgs", `{"name":"Mine"}`); w.Code != http.StatusCreated {
		t.Errorf("platform admin creates an org: %d %s", w.Code, w.Body)
	}
}

func TestSwitchOrgAndRemovalTakesEffectAtOnce(t *testing.T) {
	t.Parallel()

	a, h := newServer(t)
	def, _ := a.acc.DefaultOrg()
	second, _ := a.acc.CreateOrg(mustEnv(t, a), "Second")
	uid := addUser(t, a, def.ID, "kim", store.RoleMember)
	tok, _, _ := a.acc.Invite(mustEnv(t, a), second.ID, "kim@example.com", store.RoleAdmin)
	if _, err := a.acc.AcceptInvitation(tok, uid); err != nil {
		t.Fatal(err)
	}
	third, _ := a.acc.CreateOrg(mustEnv(t, a), "Third") // kim isn't in it

	kim := signIn(t, h, "kim", "pw-kim-long")
	if m := kim.me(); m.Org.ID != def.ID {
		t.Fatalf("starts in %+v, want the first org", m.Org)
	}
	if w := kim.do("POST", "/api/auth/switch-org", fmt.Sprintf(`{"org_id":%d}`, second.ID)); w.Code != http.StatusOK {
		t.Fatalf("switch: %d %s", w.Code, w.Body)
	}
	if m := kim.me(); m.Org.ID != second.ID || m.Org.Role != store.RoleAdmin {
		t.Fatalf("after switching: %+v", m.Org)
	}
	if w := kim.do("POST", "/api/auth/switch-org", fmt.Sprintf(`{"org_id":%d}`, third.ID)); w.Code != http.StatusForbidden {
		t.Fatalf("switch to a non-member org: %d", w.Code)
	}

	// Removed from the active org: the very next request is in another one.
	if err := a.acc.RemoveMember(mustEnv(t, a), second.ID, uid); err != nil {
		t.Fatal(err)
	}
	if m := kim.me(); m.Org == nil || m.Org.ID != def.ID {
		t.Fatalf("after removal: %+v", m.Org)
	}
	// Removed from the last: signed in, but the API is closed.
	if err := a.acc.RemoveMember(mustEnv(t, a), def.ID, uid); err != nil {
		t.Fatal(err)
	}
	if m := kim.me(); m.Org != nil || len(m.Orgs) != 0 {
		t.Fatalf("with no org: %+v", m)
	}
	if w := kim.do("GET", "/api/projects", ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "no_org") {
		t.Fatalf("API with no org: %d %s", w.Code, w.Body)
	}
}

// An open WebSocket is closed soon after its user is removed from the org.
func TestWebSocketClosedOnRemoval(t *testing.T) {
	t.Parallel()

	a := newAuth(t, "admin", "s3cret")
	a.recheck = 20 * time.Millisecond
	def, _ := a.acc.DefaultOrg()
	uid := addUser(t, a, def.ID, "lee", store.RoleMember)
	h := socketServer(t, a, "/ws/events")
	lee := signIn(t, h, "lee", "pw-lee-long")
	br := openSocket(t, h, lee, "/ws/events")

	if err := a.acc.RemoveMember(mustEnv(t, a), def.ID, uid); err != nil {
		t.Fatal(err)
	}
	wantClosed(t, br)
}

// A runner's socket, which only platform admins may open over /peer, is
// closed soon after its user stops being a platform admin or signs out.
func TestPeerSocketClosedWhenNoLongerAdmin(t *testing.T) {
	t.Parallel()

	t.Run("demoted", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "dash.db")
		a := openAuth(t, path, "admin", "s3cret")
		a.recheck = 20 * time.Millisecond
		h := socketServer(t, a, "/peer/laptop/ws/events")
		br := openSocket(t, h, signIn(t, h, "admin", "s3cret"), "/peer/laptop/ws/events")

		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec("UPDATE users SET is_platform_admin = 0"); err != nil {
			t.Fatal(err)
		}
		wantClosed(t, br)
	})
	t.Run("signed out", func(t *testing.T) {
		t.Parallel()
		a := newAuth(t, "admin", "s3cret")
		a.recheck = 20 * time.Millisecond
		h := socketServer(t, a, "/peer/laptop/ws/events")
		admin := signIn(t, h, "admin", "s3cret")
		br := openSocket(t, h, admin, "/peer/laptop/ws/events")

		if w := admin.do("POST", "/api/logout", ""); w.Code >= 300 {
			t.Fatalf("logout: %d %s", w.Code, w.Body)
		}
		wantClosed(t, br)
	})
}

// socketServer is a's middleware in front of a WebSocket-like handler at
// path that keeps its connection open until it's closed.
func socketServer(t *testing.T, a *Auth, path string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	a.Routes(mux)
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n")
		rw.Flush()
		io.Copy(io.Discard, conn) // until it's closed
	})
	return a.Middleware(mux)
}

// openSocket opens path on h as c and returns the upgraded connection's
// reader.
func openSocket(t *testing.T, h http.Handler, c *client, path string) *bufio.Reader {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.AddCookie(c.c)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade %s: %v %v", path, resp, err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	return br
}

// wantClosed fails t unless the server closes br's connection.
func wantClosed(t *testing.T, br *bufio.Reader) {
	t.Helper()
	if _, err := br.ReadByte(); err != io.EOF {
		t.Fatalf("want the connection closed, got %v", err)
	}
}

// Billing hears of every member who joins or leaves (to update the seats),
// and an invitation's answer carries its notice.
func TestMembershipChangesReachBilling(t *testing.T) {
	t.Parallel()

	a, h := newServer(t)
	var changed []int64
	a.OnMembersChanged = func(org int64) { changed = append(changed, org) }
	a.InviteNotice = func(org int64) any { return map[string]bool{"subscription_needed": true} }
	owner := signIn(t, h, "admin", "s3cret")
	org := owner.me().Org.ID

	w := owner.do("POST", fmt.Sprintf("/api/orgs/%d/invitations", org), `{"email":"n@example.com","role":"member","password":"s3cret"}`)
	var inv struct {
		Token   string         `json:"token"`
		Billing map[string]any `json:"billing"`
	}
	decode(t, w, &inv)
	if w.Code != http.StatusCreated || inv.Billing["subscription_needed"] != true {
		t.Fatalf("invite: %d %s", w.Code, w.Body)
	}
	anon := &client{t: t, h: h}
	if w := anon.do("POST", "/api/invitations/register", `{"token":"`+inv.Token+`","username":"nia","password":"long-enough"}`); w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	other, _ := a.acc.CreateOrg(mustEnv(t, a), "Other")
	tok, _, err := a.acc.Invite(mustEnv(t, a), other.ID, "nia@example.com", store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if w := anon.do("POST", "/api/invitations/accept", `{"token":"`+tok+`"}`); w.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", w.Code, w.Body)
	}
	uid := anon.me().User.ID
	if w := owner.do("DELETE", fmt.Sprintf("/api/orgs/%d/members/%d", org, uid), `{"password":"s3cret"}`); w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	if want := []int64{org, other.ID, org}; fmt.Sprint(changed) != fmt.Sprint(want) {
		t.Fatalf("members changed in %v, want %v", changed, want)
	}
}
