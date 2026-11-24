package auth

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/store"
)

// adminEnv is a hub with the env account (a platform admin, owner of the
// default org) and members of the default org: ada (admin), bea (member),
// cal (member, also in org Two where ada isn't), ola (owner) and dan
// (member). Email goes to box.
type adminEnv struct {
	a                       *Auth
	h                       http.Handler
	box                     *outbox
	org, two                int64
	ada, bea, cal, ola, dan int64
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	a, h := newServer(t)
	box := newOutbox()
	a.SetMail(box, "https://dash.example.com/")
	e := &adminEnv{a: a, h: h, box: box}
	org, _ := a.acc.DefaultOrg()
	env, _ := a.acc.EnvUser()
	two, err := a.acc.CreateOrg(env.ID, "Two")
	if err != nil {
		t.Fatal(err)
	}
	e.org, e.two = org.ID, two.ID
	e.ada = addUser(t, a, org.ID, "ada", store.RoleAdmin)
	e.bea = addUser(t, a, org.ID, "bea", store.RoleMember)
	e.cal = addUser(t, a, org.ID, "cal", store.RoleMember)
	e.ola = addUser(t, a, org.ID, "ola", store.RoleOwner)
	e.dan = addUser(t, a, org.ID, "dan", store.RoleMember)
	if _, err := a.acc.AdminSetMember(two.ID, e.cal, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *adminEnv) as(t *testing.T, name string) *client {
	if name == "admin" {
		return signIn(t, e.h, "admin", "s3cret")
	}
	return signIn(t, e.h, name, "pw-"+name+"-long")
}

func TestAdminRoutesArePlatformAdminsOnly(t *testing.T) {
	t.Parallel()

	e := newAdminEnv(t)
	bea := e.as(t, "bea")
	ada := e.as(t, "ada") // an org admin isn't a platform admin
	anon := &client{t: t, h: e.h}
	routes := [][3]string{
		{"GET", "/api/admin/users", ""},
		{"GET", fmt.Sprintf("/api/admin/users/%d", e.dan), ""},
		{"POST", fmt.Sprintf("/api/admin/users/%d/password-reset", e.dan), `{"password":"x"}`},
		{"DELETE", fmt.Sprintf("/api/admin/users/%d/2fa", e.dan), `{"password":"x"}`},
		{"POST", fmt.Sprintf("/api/admin/users/%d/sign-out", e.dan), `{"password":"x"}`},
		{"PUT", fmt.Sprintf("/api/admin/users/%d/disabled", e.dan), `{"disabled":true,"password":"x"}`},
		{"PUT", fmt.Sprintf("/api/admin/users/%d/platform-admin", e.dan), `{"platform_admin":true,"password":"x"}`},
		{"PUT", fmt.Sprintf("/api/admin/users/%d/orgs/%d", e.dan, e.two), `{"role":"owner","password":"x"}`},
		{"DELETE", fmt.Sprintf("/api/admin/users/%d/orgs/%d", e.dan, e.org), `{"password":"x"}`},
		{"GET", "/api/admin/orgs", ""},
		{"GET", fmt.Sprintf("/api/admin/orgs/%d", e.org), ""},
		{"PATCH", fmt.Sprintf("/api/admin/orgs/%d", e.org), `{"name":"Mine","password":"x"}`},
		{"DELETE", fmt.Sprintf("/api/admin/orgs/%d", e.two), `{"name":"Two","password":"x"}`},
	}
	for _, rt := range routes {
		for name, c := range map[string]*client{"member": bea, "org admin": ada} {
			if w := c.do(rt[0], rt[1], rt[2]); w.Code != http.StatusForbidden {
				t.Errorf("%s %s as %s: %d %s", rt[0], rt[1], name, w.Code, w.Body)
			}
		}
		if w := anon.do(rt[0], rt[1], rt[2]); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s signed out: %d", rt[0], rt[1], w.Code)
		}
	}
	// Nothing changed.
	if u, _ := e.a.acc.User(e.dan); u.PlatformAdmin {
		t.Fatal("dan became a platform admin")
	}
}

func TestAdminListsUsersAndOrgs(t *testing.T) {
	t.Parallel()

	e := newAdminEnv(t)
	e.as(t, "bea") // signs in: last sign-in is recorded
	admin := e.as(t, "admin")
	var list struct {
		Users          []store.AdminUser
		Total, Matched int
	}
	w := admin.do("GET", "/api/admin/users", "")
	decode(t, w, &list)
	if list.Total != 6 || len(list.Users) != 6 {
		t.Fatalf("users: %d %s", w.Code, w.Body)
	}
	decode(t, admin.do("GET", "/api/admin/users?q=bea%40example", ""), &list)
	if list.Total != 6 || list.Matched != 1 || list.Users[0].ID != e.bea || list.Users[0].LastLogin.IsZero() ||
		!list.Users[0].HasPassword || len(list.Users[0].Orgs) != 1 || list.Users[0].Orgs[0].Role != store.RoleMember {
		t.Fatalf("search: %+v", list)
	}
	var one struct {
		User   store.AdminUser
		Events []store.AuthEvent
	}
	decode(t, admin.do("GET", fmt.Sprintf("/api/admin/users/%d", e.cal), ""), &one)
	if one.User.Username != "cal" || len(one.User.Orgs) != 2 {
		t.Fatalf("cal: %+v", one)
	}
	if w := admin.do("GET", "/api/admin/users/999", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", w.Code)
	}
	var orgs struct {
		Orgs  []store.AdminOrg
		Total int
	}
	decode(t, admin.do("GET", "/api/admin/orgs", ""), &orgs)
	if orgs.Total != 2 || orgs.Orgs[0].Members != 6 || orgs.Orgs[1].Members != 2 {
		t.Fatalf("orgs: %+v", orgs)
	}
	var org struct {
		Org     store.AdminOrg
		Members []store.Member
	}
	decode(t, admin.do("GET", fmt.Sprintf("/api/admin/orgs/%d", e.two), ""), &org)
	if org.Org.Name != "Two" || len(org.Members) != 2 {
		t.Fatalf("org: %+v", org)
	}
}

func TestAdminResetPassword(t *testing.T) {
	t.Parallel()

	e := newAdminEnv(t)
	bea := e.as(t, "bea")
	admin := e.as(t, "admin")
	path := fmt.Sprintf("/api/admin/users/%d/password-reset", e.bea)
	if w := admin.do("POST", path, `{"password":"wrong"}`); w.Code != http.StatusForbidden {
		t.Fatalf("wrong password: %d %s", w.Code, w.Body)
	}
	e.box.none(t)
	w := admin.do("POST", path, `{"password":"s3cret"}`)
	var res struct {
		Path, URL string
		Emailed   bool
		Expires   time.Time
	}
	decode(t, w, &res)
	if w.Code != http.StatusOK || !strings.HasPrefix(res.Path, "/login.html#reset=") || res.URL != "https://dash.example.com"+res.Path || !res.Emailed {
		t.Fatalf("reset: %d %s", w.Code, w.Body)
	}
	m := e.box.next(t)
	if m.To != "bea@example.com" || !strings.Contains(m.Body, "An administrator (admin)") || tokenIn(t, m) != strings.TrimPrefix(res.Path, "/login.html#reset=") {
		t.Fatalf("email: %+v", m)
	}
	// Until the link is used, nothing changes.
	if w := bea.do("GET", "/api/me", ""); w.Code != http.StatusOK {
		t.Fatalf("bea before: %d", w.Code)
	}
	tok := strings.TrimPrefix(res.Path, "/login.html#reset=")
	anon := &client{t: t, h: e.h}
	if w := anon.do("POST", "/api/password/reset", fmt.Sprintf(`{"token":%q,"password":"a-new-password"}`, tok)); w.Code != http.StatusOK {
		t.Fatalf("use the link: %d %s", w.Code, w.Body)
	}
	if w := bea.do("GET", "/api/me", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("bea's session after the reset: %d", w.Code)
	}
	signIn(t, e.h, "bea", "a-new-password")

	// "Also sign out now".
	bea = signIn(t, e.h, "bea", "a-new-password")
	if w := admin.do("POST", path, `{"password":"s3cret","sign_out":true}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"sessions":`) {
		t.Fatalf("with sign-out: %d %s", w.Code, w.Body)
	}
	e.box.next(t)
	if w := bea.do("GET", "/api/me", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("bea after sign-out: %d", w.Code)
	}
	// Not on yourself, nor the env account (DASH_PASSWORD).
	env, _ := e.a.acc.EnvUser()
	if w := admin.do("POST", fmt.Sprintf("/api/admin/users/%d/password-reset", env.ID), `{"password":"s3cret"}`); w.Code != http.StatusConflict {
		t.Fatalf("self: %d", w.Code)
	}
	// Without email, the link is still shown.
	e.a.mail = nil
	w = admin.do("POST", path, `{"password":"s3cret"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"emailed":false`) {
		t.Fatalf("without email: %d %s", w.Code, w.Body)
	}
}

func TestAdminDisableAndSignOut(t *testing.T) {
	t.Parallel()

	e := newAdminEnv(t)
	bea := e.as(t, "bea")
	admin := e.as(t, "admin")
	path := fmt.Sprintf("/api/admin/users/%d/disabled", e.bea)
	if w := admin.do("PUT", path, `{"disabled":true,"password":"s3cret"}`); w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	if w := bea.do("GET", "/api/me", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("bea's session: %d", w.Code)
	}
	w := do(e.h, "POST", "/api/login", `{"username":"bea","password":"pw-bea-long"}`)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"disabled"`) {
		t.Fatalf("sign in disabled: %d %s", w.Code, w.Body)
	}
	// A wrong password doesn't say it's disabled.
	if w := do(e.h, "POST", "/api/login", `{"username":"bea","password":"nope"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", w.Code)
	}
	if w := admin.do("PUT", path, `{"disabled":false,"password":"s3cret"}`); w.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	bea = e.as(t, "bea")
	env, _ := e.a.acc.EnvUser()
	if w := admin.do("PUT", fmt.Sprintf("/api/admin/users/%d/disabled", env.ID), `{"disabled":true,"password":"s3cret"}`); w.Code != http.StatusConflict {
		t.Fatalf("disable yourself: %d", w.Code)
	}

	// Sign out everywhere: sessions, API tokens and connected apps.
	if _, _, err := e.a.acc.CreateToken(store.NewToken{Kind: store.TokenAPI, Name: "script", UserID: e.bea, OrgID: e.org, Scopes: []string{store.ScopeRead}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	w = admin.do("POST", fmt.Sprintf("/api/admin/users/%d/sign-out", e.bea), `{"password":"s3cret"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"tokens":1`) {
		t.Fatalf("sign out: %d %s", w.Code, w.Body)
	}
	if w := bea.do("GET", "/api/me", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("bea after sign-out: %d", w.Code)
	}
	if toks, _ := e.a.acc.Tokens(e.bea); len(toks) != 0 {
		t.Fatalf("tokens left: %+v", toks)
	}
}

func TestAdminPlatformAdminsAndMemberships(t *testing.T) {
	t.Parallel()

	e := newAdminEnv(t)
	admin := e.as(t, "admin")
	env, _ := e.a.acc.EnvUser()
	self := fmt.Sprintf("/api/admin/users/%d/platform-admin", env.ID)
	if w := admin.do("PUT", self, `{"platform_admin":false,"password":"s3cret"}`); w.Code != http.StatusConflict {
		t.Fatalf("demote the last platform admin (yourself): %d %s", w.Code, w.Body)
	}
	if w := admin.do("PUT", fmt.Sprintf("/api/admin/users/%d/platform-admin", e.bea), `{"platform_admin":true,"password":"s3cret"}`); w.Code != http.StatusOK {
		t.Fatalf("make bea one: %d %s", w.Code, w.Body)
	}
	if w := admin.do("PUT", self, `{"platform_admin":false,"password":"s3cret"}`); w.Code != http.StatusOK {
		t.Fatalf("demote yourself with bea left: %d %s", w.Code, w.Body)
	}
	if w := admin.do("GET", "/api/admin/users", ""); w.Code != http.StatusForbidden {
		t.Fatalf("no longer one: %d", w.Code)
	}
	bea := e.as(t, "bea")
	beaSelf := fmt.Sprintf("/api/admin/users/%d/platform-admin", e.bea)
	if w := bea.do("PUT", beaSelf, `{"platform_admin":false,"password":"pw-bea-long"}`); w.Code != http.StatusConflict {
		t.Fatalf("bea demotes the last one: %d %s", w.Code, w.Body)
	}

	// Add dan to Two, make him owner, then the org keeps an owner.
	member := fmt.Sprintf("/api/admin/users/%d/orgs/%d", e.dan, e.two)
	if w := bea.do("PUT", member, `{"role":"admin","password":"pw-bea-long"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"added":true`) {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	if w := bea.do("PUT", member, `{"role":"owner","password":"pw-bea-long"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"added":false`) {
		t.Fatalf("change role: %d %s", w.Code, w.Body)
	}
	envInTwo := fmt.Sprintf("/api/admin/users/%d/orgs/%d", env.ID, e.two)
	if w := bea.do("DELETE", envInTwo, `{"password":"pw-bea-long"}`); w.Code != http.StatusNoContent {
		t.Fatalf("remove the other owner: %d %s", w.Code, w.Body)
	}
	if w := bea.do("DELETE", member, `{"password":"pw-bea-long"}`); w.Code != http.StatusConflict {
		t.Fatalf("remove the last owner: %d %s", w.Code, w.Body)
	}
	if w := bea.do("PUT", member, `{"role":"member","password":"pw-bea-long"}`); w.Code != http.StatusConflict {
		t.Fatalf("demote the last owner: %d %s", w.Code, w.Body)
	}
}

func TestAdminOrgs(t *testing.T) {
	t.Parallel()

	e := newAdminEnv(t)
	admin := e.as(t, "admin")
	two := fmt.Sprintf("/api/admin/orgs/%d", e.two)
	w := admin.do("PATCH", two, `{"name":"Second","slug":"second","password":"s3cret"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"slug":"second"`) || !strings.Contains(w.Body.String(), `"name":"Second"`) {
		t.Fatalf("rename: %d %s", w.Code, w.Body)
	}
	if w := admin.do("DELETE", two, `{"name":"Two","password":"s3cret"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("delete with the old name: %d %s", w.Code, w.Body)
	}
	if w := admin.do("DELETE", fmt.Sprintf("/api/admin/orgs/%d", e.org), `{"name":"Default","password":"s3cret"}`); w.Code != http.StatusConflict {
		t.Fatalf("delete the default org: %d %s", w.Code, w.Body)
	}
	setStatus := func(status string) {
		t.Helper()
		if _, err := e.a.acc.UpdateOrgBilling(e.two, func(b *store.OrgBilling) error { b.Status = status; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	setStatus("active")
	if w := admin.do("DELETE", two, `{"name":"Second","password":"s3cret"}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "subscription") {
		t.Fatalf("delete an org with a live subscription: %d %s", w.Code, w.Body)
	}
	if w := admin.do("GET", two, ""); w.Code != http.StatusOK {
		t.Fatalf("org after the refusal: %d", w.Code)
	}
	setStatus("canceled")
	cal := e.as(t, "cal")
	if w := admin.do("DELETE", two, `{"name":"Second","password":"s3cret"}`); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if m := cal.me(); len(m.Orgs) != 1 {
		t.Fatalf("cal's orgs: %+v", m.Orgs)
	}
	if w := admin.do("GET", two, ""); w.Code != http.StatusNotFound {
		t.Fatalf("deleted org: %d", w.Code)
	}
}

// Section 2: org owners and admins manage their members' accounts, within
// the rules MemberAccess enforces.
func TestOrgAdminMemberAccounts(t *testing.T) {
	t.Parallel()

	e := newAdminEnv(t)
	ada := e.as(t, "ada")
	reset := func(c *client, pw string, uid int64) int {
		return c.do("POST", fmt.Sprintf("/api/orgs/%d/members/%d/password-reset", e.org, uid), `{"password":"`+pw+`"}`).Code
	}
	env, _ := e.a.acc.EnvUser()
	for _, c := range []struct {
		name string
		uid  int64
		want int
	}{
		{"a member only in their org", e.bea, http.StatusOK},
		{"a member also in another org", e.cal, http.StatusForbidden},
		{"an owner", e.ola, http.StatusForbidden},
		{"a platform admin", env.ID, http.StatusForbidden},
		{"themselves", e.ada, http.StatusForbidden},
		{"someone not in the org", 999, http.StatusNotFound},
	} {
		if got := reset(ada, "pw-ada-long", c.uid); got != c.want {
			t.Errorf("admin resets %s: %d, want %d", c.name, got, c.want)
		}
	}
	e.box.next(t) // bea's link
	// A refusal costs no password try: a wrong password still says why.
	w := ada.do("POST", fmt.Sprintf("/api/orgs/%d/members/%d/password-reset", e.org, e.cal), `{"password":"wrong"}`)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "another organization") {
		t.Fatalf("cal: %d %s", w.Code, w.Body)
	}
	dan := e.as(t, "dan")
	if got := reset(dan, "pw-dan-long", e.bea); got != http.StatusForbidden {
		t.Fatalf("a member resets: %d", got)
	}
	ola := e.as(t, "ola")
	if got := reset(ola, "pw-ola-long", e.ada); got != http.StatusOK {
		t.Fatalf("owner resets an admin: %d", got)
	}
	e.box.next(t)

	// The members list says, for admins only.
	var list struct {
		Accounts map[string]store.MemberAccount
	}
	decode(t, ada.do("GET", fmt.Sprintf("/api/orgs/%d/members", e.org), ""), &list)
	if a := list.Accounts[fmt.Sprint(e.cal)]; a.Can || !strings.Contains(a.Why, "another organization") {
		t.Fatalf("cal: %+v", a)
	}
	if a := list.Accounts[fmt.Sprint(e.bea)]; !a.Can || !a.HasPassword {
		t.Fatalf("bea: %+v", a)
	}
	if w := dan.do("GET", fmt.Sprintf("/api/orgs/%d/members", e.org), ""); strings.Contains(w.Body.String(), `"accounts"`) {
		t.Fatalf("a member sees accounts: %s", w.Body)
	}

	// Two-factor sign-in off, with a notice by email.
	e.a.acc.BeginTOTP(e.bea, "SECRET")
	if err := e.a.acc.ConfirmTOTP(e.bea, "SECRET", 1, []string{"code"}); err != nil {
		t.Fatal(err)
	}
	off := fmt.Sprintf("/api/orgs/%d/members/%d/2fa", e.org, e.bea)
	if w := dan.do("DELETE", off, `{"password":"pw-dan-long"}`); w.Code != http.StatusForbidden {
		t.Fatalf("member turns 2FA off: %d", w.Code)
	}
	if w := ada.do("DELETE", off, `{"password":"pw-ada-long"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"emailed":true`) {
		t.Fatalf("2FA off: %d %s", w.Code, w.Body)
	}
	if m := e.box.next(t); m.To != "bea@example.com" || !strings.Contains(m.Subject, "Two-factor sign-in was turned off") {
		t.Fatalf("notice: %+v", m)
	}
	if u, _ := e.a.acc.User(e.bea); u.TOTP {
		t.Fatal("still on")
	}
	if w := ada.do("DELETE", fmt.Sprintf("/api/orgs/%d/members/%d/2fa", e.org, e.cal), `{"password":"pw-ada-long"}`); w.Code != http.StatusForbidden {
		t.Fatalf("2FA off for cal: %d", w.Code)
	}

	// Sign out: everywhere for bea, in this org only for cal.
	bea := e.as(t, "bea")
	w = ada.do("POST", fmt.Sprintf("/api/orgs/%d/members/%d/sign-out", e.org, e.bea), `{"password":"pw-ada-long"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"everywhere":true`) {
		t.Fatalf("sign out bea: %d %s", w.Code, w.Body)
	}
	if w := bea.do("GET", "/api/me", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("bea: %d", w.Code)
	}
	w = ada.do("POST", fmt.Sprintf("/api/orgs/%d/members/%d/sign-out", e.org, e.cal), `{"password":"pw-ada-long"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"everywhere":false`) {
		t.Fatalf("sign out cal: %d %s", w.Code, w.Body)
	}

	// An account that signs in only with Google has no password to reset.
	g, _, err := e.a.acc.CreateIdentityUser("gia@example.com", store.Identity{Provider: store.ProviderGoogle, Subject: "g1", Email: "gia@example.com"},
		store.Join{Org: e.org, Role: store.RoleMember}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := reset(ada, "pw-ada-long", g.ID); got != http.StatusConflict {
		t.Fatalf("Google only: %d", got)
	}
}
