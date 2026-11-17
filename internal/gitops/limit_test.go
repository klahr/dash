package gitops

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLimitedBuffer(t *testing.T) {
	b := &limitedBuffer{max: 5}
	if n, err := b.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if n, err := b.Write([]byte("defg")); n != 2 || !errors.Is(err, errOutputLimit) {
		t.Fatalf("Write past the limit = %d, %v", n, err)
	}
	if !b.full || b.buf.String() != "abcde" {
		t.Fatalf("kept %q, full %v", b.buf.String(), b.full)
	}
	if n, _ := b.Write([]byte("h")); n != 0 || b.buf.Len() != 5 {
		t.Fatalf("a full buffer took more: %q", b.buf.String())
	}
}

// A patch over the limit is cut with a note; any other command over the
// limit fails, rather than being parsed cut.
func TestOutputLimit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	gitOut(t, repo, "init", "-q", "-b", "main")
	gitOut(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	gitOut(t, repo, "checkout", "-q", "-b", "big")
	line := strings.Repeat("x", 99) + "\n"
	if err := os.WriteFile(filepath.Join(repo, "big.txt"), []byte(strings.Repeat(line, 2000)), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, repo, "add", "big.txt")
	gitOut(t, repo, "commit", "-q", "-m", "big")

	g := New(nil)
	ctx := context.Background()
	out, err := g.runLimited(ctx, false, nil, nil, 50_000, true, "", repo, "git", "diff", "--stat", "--patch", "main...big")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 50_200 || !strings.Contains(out, "big.txt | 2000 +") || !strings.HasSuffix(out, "isn't shown]") {
		t.Fatalf("cut patch: %d bytes, starts %q, ends %q", len(out), out[:80], out[len(out)-80:])
	}

	if _, err := g.runLimited(ctx, false, nil, nil, 50_000, false, "", repo, "git", "show", "big:big.txt"); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("over the limit: %v", err)
	}

	full, err := g.Diff(ctx, "", repo, "main", "big")
	if err != nil || strings.Contains(full, "isn't shown]") || !strings.HasSuffix(full, "+"+strings.TrimSpace(line)) {
		t.Fatalf("a patch under the limit changed: %v", err)
	}
}
