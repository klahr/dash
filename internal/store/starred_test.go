package store

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

func TestStarredProjects(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-password"); err != nil {
		t.Fatal(err)
	}
	u, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	other, _ := acc.CreateOrg(u.ID, "Other")
	bob := addMember(t, acc, u.ID, def.ID, "bob", RoleMember)
	p, _ := s.CreateProject(model.Project{Name: "shop", Path: t.TempDir(), OrgID: def.ID})
	q, _ := s.CreateProject(model.Project{Name: "blog", Path: t.TempDir(), OrgID: def.ID})
	elsewhere, _ := s.CreateProject(model.Project{Name: "elsewhere", Path: t.TempDir(), OrgID: other.ID})
	me := s.ForOrg(def.ID).AsUser(u.ID)
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

	if got, err := me.StarredProjects(); err != nil || len(got) != 0 || got == nil {
		t.Fatalf("none yet: %#v %v", got, err)
	}
	// In the order starred; starring again changes nothing.
	for i, k := range []string{q.ID, p.ID, "build-box/abc123", q.ID} {
		if err := me.StarProject(k, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("star %s: %v", k, err)
		}
	}
	want := []string{q.ID, p.ID, "build-box/abc123"}
	if got, _ := me.StarredProjects(); !slices.Equal(got, want) {
		t.Fatalf("starred %v, want %v", got, want)
	}
	// Keys that name no project as the browser does are refused.
	for _, bad := range []string{"", "a/b/c", "Big Runner/x", "../x", "x y", "runner/"} {
		if err := me.StarProject(bad, now); !errors.Is(err, ErrBadStarKey) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// Per user and per org.
	if got, _ := s.ForOrg(def.ID).AsUser(bob).StarredProjects(); len(got) != 0 {
		t.Fatalf("bob sees %v", got)
	}
	if got, _ := s.ForOrg(other.ID).AsUser(u.ID).StarredProjects(); len(got) != 0 {
		t.Fatalf("the other org has %v", got)
	}
	// Another org's project, starred by key, isn't listed in this one.
	if err := me.StarProject(elsewhere.ID, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := me.StarredProjects(); slices.Contains(got, elsewhere.ID) {
		t.Fatalf("another org's project listed: %v", got)
	}
	// No user, no stars.
	if _, err := s.ForOrg(def.ID).StarredProjects(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no user: %v", err)
	}

	// Unstarring, twice.
	for range 2 {
		if err := me.UnstarProject(p.ID); err != nil {
			t.Fatal(err)
		}
	}
	// A deleted project's stars go with it.
	if err := s.ForOrg(def.ID).AsUser(bob).StarProject(q.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteProject(q.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM starred_projects WHERE project_key = ?", q.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("stars of a deleted project: %d %v", n, err)
	}
	want = []string{"build-box/abc123"}
	if got, _ := me.StarredProjects(); !slices.Equal(got, want) {
		t.Fatalf("after unstar and delete %v, want %v", got, want)
	}

	// From the database.
	s.Close()
	s2, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got, _ := s2.ForOrg(def.ID).AsUser(u.ID).StarredProjects(); !slices.Equal(got, want) {
		t.Fatalf("reopened %v, want %v", got, want)
	}
}

func TestStarredProjectsCap(t *testing.T) {
	t.Parallel()
	s, acc := openAccounts(t)
	u, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	now := time.Now()
	for i := range maxStars {
		if err := s.StarProject(u.ID, def.ID, "r/p"+itoa(int64(i)), now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.StarProject(u.ID, def.ID, "r/one-more", now); !errors.Is(err, ErrTooManyStars) {
		t.Fatalf("over the cap: %v", err)
	}
	if err := s.StarProject(u.ID, def.ID, "r/p0", now); err != nil {
		t.Fatalf("again at the cap: %v", err)
	}
}
