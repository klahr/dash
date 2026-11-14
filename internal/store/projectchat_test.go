package store

import (
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// Each member has their own chat about a project: another's isn't theirs
// to read, clearing one leaves the others, and only its user hears that it
// changed. It goes with its project, and with its user leaving the org.
func TestProjectChatsArePerUser(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	evs := &recordedEvents{}
	s, err := Open(path, evs)
	if err != nil {
		t.Fatal(err)
	}
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	admin, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	ann := addMember(t, acc, admin.ID, def.ID, "ann", RoleMember)
	bob := addMember(t, acc, admin.ID, def.ID, "bob", RoleMember)
	p, err := s.CreateProject(model.Project{Name: "app", Path: t.TempDir(), OrgID: def.ID})
	if err != nil {
		t.Fatal(err)
	}
	q := func(user int64, text string) model.ChatMessage {
		return model.ChatMessage{Role: "user", Content: text, CreatedBy: user}
	}
	evs.evs = nil
	if err := s.AppendProjectChat(p.ID, ann, q(ann, "ann 1"), model.ChatMessage{Role: "assistant", Content: "to ann"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendProjectChat(p.ID, bob, q(bob, "bob 1")); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendProjectChat(p.ID, ann, q(ann, "ann 2")); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendProjectChat(p.ID, admin.ID, q(admin.ID, "admin 1")); err != nil {
		t.Fatal(err)
	}
	contents := func(msgs []model.ChatMessage) []string {
		var out []string
		for _, m := range msgs {
			out = append(out, m.Content)
		}
		return out
	}
	want := func(s *Store, user int64, texts ...string) {
		t.Helper()
		got := contents(s.ProjectChat(p.ID, user))
		if len(got) != len(texts) {
			t.Fatalf("user %d's chat: %q, want %q", user, got, texts)
		}
		for i := range texts {
			if got[i] != texts[i] {
				t.Fatalf("user %d's chat: %q, want %q", user, got, texts)
			}
		}
	}
	want(s, ann, "ann 1", "to ann", "ann 2")
	want(s, bob, "bob 1")

	// Each save told its own user only.
	var to []int64
	for _, ev := range evs.evs {
		if ev.Type == model.EvProjectChat {
			if ev.ProjectID != p.ID || ev.OrgID != def.ID {
				t.Errorf("event %+v", ev)
			}
			to = append(to, ev.UserID)
		}
	}
	if len(to) != 4 || to[0] != ann || to[1] != bob || to[2] != ann || to[3] != admin.ID {
		t.Fatalf("project chat events went to %v, want [ann bob ann admin]", to)
	}

	// Clearing ann's leaves bob's.
	evs.evs = nil
	if err := s.ClearProjectChat(p.ID, ann); err != nil {
		t.Fatal(err)
	}
	want(s, ann)
	want(s, bob, "bob 1")
	if len(evs.evs) != 1 || evs.evs[0].Type != model.EvProjectChat || evs.evs[0].UserID != ann {
		t.Fatalf("clearing told %+v", evs.evs)
	}
	if err := s.AppendProjectChat(p.ID, ann, q(ann, "ann again")); err != nil {
		t.Fatal(err)
	}

	// The same from the database.
	s.Close()
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	want(s, ann, "ann again")
	want(s, bob, "bob 1")
	want(s, admin.ID, "admin 1")

	// Bob leaves: his chat goes, the others' stay.
	if err := s.Accounts().RemoveMember(bob, def.ID, bob); err != nil {
		t.Fatal(err)
	}
	want(s, bob)
	want(s, ann, "ann again")
	count := func(s *Store) int {
		t.Helper()
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM chat_messages WHERE kind = 'project'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(s); n != 2 {
		t.Fatalf("%d project chat messages after bob left, want ann's and admin's", n)
	}

	// The project goes with everyone's chats about it.
	if _, err := s.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	if n := count(s); n != 0 || len(s.ProjectChats()) != 0 {
		t.Fatalf("project chats of a deleted project: %d rows, %d in memory", n, len(s.ProjectChats()))
	}
}

// The shared project chats of before (keyed by the project alone) are
// dropped when the database is migrated.
func TestSharedProjectChatsDropped(t *testing.T) {
	t.Parallel()

	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	last := ms[len(ms)-1]
	if last.name != "0022_per_user_project_chats.sql" {
		t.Skipf("the newest migration is %s", last.name)
	}
	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(model.Project{Name: "app", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendProjectChat(p.ID, 7, model.ChatMessage{Role: "user", Content: "mine"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO chat_messages (kind, owner_id, seq, data) VALUES ('project', '" + p.ID + "', 0, '{\"role\":\"user\",\"content\":\"shared\"}')",
		"PRAGMA user_version = " + itoa(int64(last.version-1)),
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM chat_messages WHERE kind = 'project'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d project chat messages after migrating, want only the user's (%v)", n, err)
	}
	if c := s.ProjectChat(p.ID, 7); len(c) != 1 || c[0].Content != "mine" {
		t.Fatalf("user's chat: %+v", c)
	}
}

// A member's general chat in an org goes with them leaving it, from memory
// and from the database; the other members' stay.
func TestGeneralChatGoesWithMember(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	admin, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	ann := addMember(t, acc, admin.ID, def.ID, "ann", RoleMember)
	bob := addMember(t, acc, admin.ID, def.ID, "bob", RoleMember)
	for _, u := range []int64{ann, bob} {
		if err := s.SetChat(def.ID, u, []model.ChatMessage{{Role: "user", Content: "hi", CreatedBy: u}}); err != nil {
			t.Fatal(err)
		}
	}

	if err := acc.RemoveMember(admin.ID, def.ID, bob); err != nil {
		t.Fatal(err)
	}
	check := func(s *Store) {
		t.Helper()
		if n := len(s.Chat(def.ID, bob)); n != 0 {
			t.Fatalf("bob's general chat has %d messages after he left", n)
		}
		if n := len(s.Chat(def.ID, ann)); n != 1 {
			t.Fatalf("ann's general chat has %d messages, want 1", n)
		}
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM chat_messages WHERE kind = 'main'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("%d general chat messages in the database, want ann's", n)
		}
	}
	check(s)
	s.Close()
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	check(s)
}
