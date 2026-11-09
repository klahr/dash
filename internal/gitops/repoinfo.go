package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// RepoInfo is what GitHub says about a project's repository, for the project
// settings: its description, license, latest release, counts and the
// repository's own settings (merge methods, features).
type RepoInfo struct {
	NameWithOwner    string       `json:"name_with_owner"`
	URL              string       `json:"url"`
	Description      string       `json:"description,omitempty"`
	Homepage         string       `json:"homepage,omitempty"`
	Visibility       string       `json:"visibility,omitempty"` // PUBLIC | PRIVATE | INTERNAL
	IsArchived       bool         `json:"is_archived"`
	IsFork           bool         `json:"is_fork"`
	IsTemplate       bool         `json:"is_template"`
	Parent           string       `json:"parent,omitempty"` // OWNER/REPO it was forked from
	DefaultBranch    string       `json:"default_branch,omitempty"`
	License          *RepoLicense `json:"license,omitempty"`
	LatestRelease    *RepoRelease `json:"latest_release,omitempty"`
	Language         string       `json:"language,omitempty"`
	Topics           []string     `json:"topics"`
	Stars            int          `json:"stars"`
	Forks            int          `json:"forks"`
	Watchers         int          `json:"watchers"`
	OpenIssues       int          `json:"open_issues"`
	OpenPRs          int          `json:"open_prs"`
	DiskKB           int          `json:"disk_kb"`
	CreatedAt        time.Time    `json:"created_at"`
	PushedAt         time.Time    `json:"pushed_at"`
	HasIssues        bool         `json:"has_issues"`
	HasWiki          bool         `json:"has_wiki"`
	HasDiscussions   bool         `json:"has_discussions"`
	HasProjects      bool         `json:"has_projects"`
	MergeCommit      bool         `json:"merge_commit"`
	SquashMerge      bool         `json:"squash_merge"`
	RebaseMerge      bool         `json:"rebase_merge"`
	DeleteOnMerge    bool         `json:"delete_branch_on_merge"`
	ViewerPermission string       `json:"viewer_permission,omitempty"` // ADMIN | MAINTAIN | WRITE | TRIAGE | READ
}

// RepoLicense is a repository's license as GitHub detected it.
type RepoLicense struct {
	Key  string `json:"key"`  // e.g. mit; "other" when GitHub can't tell
	Name string `json:"name"` // e.g. MIT License
}

// RepoRelease is a repository's latest GitHub release.
type RepoRelease struct {
	Name        string    `json:"name,omitempty"`
	Tag         string    `json:"tag"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"published_at"`
}

// repoInfoFields are the fields RepoInfo asks `gh repo view` for.
const repoInfoFields = "nameWithOwner,url,description,homepageUrl,visibility,isArchived,isFork,isTemplate,parent," +
	"defaultBranchRef,licenseInfo,latestRelease,primaryLanguage,repositoryTopics,stargazerCount,forkCount,watchers," +
	"issues,pullRequests,diskUsage,createdAt,pushedAt,hasIssuesEnabled,hasWikiEnabled,hasDiscussionsEnabled," +
	"hasProjectsEnabled,mergeCommitAllowed,squashMergeAllowed,rebaseMergeAllowed,deleteBranchOnMerge,viewerPermission"

// RepoInfo looks up origin's repository on GitHub with gh.
func (g *Git) RepoInfo(ctx context.Context, remote string) (RepoInfo, error) {
	name := RepoName(remote)
	if name == "" {
		return RepoInfo{}, fmt.Errorf("%q is not a GitHub repository URL", remote)
	}
	out, err := g.ghQuiet(ctx, "", "", "repo", "view", name, "--json", repoInfoFields)
	if err != nil {
		return RepoInfo{}, err
	}
	return ParseRepoInfo(out)
}

// ParseRepoInfo reads `gh repo view --json <repoInfoFields>` output.
func ParseRepoInfo(out string) (RepoInfo, error) {
	type count struct {
		TotalCount int `json:"totalCount"`
	}
	type named struct {
		Name string `json:"name"`
	}
	var raw struct {
		NameWithOwner string  `json:"nameWithOwner"`
		URL           string  `json:"url"`
		Description   *string `json:"description"`
		HomepageURL   *string `json:"homepageUrl"`
		Visibility    string  `json:"visibility"`
		IsArchived    bool    `json:"isArchived"`
		IsFork        bool    `json:"isFork"`
		IsTemplate    bool    `json:"isTemplate"`
		Parent        *struct {
			Name  string `json:"name"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"parent"`
		DefaultBranchRef *named `json:"defaultBranchRef"`
		LicenseInfo      *struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"licenseInfo"`
		LatestRelease *struct {
			Name        string    `json:"name"`
			TagName     string    `json:"tagName"`
			URL         string    `json:"url"`
			PublishedAt time.Time `json:"publishedAt"`
		} `json:"latestRelease"`
		PrimaryLanguage    *named    `json:"primaryLanguage"`
		RepositoryTopics   []named   `json:"repositoryTopics"`
		StargazerCount     int       `json:"stargazerCount"`
		ForkCount          int       `json:"forkCount"`
		Watchers           count     `json:"watchers"`
		Issues             count     `json:"issues"`
		PullRequests       count     `json:"pullRequests"`
		DiskUsage          int       `json:"diskUsage"`
		CreatedAt          time.Time `json:"createdAt"`
		PushedAt           time.Time `json:"pushedAt"`
		HasIssuesEnabled   bool      `json:"hasIssuesEnabled"`
		HasWikiEnabled     bool      `json:"hasWikiEnabled"`
		HasDiscussions     bool      `json:"hasDiscussionsEnabled"`
		HasProjects        bool      `json:"hasProjectsEnabled"`
		MergeCommitAllowed bool      `json:"mergeCommitAllowed"`
		SquashMergeAllowed bool      `json:"squashMergeAllowed"`
		RebaseMergeAllowed bool      `json:"rebaseMergeAllowed"`
		DeleteBranch       bool      `json:"deleteBranchOnMerge"`
		ViewerPermission   string    `json:"viewerPermission"`
	}
	if err := json.Unmarshal([]byte(jsonPart(strings.TrimSpace(out))), &raw); err != nil {
		return RepoInfo{}, fmt.Errorf("gh repo view: %w", err)
	}
	str := func(s *string) string {
		if s == nil {
			return ""
		}
		return strings.TrimSpace(*s)
	}
	info := RepoInfo{
		NameWithOwner: raw.NameWithOwner, URL: raw.URL,
		Description: str(raw.Description), Homepage: str(raw.HomepageURL),
		Visibility: raw.Visibility, IsArchived: raw.IsArchived, IsFork: raw.IsFork, IsTemplate: raw.IsTemplate,
		Topics: []string{},
		Stars:  raw.StargazerCount, Forks: raw.ForkCount, Watchers: raw.Watchers.TotalCount,
		OpenIssues: raw.Issues.TotalCount, OpenPRs: raw.PullRequests.TotalCount, DiskKB: raw.DiskUsage,
		CreatedAt: raw.CreatedAt, PushedAt: raw.PushedAt,
		HasIssues: raw.HasIssuesEnabled, HasWiki: raw.HasWikiEnabled, HasDiscussions: raw.HasDiscussions, HasProjects: raw.HasProjects,
		MergeCommit: raw.MergeCommitAllowed, SquashMerge: raw.SquashMergeAllowed, RebaseMerge: raw.RebaseMergeAllowed,
		DeleteOnMerge: raw.DeleteBranch, ViewerPermission: raw.ViewerPermission,
	}
	if p := raw.Parent; p != nil && p.Name != "" {
		info.Parent = p.Name
		if p.Owner.Login != "" {
			info.Parent = p.Owner.Login + "/" + p.Name
		}
	}
	if raw.DefaultBranchRef != nil {
		info.DefaultBranch = raw.DefaultBranchRef.Name
	}
	if l := raw.LicenseInfo; l != nil && (l.Key != "" || l.Name != "") {
		info.License = &RepoLicense{Key: l.Key, Name: l.Name}
	}
	if r := raw.LatestRelease; r != nil && r.TagName != "" {
		info.LatestRelease = &RepoRelease{Name: strings.TrimSpace(r.Name), Tag: r.TagName, URL: r.URL, PublishedAt: r.PublishedAt}
	}
	if raw.PrimaryLanguage != nil {
		info.Language = raw.PrimaryLanguage.Name
	}
	for _, t := range raw.RepositoryTopics {
		if t.Name != "" {
			info.Topics = append(info.Topics, t.Name)
		}
	}
	return info, nil
}
