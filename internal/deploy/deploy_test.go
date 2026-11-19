package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

func TestReadEnvFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := "# production secrets\n\nAPI_TOKEN=s3cr3t-value\nexport DB_URL=\"postgres://u:pw@db/x\"\nEMPTY=\nQUOTED='a b'\n"
	if err := os.WriteFile(filepath.Join(dir, "production.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Not Valid.env"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEnvFile(dir, "production")
	if err != nil {
		t.Fatal(err)
	}
	want := "API_TOKEN=s3cr3t-value|DB_URL=postgres://u:pw@db/x|EMPTY=|QUOTED=a b"
	if strings.Join(got, "|") != want {
		t.Errorf("read %q", got)
	}
	if got, err := ReadEnvFile(dir, "staging"); got != nil || err != nil {
		t.Errorf("no file: %q %v", got, err)
	}
	if got, err := ReadEnvFile(dir, "../production"); got != nil || err != nil {
		t.Errorf("a path for a name: %q %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.env"), []byte("just words\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEnvFile(dir, "bad"); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("a bad line: %v", err)
	}
	if names := EnvFiles(dir); strings.Join(names, ",") != "bad,production" {
		t.Errorf("env files: %q", names)
	}
}

func TestMask(t *testing.T) {
	t.Parallel()
	secrets := []string{"TOKEN=abcd1234", "LONG=abcd1234-and-more", "SHORT=on", "EMPTY="}
	got := Mask("token abcd1234, long abcd1234-and-more, short on", secrets)
	if got != "token ***, long ***, short on" {
		t.Errorf("masked: %q", got)
	}
}

func TestParseOutputs(t *testing.T) {
	t.Parallel()
	got := ParseOutputs([]byte("url=https://example.com/app\nVERSION = 1.4.2\nsecret=x\nartifact=build-7.tgz\n"))
	if got["url"] != "https://example.com/app" || got["version"] != "1.4.2" || got["artifact"] != "build-7.tgz" || len(got) != 3 {
		t.Errorf("outputs: %v", got)
	}
}

func wait(t *testing.T, m *Manager, id string) State {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		st, err := m.State(id)
		if err != nil {
			t.Fatal(err)
		}
		if !st.Running {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the deploy didn't finish")
	return State{}
}

// A deploy runs the build, then the deploy command, with the DASH_
// variables and the secrets; its output has the secrets masked; what the
// script writes to $DASH_OUTPUT comes back, and its URL is checked.
func TestRunBuildDeployVerify(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var checked string
	m := &Manager{Verify: func(_ context.Context, url string) error { checked = url; return nil }}
	j := Job{ID: "d1", Dir: dir,
		Build:   `echo building $DASH_COMMIT > built.txt`,
		Deploy:  "echo deploying $DASH_ENV with $API_TOKEN\necho url=https://example.com/v2 >> \"$DASH_OUTPUT\"\necho version=1.2 >> \"$DASH_OUTPUT\"",
		Env:     append(os.Environ(), "API_TOKEN=supersecret", model.DeployVarEnv+"=staging", model.DeployVarCommit+"=abc123"),
		Secrets: []string{"API_TOKEN=supersecret"},
		URL:     "https://example.com",
	}
	if err := m.Start(context.Background(), "p/staging", j); err != nil {
		t.Fatal(err)
	}
	st := wait(t, m, "d1")
	if st.Phase != model.DeployLive || st.ExitCode == nil || *st.ExitCode != 0 {
		t.Fatalf("state: %+v", st)
	}
	if !strings.Contains(st.Log, "deploying staging with ***") || strings.Contains(st.Log, "supersecret") {
		t.Errorf("log: %q", st.Log)
	}
	if st.Outputs["version"] != "1.2" || st.URL != "https://example.com/v2" || checked != "https://example.com/v2" {
		t.Errorf("outputs %v, url %q, checked %q", st.Outputs, st.URL, checked)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "built.txt")); strings.TrimSpace(string(b)) != "building abc123" {
		t.Errorf("built: %q", b)
	}
}

// A failing build stops the deploy; a failing check fails it.
func TestRunFails(t *testing.T) {
	t.Parallel()
	m := &Manager{Verify: func(context.Context, string) error { return errors.New("it answers 502 Bad Gateway") }}
	if err := m.Start(context.Background(), "p/a", Job{ID: "b", Dir: t.TempDir(), Build: "exit 3", Deploy: "touch deployed", Env: os.Environ()}); err != nil {
		t.Fatal(err)
	}
	st := wait(t, m, "b")
	if st.Phase != model.DeployFailed || st.ExitCode == nil || *st.ExitCode != 3 || !strings.Contains(st.Error, "build command exited with 3") {
		t.Errorf("failed build: %+v", st)
	}
	if err := m.Start(context.Background(), "p/a", Job{ID: "v", Dir: t.TempDir(), Deploy: "true", Env: os.Environ(), URL: "https://example.com"}); err != nil {
		t.Fatal(err)
	}
	if st := wait(t, m, "v"); st.Phase != model.DeployFailed || !strings.Contains(st.Error, "502") {
		t.Errorf("failed check: %+v", st)
	}
}

// One deploy at a time per key; Stop ends one.
func TestOneAtATimeAndStop(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	if err := m.Start(context.Background(), "p/prod", Job{ID: "long", Dir: t.TempDir(), Deploy: "sleep 30", Env: os.Environ()}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background(), "p/prod", Job{ID: "next", Dir: t.TempDir(), Deploy: "true", Env: os.Environ()}); !errors.Is(err, ErrBusy) {
		t.Errorf("a second deploy at once: %v", err)
	}
	if err := m.Start(context.Background(), "p/prod", Job{ID: "long", Dir: t.TempDir(), Deploy: "sleep 30", Env: os.Environ()}); err != nil {
		t.Errorf("the same deploy asked again: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := m.Stop("long"); err != nil {
		t.Fatal(err)
	}
	if st := wait(t, m, "long"); st.Phase != model.DeployFailed || !strings.Contains(st.Error, "stopped") {
		t.Errorf("stopped: %+v", st)
	}
}
