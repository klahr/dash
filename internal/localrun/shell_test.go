package localrun

import (
	"errors"
	"path/filepath"
	"testing"
)

// On Windows, project commands run with Git for Windows' bash, found from
// where git is, not whatever "bash" (WSL's launcher) is first on the PATH.
func TestWindowsBash(t *testing.T) {
	root := filepath.Join("C", "Program Files", "Git")
	bash := filepath.Join(root, "bin", "bash.exe")
	exists := func(p string) bool { return p == bash }
	for _, git := range []string{
		filepath.Join(root, "cmd", "git.exe"),
		filepath.Join(root, "mingw64", "bin", "git.exe"),
		filepath.Join(root, "bin", "git.exe"),
	} {
		look := func(string) (string, error) { return git, nil }
		if got := windowsBash("", look, exists); got != bash {
			t.Errorf("git at %s: bash %q, want %q", git, got, bash)
		}
	}
	none := func(string) (string, error) { return "", errors.New("not found") }
	if got := windowsBash(`D:\tools\bash.exe`, none, exists); got != `D:\tools\bash.exe` {
		t.Errorf("DASH_BASH is used first: got %q", got)
	}
	if got := windowsBash("", none, func(string) bool { return false }); got != "bash" {
		t.Errorf("without Git for Windows: got %q, want bash", got)
	}
}
