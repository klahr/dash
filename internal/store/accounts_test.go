package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/passhash"
)

// orgsOf counts each table's rows by org ("null" for none).
func orgsOf(t *testing.T, s *Store) map[string]map[string]int {
	t.Helper()
	out := map[string]map[string]int{}
	for _, table := range orgTables {
		rows, err := s.db.Query("SELECT COALESCE(CAST(org_id AS TEXT), 'null'), COUNT(*) FROM " + table + " GROUP BY 1")
		if err != nil {
			t.Fatal(err)
		}
		out[table] = map[string]int{}
		for rows.Next() {
			var org string
			var n int
			if err := rows.Scan(&org, &n); err != nil {
				t.Fatal(err)
			}
			out[table][org] = n
		}
		rows.Close()
	}
	return out
}

// A database from before accounts (schema version 2, with records) gets the
// env account as its first user, owner of a "default" org that every record
// is assigned to.
func TestMigrateToAccounts(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := migrations()
	for _, m := range ms[:2] {
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatal(err)
		}
	}
	// Rows as the version-2 writers stored them.
	for _, q := range []string{
		`INSERT INTO projects (id, data) VALUES ('p1', '{"id":"p1","name":"app"}')`,
		`INSERT INTO todos (id, project_id, data) VALUES ('t1', 'p1', '{"id":"t1","project_id":"p1","status":"draft"}')`,
		`INSERT INTO issues (id, project_id, data) VALUES ('i1', 'p1', '{"id":"i1","project_id":"p1","status":"open"}')`,
		`INSERT INTO scan_runs (id, project_id, scan, data) VALUES ('r1', 'p1', 'sec', '{"id":"r1","project_id":"p1","scan":"sec"}')`,
		`INSERT INTO scan_settings (project_id, scan, data) VALUES ('p1', 'sec', '{"project_id":"p1","scan":"sec"}')`,
		`INSERT INTO scan_templates (project_id, name, data) VALUES ('p1', 'deps', '{"project_id":"p1","name":"deps"}')`,
		`INSERT INTO chat_messages (kind, owner_id, seq, data) VALUES ('main', '', 0, '{"role":"user","content":"hi"}')`,
		`PRAGMA user_version = 2`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	db.Close()

	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	acc := s.Accounts()
	if err := acc.Bootstrap("", ""); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("no users and no credentials: %v", err)
	}
	if err := acc.Bootstrap("admin", "s3cret"); err != nil {
		t.Fatal(err)
	}

	u, err := acc.EnvUser()
	if err != nil || u.Username != "admin" || !u.PlatformAdmin {
		t.Fatalf("env user %+v (%v)", u, err)
	}
	_, hash, err := acc.Credentials("admin")
	if ok, _, _ := passhash.Verify(hash, "s3cret"); err != nil || !ok {
		t.Fatalf("password not stored as Argon2id: %q (%v)", hash, err)
	}
	org, err := acc.DefaultOrg()
	if err != nil || org.Slug != "default" {
		t.Fatalf("default org %+v (%v)", org, err)
	}
	if m, err := acc.Membership(u.ID, org.ID); err != nil || m.Role != RoleOwner {
		t.Fatalf("membership %+v (%v)", m, err)
	}
	want := fmt.Sprint(org.ID)
	for table, byOrg := range orgsOf(t, s) {
		if table == "goals" {
			continue // came after accounts: a version-2 database has none
		}
		if len(byOrg) != 1 || byOrg[want] != 1 {
			t.Errorf("%s: rows by org %v, want 1 in %s", table, byOrg, want)
		}
	}
	// The records themselves are untouched.
	if p, err := s.GetProject("p1"); err != nil || p.Name != "app" {
		t.Fatalf("project after migrating: %+v (%v)", p, err)
	}
	// The store sees the orgs without a restart, and the general chat from
	// before users is the first account's own.
	if p, _ := s.GetProject("p1"); p.OrgID != org.ID {
		t.Fatalf("project's org in memory: %d, want %d", p.OrgID, org.ID)
	}
	if got := s.ForOrg(org.ID).Chat(u.ID); len(got) != 1 || got[0].Content != "hi" {
		t.Fatalf("the old general chat isn't the first account's: %+v", got)
	}
	if got := s.ForOrg(org.ID).ListTodos(""); len(got) != 1 {
		t.Fatalf("org's todos: %+v", got)
	}

	// Starting again changes nothing.
	s = reopen(t, s, path)
	if err := s.Accounts().Bootstrap("admin", "s3cret"); err != nil {
		t.Fatal(err)
	}
	var users, orgs int
	s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&users)
	s.db.QueryRow("SELECT COUNT(*) FROM organizations").Scan(&orgs)
	if users != 1 || orgs != 1 {
		t.Fatalf("after a restart: %d users, %d orgs", users, orgs)
	}
}

// Records imported from a state.json are assigned to the default org too.
func TestImportedRecordsGetTheDefaultOrg(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, LegacyFile), []byte(legacyState), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(dir, "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Accounts().Bootstrap("admin", "s3cret"); err != nil {
		t.Fatal(err)
	}
	org, _ := s.Accounts().DefaultOrg()
	for table, byOrg := range orgsOf(t, s) {
		if byOrg["null"] != 0 || len(byOrg) > 1 {
			t.Errorf("%s: rows by org %v, want all in %d", table, byOrg, org.ID)
		}
	}
}

// New rows get the default org; updating a row keeps whatever org it has.
func TestWritesKeepTheRowsOrg(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret"); err != nil {
		t.Fatal(err)
	}
	def, _ := acc.DefaultOrg()
	u, _ := acc.EnvUser()
	other, err := acc.CreateOrg(u.ID, "Other")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(model.Project{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	orgOf := func() int64 {
		var id int64
		if err := s.db.QueryRow("SELECT org_id FROM projects WHERE id = ?", p.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if got := orgOf(); got != def.ID {
		t.Fatalf("new project in org %d, want the default %d", got, def.ID)
	}
	if _, err := s.db.Exec("UPDATE projects SET org_id = ? WHERE id = ?", other.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	p.Name = "renamed"
	if _, err := s.UpdateProject(p.ID, func(q *model.Project) error { q.Name = "renamed"; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := orgOf(); got != other.ID {
		t.Fatalf("updated project moved to org %d, want %d", got, other.ID)
	}
}

func TestEnvAccountFollowsTheEnvironment(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret"); err != nil {
		t.Fatal(err)
	}
	u, _ := acc.EnvUser()
	now := time.Now()
	if err := acc.CreateSession("k", u.ID, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	before := time.Now()

	if err := acc.Bootstrap("root", "changed"); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.Session("k", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session survived new credentials: %v", err)
	}
	var changed int64
	if err := s.db.QueryRow("SELECT password_changed_at FROM users WHERE id = ?", u.ID).Scan(&changed); err != nil || changed <= before.UnixNano() {
		t.Fatalf("password_changed_at %d not after %d (%v): older tokens would still be good", changed, before.UnixNano(), err)
	}
	got, hash, err := acc.Credentials("root")
	if ok, _, _ := passhash.Verify(hash, "changed"); err != nil || got.ID != u.ID || !ok {
		t.Fatalf("renamed account %+v (%v)", got, err)
	}
}

func TestSlugs(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	acc := s.Accounts()
	acc.Bootstrap("admin", "s3cret")
	u, _ := acc.EnvUser()
	for _, tc := range []struct{ name, slug string }{
		{"Acme Corp", "acme-corp"},
		{"acme corp!", "acme-corp-2"},
		{"Default", "default-2"},
		{"Åäö", "org"},
	} {
		o, err := acc.CreateOrg(u.ID, tc.name)
		if err != nil || o.Slug != tc.slug {
			t.Errorf("%q: slug %q (%v), want %q", tc.name, o.Slug, err, tc.slug)
		}
	}
	if _, err := acc.CreateOrg(u.ID, "  "); !errors.Is(err, ErrBadOrgName) {
		t.Errorf("blank name: %v", err)
	}
}

func TestInvitationsExpire(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	acc := s.Accounts()
	acc.Bootstrap("admin", "s3cret")
	u, _ := acc.EnvUser()
	org, _ := acc.DefaultOrg()
	tok, inv, err := acc.Invite(u.ID, org.ID, "x@example.com", RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE org_invitations SET expires_at = ? WHERE id = ?", time.Now().Unix()-1, inv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.InvitationByToken(tok); !errors.Is(err, ErrInvitation) {
		t.Fatalf("expired invitation: %v", err)
	}
	if _, _, err := acc.RegisterWithInvitation(tok, "late", "long-enough"); !errors.Is(err, ErrInvitation) {
		t.Fatalf("register with an expired invitation: %v", err)
	}
	if _, _, err := acc.Invite(u.ID, org.ID, "not an email", RoleMember); !errors.Is(err, ErrBadEmail) {
		t.Fatalf("bad email: %v", err)
	}
}
