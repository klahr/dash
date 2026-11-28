package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "f.json")
	if err := WriteFileAtomic(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "two" {
		t.Fatalf("read %q, %v", b, err)
	}
	if runtime.GOOS != "windows" { // which has no Unix permission bits
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode %v", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Dir(path))
		if di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode %v", di.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
}

func TestWriteFileAtomicRemovesTempOnRenameError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	// A non-empty directory at path makes the rename fail.
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("x"), 0o644); err == nil {
		t.Fatal("want error")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
}

func TestRelInside(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{".", true},
		{"", true}, // Clean makes it "."
		{"a", true},
		{"a/b", true},
		{"..env", true},
		{"...", true},
		{"a/..", true},
		{"a/../b", true},
		{"..", false},
		{"../a", false},
		{"a/../../b", false},
		{"/a", false},
	}
	for _, c := range cases {
		if got := RelInside(filepath.FromSlash(c.rel)); got != c.want {
			t.Errorf("RelInside(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

func TestCopyProjectFiles(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("A=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []string{" .env ", "", ".", "../outside", "/abs", "missing", "a/../.env"}
	var got []string
	err := CopyProjectFiles(src, dst, files, func(p string, err error) error {
		switch {
		case err == nil:
			got = append(got, "ok "+p)
		case errors.Is(err, fs.ErrNotExist):
			got = append(got, "missing "+p)
		default:
			t.Errorf("%s: %v", p, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ok .env", "missing missing", "ok .env"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if b, err := os.ReadFile(filepath.Join(dst, ".env")); err != nil || string(b) != "A=1" {
		t.Errorf("copied .env = %q, %v", b, err)
	}

	stop := errors.New("stop")
	calls := 0
	err = CopyProjectFiles(src, dst, []string{"missing", ".env"}, func(string, error) error { calls++; return stop })
	if err != stop || calls != 1 {
		t.Errorf("err = %v after %d calls, want stop after 1", err, calls)
	}
}
