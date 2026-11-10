package gitops

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// The protocols a runner clones, fetches and pushes GitHub repositories
// over (dash runner -github-protocol). SSH uses the computer's SSH keys
// (ssh-agent, ~/.ssh); HTTPS needs a git credential helper for private
// repositories (gh auth setup-git, or a personal access token).
const (
	ProtocolSSH   = "ssh"
	ProtocolHTTPS = "https"
)

// ParseGitHubProtocol checks a -github-protocol value: "" is ProtocolSSH.
func ParseGitHubProtocol(s string) (string, error) {
	switch p := strings.ToLower(strings.TrimSpace(s)); p {
	case "", ProtocolSSH:
		return ProtocolSSH, nil
	case ProtocolHTTPS:
		return ProtocolHTTPS, nil
	default:
		return "", fmt.Errorf("unknown GitHub protocol %q: use ssh or https", s)
	}
}

// GitHubOwnerRepo is "OWNER/REPO" for a github.com remote in any form
// (https://, ssh://, git@github.com:…), else "".
func GitHubOwnerRepo(remote string) string {
	name, ok := strings.CutPrefix(RepoName(remote), "github.com/")
	if !ok {
		return ""
	}
	return name
}

// GitHubRemote is the URL of the GitHub repository OWNER/REPO over
// protocol: git@github.com:OWNER/REPO.git for ProtocolSSH (and ""), else
// https://github.com/OWNER/REPO.git.
func GitHubRemote(ownerRepo, protocol string) string {
	ownerRepo = strings.TrimSuffix(strings.Trim(strings.TrimSpace(ownerRepo), "/"), ".git")
	if protocol == ProtocolHTTPS {
		return "https://github.com/" + ownerRepo + ".git"
	}
	return "git@github.com:" + ownerRepo + ".git"
}

// CloneRemote is the URL a runner clones a project from: override (the
// project's own remote URL) as it is when set; else remote, rebuilt for
// protocol when it is a GitHub repository; else remote as it is.
func CloneRemote(remote, override, protocol string) string {
	if o := strings.TrimSpace(override); o != "" {
		return o
	}
	if name := GitHubOwnerRepo(remote); name != "" {
		return GitHubRemote(name, protocol)
	}
	return strings.TrimSpace(remote)
}

// RemoteProtocol names the transport remote uses: "ssh", "https", "file",
// or the URL's scheme.
func RemoteProtocol(remote string) string {
	r := strings.TrimSpace(remote)
	if i := strings.Index(r, "://"); i > 0 {
		return strings.ToLower(r[:i])
	}
	if scpRemoteRe.MatchString(r) {
		return ProtocolSSH
	}
	return "file"
}

// AccessError is a remote this computer can't read: no SSH key GitHub
// accepts, no HTTPS credentials, a repository that doesn't exist.
type AccessError struct {
	Runner   string // this computer's name
	Repo     string // OWNER/REPO, or the URL when it isn't GitHub's
	Protocol string // see RemoteProtocol
	Detail   string // a summary of git's output
}

func (e *AccessError) Error() string {
	who := "this runner"
	if e.Runner != "" {
		who = "runner " + e.Runner
	}
	msg := fmt.Sprintf("%s cannot access %s over %s", who, e.Repo, e.Protocol)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	switch e.Protocol {
	case ProtocolSSH:
		msg += " (check this computer's SSH key with `ssh -T git@github.com`, or start the runner with -github-protocol=https)"
	case ProtocolHTTPS:
		msg += " (set up a credential helper, e.g. `gh auth setup-git`, or start the runner with -github-protocol=ssh)"
	}
	return msg
}

// CheckAccess runs `git ls-remote` on remote (checked with CheckRemote)
// without prompting for anything (no terminal prompt; ssh in batch mode),
// so that a clone that would fail fails with an AccessError naming runner,
// the repository and the protocol. An empty repository has no refs, and
// passes.
func (g *Git) CheckAccess(ctx context.Context, projectID, runner, remote string) error {
	remote = strings.TrimSpace(remote)
	if err := CheckRemote(remote); err != nil {
		return err
	}
	args := append(transportArgs(), "ls-remote", "--", remote)
	out, err := g.runEnv(ctx, false, batchSSHEnv(), projectID, "", "git", args...)
	if err == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ctx.Err()
	}
	repo := GitHubOwnerRepo(remote)
	if repo == "" {
		repo = redactURL(remote)
	}
	return &AccessError{Runner: runner, Repo: repo, Protocol: RemoteProtocol(remote), Detail: summarizeGitOutput(out)}
}

// batchSSHEnv makes ssh fail rather than ask (for a passphrase, a
// password, or whether to trust a new host key): GIT_SSH_COMMAND with
// -o BatchMode=yes, after the user's own GIT_SSH_COMMAND when they set one.
func batchSSHEnv() []string {
	ssh := strings.TrimSpace(os.Getenv("GIT_SSH_COMMAND"))
	if ssh == "" {
		ssh = "ssh"
	}
	return []string{"GIT_SSH_COMMAND=" + ssh + " -o BatchMode=yes"}
}

// transportArgs allow git only network transports: no ext:: or fd::
// (which run commands), nor file:// and local paths unless
// AllowLocalRemotes.
func transportArgs() []string {
	args := []string{"-c", "protocol.ext.allow=never", "-c", "protocol.fd.allow=never"}
	if !AllowLocalRemotes {
		args = append(args, "-c", "protocol.file.allow=never")
	}
	return args
}

// summarizeGitOutput keeps what says why git failed: its non-empty lines,
// joined, without ssh's boilerplate, and at most maxDetail bytes.
func summarizeGitOutput(out string) string {
	const maxDetail = 300
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case l == "",
			strings.HasPrefix(l, "Please make sure you have the correct access rights"),
			l == "and the repository exists.":
			continue
		}
		keep = append(keep, l)
	}
	s := strings.Join(keep, "; ")
	if len(s) > maxDetail {
		s = s[:maxDetail] + "…"
	}
	return s
}

// redactURL drops a password from remote, should someone have put one in.
func redactURL(remote string) string {
	if u, err := url.Parse(remote); err == nil && u.User != nil {
		if _, ok := u.User.Password(); ok {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
			return u.String()
		}
	}
	return remote
}
