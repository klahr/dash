package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/klahr/dash/internal/passhash"
)

func openAccounts(t *testing.T) (*Store, *Accounts) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a := s.Accounts()
	if err := a.Bootstrap("admin", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	return s, a
}

func TestSignUpVerifyAndPersonalOrg(t *testing.T) {
	s, a := openAccounts(t)
	now := time.Now()
	res, err := a.SignUp("ann@example.com", now)
	if err != nil || !res.Created || res.Token == "" || res.User.Username != "ann" {
		t.Fatalf("signup: %+v %v", res, err)
	}
	if v, _ := a.Unverified(res.User.ID); !v {
		t.Fatal("verified before the link")
	}
	if ms, _ := a.Memberships(res.User.ID); len(ms) != 0 {
		t.Fatalf("orgs before verifying: %+v", ms)
	}
	// A second "ann" gets another username.
	res2, err := a.SignUp("ann@other.example.com", now)
	if err != nil || res2.User.Username != "ann2" {
		t.Fatalf("second ann: %+v %v", res2, err)
	}
	// Expired links don't work.
	if _, err := a.VerifyEmail(res.Token, "ann-password", now.Add(VerifyTTL+time.Second)); !errors.Is(err, ErrEmailLink) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := a.VerifyEmail(res.Token, "short", now); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak password: %v", err)
	}
	u, err := a.VerifyEmail(res.Token, "ann-password", now)
	if err != nil || u.ID != res.User.ID {
		t.Fatalf("verify: %+v %v", u, err)
	}
	ms, _ := a.Memberships(u.ID)
	if len(ms) != 1 || !ms[0].Personal || ms[0].Role != RoleOwner {
		t.Fatalf("orgs: %+v", ms)
	}
	if n, err := s.ForOrg(ms[0].ID).MemberCount(); n != 1 || err != nil {
		t.Fatalf("members %d %v", n, err)
	}
	if p, _ := s.ForOrg(ms[0].ID).Personal(); !p {
		t.Fatal("not personal")
	}
	// Idempotent.
	if o, err := a.EnsurePersonalOrg(u.ID); err != nil || o.ID != ms[0].ID {
		t.Fatalf("ensure again: %+v %v", o, err)
	}
	// Signing up again for a confirmed address changes nothing.
	again, err := a.SignUp("ANN@example.com", now)
	if err != nil || !again.Existing || again.Token != "" {
		t.Fatalf("again: %+v %v", again, err)
	}
	if _, hash, _ := a.Credentials("ann"); hash == "" {
		t.Fatal("no hash")
	}
}

func TestPasswordResetSkipsEnvAccount(t *testing.T) {
	s, a := openAccounts(t)
	if _, err := s.db.Exec("UPDATE users SET email = 'admin@example.com' WHERE env_managed = 1"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := a.RequestPasswordReset("admin@example.com", time.Now()); ok || err != nil {
		t.Fatalf("env account: ok=%v %v", ok, err)
	}
	// Default stays the env account's ordinary org; a personal one is new.
	env, _ := a.EnvUser()
	def, _ := a.DefaultOrg()
	o, err := a.EnsurePersonalOrg(env.ID)
	if err != nil || o.ID == def.ID {
		t.Fatalf("personal: %+v %v", o, err)
	}
	if p, _ := s.ForOrg(def.ID).Personal(); p {
		t.Fatal("Default became personal")
	}
}

func TestResetTokenExpires(t *testing.T) {
	_, a := openAccounts(t)
	now := time.Now()
	res, _ := a.SignUp("bo@example.com", now)
	if _, err := a.VerifyEmail(res.Token, "bo-password", now); err != nil {
		t.Fatal(err)
	}
	_, tok, ok, err := a.RequestPasswordReset("bo@example.com", now)
	if !ok || err != nil {
		t.Fatalf("request: %v %v", ok, err)
	}
	if _, err := a.ResetPassword(tok, "new-password", now.Add(ResetTTL+time.Second)); !errors.Is(err, ErrEmailLink) {
		t.Fatalf("expired: %v", err)
	}
	// A newer link replaces the older.
	_, tok2, _, _ := a.RequestPasswordReset("bo@example.com", now)
	if _, err := a.ResetPassword(tok, "new-password", now); !errors.Is(err, ErrEmailLink) {
		t.Fatalf("replaced link: %v", err)
	}
	if _, err := a.ResetPassword(tok2, "new-password", now); err != nil {
		t.Fatal(err)
	}
}

// Signing up for someone else's address leaves no password the signer knows:
// the address's owner chooses it from the link.
func TestSignUpBeforeOwnerCantTakeOver(t *testing.T) {
	_, a := openAccounts(t)
	now := time.Now()
	first, err := a.SignUp("vic@example.com", now) // the attacker
	if err != nil || !first.Created {
		t.Fatalf("first: %+v %v", first, err)
	}
	_, hash, err := a.Credentials("vic@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _, _ := passhash.Verify(hash, "attacker-pw"); ok {
		t.Fatal("unverified account has a known password")
	}
	second, err := a.SignUp("vic@example.com", now) // the owner
	if err != nil || !second.Pending {
		t.Fatalf("second: %+v %v", second, err)
	}
	if _, err := a.VerifyEmail(first.Token, "attacker-pw", now); !errors.Is(err, ErrEmailLink) {
		t.Fatalf("replaced link: %v", err)
	}
	if _, err := a.VerifyEmail(second.Token, "victim-password", now); err != nil {
		t.Fatal(err)
	}
	_, hash, _ = a.Credentials("vic@example.com")
	if ok, _, _ := passhash.Verify(hash, "victim-password"); !ok {
		t.Fatal("owner's password isn't the account's")
	}
}

func TestStaleUnverifiedAccountsAreDeleted(t *testing.T) {
	_, a := openAccounts(t)
	now := time.Now()
	if _, err := a.SignUp("old@example.com", now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(VerifyTTL + time.Minute)
	if _, err := a.SignUp("someone@example.com", later); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Credentials("old@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale account kept: %v", err)
	}
	again, err := a.SignUp("old@example.com", later)
	if err != nil || !again.Created || again.User.Username != "old" {
		t.Fatalf("again: %+v %v", again, err)
	}
}
