package localrun

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentPath(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "bin")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(home, "AppData", "Roaming", "npm")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sep := string(os.PathListSeparator)
	got := AgentPath("/a"+sep+sep+"/b", home)
	parts := strings.Split(got, sep)
	if parts[0] != "/a" || parts[1] != "/b" || !strings.Contains(got, dir) {
		t.Errorf("AgentPath = %q", got)
	}
	// Folders it has keep their place; missing ones aren't added.
	if again := AgentPath(dir+sep+"/a", home); strings.Count(again, dir) != 1 || !strings.HasPrefix(again, dir) {
		t.Errorf("AgentPath with it already = %q", again)
	}
	if strings.Contains(got, filepath.Join(home, ".bun")) {
		t.Errorf("AgentPath added a missing folder: %q", got)
	}
}
