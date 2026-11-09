package gitops

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestParseRepoInfo(t *testing.T) {
	out := `A new release of gh is available
{"nameWithOwner":"klahr/dash","url":"https://github.com/klahr/dash","description":" Dashboard for Claude ","homepageUrl":"",
 "visibility":"PUBLIC","isArchived":false,"isFork":true,"isTemplate":false,"parent":{"id":"x","name":"up","owner":{"id":"y","login":"acme"}},
 "defaultBranchRef":{"name":"main"},"licenseInfo":{"key":"mit","name":"MIT License","nickname":""},
 "latestRelease":{"name":"v1.2.3","tagName":"v1.2.3","url":"https://github.com/klahr/dash/releases/tag/v1.2.3","publishedAt":"2026-10-01T12:00:00Z"},
 "primaryLanguage":{"name":"Go"},"repositoryTopics":[{"name":"claude"},{"name":"dashboard"}],
 "stargazerCount":42,"forkCount":3,"watchers":{"totalCount":5},"issues":{"totalCount":7},"pullRequests":{"totalCount":2},
 "diskUsage":1234,"createdAt":"2025-01-01T00:00:00Z","pushedAt":"2026-11-08T00:00:00Z",
 "hasIssuesEnabled":true,"hasWikiEnabled":false,"hasDiscussionsEnabled":true,"hasProjectsEnabled":false,
 "mergeCommitAllowed":false,"squashMergeAllowed":true,"rebaseMergeAllowed":true,"deleteBranchOnMerge":true,"viewerPermission":"ADMIN"}`
	info, err := ParseRepoInfo(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.NameWithOwner != "klahr/dash" || info.Description != "Dashboard for Claude" || info.Homepage != "" {
		t.Errorf("name/description: %+v", info)
	}
	if info.License == nil || info.License.Key != "mit" || info.License.Name != "MIT License" {
		t.Errorf("license = %+v", info.License)
	}
	if info.LatestRelease == nil || info.LatestRelease.Tag != "v1.2.3" || info.LatestRelease.PublishedAt.Year() != 2026 {
		t.Errorf("latest release = %+v", info.LatestRelease)
	}
	if info.Parent != "acme/up" || info.DefaultBranch != "main" || info.Language != "Go" {
		t.Errorf("parent/branch/language: %+v", info)
	}
	if !slices.Equal(info.Topics, []string{"claude", "dashboard"}) {
		t.Errorf("topics = %v", info.Topics)
	}
	if info.Stars != 42 || info.Forks != 3 || info.Watchers != 5 || info.OpenIssues != 7 || info.OpenPRs != 2 || info.DiskKB != 1234 {
		t.Errorf("counts: %+v", info)
	}
	if info.MergeCommit || !info.SquashMerge || !info.RebaseMerge || !info.DeleteOnMerge || !info.HasIssues || info.HasWiki || !info.HasDiscussions {
		t.Errorf("settings: %+v", info)
	}
	if info.ViewerPermission != "ADMIN" {
		t.Errorf("viewer permission = %q", info.ViewerPermission)
	}
}

// A bare repository: no license, release, language, parent or topics.
func TestParseRepoInfoEmpty(t *testing.T) {
	info, err := ParseRepoInfo(`{"nameWithOwner":"a/b","url":"u","description":null,"homepageUrl":null,"parent":null,"licenseInfo":null,"latestRelease":null,"primaryLanguage":null,"repositoryTopics":null,"defaultBranchRef":null}`)
	if err != nil {
		t.Fatal(err)
	}
	if info.License != nil || info.LatestRelease != nil || info.Parent != "" || info.Language != "" || info.Topics == nil || len(info.Topics) != 0 {
		t.Errorf("got %+v", info)
	}
}

func TestRepoInfoAsksGh(t *testing.T) {
	g := fakeGh(t, `echo '{"nameWithOwner":"klahr/dash","url":"https://github.com/klahr/dash"}'`)
	info, err := g.RepoInfo(context.Background(), "git@github.com:klahr/dash.git")
	if err != nil {
		t.Fatal(err)
	}
	if info.NameWithOwner != "klahr/dash" {
		t.Errorf("got %+v", info)
	}
	if _, err := g.RepoInfo(context.Background(), "/srv/git/dash"); err == nil || !strings.Contains(err.Error(), "not a GitHub") {
		t.Errorf("local remote: err = %v", err)
	}
}
