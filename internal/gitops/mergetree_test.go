package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/klahr/dash/internal/testbin"
)

func TestParseMergeTree(t *testing.T) {
	t.Parallel()
	tree := "19f49650a2bb98e1f3cbcbd5b85f2579ae07e44e"
	for _, tc := range []struct {
		name, out string
		want      []string
	}{
		{"clean", tree + "\x00", nil},
		{"clean, trimmed", tree, nil},
		{"empty", "", nil},
		{"conflicts", tree + "\x00f.go\x00sp ace.go\x00", []string{"f.go", "sp ace.go"}},
		{"conflicts and messages", tree + "\x00f.go\x00dir/b.go\x00\x001\x00f.go\x00Auto-merging\x00Auto-merging f.go\n\x00",
			[]string{"f.go", "dir/b.go"}},
		{"a path twice", tree + "\x00a\x00a\x00b\x00", []string{"a", "b"}},
		{"a newline in a path", tree + "\x00odd\nname\x00", []string{"odd\nname"}},
	} {
		if got := ParseMergeTree(tc.out); !slices.Equal(got, tc.want) {
			t.Errorf("%s: ParseMergeTree = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// MergeConflicts names the files two branches conflict in, without a
// checkout and without moving anything.
func TestMergeConflicts(t *testing.T) {
	t.Parallel()
	ctx, g := context.Background(), New(nil)
	dir := filepath.Join(t.TempDir(), "repo")
	if err := g.InitRepo(ctx, "p", dir, "main"); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(msg string) {
		t.Helper()
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", msg)
	}
	write("a.go", "a\n")
	write("b.go", "b\n")
	write("c.go", "c\n")
	commit("base")
	gitIn(t, dir, "checkout", "-q", "-b", "x")
	write("a.go", "x\n")
	write("c.go", "x\n")
	commit("x")
	gitIn(t, dir, "checkout", "-q", "main")
	gitIn(t, dir, "checkout", "-q", "-b", "y")
	write("a.go", "y\n")
	write("b.go", "y\n")
	commit("y")
	gitIn(t, dir, "checkout", "-q", "main")
	gitIn(t, dir, "checkout", "-q", "-b", "z")
	write("b.go", "z\n")
	commit("z")
	before, _ := g.Head(ctx, "p", dir, "HEAD")

	files, err := g.MergeConflicts(ctx, "p", dir, "x", "y")
	if errors.Is(err, ErrNoMergeTree) {
		t.Skip("git is older than 2.38")
	}
	if err != nil || !slices.Equal(files, []string{"a.go"}) {
		t.Fatalf("x and y: %q, %v; want a.go", files, err)
	}
	if files, err := g.MergeConflicts(ctx, "p", dir, "x", "z"); err != nil || files != nil {
		t.Fatalf("x and z: %q, %v; want a clean merge", files, err)
	}
	if files, err := g.MergeConflicts(ctx, "p", dir, "y", "z"); err != nil || !slices.Equal(files, []string{"b.go"}) {
		t.Fatalf("y and z: %q, %v; want b.go", files, err)
	}
	if _, err := g.MergeConflicts(ctx, "p", dir, "x", "no-such-branch"); err == nil || errors.Is(err, ErrNoMergeTree) {
		t.Fatalf("a missing branch: %v, want an error", err)
	}
	if after, _ := g.Head(ctx, "p", dir, "HEAD"); after != before {
		t.Fatal("MergeConflicts moved HEAD")
	}

	heads, err := g.BranchHeads(ctx, "p", dir)
	if err != nil || len(heads) != 4 || heads["refs/heads/z"] != before {
		t.Fatalf("BranchHeads = %v, %v", heads, err)
	}
	if files, err := g.FilesSinceFork(ctx, "p", dir, "main", "y"); err != nil || !slices.Equal(files, []string{"a.go", "b.go"}) {
		t.Fatalf("FilesSinceFork(main, y) = %q, %v", files, err)
	}
	if files, err := g.FilesSinceFork(ctx, "p", dir, "y", "main"); err != nil || files != nil {
		t.Fatalf("FilesSinceFork(y, main) = %q, %v; want none", files, err)
	}
}

// A git older than 2.38 doesn't know --write-tree: MergeConflicts says so
// with ErrNoMergeTree, whichever way that git fails. Other failures are
// plain errors, and conflicts are still read from exit 1.
func TestMergeConflictsOldGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	for _, tc := range []struct {
		name, script string
		want         []string
		noMergeTree  bool
		fails        bool
	}{
		{"unknown option", "echo 'usage: git merge-tree [--write-tree] [<options>] <branch1> <branch2>' >&2; exit 129", nil, true, true},
		{"usage, another exit code", "echo 'usage: git merge-tree <base-tree> <branch1> <branch2>' >&2; exit 128", nil, true, true},
		{"missing commit", "echo 'fatal: not a valid object name no-such-branch' >&2; exit 1", nil, false, true},
		{"another failure", "echo 'fatal: not a git repository' >&2; exit 128", nil, false, true},
		{"conflicts", "printf '19f49650a2bb98e1f3cbcbd5b85f2579ae07e44e\\000a.go\\000'; exit 1", []string{"a.go"}, false, false},
		{"clean", "echo 19f49650a2bb98e1f3cbcbd5b85f2579ae07e44e; exit 0", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			testbin.Write(t, filepath.Join(bin, "git"), "#!/bin/sh\n"+tc.script+"\n")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			files, err := New(nil).MergeConflicts(context.Background(), "p", t.TempDir(), "x", "y")
			if got := errors.Is(err, ErrNoMergeTree); got != tc.noMergeTree {
				t.Fatalf("err = %v; ErrNoMergeTree %v, want %v", err, got, tc.noMergeTree)
			}
			if (err != nil) != tc.fails || !slices.Equal(files, tc.want) {
				t.Fatalf("got %q, %v; want %q, failing %v", files, err, tc.want, tc.fails)
			}
		})
	}
}
