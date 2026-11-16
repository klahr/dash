package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/testbin"
)

// mergeGh is a fake gh that logs its calls to the returned file and runs
// script for them.
func mergeGh(t *testing.T, script string) (*Git, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "gh")
	testbin.Write(t, bin, "#!/bin/sh\necho \"$*\" >> "+calls+"\n"+script)
	return &Git{GhBin: bin}, calls
}

func readCalls(t *testing.T, calls string) string {
	b, _ := os.ReadFile(calls)
	return string(b)
}

// MergePR merges with the method asked for, and never with --admin
// (branch protection always applies) nor --delete-branch (Dash deletes the
// branch on GitHub once it sees the merge).
func TestMergePR(t *testing.T) {
	t.Parallel()

	for _, method := range []string{MergeSquash, MergeCommit, MergeRebase} {
		g, calls := mergeGh(t, `echo "✓ Merged pull request #7"`)
		queued, err := g.MergePR(context.Background(), "p", t.TempDir(), 7, method)
		if err != nil || queued {
			t.Fatalf("%s: queued %v, err %v", method, queued, err)
		}
		got := readCalls(t, calls)
		if got != "pr merge 7 --"+method+"\n" {
			t.Fatalf("%s: gh calls %q", method, got)
		}
	}
	g, calls := mergeGh(t, ``)
	if _, err := g.MergePR(context.Background(), "p", t.TempDir(), 7, "admin"); err == nil || readCalls(t, calls) != "" {
		t.Fatal("an unknown method must be refused before gh runs")
	}
}

// GitHub's refusal comes back as a *CmdError with its message, after one
// call.
func TestMergePRRefused(t *testing.T) {
	t.Parallel()

	g, calls := mergeGh(t, `echo "X Pull request o/r#7 is not mergeable: the base branch policy prohibits the merge." >&2; exit 1`)
	_, err := g.MergePR(context.Background(), "p", t.TempDir(), 7, MergeSquash)
	var ce *CmdError
	if !errors.As(err, &ce) || !strings.Contains(ce.Output, "base branch policy prohibits the merge") {
		t.Fatalf("err = %v", err)
	}
	if n := strings.Count(readCalls(t, calls), "\n"); n != 1 {
		t.Fatalf("want one call, got %q", readCalls(t, calls))
	}
}

// A base branch with a merge queue: gh adds the pull request to it (newer
// gh does so by itself; older gh wants --auto).
func TestMergePRMergeQueue(t *testing.T) {
	t.Parallel()

	g, _ := mergeGh(t, `echo "✓ Pull request o/r#7 will be added to the merge queue for main when ready"`)
	if queued, err := g.MergePR(context.Background(), "p", t.TempDir(), 7, MergeSquash); err != nil || !queued {
		t.Fatalf("queued %v, err %v", queued, err)
	}

	g, calls := mergeGh(t, `case "$*" in
*--auto*) echo "✓ Pull request o/r#7 will be added to the merge queue for main when ready" ;;
*) echo "X the merge queue is required: use --auto" >&2; exit 1 ;;
esac`)
	if queued, err := g.MergePR(context.Background(), "p", t.TempDir(), 7, MergeSquash); err != nil || !queued {
		t.Fatalf("queued %v, err %v", queued, err)
	}
	if got := readCalls(t, calls); got != "pr merge 7 --squash\npr merge 7 --squash --auto\n" {
		t.Fatalf("gh calls %q", got)
	}
}

// The repository's default merge method: the viewer's default when it is
// allowed, else the first allowed of squash, merge commit and rebase.
func TestDefaultMergeMethod(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ out, want string }{
		{`{"viewerDefaultMergeMethod":"REBASE","squashMergeAllowed":true,"mergeCommitAllowed":true,"rebaseMergeAllowed":true}`, MergeRebase},
		{`{"viewerDefaultMergeMethod":"MERGE","squashMergeAllowed":true,"mergeCommitAllowed":false,"rebaseMergeAllowed":true}`, MergeSquash},
		{`{"viewerDefaultMergeMethod":"","squashMergeAllowed":false,"mergeCommitAllowed":true,"rebaseMergeAllowed":true}`, MergeCommit},
		{`{"squashMergeAllowed":false,"mergeCommitAllowed":false,"rebaseMergeAllowed":true}`, MergeRebase},
	} {
		got, err := parseMergeMethod(c.out)
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.out, got, err, c.want)
		}
	}
	if _, err := parseMergeMethod(`{}`); err == nil {
		t.Error("a repository allowing no method should be an error")
	}
	g, calls := mergeGh(t, `echo '{"viewerDefaultMergeMethod":"SQUASH","squashMergeAllowed":true}'`)
	if m, err := g.DefaultMergeMethod(context.Background(), "p", t.TempDir()); err != nil || m != MergeSquash {
		t.Fatalf("%q %v", m, err)
	}
	if got := readCalls(t, calls); !strings.HasPrefix(got, "repo view --json ") {
		t.Fatalf("gh calls %q", got)
	}
}
