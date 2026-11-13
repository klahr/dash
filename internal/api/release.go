package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/ci"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/release"
)

// Releases: a release is an annotated tag, named in the project's tag format
// (model.Project.TagFormat), on a commit origin already has, pushed to origin
// so the repository's release workflow builds it. The History view's
// "Tag release…", POST /api/projects/{pid}/tags and the create_release tool
// all end in createRelease.

var (
	errForbidden = errors.New("forbidden")
	errConflict  = errors.New("conflict")
)

// tagTimeout bounds talking to origin while preparing or pushing a release.
const tagTimeout = 30 * time.Second

// canRelease reports why the caller may not tag releases: only those who may
// push, the org's owners and admins (and another Dash, which checked its own
// user's rights).
func (sv *service) canRelease() error {
	if err := sv.writable(); err != nil {
		return err
	}
	if sv.c.Machine || sv.c.PlatformAdmin || sv.s.orgAdmin(sv.c) {
		return nil
	}
	return fmt.Errorf("%w: only the organization's owners and admins can tag releases", errForbidden)
}

// tagFormat is the project's tag format, parsed.
func tagFormat(p model.Project) (release.Format, error) {
	f, err := release.ParseFormat(p.TagFormat)
	if err != nil {
		return f, fmt.Errorf("%w: the project's tag format %q is invalid: %v; fix it in the project settings", errBadRequest, p.TagFormat, err)
	}
	return f, nil
}

// checkTagFormat validates a tag format for saving: its placeholders, and
// that git accepts the tags it makes.
func (s *Server) checkTagFormat(ctx context.Context, format string) (release.Format, error) {
	f, err := release.ParseFormat(format)
	if err != nil {
		return f, fmt.Errorf("%w: %v", errBadRequest, err)
	}
	if s.Git != nil {
		if err := s.Git.CheckRefFormat(ctx, "refs/tags/"+f.Render(release.Version{Major: 1, Minor: 2, Patch: 3})); err != nil {
			return f, fmt.Errorf("%w: the tag format doesn't make a valid git tag name (git check-ref-format)", errBadRequest)
		}
	}
	return f, nil
}

// releaseCommit resolves sha ("" for the tip of origin's default branch) to
// a full commit id.
func (s *Server) releaseCommit(ctx context.Context, p model.Project, sha string) (string, error) {
	ref := strings.TrimSpace(sha)
	if ref == "" {
		ref = "refs/remotes/origin/" + p.DefaultBranch
	} else if strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("%w: %q isn't a commit", errBadRequest, sha)
	}
	full, err := s.Git.Commit(ctx, p.ID, p.Path, ref)
	if err != nil || full == "" {
		if sha == "" {
			return "", fmt.Errorf("%w: origin has no %s yet; push it first", errBadRequest, p.DefaultBranch)
		}
		return "", fmt.Errorf("%w: no commit %q in this repository", errBadRequest, sha)
	}
	return full, nil
}

// releaseInfo is what the "Tag release…" dialog shows for a commit.
type releaseInfo struct {
	SHA         string              `json:"sha"`
	Format      string              `json:"format"`
	Suggestions release.Suggestions `json:"suggestions"`
	Tags        []string            `json:"tags"`     // existing tags in the format, highest version first
	Previous    string              `json:"previous"` // the tag the notes start after ("" for the first release)
	Notes       string              `json:"notes"`
	Pushed      bool                `json:"pushed"` // origin has the commit
	CI          struct {
		State string       `json:"state"`
		Runs  []gitops.Run `json:"runs"`
	} `json:"ci"`
	Running    int           `json:"running"` // tasks running or merging in the project
	Hint       *release.Hint `json:"hint,omitempty"`
	FetchError string        `json:"fetch_error,omitempty"` // origin's tags couldn't be fetched
	CanTag     bool          `json:"can_tag"`
	CantTag    string        `json:"cant_tag,omitempty"` // why not
	CommitURL  string        `json:"commit_url,omitempty"`
}

// prepareRelease fetches origin's tags and works out the dialog's suggestions,
// notes, CI status and warnings for tagging sha.
func (sv *service) prepareRelease(ctx context.Context, p model.Project, sha string) (releaseInfo, error) {
	var info releaseInfo
	f, err := tagFormat(p)
	if err != nil {
		return info, err
	}
	if p.RemoteURL == "" {
		return info, fmt.Errorf("%w: the project has no remote to push a release tag to", errBadRequest)
	}
	g := sv.s.Git
	fctx, cancel := context.WithTimeout(ctx, tagTimeout)
	if err := sv.s.withRemote(fctx, p.ID, func() error {
		_, err := g.FetchTags(fctx, p.ID, p.Path)
		return err
	}); err != nil {
		info.FetchError = briefGit(err)
	}
	cancel()
	if info.SHA, err = sv.s.releaseCommit(ctx, p, sha); err != nil {
		return info, err
	}
	tags, err := g.Tags(ctx, p.ID, p.Path)
	if err != nil {
		return info, err
	}
	info.Format = f.String()
	info.Suggestions = f.Suggest(tags)
	info.Tags = []string{}
	for _, t := range f.SortTags(tags) {
		info.Tags = append(info.Tags, t.Name)
	}
	info.Previous = info.Suggestions.Latest
	info.Notes = sv.releaseNotes(ctx, p, info.Previous, info.SHA)
	info.Pushed = g.OnRemote(ctx, p.ID, p.Path, info.SHA)
	info.CI.State, info.CI.Runs = ci.Unknown, []gitops.Run{}
	if sv.s.CI != nil && strings.Contains(p.RemoteURL, "github.com") {
		cctx, cancel := context.WithTimeout(ctx, tagTimeout)
		info.CI.State, info.CI.Runs = sv.s.CI.CommitRuns(cctx, p, info.SHA)
		cancel()
	}
	info.Running = len(sv.data.ListTodos(p.ID, model.StatusInProgress, model.StatusMerging))
	info.Hint = sv.s.workflowHint(ctx, p, f)
	info.CommitURL = gitops.CommitPage(p.RemoteURL, info.SHA)
	switch err := sv.canRelease(); {
	case err != nil:
		info.CantTag = strings.TrimPrefix(err.Error(), errForbidden.Error()+": ")
	case !info.Pushed:
		info.CantTag = "Push first: origin doesn't have this commit yet."
	default:
		info.CanTag = true
	}
	return info, nil
}

// workflowHint warns when the repository's tag-triggered workflows (on the
// default branch) won't run for tags in format f.
func (s *Server) workflowHint(ctx context.Context, p model.Project, f release.Format) *release.Hint {
	files, err := s.Git.WorkflowFiles(ctx, p.ID, p.Path, p.DefaultBranch)
	if err != nil || len(files) == 0 {
		return nil
	}
	return release.WorkflowHint(f, release.TagTriggers(files))
}

// releaseNotes lists the Dash tasks merged between the previous tag and sha
// (title, and Claude's summary), oldest first.
func (sv *service) releaseNotes(ctx context.Context, p model.Project, prev, sha string) string {
	from := ""
	if prev != "" {
		from = "refs/tags/" + prev
	}
	commits, err := sv.s.Git.CommitsBetween(ctx, p.ID, p.Path, from, sha)
	if err != nil {
		return ""
	}
	inRange, named := map[string]bool{}, map[string]int{}
	for i, c := range commits {
		inRange[c.SHA] = true
		for _, id := range c.Todos {
			named[id] = i
		}
	}
	type entry struct {
		at int // index in commits: newest first
		t  model.Todo
	}
	var es []entry
	for _, t := range sv.data.ListTodos(p.ID, model.StatusMerged) {
		if t.Revert != nil {
			continue
		}
		if i, ok := named[t.ID]; ok {
			es = append(es, entry{i, t})
		} else if t.MergedCommit != "" && inRange[t.MergedCommit] {
			es = append(es, entry{slices.IndexFunc(commits, func(c gitops.RangeCommit) bool { return c.SHA == t.MergedCommit }), t})
		}
	}
	slices.SortStableFunc(es, func(a, b entry) int { return b.at - a.at })
	var b strings.Builder
	for _, e := range es {
		title := strings.TrimSpace(e.t.Title)
		if title == "" {
			title = firstLine(e.t.Prompt)
		}
		b.WriteString("- " + title)
		if s := strings.TrimSpace(e.t.Summary); s != "" {
			b.WriteString(" — " + firstLine(s))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// briefGit is the gist of a git failure (its last line of output).
func briefGit(err error) string {
	var ce *gitops.CmdError
	if errors.As(err, &ce) {
		out := strings.TrimSpace(ce.Output)
		if i := strings.LastIndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
		if out != "" {
			return out
		}
	}
	return err.Error()
}

// createRelease tags a commit and pushes the tag; see mcptools.NewRelease.
func (sv *service) createRelease(ctx context.Context, p model.Project, req mcptools.NewRelease) (mcptools.Release, error) {
	var out mcptools.Release
	if err := sv.canRelease(); err != nil {
		return out, err
	}
	f, err := tagFormat(p)
	if err != nil {
		return out, err
	}
	if p.RemoteURL == "" {
		return out, fmt.Errorf("%w: the project has no remote to push a release tag to", errBadRequest)
	}
	g := sv.s.Git
	tag := func() error {
		fctx, cancel := context.WithTimeout(ctx, tagTimeout)
		defer cancel()
		if _, err := g.FetchTags(fctx, p.ID, p.Path); err != nil {
			return fmt.Errorf("can't fetch origin's tags: %w", err)
		}
		sha, err := sv.s.releaseCommit(ctx, p, req.SHA)
		if err != nil {
			return err
		}
		if !g.OnRemote(ctx, p.ID, p.Path, sha) {
			return fmt.Errorf("%w: push first: origin doesn't have commit %.8s yet", errBadRequest, sha)
		}
		tags, err := g.Tags(ctx, p.ID, p.Path)
		if err != nil {
			return err
		}
		t, err := f.Resolve(tags, req.Version, req.Bump)
		if err != nil {
			return fmt.Errorf("%w: %v", errBadRequest, err)
		}
		if err := release.CheckRefName(t.Name); err != nil {
			return fmt.Errorf("%w: %v", errBadRequest, err)
		}
		if g.HasTag(ctx, p.ID, p.Path, t.Name) {
			return fmt.Errorf("%w: tag %s already exists", errConflict, t.Name)
		}
		if remote, err := g.RemoteHasTag(fctx, p.ID, p.Path, t.Name); err != nil {
			return fmt.Errorf("can't ask origin for its tags: %w", err)
		} else if remote {
			return fmt.Errorf("%w: tag %s already exists on origin", errConflict, t.Name)
		}
		prev, _ := f.Previous(tags, t.Version)
		notes := strings.TrimSpace(req.Notes)
		if notes == "" {
			notes = sv.releaseNotes(ctx, p, prev.Name, sha)
		}
		msg := "Release " + t.Name
		if notes != "" {
			msg += "\n\n" + notes
		}
		if err := g.CreateTag(ctx, p.ID, p.Path, t.Name, sha, msg); err != nil {
			return err
		}
		pctx, pcancel := context.WithTimeout(ctx, 2*time.Minute)
		defer pcancel()
		if err := g.PushTag(pctx, p.ID, p.Path, t.Name); err != nil {
			_ = g.DeleteTag(ctx, p.ID, p.Path, t.Name) // try again later from a clean slate
			return err
		}
		out = mcptools.Release{Tag: t.Name, SHA: sha, Previous: prev.Name, Notes: notes,
			URL: gitops.ReleasePage(p.RemoteURL, t.Name)}
		return nil
	}
	if err := sv.s.withRemote(ctx, p.ID, tag); err != nil {
		return mcptools.Release{}, err
	}
	slog.Info("tagged a release", "project", p.Name, "tag", out.Tag, "sha", out.SHA)
	if sv.s.CI != nil && strings.Contains(p.RemoteURL, "github.com") {
		sv.s.CI.WatchRelease(p.ID, out.Tag, out.SHA)
		out.Build = ci.ReleaseBuilding
	}
	return out, nil
}

// tagFetchEvery limits how often showing the History view fetches origin's tags.
const tagFetchEvery = time.Minute

var lastTagFetch sync.Map // project ID → time.Time

// fetchTagsSoon fetches origin's tags in the background, at most every
// tagFetchEvery, so tags pushed elsewhere show up in the graph: FetchTags
// publishes a git.op event when it brought something, and the graph reloads.
func (s *Server) fetchTagsSoon(p model.Project) {
	if p.RemoteURL == "" || s.Git == nil {
		return
	}
	now := time.Now()
	if last, ok := lastTagFetch.Load(p.ID); ok && now.Sub(last.(time.Time)) < tagFetchEvery {
		return
	}
	lastTagFetch.Store(p.ID, now)
	go func() {
		if s.Merges != nil {
			unlock, ok := s.Merges.Remote.TryLock(p.ID)
			if !ok {
				lastTagFetch.Delete(p.ID) // origin is busy; next time
				return
			}
			defer unlock()
		}
		ctx, cancel := context.WithTimeout(context.Background(), tagTimeout)
		defer cancel()
		if _, err := s.Git.FetchTags(ctx, p.ID, p.Path); err != nil {
			slog.Debug("fetch tags", "project", p.Name, "err", err)
		}
	}()
}

// withRemote runs fn holding the project's remote lock, so it never races
// another fetch or push of the same repository.
func (s *Server) withRemote(ctx context.Context, projectID string, fn func() error) error {
	if s.Merges == nil {
		return fn()
	}
	return s.Merges.Remote.With(ctx, projectID, fn)
}

// ---------------------------------------------------------------- handlers

// releaseProject is the request's project, refreshed with origin's URL.
func (s *Server) releaseProject(w http.ResponseWriter, r *http.Request) (*service, model.Project, bool) {
	sv, p, ok := s.project(w, r)
	if !ok {
		return nil, p, false
	}
	if url := s.Git.RemoteURL(r.Context(), p.ID, p.Path); url != "" && url != p.RemoteURL {
		p, _ = sv.data.UpdateProject(p.ID, func(x *model.Project) error { x.RemoteURL = url; return nil })
	}
	return sv, p, true
}

// getRelease handles GET /api/projects/{pid}/release?sha=: what tagging the
// commit as a release would look like.
func (s *Server) getRelease(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.releaseProject(w, r)
	if !ok {
		return
	}
	info, err := sv.prepareRelease(r.Context(), p, r.URL.Query().Get("sha"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// createTag handles POST /api/projects/{pid}/tags: tag a commit as a release
// and push the tag.
func (s *Server) createTag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SHA     string `json:"sha"`
		Version string `json:"version"` // "1.4.0" or a tag in the format
		Bump    string `json:"bump"`    // patch | minor | major, instead of version
		Notes   string `json:"notes"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	sv, p, ok := s.releaseProject(w, r)
	if !ok {
		return
	}
	out, err := sv.createRelease(r.Context(), p, mcptools.NewRelease{SHA: req.SHA, Version: req.Version, Bump: req.Bump, Notes: req.Notes})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// tagFormatInfo handles GET /api/projects/{pid}/tag-format?format=: whether a
// tag format is valid, an example tag, and whether the repository's release
// workflows would run for its tags. Without format, the project's own.
func (s *Server) tagFormatInfo(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.project(w, r)
	if !ok {
		return
	}
	format := p.TagFormat
	if r.URL.Query().Has("format") {
		format = strings.TrimSpace(r.URL.Query().Get("format"))
	}
	out := struct {
		Format  string        `json:"format"`
		Valid   bool          `json:"valid"`
		Error   string        `json:"error,omitempty"`
		Example string        `json:"example,omitempty"`
		Hint    *release.Hint `json:"hint,omitempty"`
	}{Format: format}
	f, err := s.checkTagFormat(r.Context(), format)
	if err != nil {
		out.Error = strings.TrimPrefix(err.Error(), errBadRequest.Error()+": ")
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Valid, out.Format, out.Example = true, f.String(), f.Render(release.Version{Major: 1, Minor: 2, Patch: 3})
	out.Hint = s.workflowHint(r.Context(), p, f)
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------- MCP

func (b board) CreateRelease(projectID string, r mcptools.NewRelease) (mcptools.Release, error) {
	p, err := b.sv.data.GetProject(projectID)
	if err != nil {
		return mcptools.Release{}, fmt.Errorf("no project %q (see list_projects)", projectID)
	}
	if b.sv.s.hub() {
		return b.sv.releaseOnRunner(context.Background(), p, r)
	}
	return b.sv.createRelease(context.Background(), p, r)
}

// releaseOnRunner tags a release of the hub's project p on a runner linked
// to it, which makes and pushes the tag in its clone. The hub checks the
// caller's rights itself: the runner sees the hub as a Machine, which it
// trusts to have done so.
func (sv *service) releaseOnRunner(ctx context.Context, p model.Project, r mcptools.NewRelease) (mcptools.Release, error) {
	var out mcptools.Release
	if err := sv.canRelease(); err != nil {
		return out, err
	}
	run, rpid, err := sv.projectRunner(ctx, p, "")
	if err != nil {
		return out, err
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	body := map[string]string{"sha": r.SHA, "version": r.Version, "bump": r.Bump, "notes": r.Notes}
	err = sv.s.fleet().Call(cctx, run.ID, http.MethodPost, "/api/projects/"+url.PathEscape(rpid)+"/tags", body, &out)
	return out, err
}
