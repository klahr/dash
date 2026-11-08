package gitops

import (
	"context"
	"os/exec"
	"testing"
)

// CheckRefName, the hub's check without git, agrees with git check-ref-format.
func TestCheckRefNameAgreesWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	g := New(nil)
	for _, name := range []string{
		"refs/tags/v1.2.3", "refs/heads/feature/x", "refs/tags/1.2.3", "refs/heads/a b", "refs/heads/a..b",
		"refs/heads/a~1", "refs/heads/x.lock", "refs/heads/.hidden", "refs/heads/end.", "refs/heads/end/",
		"refs/heads/a@{b", "refs/heads/a:b", "refs/heads/a?b", "refs/heads/a*b", "refs/heads/a[b", "refs/heads/a\\b",
		"refs/heads/a//b", "refs/heads/ünïcode", "refs/tags/release-2026.11",
	} {
		want := g.CheckRefFormat(context.Background(), name) == nil
		if got := CheckRefName(name) == nil; got != want {
			t.Errorf("%q: CheckRefName ok=%v, git ok=%v", name, got, want)
		}
	}
}
