package store

import (
	"errors"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// An org admin manages an account only when every org of it is one they
// own or administer, never a platform admin's or their own, and only owners
// an owner's.
func TestMemberAccess(t *testing.T) {
	t.Parallel()

	_, acc, admin, org1, org2 := tokenAccounts(t)
	ada := addMember(t, acc, admin, org1, "ada", RoleAdmin)
	bea := addMember(t, acc, admin, org1, "bea", RoleMember)
	cal := addMember(t, acc, admin, org1, "cal", RoleMember)
	ola := addMember(t, acc, admin, org1, "ola", RoleOwner)
	dan := addMember(t, acc, admin, org1, "dan", RoleMember)
	if _, err := acc.AdminSetMember(org2, cal, RoleMember); err != nil { // cal is also in Two, where ada isn't
		t.Fatal(err)
	}

	for _, c := range []struct {
		name          string
		actor, target int64
		platform      bool
		want          error
	}{
		{"admin on a member of only their org", ada, bea, false, nil},
		{"admin on a member also in another org", ada, cal, false, WhyOtherOrg},
		{"admin on an owner", ada, ola, false, WhyOwner},
		{"admin on a platform admin", ada, admin, false, WhyPlatformAdmin},
		{"admin on themselves", ada, ada, false, WhySelf},
		{"member on a member", dan, bea, false, WhyNotAdmin},
		{"owner on an admin", ola, ada, false, nil},
		{"owner on a member also in another org", ola, cal, false, WhyOtherOrg},
		{"platform admin on anyone", admin, cal, true, nil},
		{"platform admin on themselves", admin, admin, true, WhySelf},
	} {
		err := acc.MemberAccess(c.actor, c.platform, org1, c.target)
		if c.want == nil && err != nil || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
		if c.want != nil && !errors.Is(err, ErrForbidden) {
			t.Errorf("%s: %v isn't ErrForbidden", c.name, err)
		}
	}
	// Once cal leaves Two, ada may.
	if err := acc.AdminRemoveMember(org2, cal); err != nil {
		t.Fatal(err)
	}
	if err := acc.MemberAccess(ada, false, org1, cal); err != nil {
		t.Fatalf("cal only in ada's org: %v", err)
	}
	// Someone who isn't in the org is not found.
	if err := acc.MemberAccess(ada, false, org2, bea); !errors.Is(err, ErrNotMember) {
		t.Fatalf("ada in Two: %v", err)
	}

	members, _ := acc.Members(org1)
	all, err := acc.MemberAccessAll(ada, false, members)
	if err != nil {
		t.Fatal(err)
	}
	if !all[bea].Can || all[ola].Can || all[ola].Why != WhyOwner.Error() || !all[bea].HasPassword {
		t.Fatalf("accounts: %+v", all)
	}
}

func TestLastPlatformAdmin(t *testing.T) {
	t.Parallel()

	_, acc, admin, org1, _ := tokenAccounts(t)
	bea := addMember(t, acc, admin, org1, "bea", RoleMember)
	if err := acc.SetPlatformAdmin(admin, false); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote the only platform admin: %v", err)
	}
	if _, err := acc.SetDisabled(admin, true, time.Now()); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("disable the only platform admin: %v", err)
	}
	if err := acc.SetPlatformAdmin(bea, true); err != nil {
		t.Fatal(err)
	}
	// A disabled platform admin doesn't count.
	if _, err := acc.SetDisabled(bea, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := acc.SetPlatformAdmin(admin, false); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote with only a disabled one left: %v", err)
	}
	if _, err := acc.SetDisabled(bea, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := acc.SetPlatformAdmin(admin, false); err != nil {
		t.Fatalf("demote with another one: %v", err)
	}
	if err := acc.SetPlatformAdmin(bea, false); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote the last one: %v", err)
	}
	if err := acc.SetPlatformAdmin(999, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

// A disabled account has no sessions, gets none, and its tokens don't
// work, until it's enabled again.
func TestDisabledAccount(t *testing.T) {
	t.Parallel()

	_, acc, admin, org1, _ := tokenAccounts(t)
	bea := addMember(t, acc, admin, org1, "bea", RoleMember)
	now := time.Now()
	if err := acc.CreateSessionVia("k1", bea, 0, "", MethodPassword, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	secret, _, err := acc.CreateToken(NewToken{Kind: TokenAPI, Name: "t", UserID: bea, OrgID: org1, Scopes: []string{ScopeRead}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := acc.AdminUser(bea); u.LastLogin.IsZero() || u.Disabled {
		t.Fatalf("after signing in: %+v", u)
	}
	n, err := acc.SetDisabled(bea, true, now)
	if err != nil || n != 1 {
		t.Fatalf("disable: %d %v", n, err)
	}
	if _, err := acc.Session("k1", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session: %v", err)
	}
	if err := acc.CreateSessionVia("k2", bea, 0, "", MethodPassword, now, now.Add(time.Hour)); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("new session: %v", err)
	}
	if _, _, err := acc.Authenticate(secret, now, TokenAPI); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("token: %v", err)
	}
	if d, _ := acc.Disabled(bea); !d {
		t.Fatal("not disabled")
	}
	if _, err := acc.SetDisabled(bea, false, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := acc.Authenticate(secret, now, TokenAPI); err != nil {
		t.Fatalf("token once enabled: %v", err)
	}
	if err := acc.CreateSessionVia("k3", bea, 0, "", MethodPassword, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("session once enabled: %v", err)
	}
}

func TestSignOutEverywhereAndResetLink(t *testing.T) {
	t.Parallel()

	_, acc, admin, org1, org2 := tokenAccounts(t)
	bea := addMember(t, acc, admin, org1, "bea", RoleMember)
	acc.AdminSetMember(org2, bea, RoleMember)
	now := time.Now()
	acc.CreateSessionVia("k1", bea, org1, "", MethodPassword, now, now.Add(time.Hour))
	acc.CreateSessionVia("k2", bea, org2, "", MethodPassword, now, now.Add(time.Hour))
	acc.CreateToken(NewToken{Kind: TokenAPI, Name: "one", UserID: bea, OrgID: org1, Scopes: []string{ScopeRead}}, now)
	acc.CreateToken(NewToken{Kind: TokenAPI, Name: "two", UserID: bea, OrgID: org2, Scopes: []string{ScopeRead}}, now)
	acc.CreateToken(NewToken{Kind: TokenRunner, Name: "laptop", UserID: bea, OrgID: org1, RunnerID: "r1"}, now)

	// In one org: its sessions and tokens only.
	n, toks, err := acc.SignOutMemberTokens(bea, org1)
	if err != nil || n != 1 || len(toks) != 1 || toks[0].Name != "one" {
		t.Fatalf("in org1: %d %+v %v", n, toks, err)
	}
	// Everywhere: the rest, but not the runner.
	n, toks, err = acc.SignOutEverywhere(bea)
	if err != nil || n != 1 || len(toks) != 1 || toks[0].Name != "two" {
		t.Fatalf("everywhere: %d %+v %v", n, toks, err)
	}
	left, _ := acc.Tokens(bea)
	if len(left) != 1 || left[0].Kind != TokenRunner {
		t.Fatalf("left: %+v", left)
	}

	// A reset link works like Forgot password's.
	u, tok, expires, err := acc.AdminResetLink(bea, now)
	if err != nil || u.ID != bea || tok == "" || expires.Before(now.Add(ResetTTL-time.Second)) {
		t.Fatalf("link: %+v %q %v %v", u, tok, expires, err)
	}
	if _, err := acc.ResetPassword(tok, "brand-new-password", now); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := acc.AdminResetLink(admin, now); !errors.Is(err, ErrEnvPassword) {
		t.Fatalf("env account: %v", err)
	}
	g, _, err := acc.CreateIdentityUser("gia@example.com", Identity{Provider: ProviderGoogle, Subject: "g1", Email: "gia@example.com"}, Join{Org: org1, Role: RoleMember}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := acc.AdminResetLink(g.ID, now); !errors.Is(err, ErrSSOOnly) {
		t.Fatalf("Google only: %v", err)
	}
}

func TestAdminTOTPMembersAndOrgs(t *testing.T) {
	t.Parallel()

	_, acc, admin, org1, org2 := tokenAccounts(t)
	bea := addMember(t, acc, admin, org1, "bea", RoleMember)
	if _, err := acc.AdminDisableTOTP(bea); !errors.Is(err, ErrTOTPNotSetUp) {
		t.Fatalf("off already: %v", err)
	}
	acc.BeginTOTP(bea, "SECRET")
	if err := acc.ConfirmTOTP(bea, "SECRET", 1, []string{"code"}); err != nil {
		t.Fatal(err)
	}
	// Even an org that requires it doesn't stop an admin.
	if _, err := acc.SetRequire2FA(admin, org1, false); err != nil {
		t.Fatal(err)
	}
	acc.db.Exec("UPDATE organizations SET require_2fa = 1 WHERE id = ?", org1)
	if u, err := acc.AdminDisableTOTP(bea); err != nil || u.TOTP {
		t.Fatalf("disable: %+v %v", u, err)
	}

	// Membership: add, change, keep an owner, remove.
	if added, err := acc.AdminSetMember(org2, bea, RoleAdmin); err != nil || !added {
		t.Fatalf("add: %v %v", added, err)
	}
	if added, err := acc.AdminSetMember(org2, bea, RoleOwner); err != nil || added {
		t.Fatalf("owner: %v %v", added, err)
	}
	if err := acc.SetRole(admin, org2, admin, RoleMember); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.AdminSetMember(org2, bea, RoleMember); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("demote the last owner: %v", err)
	}
	if err := acc.AdminRemoveMember(org2, bea); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("remove the last owner: %v", err)
	}
	if _, err := acc.AdminSetMember(org2, bea, "boss"); !errors.Is(err, ErrBadRole) {
		t.Fatalf("bad role: %v", err)
	}

	// Orgs: list, rename, slug, delete.
	orgs, err := acc.AdminOrgs("")
	if err != nil || len(orgs) != 2 || orgs[1].Name != "Two" || orgs[1].Members != 2 || !orgs[0].Default {
		t.Fatalf("orgs: %+v %v", orgs, err)
	}
	if found, _ := acc.AdminOrgs("tw"); len(found) != 1 {
		t.Fatalf("search: %+v", found)
	}
	if o, err := acc.AdminSetOrgName(org2, "Second"); err != nil || o.Name != "Second" {
		t.Fatalf("rename: %+v %v", o, err)
	}
	if o, err := acc.AdminSetOrgSlug(org2, "second", time.Now()); err != nil || o.Slug != "second" {
		t.Fatalf("slug: %+v %v", o, err)
	}
	if _, err := acc.DeleteOrg(org1); !errors.Is(err, ErrOrgInUse) {
		t.Fatalf("delete the default org: %v", err)
	}
	d, err := acc.DeleteOrg(org2)
	if err != nil || len(d.Members) != 2 {
		t.Fatalf("delete: %+v %v", d, err)
	}
	if _, err := acc.Membership(bea, org2); !errors.Is(err, ErrNotMember) {
		t.Fatalf("membership after delete: %v", err)
	}

	users, total, matched, err := acc.AdminUsers("BE")
	if err != nil || total != 2 || matched != 1 || users[0].ID != bea || len(users[0].Orgs) != 1 {
		t.Fatalf("users: %+v %d %d %v", users, total, matched, err)
	}
	if users, _, _, _ := acc.AdminUsers("%"); len(users) != 0 { // LIKE wildcards are literal
		t.Fatalf("%%: %+v", users)
	}
}

// DeleteOrg refuses while the org has projects or a subscription that still
// bills, and leaves both in place; once they are gone it deletes the org
// with its planner settings.
func TestDeleteOrgRefusesWhileInUse(t *testing.T) {
	t.Parallel()

	s, acc, admin, _, org2 := tokenAccounts(t)
	p, err := s.ForOrg(org2).CreateProject(model.Project{Name: "keep"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acc.DeleteOrg(org2); !errors.Is(err, ErrOrgInUse) {
		t.Fatalf("delete an org with a project: %v", err)
	}
	if _, err := acc.AdminOrg(org2); err != nil {
		t.Fatalf("org after the refusal: %v", err)
	}
	if _, err := s.ForOrg(org2).GetProject(p.ID); err != nil {
		t.Fatalf("project after the refusal: %v", err)
	}
	if _, err := s.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}

	for _, status := range []string{"active", "trialing", "past_due"} {
		if _, err := acc.UpdateOrgBilling(org2, func(b *OrgBilling) error { b.Status = status; return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := acc.DeleteOrg(org2); !errors.Is(err, ErrOrgInUse) {
			t.Fatalf("delete an org with a %s subscription: %v", status, err)
		}
		if b, err := acc.OrgBilling(org2); err != nil || b.Status != status {
			t.Fatalf("billing after the refusal: %+v %v", b, err)
		}
	}
	if _, err := acc.UpdateOrgBilling(org2, func(b *OrgBilling) error { b.Status = "canceled"; return nil }); err != nil {
		t.Fatal(err)
	}

	if err := acc.SetPlannerContext(admin, org2, false); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.DeleteOrg(org2); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := acc.AdminOrg(org2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org after delete: %v", err)
	}
	var n int
	if err := acc.db.QueryRow("SELECT COUNT(*) FROM org_planner_settings WHERE org_id = ?", org2).Scan(&n); err != nil || n != 0 {
		t.Fatalf("planner settings after delete: %d %v", n, err)
	}
	if b, err := acc.OrgBilling(org2); err != nil || b.Status != "" {
		t.Fatalf("billing after delete: %+v %v", b, err)
	}
}
