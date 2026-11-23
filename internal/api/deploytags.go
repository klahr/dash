package api

// Deploys and release tags: the Deploys page's Deploy picks a release tag
// (GET /api/projects/{pid}/deploys/tags), each deploy records the release
// tag on its commit (model.Deploy.Tag), and an environment with "Deploys:
// release tags only" (model.Environment.ReleaseTagsOnly) refuses any other
// commit, whatever asks: the API, History's "Deploy to…", the deploy
// tool, Promote, Deploy again and rollbacks. A release tag's own deploy
// (model.DeployByRelease) always may; merges deploy nothing there.
//
// Tags live in the project's clone, where its code is: on a hub, a runner
// answers (POST /api/hub/projects/{hpid}/deploys/tags); elsewhere, this
// computer. The runner that runs a deploy checks again when it starts it,
// so a deploy queued before the setting was turned on doesn't slip through.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/release"
)

// maxPickTags caps the tags Deploy's picker lists (the newest ones).
const maxPickTags = 30

// tagsTimeout bounds asking for tags (fetching origin's first, if need be).
const tagsTimeout = 30 * time.Second

// tagsQuery asks, where the project's code is, for its release tags: those
// in Format (the project's tag format).
type tagsQuery struct {
	Format string `json:"format"`
	// List: list the tags, newest first (at most maxPickTags), each
	// compared with Live (a commit id, "" none): how far ahead or behind.
	List bool   `json:"list,omitempty"`
	Live string `json:"live,omitempty"`
	// Commit: resolve this commit too, and name the release tags on it.
	Commit string `json:"commit,omitempty"`
}

// releaseTag is a release tag in Deploy's picker.
type releaseTag struct {
	Tag    string `json:"tag"`
	Commit string `json:"commit"`
	// Compared: Ahead and Behind are known (there is a live commit, and git
	// could count): the commits the tag has that the live one hasn't, and
	// the other way round.
	Compared bool `json:"compared"`
	Ahead    int  `json:"ahead"`
	Behind   int  `json:"behind"`
	// Build is how the tag's release build went, as this Dash (the hub,
	// on a hub) follows it; the hub fills it in (addTagBuilds).
	Build *tagBuild `json:"build,omitempty"`
}

// deployTags answers a tagsQuery.
type deployTags struct {
	Tags []releaseTag `json:"tags"`
	// Live is the commit the tags were compared with, if any.
	Live string `json:"live,omitempty"`
	// Commit is the full id of the commit asked about, CommitTags the
	// release tags on it (highest version first).
	Commit     string   `json:"commit,omitempty"`
	CommitTags []string `json:"commit_tags,omitempty"`
	// CommitBuild is the build of CommitTags[0], if it has any.
	CommitBuild *tagBuild `json:"commit_build,omitempty"`
	// Format is the project's tag format.
	Format string `json:"format"`
}

// releaseTagsOf answers q for p where its code is: a runner of a hub's
// project, else this computer.
func (sv *service) releaseTagsOf(ctx context.Context, p model.Project, q tagsQuery) (deployTags, error) {
	s := sv.s
	if !s.hub() {
		return s.tagsHere(ctx, p, q)
	}
	run, _, err := sv.projectRunner(ctx, p, "")
	if err != nil {
		return deployTags{}, err
	}
	var out deployTags
	err = s.runnerCall(ctx, run.ID, http.MethodPost, deployPath(p.ID, "tags"), q, &out)
	return out, err
}

// tagsHere answers q in p's clone here. When the commit asked about has no
// release tag here, origin's tags are fetched and it is looked at again.
func (s *Server) tagsHere(ctx context.Context, p model.Project, q tagsQuery) (deployTags, error) {
	f, err := release.ParseFormat(q.Format)
	if err != nil {
		return deployTags{}, fmt.Errorf("%w: the project's tag format %q is invalid: %v; fix it in the project settings", errBadRequest, q.Format, err)
	}
	out := deployTags{Tags: []releaseTag{}, Format: f.String()}
	if s.Git == nil {
		return out, fmt.Errorf("%w: this Dash can't read the project's git tags", errBadRequest)
	}
	if q.List {
		s.fetchTagsNow(ctx, p) // the picker lists what origin has
	}
	refs, err := s.Git.TagCommits(ctx, p.ID, p.Path)
	if err != nil {
		return out, err
	}
	if c := strings.TrimSpace(q.Commit); c != "" {
		if gitops.CheckRev(c) != nil {
			return out, fmt.Errorf("%w: %q isn't a commit", errBadRequest, c)
		}
		full, err := s.Git.Commit(ctx, p.ID, p.Path, c)
		if err != nil || full == "" {
			return out, fmt.Errorf("%w: no commit %s in %s", errBadRequest, c, p.Name)
		}
		out.Commit = full
		out.CommitTags = releaseTagsAt(f, refs, full)
		if len(out.CommitTags) == 0 && !q.List && s.fetchTagsNow(ctx, p) {
			if refs, err = s.Git.TagCommits(ctx, p.ID, p.Path); err == nil {
				out.CommitTags = releaseTagsAt(f, refs, full)
			}
		}
	}
	if !q.List {
		return out, nil
	}
	if gitops.IsSHA(q.Live) {
		out.Live = q.Live
	}
	byName := map[string]string{}
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		byName[r.Name] = r.Commit
		names = append(names, r.Name)
	}
	for _, t := range f.SortTags(names) {
		if len(out.Tags) >= maxPickTags {
			break
		}
		rt := releaseTag{Tag: t.Name, Commit: byName[t.Name]}
		switch {
		case out.Live == "":
		case rt.Commit == out.Live:
			rt.Compared = true
		default:
			if a, b, err := s.Git.CommitsApart(ctx, p.ID, p.Path, out.Live, rt.Commit); err == nil {
				rt.Ahead, rt.Behind, rt.Compared = a, b, true
			}
		}
		out.Tags = append(out.Tags, rt)
	}
	return out, nil
}

// releaseTagsAt are the tags among refs in format f on commit full,
// highest version first.
func releaseTagsAt(f release.Format, refs []gitops.TagRef, full string) []string {
	var names []string
	for _, r := range refs {
		if r.Commit == full {
			names = append(names, r.Name)
		}
	}
	var out []string
	for _, t := range f.SortTags(names) {
		out = append(out, t.Name)
	}
	return out
}

// fetchTagsNow fetches origin's tags into p's clone here, waiting a little
// at most; it reports whether it could.
func (s *Server) fetchTagsNow(ctx context.Context, p model.Project) bool {
	if p.RemoteURL == "" && s.Git.RemoteURL(ctx, p.ID, p.Path) == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := s.withRemote(ctx, p.ID, func() error {
		_, err := s.Git.FetchTags(ctx, p.ID, p.Path)
		return err
	})
	return err == nil
}

// tagsOnlyCheck refuses a deploy of ref ("": the branch's tip) to e of p
// when e deploys release tags only and ref has none; what names ref in the
// error when it is someone else's choice ("the deploy to roll back to").
// It returns the release tag on ref, if it looked.
func (sv *service) tagsOnlyCheck(p model.Project, e model.Environment, ref, what string) (string, error) {
	if !e.ReleaseTagsOnly {
		return "", nil
	}
	if ref == "" {
		return "", fmt.Errorf("%w: %s only deploys release tags: pick a release tag to deploy, not the tip of %s", errConflict, e.Name, p.DefaultBranch)
	}
	ctx, cancel := context.WithTimeout(context.Background(), tagsTimeout)
	defer cancel()
	got, err := sv.releaseTagsOf(ctx, p, tagsQuery{Format: p.TagFormat, Commit: ref})
	if err != nil {
		return "", fmt.Errorf("%w: %s only deploys release tags, and Dash couldn't tell whether %s is tagged: %v", errConflict, e.Name, gitops.ShortSHA(ref), err)
	}
	if len(got.CommitTags) == 0 {
		return "", untaggedErr(e, ref, what)
	}
	return got.CommitTags[0], nil
}

// untaggedError is why an environment that deploys release tags only
// refuses a commit: it has none. It is a conflict (errConflict).
type untaggedError struct{ msg string }

func (e untaggedError) Error() string { return errConflict.Error() + ": " + e.msg }
func (e untaggedError) Unwrap() error { return errConflict }

// untaggedErr says e refuses commit, which has no release tag.
func untaggedErr(e model.Environment, commit, what string) error {
	name := gitops.ShortSHA(commit)
	if what != "" {
		name += " (" + what + ")"
	}
	return untaggedError{fmt.Sprintf("%s only deploys release tags; %s isn't tagged", e.Name, name)}
}

// deployTagsRoute handles GET /api/projects/{pid}/deploys/tags
// ?environment=&commit=: the project's release tags, newest first, each
// compared with what the environment runs now (Deploy's picker); with
// commit (and no environment), only that commit and its release tags
// (History's "Deploy to…"). Each tag has its release build (addTagBuilds).
func (s *Server) deployTagsRoute(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.project(w, r)
	if !ok {
		return
	}
	q := tagsQuery{Format: p.TagFormat, Commit: r.URL.Query().Get("commit")}
	if env := r.URL.Query().Get("environment"); env != "" || q.Commit == "" {
		q.List = true
		if env != "" {
			if _, ok := p.Environment(env); !ok {
				writeErr(w, fmt.Errorf("%w: the project has no environment %q", errBadRequest, env))
				return
			}
			if live, ok := sv.data.LiveDeploy(p.ID, env); ok {
				q.Live = live.Commit
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), tagsTimeout)
	defer cancel()
	out, err := sv.releaseTagsOf(ctx, p, q)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.addTagBuilds(p, &out)
	writeJSON(w, http.StatusOK, out)
}

// hubDeployTags handles POST /api/hub/projects/{hpid}/deploys/tags on a
// runner: a tagsQuery, answered with deployTags.
func (s *Server) hubDeployTags(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.deployHubProject(w, r, "")
	if !ok {
		return
	}
	var q tagsQuery
	if err := decode(r, &q); err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.tagsHere(r.Context(), p, q)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
