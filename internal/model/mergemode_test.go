package model

import "testing"

func TestDefaultMergeMode(t *testing.T) {
	for remote, github := range map[string]bool{
		"https://github.com/o/r.git":              true,
		"git@github.com:o/r.git":                  true,
		"ssh://git@ssh.github.com:443/o/r.git":    true,
		"https://x-access-token:t@github.com/o/r": true,
		"https://GitHub.com/o/r":                  true,
		"https://gitlab.com/o/r.git":              false,
		"https://example.com/github.com/r.git":    false,
		"/tmp/origin.git":                         false,
		"":                                        false,
	} {
		if IsGitHub(remote) != github {
			t.Errorf("IsGitHub(%q) = %v", remote, !github)
		}
	}
	gh := Project{RemoteURL: "https://github.com/o/r.git"}
	if gh.DefaultMergeMode(true) != MergePR || gh.DefaultMergeMode(false) != MergeLocal {
		t.Error("a GitHub project: pull requests with gh, merging without")
	}
	gh.Runners = []string{"r"}
	if gh.DefaultMergeMode(false) != MergePR {
		t.Error("a GitHub project on runners: pull requests whether the hub has gh or not")
	}
	if (Project{RemoteURL: "https://gitlab.com/o/r.git"}).DefaultMergeMode(true) != MergeLocal {
		t.Error("a GitLab project: merging")
	}
	if !(Project{}).MergesGreenPRs() {
		t.Error("merging green pull requests is on by default")
	}
}
