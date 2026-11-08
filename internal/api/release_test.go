package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// releaseRepo is a checkout with an origin (a bare repository) and a
// project for it in s.
type releaseRepo struct {
	s            *Server
	h, owner     http.Handler // owner: signed in as the org's owner
	p            model.Project
	repo, origin string
}

func rgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newReleaseRepo(t *testing.T, format string) *releaseRepo {
	t.Helper()
	s, _, _ := boardServer(t)
	s.Git = gitops.New(nil)
	root := t.TempDir()
	origin, repo := filepath.Join(root, "origin.git"), filepath.Join(root, "repo")
	rgit(t, root, "init", "-q", "--bare", "-b", "main", origin)
	rgit(t, root, "clone", "-q", origin, repo)
	rgit(t, repo, "checkout", "-q", "-b", "main")
	rgit(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	rgit(t, repo, "push", "-q", "origin", "main")
	owner := testOwner(t, s)
	p, err := raw(s).CreateProject(model.Project{Name: "app", Path: repo, DefaultBranch: "main", RemoteURL: origin, OrgID: owner.Org, TagFormat: format})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Routes()
	return &releaseRepo{s: s, h: h, owner: loginAs(t, h, "u", "p"), p: p, repo: repo, origin: origin}
}

func (rr *releaseRepo) call(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return send(rr.owner, method, "/api/projects/"+rr.p.ID+path, body)
}

func (rr *releaseRepo) info(t *testing.T, sha string) releaseInfo {
	t.Helper()
	w := rr.call(t, "GET", "/release?sha="+sha, "")
	var info releaseInfo
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &info) != nil {
		t.Fatalf("release info: %d %s", w.Code, w.Body)
	}
	return info
}

// commit makes a commit landing a merged Dash task, and pushes it if push.
func (rr *releaseRepo) commit(t *testing.T, title, summary string, push bool) string {
	t.Helper()
	td, err := raw(rr.s).CreateTodo(model.Todo{ProjectID: rr.p.ID, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	rgit(t, rr.repo, "commit", "-q", "--allow-empty", "-m", title+"\n\nDash-Todo: "+td.ID)
	sha := rgit(t, rr.repo, "rev-parse", "HEAD")
	for _, st := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusReview, model.StatusMerging, model.StatusMerged} {
		if _, err := raw(rr.s).UpdateTodo(td.ID, func(x *model.Todo) error {
			x.Status, x.Summary, x.MergedCommit = st, summary, sha
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if push {
		rgit(t, rr.repo, "push", "-q", "origin", "main")
	}
	return sha
}

func TestTagRelease(t *testing.T) {
	rr := newReleaseRepo(t, "vX.Y.Z")
	sha := rr.commit(t, "Add login", "Users can sign in with a password.", true)

	info := rr.info(t, sha)
	if info.SHA != sha || info.Format != "vX.Y.Z" || info.Suggestions.First != "v0.1.0" || !info.Pushed || !info.CanTag {
		t.Fatalf("first release info: %+v", info)
	}
	if info.Notes != "- Add login — Users can sign in with a password." {
		t.Errorf("notes %q", info.Notes)
	}

	w := rr.call(t, "POST", "/tags", `{"sha":"`+sha+`","bump":"minor","notes":"First!"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("tag: %d %s", w.Code, w.Body)
	}
	var out struct{ Tag, SHA string }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Tag != "v0.1.0" || out.SHA != sha {
		t.Fatalf("created %+v", out)
	}
	if got := rgit(t, rr.origin, "rev-parse", "v0.1.0^{commit}"); got != sha {
		t.Errorf("origin's v0.1.0 is %s, want %s", got, sha)
	}
	if msg := rgit(t, rr.repo, "tag", "-l", "--format=%(contents)", "v0.1.0"); !strings.Contains(msg, "First!") {
		t.Errorf("tag message %q", msg)
	}

	// The same tag again is refused, as a version or a full tag name.
	for _, body := range []string{`{"version":"0.1.0"}`, `{"version":"v0.1.0"}`} {
		if w := rr.call(t, "POST", "/tags", body); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already exists") {
			t.Errorf("%s again: %d %s", body, w.Code, w.Body)
		}
	}
	// Not the format: refused.
	if w := rr.call(t, "POST", "/tags", `{"version":"release-1.0.0"}`); w.Code != http.StatusBadRequest {
		t.Errorf("wrong format: %d %s", w.Code, w.Body)
	}

	// The next release's notes start after v0.1.0, and default to the tip of origin's branch.
	sha2 := rr.commit(t, "Fix logout", "", true)
	info = rr.info(t, "")
	if info.SHA != sha2 || info.Previous != "v0.1.0" || info.Suggestions.Patch != "v0.1.1" || info.Suggestions.Major != "v1.0.0" || info.Notes != "- Fix logout" {
		t.Fatalf("second release info: %+v", info)
	}
}

func TestTagReleaseNeedsAPushedCommit(t *testing.T) {
	rr := newReleaseRepo(t, "")
	sha := rr.commit(t, "Local only", "", false)
	if info := rr.info(t, sha); info.Pushed || info.CanTag || !strings.Contains(info.CantTag, "Push first") {
		t.Fatalf("unpushed: %+v", info)
	}
	w := rr.call(t, "POST", "/tags", `{"sha":"`+sha+`","bump":"patch"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "push first") {
		t.Fatalf("unpushed tag: %d %s", w.Code, w.Body)
	}
	if out := rgit(t, rr.repo, "tag", "-l"); out != "" {
		t.Errorf("tags made: %q", out)
	}
}

// Tags pushed from another clone are fetched before working out the next
// version, and the highest version wins over the newest tag.
func TestReleaseSeesTagsPushedElsewhere(t *testing.T) {
	rr := newReleaseRepo(t, "X.Y.Z")
	other := filepath.Join(t.TempDir(), "other")
	rgit(t, filepath.Dir(other), "clone", "-q", rr.origin, other)
	rgit(t, other, "tag", "1.4.0")
	rgit(t, other, "tag", "1.10.0")
	rgit(t, other, "tag", "1.9.0") // newest, not highest
	rgit(t, other, "tag", "nightly")
	rgit(t, other, "push", "-q", "origin", "--tags")

	info := rr.info(t, "")
	if info.Suggestions.Latest != "1.10.0" || info.Suggestions.Patch != "1.10.1" || strings.Join(info.Tags, ",") != "1.10.0,1.9.0,1.4.0" {
		t.Fatalf("info %+v", info)
	}
	if w := rr.call(t, "POST", "/tags", `{"version":"1.9.0"}`); w.Code != http.StatusConflict {
		t.Errorf("existing tag: %d %s", w.Code, w.Body)
	}
}

func TestOnlyAdminsTagReleases(t *testing.T) {
	rr := newReleaseRepo(t, "")
	owner := testOwner(t, rr.s)
	member(t, rr.s, owner.Org, "m", store.RoleMember)
	member(t, rr.s, owner.Org, "a", store.RoleAdmin)
	m := loginAs(t, rr.h, "m", "long-enough-m")
	if w := send(m, "POST", "/api/projects/"+rr.p.ID+"/tags", `{"bump":"patch"}`); w.Code != http.StatusForbidden {
		t.Fatalf("member: %d %s", w.Code, w.Body)
	}
	var info releaseInfo
	w := send(m, "GET", "/api/projects/"+rr.p.ID+"/release", "")
	if json.Unmarshal(w.Body.Bytes(), &info); w.Code != http.StatusOK || info.CanTag || !strings.Contains(info.CantTag, "owners and admins") {
		t.Fatalf("member's info: %d %s", w.Code, w.Body)
	}
	a := loginAs(t, rr.h, "a", "long-enough-a")
	if w := send(a, "POST", "/api/projects/"+rr.p.ID+"/tags", `{"bump":"patch"}`); w.Code != http.StatusCreated {
		t.Fatalf("admin: %d %s", w.Code, w.Body)
	}
}

func TestTagFormatSetting(t *testing.T) {
	rr := newReleaseRepo(t, "")
	for _, bad := range []string{"X.Y", "Z.Y.X", "v X.Y.Z", "X.Y.Z.lock"} {
		if w := rr.call(t, "PATCH", "", `{"tag_format":"`+bad+`"}`); w.Code != http.StatusBadRequest {
			t.Errorf("%q saved: %d %s", bad, w.Code, w.Body)
		}
	}
	w := rr.call(t, "PATCH", "", `{"tag_format":" vX.Y.Z "}`)
	var p model.Project
	if json.Unmarshal(w.Body.Bytes(), &p); w.Code != http.StatusOK || p.TagFormat != "vX.Y.Z" {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	// Other settings leave it alone.
	if w := rr.call(t, "PATCH", "", `{"name":"app2"}`); !strings.Contains(w.Body.String(), `"tag_format":"vX.Y.Z"`) {
		t.Errorf("after renaming: %s", w.Body)
	}
}

// The settings warn when the repository's release workflow runs on tags the
// format doesn't make (the Dash repository's own runs on "v*").
func TestTagFormatWorkflowHint(t *testing.T) {
	rr := newReleaseRepo(t, "X.Y.Z")
	wf := filepath.Join(rr.repo, ".github", "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "release.yml"), []byte("name: Release\non:\n  push:\n    tags: [\"v*\"]\njobs: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rgit(t, rr.repo, "add", ".")
	rgit(t, rr.repo, "commit", "-q", "-m", "release workflow")

	type hintResp struct {
		Valid   bool
		Error   string
		Example string
		Hint    *struct{ Message, Suggest string }
	}
	get := func(q string) hintResp {
		w := rr.call(t, "GET", "/tag-format"+q, "")
		var out hintResp
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("tag-format%s: %d %s", q, w.Code, w.Body)
		}
		return out
	}
	if out := get(""); !out.Valid || out.Example != "1.2.3" || out.Hint == nil || out.Hint.Suggest != "vX.Y.Z" || !strings.Contains(out.Hint.Message, "release.yml") {
		t.Fatalf("project's format: %+v %+v", out, out.Hint)
	}
	if out := get("?format=vX.Y.Z"); !out.Valid || out.Hint != nil {
		t.Errorf("vX.Y.Z: %+v", out)
	}
	if out := get("?format=X-Y"); out.Valid || out.Error == "" {
		t.Errorf("invalid: %+v", out)
	}
	// The release dialog shows it too.
	if info := rr.info(t, ""); info.Hint == nil {
		t.Error("no hint in the release info")
	}
}

// create_release is a board tool: the chat creates a release through the
// same backend.
func TestCreateReleaseTool(t *testing.T) {
	rr := newReleaseRepo(t, "release-X.Y.Z")
	rr.commit(t, "Ship it", "", true)
	token, release := rr.s.Board.Register(chatCaller(t, rr.s), rr.p.ID)
	defer release()
	text, isErr := callResult(t, rpc(rr.h, token, "tools/call", `{"name":"create_release","arguments":{"bump":"patch"}}`))
	if isErr || !strings.Contains(text, `"tag": "release-0.1.0"`) && !strings.Contains(text, `"tag":"release-0.1.0"`) {
		t.Fatalf("create_release: %s", text)
	}
	if got := rgit(t, rr.origin, "tag", "-l"); got != "release-0.1.0" {
		t.Errorf("origin's tags %q", got)
	}
	if _, isErr := callResult(t, rpc(rr.h, token, "tools/call", `{"name":"create_release","arguments":{}}`)); !isErr {
		t.Error("no version or bump accepted")
	}
}

func TestRunnerGuardAllowsReleases(t *testing.T) {
	for _, p := range []string{"GET /api/projects/{pid}/release", "POST /api/projects/{pid}/tags", "GET /api/projects/{pid}/tag-format"} {
		if !hubRoutes[p] {
			t.Errorf("%s isn't offered to the hub", p)
		}
	}
}
