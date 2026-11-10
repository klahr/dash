package gitops

import "testing"

func TestRepoName(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"https://github.com/klahr/dash.git":           "github.com/klahr/dash",
		"https://github.com/klahr/dash":               "github.com/klahr/dash",
		"https://token@github.com/klahr/dash.git":     "github.com/klahr/dash",
		"git@github.com:klahr/dash.git":               "github.com/klahr/dash",
		"ssh://git@ssh.github.com:443/klahr/dash.git": "github.com/klahr/dash",
		"git@ghe.corp.example:team/app.git":           "ghe.corp.example/team/app",
		"/srv/git/dash.git":                           "",
		"https://github.com/klahr":                    "",
	} {
		if got := RepoName(in); got != want {
			t.Errorf("RepoName(%q) = %q, want %q", in, got, want)
		}
	}
}
