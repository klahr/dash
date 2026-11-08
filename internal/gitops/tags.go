package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

// tagRefspec fetches origin's tags without overwriting a local tag of the
// same name that points elsewhere (git reports those as "would clobber").
const tagRefspec = "refs/tags/*:refs/tags/*"

// onlyClobbered reports whether a failed fetch's output only complains about
// local tags that differ from origin's: everything else was fetched.
func onlyClobbered(out string) bool {
	seen := false
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.Contains(l, "would clobber existing tag"):
			seen = true
		case strings.HasPrefix(l, "!"), strings.HasPrefix(l, "error:"), strings.HasPrefix(l, "fatal:"):
			return false
		}
	}
	return seen
}

// FetchTags brings origin's tags in, so tags pushed from elsewhere show up.
// changed reports whether any tag was added or moved. A git.op activity
// event is published only when something changed.
func (g *Git) FetchTags(ctx context.Context, projectID, repo string) (changed bool, err error) {
	start := time.Now()
	out, err := g.run(ctx, false, projectID, repo, "git", "fetch", "--no-tags", "--no-write-fetch-head", "origin", tagRefspec)
	if err != nil && !onlyClobbered(out) {
		return false, err
	}
	changed = strings.Contains(out, "[new tag]") || strings.Contains(out, "[tag update]")
	if changed && g.pub != nil {
		g.pub.Publish(model.Event{Type: model.EvGitOp, ProjectID: projectID, Time: time.Now(), Data: map[string]any{
			"cmd": "git fetch origin " + tagRefspec, "dir": repo, "ok": true, "ms": time.Since(start).Milliseconds(),
		}})
	}
	return changed, nil
}

// Tags lists the repository's tags.
func (g *Git) Tags(ctx context.Context, projectID, repo string) ([]string, error) {
	out, err := g.run(ctx, false, projectID, repo, "git", "for-each-ref", "--format=%(refname:strip=2)", "refs/tags")
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// HasTag reports whether the local repository has the tag.
func (g *Git) HasTag(ctx context.Context, projectID, repo, tag string) bool {
	_, err := g.run(ctx, false, projectID, repo, "git", "rev-parse", "--verify", "--quiet", "refs/tags/"+tag)
	return err == nil
}

// RemoteHasTag asks origin whether it has the tag.
func (g *Git) RemoteHasTag(ctx context.Context, projectID, repo, tag string) (bool, error) {
	out, err := g.run(ctx, false, projectID, repo, "git", "ls-remote", "--tags", "origin", "refs/tags/"+tag)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// TagCommit is the commit a tag points at.
func (g *Git) TagCommit(ctx context.Context, projectID, repo, tag string) (string, error) {
	return g.Commit(ctx, projectID, repo, "refs/tags/"+tag)
}

// CreateTag makes an annotated tag on sha with msg. Like commits, it isn't
// signed unless SignCommits is on.
func (g *Git) CreateTag(ctx context.Context, projectID, repo, tag, sha, msg string) error {
	args := g.committing(ctx, repo, "tag", "-a", tag, sha, "-m", msg)
	if !g.SignCommits {
		args = append([]string{"-c", "tag.gpgsign=false"}, args...)
	}
	_, err := g.git(ctx, projectID, repo, args...)
	return err
}

// DeleteTag removes a local tag.
func (g *Git) DeleteTag(ctx context.Context, projectID, repo, tag string) error {
	_, err := g.git(ctx, projectID, repo, "tag", "-d", tag)
	return err
}

// PushTag publishes a tag to origin.
func (g *Git) PushTag(ctx context.Context, projectID, repo, tag string) error {
	return g.Push(ctx, projectID, repo, "refs/tags/"+tag)
}

// OnRemote reports whether origin has the commit, as far as its
// remote-tracking branches (as of the last fetch) know.
func (g *Git) OnRemote(ctx context.Context, projectID, repo, sha string) bool {
	out, err := g.run(ctx, false, projectID, repo, "git", "for-each-ref", "--count=1", "--contains", sha, "refs/remotes/origin")
	return err == nil && out != ""
}

// RangeCommit is a commit in a range and the tasks its message names (by
// "Dash-Todo: <id>" trailers).
type RangeCommit struct {
	SHA   string
	Todos []string
}

// CommitsBetween lists the commits reachable from to but not from from
// (every commit up to to when from is ""), newest first.
func (g *Git) CommitsBetween(ctx context.Context, projectID, repo, from, to string) ([]RangeCommit, error) {
	rng := to
	if from != "" {
		rng = from + ".." + to
	}
	out, err := g.run(ctx, false, projectID, repo, "git", "log", "--format=%H%x1f%B%x1e", rng, "--")
	if err != nil {
		return nil, err
	}
	var cs []RangeCommit
	for _, rec := range strings.Split(out, "\x1e") {
		sha, body, ok := strings.Cut(strings.TrimSpace(rec), "\x1f")
		if !ok {
			continue
		}
		c := RangeCommit{SHA: sha}
		for _, l := range strings.Split(body, "\n") {
			if id, ok := strings.CutPrefix(strings.TrimSpace(l), "Dash-Todo: "); ok {
				c.Todos = append(c.Todos, strings.TrimSpace(id))
			}
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// WorkflowFiles reads the GitHub Actions workflows (.github/workflows/*.yml
// and *.yaml) at ref; name → content.
func (g *Git) WorkflowFiles(ctx context.Context, projectID, repo, ref string) (map[string][]byte, error) {
	out, err := g.run(ctx, false, projectID, repo, "git", "ls-tree", "--name-only", "-z", ref+":.github/workflows")
	if err != nil {
		return nil, nil // no workflows directory
	}
	files := map[string][]byte{}
	for _, name := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		if ext := path.Ext(name); ext != ".yml" && ext != ".yaml" {
			continue
		}
		blob, err := g.Blob(ctx, projectID, repo, ref, ".github/workflows/"+name)
		if err != nil {
			continue
		}
		b, _, err := g.ReadBlob(ctx, projectID, repo, blob, 1<<20)
		if err != nil || b == nil {
			continue
		}
		files[name] = b
	}
	return files, nil
}

// ErrNoRelease is returned by ReleaseURL when GitHub has no release for the tag.
var ErrNoRelease = errors.New("no GitHub release for this tag")

// ReleaseURL is the GitHub release page of tag.
func (g *Git) ReleaseURL(ctx context.Context, projectID, dir, tag string) (string, error) {
	out, err := g.ghQuiet(ctx, projectID, dir, "release", "view", tag, "--json", "url")
	if err != nil {
		if strings.Contains(out, "release not found") || strings.Contains(out, "HTTP 404") {
			return "", ErrNoRelease
		}
		return "", err
	}
	var r struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(jsonPart(out)), &r); err != nil {
		return "", fmt.Errorf("gh release view: %w", err)
	}
	return r.URL, nil
}

// ReleasePage is where GitHub shows tag's release, from the origin URL ("" if
// origin isn't on GitHub).
func ReleasePage(remote, tag string) string {
	name := RepoName(remote)
	if !strings.HasPrefix(name, "github.com/") {
		return ""
	}
	return "https://" + name + "/releases/tag/" + tag
}

// CommitPage is where GitHub shows a commit ("" if origin isn't on GitHub).
func CommitPage(remote, sha string) string {
	name := RepoName(remote)
	if !strings.HasPrefix(name, "github.com/") {
		return ""
	}
	return "https://" + name + "/commit/" + sha
}

// RunsForCommit lists the newest workflow runs of one commit, newest first.
func (g *Git) RunsForCommit(ctx context.Context, projectID, dir, sha string, limit int) ([]Run, error) {
	return g.runList(ctx, projectID, dir, []string{"--commit", sha}, limit)
}

func (g *Git) runList(ctx context.Context, projectID, dir string, filter []string, limit int) ([]Run, error) {
	args := append([]string{"run", "list"}, filter...)
	args = append(args, "--limit", strconv.Itoa(limit),
		"--json", "databaseId,workflowName,displayTitle,status,conclusion,headSha,headBranch,url,event,createdAt")
	out, err := g.ghQuiet(ctx, projectID, dir, args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID         int64     `json:"databaseId"`
		Workflow   string    `json:"workflowName"`
		Title      string    `json:"displayTitle"`
		Status     string    `json:"status"`
		Conclusion string    `json:"conclusion"`
		SHA        string    `json:"headSha"`
		Branch     string    `json:"headBranch"`
		URL        string    `json:"url"`
		Event      string    `json:"event"`
		CreatedAt  time.Time `json:"createdAt"`
	}
	if err := json.Unmarshal([]byte(jsonPart(out)), &raw); err != nil {
		return nil, fmt.Errorf("gh run list: %w", err)
	}
	runs := make([]Run, len(raw))
	for i, r := range raw {
		runs[i] = Run(r)
	}
	return runs, nil
}

// CheckRefFormat asks git whether name (e.g. "refs/tags/v1.2.3") is a valid ref name.
func (g *Git) CheckRefFormat(ctx context.Context, name string) error {
	_, err := g.run(ctx, false, "", "", "git", "check-ref-format", name)
	return err
}
