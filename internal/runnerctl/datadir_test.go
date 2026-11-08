package runnerctl

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHubAndRunnerHaveTheirOwnDataDirs(t *testing.T) {
	home := t.TempDir()
	hub, runner := HubData(home), RunnerData(home)
	if hub == runner {
		t.Fatalf("both default to %s", hub)
	}
	if err := ClaimData(hub, ModeHub); err != nil {
		t.Fatal(err)
	}
	if err := ClaimData(runner, ModeRunner); err != nil {
		t.Fatal(err)
	}
	// Claiming again is fine; the other kind is refused.
	if err := ClaimData(hub, ModeHub); err != nil {
		t.Error(err)
	}
	if err := ClaimData(hub, ModeRunner); err == nil {
		t.Error("a runner took the hub's data directory")
	}
	if err := ClaimData(runner, ModeHub); err == nil {
		t.Error("a hub took the runner's data directory")
	}
}

func TestRunnerRegisteredInTheOldDirKeepsIt(t *testing.T) {
	home := t.TempDir()
	touch(t, filepath.Join(HubData(home), "runner.json"), "{}")
	if got := RunnerData(home); got != HubData(home) {
		t.Fatalf("runner data %s, want its old %s", got, HubData(home))
	}
	if err := ClaimData(HubData(home), ModeRunner); err != nil {
		t.Fatal(err)
	}
	if err := ClaimData(HubData(home), ModeHub); err == nil {
		t.Error("a hub took the runner's old data directory")
	}
}

func TestRunnerSharingTheHubsDirMovesItsRegistration(t *testing.T) {
	home := t.TempDir()
	old := HubData(home)
	touch(t, filepath.Join(old, "runner.json"), `{"token":"dr_x"}`)
	touch(t, filepath.Join(old, "runners.json"), "{}") // the hub's
	dir := RunnerData(home)
	if dir == old {
		t.Fatal("runner kept the hub's directory")
	}
	if err := MigrateRunnerData(home, dir); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "runner.json")); err != nil || string(b) != `{"token":"dr_x"}` {
		t.Fatalf("registration %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(old, "runner.json")); !os.IsNotExist(err) {
		t.Error("registration left in the hub's directory")
	}
	if err := ClaimData(dir, ModeRunner); err != nil {
		t.Fatal(err)
	}
	if err := ClaimData(old, ModeHub); err != nil {
		t.Errorf("hub refused its own directory: %v", err)
	}
	if RunnerData(home) != dir {
		t.Error("runner's directory changed after moving")
	}
}
