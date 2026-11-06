package api

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

func TestCleanRepoPath(t *testing.T) {
	for in, want := range map[string]string{"a.go": "a.go", "./web/x.js": "web/x.js", "a/../b": "b"} {
		if got, err := cleanRepoPath(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", ".", "..", "../x", "a/../../x", "/etc/passwd"} {
		if got, err := cleanRepoPath(in); err == nil {
			t.Errorf("%q: got %q, want an error", in, got)
		}
	}
}

func TestLocalPath(t *testing.T) {
	ctx, g := context.Background(), gitops.New(nil)
	dir := filepath.Join(t.TempDir(), "repo")
	if err := g.InitRepo(ctx, "p", dir, "main"); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", ".env\nnode_modules/\n")
	write(".env", "KEY=1\n")
	write("notes.txt", "hi\n")
	write("node_modules/x/index.js", "x\n")
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	s := &Server{Git: g}
	p := model.Project{ID: "p", Path: dir, DefaultBranch: "main"}
	local, err := g.LocalFiles(ctx, p.ID, dir)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(local)
	if want := []string{".env", ".gitignore", "link", "notes.txt"}; !slices.Equal(local, want) {
		t.Fatalf("LocalFiles = %q, want %q", local, want)
	}
	for _, name := range []string{".env", "notes.txt"} {
		if _, err := s.localPath(ctx, p, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"link", ".git/config", "node_modules/x/index.js", "missing"} {
		if got, err := s.localPath(ctx, p, name); err == nil {
			t.Errorf("%s: got %q, want an error", name, got)
		}
	}
}
