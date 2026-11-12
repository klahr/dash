package runnerctl

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releaseServer serves release 1.3.0 for linux/amd64: an archive holding a
// dash that says its version, and checksums.txt (sum overrides the
// archive's checksum when not "").
func releaseServer(t *testing.T, sum string) string {
	t.Helper()
	bin := []byte("#!/bin/sh\necho dash 1.3.0\n")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.WriteHeader(&tar.Header{Name: "dash", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(bin)
	_ = tw.Close()
	_ = gz.Close()
	archive := buf.Bytes()
	if sum == "" {
		s := sha256.Sum256(archive)
		sum = hex.EncodeToString(s[:])
	}
	const asset = "dash_1.3.0_linux_amd64.tar.gz"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /download/1.3.0/"+asset, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("GET /download/1.3.0/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  dash_1.3.0_darwin_arm64.tar.gz\n%s  %s\n", strings.Repeat("0", 64), sum, asset)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// installed is a dash binary at version 1.2.0 in a fresh directory.
func installed(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "dash")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho dash 1.2.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpdateInstallsTheReleaseAndKeepsTheOldBinary(t *testing.T) {
	t.Parallel()

	bin, data := installed(t), t.TempDir()
	old := read(t, bin)
	u := Updater{Version: "1.3.0", Bin: bin, Data: data, Current: "1.2.0", Releases: releaseServer(t, ""), GOOS: "linux", GOARCH: "amd64"}
	if err := u.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := read(t, bin); !strings.Contains(got, "dash 1.3.0") {
		t.Errorf("binary after the update: %q", got)
	}
	if got := read(t, bin+".prev"); got != old {
		t.Errorf("backup: %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(bin), ".dash.new-*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}

	// The new binary is on trial: it starts, and reaching the hub confirms it.
	tr, err := BeginTrial(data, "1.3.0")
	if err != nil || tr == nil || tr.From() != "1.2.0" {
		t.Fatalf("trial: %+v %v", tr, err)
	}
	tr.Confirm()
	if tr, err := BeginTrial(data, "1.3.0"); tr != nil || err != nil {
		t.Errorf("a confirmed update is on trial again: %v", err)
	}
}

// A download whose checksum isn't the release's is never installed: the
// old binary stays, with no backup or trial made.
func TestUpdateRefusesAChecksumMismatch(t *testing.T) {
	t.Parallel()

	bin, data := installed(t), t.TempDir()
	old := read(t, bin)
	u := Updater{Version: "1.3.0", Bin: bin, Data: data, Releases: releaseServer(t, strings.Repeat("ab", 32)), GOOS: "linux", GOARCH: "amd64"}
	err := u.Run(context.Background())
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
	if read(t, bin) != old {
		t.Error("the old binary was replaced")
	}
	for _, p := range []string{bin + ".prev", TrialPath(data)} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s made: %v", p, err)
		}
	}

	// No release for this computer, or no such release: nothing changes either.
	u = Updater{Version: "1.3.0", Bin: bin, Releases: releaseServer(t, ""), GOOS: "linux", GOARCH: "arm64"}
	if err := u.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "lists no") {
		t.Errorf("no archive for linux/arm64: %v", err)
	}
	if err := (Updater{Version: "main-dirty", Bin: bin}).Run(context.Background()); err == nil {
		t.Error("updated to a version that isn't a release")
	}
	if read(t, bin) != old {
		t.Error("the old binary was replaced")
	}
}

// A binary in a directory the runner can't write (installed by root) is
// reported before anything is downloaded.
func TestUpdateReportsAnUnwritableBinary(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}

	bin := installed(t)
	dir := filepath.Dir(bin)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	u := Updater{Version: "1.3.0", Bin: bin, Releases: "http://127.0.0.1:1", GOOS: "linux", GOARCH: "amd64"}
	if err := u.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "can't replace") {
		t.Fatalf("err = %v, want can't replace", err)
	}
}

// A new binary that keeps failing to start is put back: after a few starts
// without reaching the hub, or when told to (no connection in time).
func TestTrialRollsBackAFailingUpdate(t *testing.T) {
	t.Parallel()

	bin, data := installed(t), t.TempDir()
	u := Updater{Version: "1.3.0", Bin: bin, Data: data, Current: "1.2.0", Releases: releaseServer(t, ""), GOOS: "linux", GOARCH: "amd64"}
	if err := u.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < trialStarts; i++ {
		if tr, err := BeginTrial(data, "1.3.0"); tr == nil || err != nil {
			t.Fatalf("start %d: %v", i+1, err)
		}
	}
	if _, err := BeginTrial(data, "1.3.0"); !errors.Is(err, ErrRolledBack) {
		t.Fatalf("crash loop: %v, want a rollback", err)
	}
	if got := read(t, bin); !strings.Contains(got, "dash 1.2.0") {
		t.Errorf("after the rollback: %q", got)
	}
	// The old binary runs: no trial.
	if tr, err := BeginTrial(data, "1.2.0"); tr != nil || err != nil {
		t.Errorf("trial of the old binary: %v", err)
	}

	// No connection in time.
	if err := u.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	tr, err := BeginTrial(data, "1.3.0")
	if err != nil || tr == nil {
		t.Fatal(err)
	}
	if err := tr.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, bin); !strings.Contains(got, "dash 1.2.0") {
		t.Errorf("after the rollback: %q", got)
	}
}

func TestUpdateCommandUsesTheHubsVersion(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	data := RunnerData(home)
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "runner.json"), []byte(`{"server":"https://hub.example.com","id":"x","name":"box","token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := installed(t)
	releases := releaseServer(t, "")
	var out, errOut bytes.Buffer
	c := &CLI{Version: "1.2.0", Home: home, Hostname: "box", Out: &out, Err: &errOut,
		Executable: func() (string, error) { return bin, nil },
		HubVersion: func(_ context.Context, server string) (string, error) { return "1.3.0", nil },
		Updater:    func(u *Updater) { u.Releases, u.GOOS, u.GOARCH = releases, "linux", "amd64" },
		Service:    func(s *Service) { s.GOOS = "plan9" }}
	if code := c.Main(context.Background(), "update", nil); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if got := read(t, bin); !strings.Contains(got, "dash 1.3.0") || !strings.Contains(out.String(), "Restart the runner") {
		t.Errorf("binary %q, output %q", got, out.String())
	}

	// A hub built from source has no release to install.
	c.Version, c.HubVersion = "1.3.0", func(context.Context, string) (string, error) { return "dev", nil }
	errOut.Reset()
	if code := c.Main(context.Background(), "update", nil); code == 0 || !strings.Contains(errOut.String(), "from source") {
		t.Errorf("from a hub built from source: %d %s", code, errOut.String())
	}
}
