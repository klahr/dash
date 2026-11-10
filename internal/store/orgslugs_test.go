package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openSlugAccounts(t *testing.T) (*Store, *Accounts, User) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret"); err != nil {
		t.Fatal(err)
	}
	u, _ := acc.EnvUser()
	return s, acc, u
}

func TestValidSlug(t *testing.T) {
	t.Parallel()

	for s, ok := range map[string]bool{
		"acme": true, "a-b": true, "abc": true, "a1b2c3": true, "org-2": true,
		"ab": false, "": false, "-abc": false, "abc-": false, "ABC": false, "a_b": false, "a.b": false,
		"åäö": false, "a b": false, "xn--bcher": false,
		"a234567890123456789012345678901234567890": true, "a2345678901234567890123456789012345678901": false,
	} {
		if ValidSlug(s) != ok {
			t.Errorf("ValidSlug(%q) = %v", s, !ok)
		}
	}
}

func TestNewOrgSlugsAvoidReservedNames(t *testing.T) {
	t.Parallel()

	s, acc, u := openSlugAccounts(t)
	for name, want := range map[string]string{"Admin": "admin-org", "A": "a-org", "WWW": "www-org", "Ab": "ab-org"} {
		o, err := acc.CreateOrg(u.ID, name)
		if err != nil || o.Slug != want || o.Folder != want {
			t.Errorf("%q: slug %q folder %q (%v), want %q", name, o.Slug, o.Folder, err, want)
		}
	}
	s.SlugReserved = func(slug string) bool { return slug == "acme" }
	if o, _ := acc.CreateOrg(u.ID, "Acme"); o.Slug != "acme-org" {
		t.Errorf("configured reserved name: %q", o.Slug)
	}
	if o, _ := acc.CreateOrg(u.ID, "Admin"); o.Slug != "admin" {
		t.Errorf("the configured list replaces the defaults: %q", o.Slug)
	}
}

func TestSetOrgSlug(t *testing.T) {
	t.Parallel()

	_, acc, u := openSlugAccounts(t)
	now := time.Now()
	acme, _ := acc.CreateOrg(u.ID, "Acme")
	beta, _ := acc.CreateOrg(u.ID, "Beta")

	for slug, want := range map[string]error{"ab": ErrBadSlug, "Bad_Name": ErrBadSlug, "-x-": ErrBadSlug, "admin": ErrSlugReserved, "preview": ErrSlugReserved, "beta": ErrSlugTaken} {
		if _, err := acc.SetOrgSlug(u.ID, acme.ID, slug, now); !errors.Is(err, want) {
			t.Errorf("%q: %v, want %v", slug, err, want)
		}
	}
	// Only owners rename.
	tok, _, _ := acc.Invite(u.ID, acme.ID, "m@example.com", RoleAdmin)
	m, _, err := acc.RegisterWithInvitation(tok, "m", "pw-m-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acc.SetOrgSlug(m.ID, acme.ID, "newname", now); !errors.Is(err, ErrForbidden) {
		t.Errorf("admin renames: %v", err)
	}

	o, err := acc.SetOrgSlug(u.ID, acme.ID, " Acme-Two ", now)
	if err != nil || o.Slug != "acme-two" {
		t.Fatalf("rename: %+v %v", o, err)
	}
	// The folder doesn't move.
	if got, _ := acc.Org(acme.ID); got.Folder != "acme" {
		t.Errorf("folder after rename: %q", got.Folder)
	}
	// The old slug leads to the org …
	if got, moved, err := acc.OrgBySlug("acme", now); err != nil || !moved || got.ID != acme.ID || got.Slug != "acme-two" {
		t.Errorf("old slug: %+v moved=%v %v", got, moved, err)
	}
	if got, moved, err := acc.OrgBySlug("ACME-TWO", now); err != nil || moved || got.ID != acme.ID {
		t.Errorf("new slug: %+v moved=%v %v", got, moved, err)
	}
	// … and no other org can take it for 30 days, nor make a new org with it.
	if _, err := acc.SetOrgSlug(u.ID, beta.ID, "acme", now.Add(29*24*time.Hour)); !errors.Is(err, ErrSlugTaken) {
		t.Errorf("claiming a held slug: %v", err)
	}
	if o, _ := acc.CreateOrg(u.ID, "Acme"); o.Slug == "acme" || o.Folder == "acme" {
		t.Errorf("new org got the held slug or the folder: %+v", o)
	}
	// The org itself may take it back.
	if _, err := acc.SetOrgSlug(u.ID, acme.ID, "acme", now.Add(time.Hour)); err != nil {
		t.Errorf("taking back its own old slug: %v", err)
	}
	if _, moved, err := acc.OrgBySlug("acme", now.Add(time.Hour)); err != nil || moved {
		t.Errorf("after taking it back: moved=%v %v", moved, err)
	}
	redirects, _ := acc.SlugRedirects(acme.ID, now.Add(time.Hour))
	if _, ok := redirects["acme-two"]; !ok || len(redirects) != 1 {
		t.Errorf("redirects: %v", redirects)
	}
	// After 30 days the old name is free.
	later := now.Add(time.Hour + SlugRedirectTTL + time.Minute)
	if _, _, err := acc.OrgBySlug("acme-two", later); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired redirect: %v", err)
	}
	if o, err := acc.SetOrgSlug(u.ID, beta.ID, "acme-two", later); err != nil || o.Slug != "acme-two" {
		t.Errorf("claiming an expired slug: %+v %v", o, err)
	}
	// Beta keeps its folder too: one org's folder never becomes another's.
	if got, _ := acc.Org(beta.ID); got.Folder != "beta" {
		t.Errorf("beta's folder: %q", got.Folder)
	}
}

func TestFixSlugs(t *testing.T) {
	t.Parallel()

	s, acc, u := openSlugAccounts(t)
	o, _ := acc.CreateOrg(u.ID, "Acme")
	// An org from before the rules, or with a name reserved since.
	if _, err := s.db.Exec("UPDATE organizations SET slug = 'a' WHERE id = ?", o.ID); err != nil {
		t.Fatal(err)
	}
	s.SlugReserved = func(slug string) bool { return slug == "default" }
	fixed, err := acc.FixSlugs(time.Now())
	if err != nil || len(fixed) != 2 {
		t.Fatalf("fixed %+v %v", fixed, err)
	}
	for _, f := range fixed {
		got, _ := acc.Org(f.ID)
		if !acc.UsableSlug(got.Slug) || got.Slug != f.Slug {
			t.Errorf("%q → %q", f.Name, got.Slug)
		}
	}
	if got, _ := acc.Org(o.ID); got.Slug != "acme" || got.Folder != "acme" {
		t.Errorf("acme: %+v", got)
	}
	if again, _ := acc.FixSlugs(time.Now()); len(again) != 0 {
		t.Errorf("fixed twice: %+v", again)
	}
}

func TestSessionFamily(t *testing.T) {
	t.Parallel()

	_, acc, u := openSlugAccounts(t)
	now := time.Now()
	acme, _ := acc.CreateOrg(u.ID, "Acme")
	if err := acc.CreateSessionIn("base", u.ID, 0, "fam", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := acc.CreateSessionIn("org", u.ID, acme.ID, "fam", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := acc.CreateSessionIn("other", u.ID, 0, "", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s, err := acc.Session("org", now); err != nil || s.Org.ID != acme.ID || s.Family != "fam" {
		t.Fatalf("org session: %+v %v", s, err)
	}
	if f, err := acc.EnsureSessionFamily("other", "new"); err != nil || f != "new" {
		t.Errorf("ensure family: %q %v", f, err)
	}
	if f, _ := acc.EnsureSessionFamily("base", "new"); f != "fam" {
		t.Errorf("family replaced: %q", f)
	}
	if err := acc.DeleteSessionFamily("org"); err != nil {
		t.Fatal(err)
	}
	for key, gone := range map[string]bool{"base": true, "org": true, "other": false} {
		if _, err := acc.Session(key, now); errors.Is(err, ErrNotFound) != gone {
			t.Errorf("%s gone=%v", key, !gone)
		}
	}
}
