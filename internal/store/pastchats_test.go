package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// An answer's tool calls are saved with it and survive a restart.
func TestChatToolsSurviveReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s := openTest(t, path)
	want := []model.ChatMessage{{Role: "user", Content: "make a task"}, {Role: "assistant", Content: "Made it.", Tools: []model.ChatTool{
		{ID: "t1", Tool: "mcp__dash_board__create_todo", Input: json.RawMessage(`{"title":"Fix"}`), Pos: 0, Output: `{"id":"abc"}`, DurationMS: 1200},
		{ID: "t2", Tool: "Bash", Input: json.RawMessage(`{"command":"ls"}`), Pos: 0, Cut: true},
	}}}
	if err := s.SetChat(1, 1, want); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	if got := s.Chat(1, 1); !reflect.DeepEqual(got, want) {
		t.Fatalf("after reopen: %+v\nwant %+v", got, want)
	}
}

// New chat keeps the conversation as a past chat, which survives a
// restart, opens again in place of the current one, and can be deleted
// and restored for a day.
func TestPastChats(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s := openTest(t, path)
	clock := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return clock }
	img := model.Attachment{ID: "up1", Name: "a.png", Type: "image/png"}
	first := []model.ChatMessage{{Role: "user", Content: "  what is this?\nmore", Attachments: []model.Attachment{img}}, {Role: "assistant", Content: "a repo"}}
	if err := s.SetChat(1, 1, first); err != nil {
		t.Fatal(err)
	}
	if err := s.PutChatAside(1, 1, ""); err != nil {
		t.Fatal(err)
	}
	// An empty chat isn't kept.
	if err := s.PutChatAside(1, 1, ""); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	s.Now = func() time.Time { return clock }
	if got := s.Chat(1, 1); len(got) != 0 {
		t.Fatalf("current after New chat: %+v", got)
	}
	past, err := s.PastChats(1, 1, "")
	if err != nil || len(past) != 1 || past[0].Title != "what is this?" || past[0].Count != 2 || !past[0].At.Equal(clock) {
		t.Fatalf("past: %+v, %v", past, err)
	}
	if n, err := s.PastChatCount(1, 1, ""); err != nil || n != 1 {
		t.Fatalf("count: %d, %v", n, err)
	}
	if other, _ := s.PastChats(1, 2, ""); len(other) != 0 {
		t.Fatalf("another user's past chats: %+v", other)
	}
	if ups, err := s.PastChatUploads(); err != nil || !slices.Equal(ups, []string{"up1"}) {
		t.Fatalf("uploads: %v, %v", ups, err)
	}

	second := []model.ChatMessage{{Role: "user", Content: "second"}}
	if err := s.SetChat(1, 1, second); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	if _, err := s.OpenPastChat(1, 2, "", past[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another user opened it: %v", err)
	}
	got, err := s.OpenPastChat(1, 1, "", past[0].ID)
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("opened: %+v, %v", got, err)
	}
	s = openTest(t, path)
	s.Now = func() time.Time { return clock }
	if cur := s.Chat(1, 1); !reflect.DeepEqual(cur, first) {
		t.Fatalf("current after open: %+v", cur)
	}
	past, _ = s.PastChats(1, 1, "")
	if len(past) != 1 || past[0].Title != "second" {
		t.Fatalf("past after open: %+v", past)
	}

	if err := s.DeletePastChat(1, 1, "", past[0].ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.PastChats(1, 1, ""); len(l) != 0 {
		t.Fatalf("listed after delete: %+v", l)
	}
	if n, _ := s.PastChatCount(1, 1, ""); n != 0 {
		t.Fatalf("counted after delete: %d", n)
	}
	if err := s.RestorePastChat(1, 1, "", past[0].ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.PastChats(1, 1, ""); len(l) != 1 {
		t.Fatalf("not back after restore: %+v", l)
	}
	// A day after its delete, it's gone for good.
	if err := s.DeletePastChat(1, 1, "", past[0].ID); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(pastChatUndo + time.Minute)
	if err := s.RestorePastChat(1, 1, "", past[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restored after a day: %v", err)
	}
}

// A project chat's past chats are its user's, and go with the project.
func TestProjectPastChats(t *testing.T) {
	t.Parallel()

	s := openTest(t, filepath.Join(t.TempDir(), "dash.db"))
	p, err := s.CreateProject(model.Project{Name: "app", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendProjectChat(p.ID, 7, model.ChatMessage{Role: "user", Content: "about app"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutChatAside(0, 7, p.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.ProjectChat(p.ID, 7)) != 0 {
		t.Fatal("project chat not emptied")
	}
	if l, _ := s.PastChats(0, 8, p.ID); len(l) != 0 {
		t.Fatalf("another user's: %+v", l)
	}
	if l, _ := s.PastChats(0, 7, ""); len(l) != 0 {
		t.Fatalf("listed as the general chat's: %+v", l)
	}
	if l, _ := s.PastChats(0, 7, p.ID); len(l) != 1 || l[0].Title != "about app" {
		t.Fatalf("past: %+v", l)
	}
	if _, err := s.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM past_chats").Scan(&n); err != nil || n != 0 {
		t.Fatalf("past chats left after the project's delete: %d, %v", n, err)
	}
}

// 0049 copies each past chat's title and count out of data into their own
// columns, keeping the rest of the row.
func TestPastChatsTitleMigration(t *testing.T) {
	t.Parallel()
	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	if ms[48].name != "0049_past_chats_title.sql" {
		t.Fatalf("migration 49 is %s", ms[48].name)
	}
	db, err := openDB(filepath.Join(t.TempDir(), "dash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, m := range ms[:48] {
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
	}
	data := `{"id":"pc1","title":"what is this?","count":2,"messages":[{"role":"user","content":"what is this?"},{"role":"assistant","content":"a repo"}]}`
	if _, err := db.Exec(`INSERT INTO past_chats (id, kind, owner_id, project_id, user_id, org_id, at, deleted_at, data)
		VALUES ('pc1', 'main', 'o1', '', 1, 1, 5, NULL, ?), ('pc2', 'main', 'o1', '', 1, 1, 6, 7, '{}');
		PRAGMA user_version = 48`, data); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db, nil); err != nil {
		t.Fatal(err)
	}
	var title, got string
	var count int
	var at int64
	if err := db.QueryRow("SELECT title, count, at, data FROM past_chats WHERE id = 'pc1'").Scan(&title, &count, &at, &got); err != nil {
		t.Fatal(err)
	}
	if title != "what is this?" || count != 2 || at != 5 || got != data {
		t.Fatalf("pc1: %q %d %d %s", title, count, at, got)
	}
	var deleted int64
	if err := db.QueryRow("SELECT title, count, deleted_at FROM past_chats WHERE id = 'pc2'").Scan(&title, &count, &deleted); err != nil {
		t.Fatal(err)
	}
	if title != "" || count != 0 || deleted != 7 {
		t.Fatalf("pc2: %q %d %d", title, count, deleted)
	}
}
