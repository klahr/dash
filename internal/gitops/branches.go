package gitops

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// BranchInfo is a branch the branch switcher offers.
type BranchInfo struct {
	Name string `json:"name"`
	// Local is whether the branch exists in the checkout; Remote whether
	// origin has it (as of the last fetch).
	Local  bool `json:"local"`
	Remote bool `json:"remote"`
}

// ListBranches lists the checkout's branches and origin's (as of the last
// fetch), by name, leaving out Dash's own task branches ("dash/…").
func (g *Git) ListBranches(ctx context.Context, projectID, repo string) ([]BranchInfo, error) {
	out, err := g.run(ctx, false, projectID, repo, "git", "for-each-ref", "--format=%(refname)",
		"refs/heads/", "refs/remotes/origin/")
	if err != nil {
		return nil, err
	}
	byName := map[string]*BranchInfo{}
	for _, ref := range strings.Fields(out) {
		name, local := strings.CutPrefix(ref, "refs/heads/")
		if !local {
			name = strings.TrimPrefix(ref, "refs/remotes/origin/")
			if name == "HEAD" {
				continue
			}
		}
		if strings.HasPrefix(name, "dash/") {
			continue
		}
		b := byName[name]
		if b == nil {
			b = &BranchInfo{Name: name}
			byName[name] = b
		}
		if local {
			b.Local = true
		} else {
			b.Remote = true
		}
	}
	list := make([]BranchInfo, 0, len(byName))
	for _, b := range byName {
		list = append(list, *b)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

// FetchBranches updates origin's branches (refs/remotes/origin/*) without
// moving any local branch, pruning ones origin deleted. Callers bound it
// with a context timeout.
func (g *Git) FetchBranches(ctx context.Context, projectID, repo string) error {
	_, err := g.run(ctx, false, projectID, repo, "git", "fetch", "--no-tags", "--prune", "origin",
		"+refs/heads/*:refs/remotes/origin/*")
	return err
}

// RemoteHasBranch reports whether origin has branch, as of the last fetch.
func (g *Git) RemoteHasBranch(ctx context.Context, projectID, repo, branch string) bool {
	_, err := g.run(ctx, false, projectID, repo, "git", "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return branch != "" && err == nil
}

// EnsureBranch makes sure the checkout has branch: it exists already, or is
// made from origin's branch of that name, or, when origin has none either,
// from the ref from ("" refuses to make a new branch). created reports
// whether it was made from from.
func (g *Git) EnsureBranch(ctx context.Context, projectID, repo, branch, from string) (created bool, err error) {
	if err := ValidBranchName(ctx, g, branch); err != nil {
		return false, err
	}
	if g.HasBranch(ctx, projectID, repo, branch) {
		return false, nil
	}
	if g.RemoteHasBranch(ctx, projectID, repo, branch) {
		_, err := g.git(ctx, projectID, repo, "branch", "--no-track", branch, "refs/remotes/origin/"+branch)
		return false, err
	}
	if from == "" {
		return false, fmt.Errorf("there is no branch %s", branch)
	}
	if _, err := g.git(ctx, projectID, repo, "branch", "--no-track", branch, from); err != nil {
		return false, err
	}
	return true, nil
}

// ValidBranchName refuses names git wouldn't take for a branch, ones that
// would read as an option, and Dash's own task branches ("dash/…").
func ValidBranchName(ctx context.Context, g *Git, name string) error {
	switch {
	case name == "" || strings.HasPrefix(name, "-"):
		return fmt.Errorf("%q isn't a valid branch name", name)
	case strings.HasPrefix(name, "dash/"):
		return fmt.Errorf("branches under dash/ are Dash's own task branches")
	}
	if g.CheckRefFormat(ctx, "refs/heads/"+name) != nil {
		return fmt.Errorf("%q isn't a valid branch name", name)
	}
	return nil
}

// CompareBranches counts the commits head has that base doesn't (ahead) and
// the other way round (behind), comparing the local branches.
func (g *Git) CompareBranches(ctx context.Context, projectID, repo, base, head string) (ahead, behind int, err error) {
	out, err := g.run(ctx, false, projectID, repo, "git", "rev-list", "--left-right", "--count",
		"refs/heads/"+head+"...refs/heads/"+base, "--")
	if err != nil {
		return 0, 0, err
	}
	return ParseAheadBehind(out)
}
