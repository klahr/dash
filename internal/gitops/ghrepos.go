package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SameRemote reports whether two origin URLs name the same repository:
// https and ssh forms of one GitHub repository are the same (as RepoName
// sees them, ignoring case, credentials and a trailing .git), and anything
// else (a local path) must match once cleaned.
func SameRemote(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if na, nb := RepoName(a), RepoName(b); na != "" || nb != "" {
		return na != "" && strings.EqualFold(na, nb)
	}
	clean := func(s string) string {
		s = strings.TrimPrefix(s, "file://")
		return strings.TrimSuffix(filepath.Clean(strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")), "/")
	}
	return clean(a) == clean(b)
}

// RemoteRepo is the last part of a remote's path, without .git: the folder
// name `git clone` would pick ("" when there is none).
func RemoteRepo(remote string) string {
	r := strings.TrimSuffix(strings.TrimSpace(remote), "/")
	if i := strings.LastIndexAny(r, "/:"); i >= 0 {
		r = r[i+1:]
	}
	r = strings.TrimSuffix(r, ".git")
	if r == "." || r == ".." {
		return ""
	}
	return path.Base(r)
}

// GitHubListing is one repository in the signed-in user's `gh repo list`
// (or one of their organisations').
type GitHubListing struct {
	Name          string    `json:"name"`
	NameWithOwner string    `json:"nameWithOwner"`
	Owner         string    `json:"owner"` // from NameWithOwner
	Description   string    `json:"description"`
	IsPrivate     bool      `json:"isPrivate"`
	IsFork        bool      `json:"isFork"`
	IsArchived    bool      `json:"isArchived"`
	UpdatedAt     time.Time `json:"updatedAt"`
	URL           string    `json:"url"`
}

// ghListFields are the fields one `gh repo list` call asks for.
const ghListFields = "name,nameWithOwner,description,isPrivate,isFork,isArchived,updatedAt,url"

// maxGitHubRepos caps one owner's listing; maxGitHubOrgs how many of the
// user's organisations are listed too.
const (
	maxGitHubRepos = 400
	maxGitHubOrgs  = 20
)

var ghOwnerRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// ParseGitHubRepos reads `gh repo list --json …` output. Description may
// be null or missing; Owner is filled in from NameWithOwner.
func ParseGitHubRepos(out string) ([]GitHubListing, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return []GitHubListing{}, nil
	}
	var raw []struct {
		GitHubListing
		Description *string `json:"description"`
	}
	if err := json.Unmarshal([]byte(jsonPart(out)), &raw); err != nil {
		return nil, fmt.Errorf("gh repo list: %w", err)
	}
	repos := make([]GitHubListing, 0, len(raw))
	for _, r := range raw {
		x := r.GitHubListing
		if r.Description != nil {
			x.Description = strings.TrimSpace(*r.Description)
		}
		if x.NameWithOwner == "" {
			continue
		}
		x.Owner, _, _ = strings.Cut(x.NameWithOwner, "/")
		repos = append(repos, x)
	}
	return repos, nil
}

// GitHubRepos lists the repositories of gh's signed-in user and of the
// organisations they belong to: one `gh repo list` per owner. user is the
// login; the user's own repositories come first, then each organisation's,
// most recently updated first within each. An organisation that can't be
// listed (gh without the read:org scope) is skipped.
func (g *Git) GitHubRepos(ctx context.Context) (user string, repos []GitHubListing, err error) {
	user, err = g.ghQuiet(ctx, "", "", "api", "user", "--jq", ".login")
	if err != nil {
		return "", nil, fmt.Errorf("gh isn't signed in (gh auth login): %w", err)
	}
	user = strings.TrimSpace(user)
	list := func(owner string) ([]GitHubListing, error) {
		args := []string{"repo", "list"}
		if owner != "" {
			args = append(args, owner)
		}
		args = append(args, "--limit", fmt.Sprint(maxGitHubRepos), "--json", ghListFields)
		out, err := g.ghQuiet(ctx, "", "", args...)
		if err != nil {
			return nil, err
		}
		return ParseGitHubRepos(out)
	}
	if repos, err = list(""); err != nil {
		return user, nil, fmt.Errorf("gh repo list: %w", err)
	}
	byTime := func(rs []GitHubListing) {
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].UpdatedAt.After(rs[j].UpdatedAt) })
	}
	byTime(repos)
	orgs, _ := g.ghQuiet(ctx, "", "", "api", "user/orgs", "--jq", ".[].login")
	n := 0
	for _, org := range strings.Fields(orgs) {
		if !ghOwnerRe.MatchString(org) || strings.EqualFold(org, user) {
			continue
		}
		if n++; n > maxGitHubOrgs {
			break
		}
		rs, err := list(org)
		if err != nil {
			continue
		}
		byTime(rs)
		repos = append(repos, rs...)
	}
	return user, repos, nil
}

// CreateEmptyGitHubRepo creates the repository name (REPO for the signed-in
// user, or OWNER/REPO) on GitHub with gh, with nothing in it, and returns it.
func (g *Git) CreateEmptyGitHubRepo(ctx context.Context, name string, private bool) (GitHubRepo, error) {
	name = strings.Trim(strings.TrimSpace(name), "/")
	if name == "" || strings.HasPrefix(name, "-") || strings.Count(name, "/") > 1 || strings.ContainsAny(name, " \t\r\n") {
		return GitHubRepo{}, fmt.Errorf("%q isn't a repository name", name)
	}
	vis := "--public"
	if private {
		vis = "--private"
	}
	if _, err := g.gh(ctx, "", "", "repo", "create", name, vis); err != nil {
		return GitHubRepo{}, err
	}
	repo, ok, err := g.LookupGitHubRepo(ctx, name)
	if err == nil && !ok {
		err = fmt.Errorf("gh created %s but can't find it", name)
	}
	return repo, err
}

// CheckoutChanges lists the files with uncommitted changes to tracked files
// (staged or not) in dir's checkout: what a merge moving its branch could
// clash with. Untracked files aren't listed.
func (g *Git) CheckoutChanges(ctx context.Context, projectID, dir string) ([]string, error) {
	out, err := g.run(ctx, false, projectID, dir, "git", "diff", "HEAD", "--name-only", "-z", "--no-renames", "--")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// CheckedOut is the branch checked out in dir, or "" (detached, or not a
// repository).
func (g *Git) CheckedOut(ctx context.Context, projectID, dir string) string {
	out, err := g.run(ctx, false, projectID, dir, "git", "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
