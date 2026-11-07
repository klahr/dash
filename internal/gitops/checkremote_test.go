package gitops

import "testing"

// A runner clones only network remotes its hub names: no local paths,
// file:// or git's command-running transports.
func TestCheckRemote(t *testing.T) {
	for _, ok := range []string{
		"https://github.com/acme/web.git", "ssh://git@github.com/acme/web.git", "git@github.com:acme/web.git",
	} {
		if err := CheckRemote(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "/home/me/secret-repo", "../other", "file:///etc", "ext::sh -c touch% /tmp/x", "fd::3",
		"--upload-pack=touch /tmp/x", "https://-oProxyCommand=x/y", "git@github.com:a/b c", "http://github.com/a/b",
	} {
		if err := CheckRemote(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
