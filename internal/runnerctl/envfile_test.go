package runnerctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvState(t *testing.T) {
	dir := t.TempDir()
	path := EnvPath(dir)
	if path != filepath.Join(dir, "runner.env") {
		t.Fatalf("EnvPath = %q", path)
	}
	if got := envState(path); got != "no "+path {
		t.Errorf("missing file: %q", got)
	}
	if err := os.WriteFile(path, []byte("# key for the shared runner\nexport ANTHROPIC_API_KEY=sk-ant-secret\n\nDASH_LOG_LEVEL = debug\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := envState(path)
	if !strings.Contains(got, "sets ANTHROPIC_API_KEY, DASH_LOG_LEVEL") {
		t.Errorf("names: %q", got)
	}
	if strings.Contains(got, "sk-ant") || strings.Contains(got, "debug") {
		t.Errorf("shows a value: %q", got)
	}
	if err := os.WriteFile(path, []byte("# nothing yet\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := envState(path); !strings.HasSuffix(got, "(sets nothing)") {
		t.Errorf("empty: %q", got)
	}
}
