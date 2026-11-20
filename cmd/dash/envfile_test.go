package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A runner loads runner.env from its data directory after -env, and
// variables already set (or set by -env) win; a hub doesn't read it.
func TestLoadEnvFilesRunner(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, ".env"), "DASH_TEST_FROM_ENV=env\n")
	write(filepath.Join(data, "runner.env"), "DASH_TEST_KEY=from-file\nDASH_TEST_FROM_ENV=runner\nDASH_TEST_SET=file\n")
	for _, k := range []string{"DASH_TEST_KEY", "DASH_TEST_FROM_ENV"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("DASH_TEST_SET", "shell")

	hub := &options{envFile: filepath.Join(dir, "none.env"), dataDir: data}
	if _, err := loadEnvFiles(hub); err != nil {
		t.Fatal(err)
	}
	if v, ok := os.LookupEnv("DASH_TEST_KEY"); ok {
		t.Fatalf("a hub read runner.env: %q", v)
	}

	runner := &options{envFile: filepath.Join(dir, ".env"), dataDir: data, runnerMode: true}
	if _, err := loadEnvFiles(runner); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"DASH_TEST_KEY": "from-file", "DASH_TEST_FROM_ENV": "env", "DASH_TEST_SET": "shell"} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}

	// An unreadable runner.env is named in the error.
	bad := &options{envFile: filepath.Join(dir, "none.env"), dataDir: filepath.Join(dir, "baddata"), runnerMode: true}
	if err := os.MkdirAll(filepath.Join(dir, "baddata", "runner.env"), 0o700); err != nil {
		t.Fatal(err)
	}
	if path, err := loadEnvFiles(bad); err == nil || path != filepath.Join(dir, "baddata", "runner.env") {
		t.Errorf("directory as runner.env: %q, %v", path, err)
	}
}
