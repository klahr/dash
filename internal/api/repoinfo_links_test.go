package api

import (
	"testing"

	"github.com/klahr/dash/internal/gitops"
)

func TestSafeRepoLinks(t *testing.T) {
	parallel(t)

	x := projectRepoInfo{GitHub: &gitops.RepoInfo{
		URL:           "javascript:alert(1)//",
		Homepage:      "javascript:alert(2)",
		LatestRelease: &gitops.RepoRelease{Tag: "v1", URL: "https://evil.example/r"},
	}}
	safeRepoLinks(&x)
	if g := x.GitHub; g.URL != "" || g.Homepage != "" || g.LatestRelease.URL != "" {
		t.Errorf("unsafe links kept: %+v %+v", g, g.LatestRelease)
	}
	y := projectRepoInfo{GitHub: &gitops.RepoInfo{
		URL:           "https://github.com/o/r",
		Homepage:      "http://example.com/docs",
		LatestRelease: &gitops.RepoRelease{Tag: "v1", URL: "https://github.com/o/r/releases/tag/v1"},
	}}
	safeRepoLinks(&y)
	if g := y.GitHub; g.URL != "https://github.com/o/r" || g.Homepage != "http://example.com/docs" || g.LatestRelease.URL == "" {
		t.Errorf("safe links dropped: %+v", g)
	}
	safeRepoLinks(&projectRepoInfo{}) // no GitHub: no panic
}
