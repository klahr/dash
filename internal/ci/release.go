package ci

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// Release states: what became of the workflow runs a pushed release tag
// started.
const (
	ReleaseBuilding  = "building"
	ReleasePublished = "published"
	ReleaseFailed    = "failed"
	// ReleaseNoBuild: no workflow started for the tag (e.g. the release
	// workflow only runs on tags in another format).
	ReleaseNoBuild = "no_build"
)

// Release is the last release tag Dash pushed for a project and its build.
type Release struct {
	Tag    string       `json:"tag"`
	SHA    string       `json:"sha"`
	State  string       `json:"state"`
	Runs   []gitops.Run `json:"runs"`
	URL    string       `json:"url,omitempty"` // the GitHub release (or tag) page
	Pushed time.Time    `json:"pushed"`
}

// CommitState is the build state of one commit from runs (any order): failing
// if a workflow's newest run failed, running if one hasn't finished, passing
// when all passed, None without runs.
func CommitState(runs []gitops.Run, sha string) string {
	var mine []gitops.Run
	for _, r := range runs {
		if r.SHA == sha {
			mine = append(mine, r)
		}
	}
	state, _ := summarize(mine)
	return state
}

// CommitRuns lists the workflow runs of a commit: from GitHub when it
// answers, else from the default branch's runs last seen.
func (w *Watcher) CommitRuns(ctx context.Context, p model.Project, sha string) (string, []gitops.Run) {
	runs, err := w.Git.RunsForCommit(ctx, p.ID, p.Path, sha, 30)
	if err != nil {
		runs = nil
		for _, r := range w.Status(p.ID).Runs {
			if r.SHA == sha {
				runs = append(runs, r)
			}
		}
		if len(runs) == 0 {
			return Unknown, []gitops.Run{}
		}
	}
	if runs == nil {
		runs = []gitops.Run{}
	}
	return CommitState(runs, sha), runs
}

// releaseEvery and releaseFor pace following a release build; noBuildAfter is
// how long a pushed tag may go without a workflow run before Dash says none
// started.
var (
	releaseEvery = 15 * time.Second
	releaseFor   = 45 * time.Minute
	noBuildAfter = 3 * time.Minute
)

// WatchRelease follows the workflow runs that pushing tag (on sha) started:
// the project's status carries it as Release, building until they pass
// (published) or fail.
func (w *Watcher) WatchRelease(projectID, tag, sha string) {
	w.mu.Lock()
	if w.releases == nil {
		w.releases = map[string]Release{}
	}
	w.releases[projectID] = Release{Tag: tag, SHA: sha, State: ReleaseBuilding, Runs: []gitops.Run{}, Pushed: time.Now()}
	w.mu.Unlock()
	st := w.Status(projectID)
	st.ProjectID = projectID
	w.set(st) // shows "building" now
	// Read the pacing here, not in the goroutine, which outlives this call.
	every, deadline := releaseEvery, time.Now().Add(releaseFor)
	go func() {
		for time.Now().Before(deadline) {
			time.Sleep(every)
			rel, ok := w.followed(projectID)
			if !ok || rel.Tag != tag || rel.State != ReleaseBuilding {
				return
			}
			if _, err := w.Refresh(context.Background(), projectID); errors.Is(err, errGone) {
				return
			}
		}
	}()
}

var errGone = errors.New("project is gone")

func (w *Watcher) followed(projectID string) (Release, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.releases[projectID]
	return r, ok
}

// refreshRelease checks the build of the release being followed, if any.
func (w *Watcher) refreshRelease(ctx context.Context, p model.Project) *Release {
	rel, ok := w.followed(p.ID)
	if !ok {
		return nil
	}
	if rel.State == ReleaseBuilding {
		runs, err := w.Git.Runs(ctx, p.ID, p.Path, rel.Tag, 10)
		if err != nil {
			slog.Debug("release runs", "project", p.Name, "tag", rel.Tag, "err", err)
			return &rel
		}
		var mine []gitops.Run
		for _, r := range runs {
			if r.SHA == rel.SHA {
				mine = append(mine, r)
			}
		}
		rel.Runs = latestPerWorkflow(mine, "")
		if rel.Runs == nil {
			rel.Runs = []gitops.Run{}
		}
		switch state, _ := summarize(rel.Runs); {
		case len(rel.Runs) == 0 && time.Since(rel.Pushed) > noBuildAfter:
			rel.State = ReleaseNoBuild
		case len(rel.Runs) == 0, state == Running:
		case state == Failing:
			rel.State = ReleaseFailed
		default:
			rel.State = ReleasePublished
		}
		if rel.State == ReleasePublished || rel.State == ReleaseNoBuild {
			if url, err := w.Git.ReleaseURL(ctx, p.ID, p.Path, rel.Tag); err == nil && url != "" {
				rel.URL = url
			} else {
				rel.URL = gitops.ReleasePage(p.RemoteURL, rel.Tag)
			}
		}
		w.mu.Lock()
		if cur, ok := w.releases[p.ID]; ok && cur.Tag == rel.Tag {
			w.releases[p.ID] = rel
		}
		w.mu.Unlock()
	}
	return &rel
}

// sameRelease reports whether two statuses show the same release news.
func sameRelease(a, b *Release) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Tag != b.Tag || a.State != b.State || a.URL != b.URL || len(a.Runs) != len(b.Runs) {
		return false
	}
	for i := range a.Runs {
		if a.Runs[i] != b.Runs[i] {
			return false
		}
	}
	return true
}
