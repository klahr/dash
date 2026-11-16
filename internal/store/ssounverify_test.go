package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Migration 0024 sets domains verified by an owner's Google sign-in back
// to unverified; those verified by DNS or a platform admin stay verified.
func TestMigrateUnverifiesGoogleDomains(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := s.Accounts()
	if err := a.Bootstrap("admin", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	org, err := a.DefaultOrg()
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := migrations()
	if ms[23].name != "0024_sso_domain_unverify_google.sql" {
		t.Fatalf("migration 24 is %s", ms[23].name)
	}
	now := time.Now().Unix()
	for _, d := range []struct{ domain, how string }{{"victim.example.com", "google"}, {"dns.example.com", "dns"}, {"admin.example.com", "admin"}} {
		if _, err := db.Exec(`INSERT INTO org_sso_domains (org_id, domain, verified, how, created_at, verified_at, token) VALUES (?, ?, 1, ?, ?, ?, 't')`,
			org.ID, d.domain, d.how, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 23"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a = s.Accounts()

	if o, err := a.DomainOrg("victim.example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a Google-verified domain still counts: %+v %v", o, err)
	}
	for _, d := range []string{"dns.example.com", "admin.example.com"} {
		if o, err := a.DomainOrg(d); err != nil || o.OrgID != org.ID {
			t.Fatalf("%s: %+v %v", d, o, err)
		}
	}
}
