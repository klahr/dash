package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// Deleting an account removes what was only theirs, keeps what they made
// in orgs that remain without them, and refuses while an org depends on
// them.
func TestDeleteUser(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	admin, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	now := time.Now()

	ann := addMember(t, acc, admin.ID, def.ID, "ann", RoleMember)
	personal, err := acc.EnsurePersonalOrg(ann)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := acc.CreateToken(NewToken{Kind: TokenAPI, Name: "ci", UserID: ann, OrgID: def.ID, Scopes: []string{"read"}}, now); err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(model.Project{Name: "app", Path: t.TempDir(), OrgID: def.ID})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTodo(model.Todo{ProjectID: p.ID, CreatedBy: ann, Title: "Ann's task", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTodoChat(task.ID, []model.ChatMessage{{Role: "user", Content: "ann asks", CreatedBy: ann}, {Role: "assistant", Content: "answer"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChat(def.ID, ann, []model.ChatMessage{{Role: "user", Content: "private question", CreatedBy: ann}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChat(def.ID, admin.ID, []model.ChatMessage{{Role: "user", Content: "admin's own", CreatedBy: admin.ID}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []AuthEvent{
		{OrgID: def.ID, UserID: ann, Actor: ann, Kind: "google.linked", Detail: "ann@example.com", IP: "192.0.2.7"},
		{OrgID: def.ID, UserID: admin.ID, Actor: ann, Kind: "member.signed_out", Detail: "admin", IP: "192.0.2.7"},
	} {
		if err := acc.Audit(e); err != nil {
			t.Fatal(err)
		}
	}

	// The export has what Dash keeps about ann.
	x, err := acc.ExportUser(ann, now)
	if err != nil {
		t.Fatal(err)
	}
	if x.Account.Username != "ann" || len(x.Organizations) != 2 || len(x.Tokens) != 1 || len(x.Tasks) != 1 || x.Tasks[0].Title != "Ann's task" {
		t.Fatalf("export: %+v", x)
	}
	if len(x.Messages) != 2 || len(x.AuthEvents) != 2 || x.AuthEvents[0].IP != "192.0.2.7" {
		t.Fatalf("export messages %+v, log %+v", x.Messages, x.AuthEvents)
	}

	// Not the env account.
	if _, err := acc.DeleteUser(admin.ID); !errors.Is(err, ErrEnvAccount) {
		t.Fatalf("env account: %v", err)
	}
	// Not while their personal workspace has projects.
	pp, err := s.CreateProject(model.Project{Name: "mine", Path: t.TempDir(), OrgID: personal.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acc.DeleteUser(ann); !errors.Is(err, ErrAccountInUse) || !strings.Contains(err.Error(), "personal workspace still has projects") {
		t.Fatalf("with projects: %v", err)
	}
	if _, err := s.DeleteProject(pp.ID); err != nil {
		t.Fatal(err)
	}

	d, err := acc.DeleteUser(ann)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Tokens) != 1 || len(d.DeletedOrgs) != 1 || d.DeletedOrgs[0].ID != personal.ID || len(d.Orgs) != 1 || d.Orgs[0] != def.ID {
		t.Fatalf("deleted: %+v", d)
	}
	if _, err := acc.User(ann); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user still there: %v", err)
	}
	check := func(s *Store) {
		t.Helper()
		got, err := s.GetTodo(task.ID)
		if err != nil || got.CreatedBy != model.DeletedUser || got.Title != "Ann's task" {
			t.Fatalf("task: %+v %v", got, err)
		}
		if c := s.TodoChat(task.ID); len(c) != 2 || c[0].CreatedBy != model.DeletedUser || c[0].Content != "ann asks" {
			t.Fatalf("task chat: %+v", c)
		}
		if c := s.Chat(def.ID, ann); len(c) != 0 {
			t.Fatalf("ann's general chat stayed: %+v", c)
		}
		if c := s.Chat(def.ID, admin.ID); len(c) != 1 {
			t.Fatalf("admin's general chat: %+v", c)
		}
	}
	check(s)
	var n int
	for q, want := range map[string]int{
		"SELECT COUNT(*) FROM organizations WHERE id = " + itoa(personal.ID):                             0,
		"SELECT COUNT(*) FROM tokens":                                                                    0,
		"SELECT COUNT(*) FROM auth_events WHERE user_id IS NULL AND ip = '' AND detail = ''":             1,
		"SELECT COUNT(*) FROM auth_events WHERE actor_id IS NULL AND ip = '' AND detail = 'admin'":       1,
		"SELECT COUNT(*) FROM auth_events WHERE ip != '' OR user_id = " + itoa(ann):                      0,
		"SELECT COUNT(*) FROM org_memberships WHERE user_id = " + itoa(ann):                              0,
		"SELECT COUNT(*) FROM chat_messages WHERE kind = 'main' AND owner_id LIKE '%:" + itoa(ann) + "'": 0,
	} {
		if err := s.db.QueryRow(q).Scan(&n); err != nil || n != want {
			t.Errorf("%s: %d, want %d (%v)", q, n, want, err)
		}
	}
	// The same from the database.
	s.Close()
	s2, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	check(s2)

	// A user whose new id is ann's old one isn't the task's author.
	if nid := addMember(t, s2.Accounts(), admin.ID, def.ID, "newcomer", RoleMember); nid == model.DeletedUser {
		t.Fatal("a user got the deleted marker as id")
	}
	if got, _ := s2.GetTodo(task.ID); got.CreatedBy != model.DeletedUser {
		t.Fatalf("author: %d", got.CreatedBy)
	}
}

// The only owner of an org with other members hands it over first; an org
// they are alone in goes with them.
func TestDeleteUserLastOwner(t *testing.T) {
	t.Parallel()

	_, acc := openAccounts(t)
	admin, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	bob := addMember(t, acc, admin.ID, def.ID, "bob", RoleMember)
	team, err := acc.CreateOrg(bob, "Team")
	if err != nil {
		t.Fatal(err)
	}
	solo, err := acc.CreateOrg(bob, "Solo")
	if err != nil {
		t.Fatal(err)
	}
	carl := addMember(t, acc, bob, team.ID, "carl", RoleAdmin)
	if _, err := acc.DeleteUser(bob); !errors.Is(err, ErrAccountInUse) || !strings.Contains(err.Error(), `only owner of "Team"`) {
		t.Fatalf("last owner: %v", err)
	}
	if err := acc.SetRole(bob, team.ID, carl, RoleOwner); err != nil {
		t.Fatal(err)
	}
	d, err := acc.DeleteUser(bob)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.DeletedOrgs) != 1 || d.DeletedOrgs[0].ID != solo.ID || len(d.Orgs) != 2 {
		t.Fatalf("deleted: %+v", d)
	}
	if ms, _ := acc.Members(team.ID); len(ms) != 1 || ms[0].ID != carl {
		t.Fatalf("team: %+v", ms)
	}
}

// The last platform admin stays.
func TestDeleteUserLastPlatformAdmin(t *testing.T) {
	t.Parallel()

	s, acc := openAccounts(t)
	admin, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	dan := addMember(t, acc, admin.ID, def.ID, "dan", RoleMember)
	if _, err := s.db.Exec("UPDATE users SET is_platform_admin = CASE id WHEN ? THEN 1 ELSE 0 END", dan); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.DeleteUser(dan); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last admin: %v", err)
	}
}

func TestPruneAuthEvents(t *testing.T) {
	t.Parallel()

	_, acc := openAccounts(t)
	now := time.Now()
	for _, at := range []time.Time{now.Add(-400 * 24 * time.Hour), now.Add(-time.Hour)} {
		if err := acc.Audit(AuthEvent{At: at, Kind: "signin.google", IP: "192.0.2.1"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := acc.PruneAuthEvents(now.Add(-365 * 24 * time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d %v", n, err)
	}
	var left int
	if err := acc.db.QueryRow("SELECT COUNT(*) FROM auth_events").Scan(&left); err != nil || left != 1 {
		t.Fatalf("left %d %v", left, err)
	}
}
