package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// A delivery noted while the hook manager pinged the webhook survives the
// manager saving the row it read before the ping.
func TestPutGitHubHookKeepsLaterDelivery(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rec := model.GitHubHook{OrgID: 1, Repo: "O/R", HookID: 7, Secret: "s", URL: "https://dash.example.com/hook",
		State: "pending", Registered: t0, LastDelivery: t0.Add(time.Minute)}
	if err := s.PutGitHubHook(rec); err != nil {
		t.Fatal(err)
	}

	read, err := s.GitHubHook(1, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	answered := t0.Add(2 * time.Hour)
	if err := s.GitHubHookDelivered(1, "o/r", answered); err != nil {
		t.Fatal(err)
	}
	read.LastPing = answered.Add(-time.Second)
	if err := s.PutGitHubHook(read); err != nil {
		t.Fatal(err)
	}

	got, err := s.GitHubHook(1, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastDelivery.Equal(answered) {
		t.Errorf("last delivery = %v, want %v (the one noted after the row was read)", got.LastDelivery, answered)
	}
	if !got.LastPing.Equal(read.LastPing) {
		t.Errorf("last ping = %v, want %v", got.LastPing, read.LastPing)
	}

	// A later delivery in the saved row still moves it on.
	later := answered.Add(time.Hour)
	got.LastDelivery = later
	if err := s.PutGitHubHook(got); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GitHubHook(1, "o/r"); !got.LastDelivery.Equal(later) {
		t.Errorf("last delivery = %v, want %v", got.LastDelivery, later)
	}
}
