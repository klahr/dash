package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// Migration 0030 gives users who saved their notification settings before
// it the new default, "Tasks I created", and lets the summaries' activity
// keep who created each task.
func TestMigrateNotifyTasks(t *testing.T) {
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
	if _, _, err := s.SaveNotifySettings(u.ID, def.ID, model.DefaultNotifySettings(), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Back to before 0030: settings without "tasks", activity without creator.
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := migrations()
	if ms[29].name != "0030_notify_tasks.sql" {
		t.Fatalf("migration 30 is %s", ms[29].name)
	}
	for _, q := range []string{
		"UPDATE notify_settings SET settings = json_remove(settings, '$.tasks')",
		"ALTER TABLE notify_activity DROP COLUMN creator",
		"PRAGMA user_version = 29",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	var raw string
	if err := db.QueryRow("SELECT settings FROM notify_settings").Scan(&raw); err != nil || strings.Contains(raw, `"tasks"`) {
		t.Fatalf("before: %s %v", raw, err)
	}
	db.Close()

	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.db.QueryRow("SELECT json_extract(settings, '$.tasks') FROM notify_settings").Scan(&raw); err != nil || raw != model.TasksMine {
		t.Fatalf("migrated tasks %q %v", raw, err)
	}
	if set, err := s.PushSettingsFor(u.ID, def.ID); err != nil || set.Tasks != model.TasksMine {
		t.Fatalf("push settings %+v %v", set, err)
	}
	if ok, err := s.IsOrgMember(u.ID, def.ID); !ok || err != nil {
		t.Fatalf("member %v %v", ok, err)
	}
	if ok, err := s.IsOrgMember(u.ID+100, def.ID); ok || err != nil {
		t.Fatalf("stranger is a member: %v %v", ok, err)
	}
	now := time.Now().Truncate(time.Second)
	if err := s.RecordActivity(model.Activity{ProjectID: "p1", At: now, Kind: model.SummaryReview, Ref: "t1", Creator: u.ID}); err != nil {
		t.Fatal(err)
	}
	acts, err := s.ListActivity([]string{"p1"}, now.Add(-time.Minute), now)
	if err != nil || len(acts) != 1 || acts[0].Creator != u.ID {
		t.Fatalf("activity %+v %v", acts, err)
	}
}
