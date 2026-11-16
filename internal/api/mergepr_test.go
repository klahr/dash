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
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/testbin"
)

// mergePRServer is a server with a project in pull request mode and a task
// in review whose pull request #7 is open on GitHub. Its gh logs its calls
// (returned) and runs ghMerge for `pr merge`; `repo view` says the
// repository squashes, `api user` that it is signed in as octocat.
func mergePRServer(t *testing.T, ghMerge string) (*Server, model.Project, model.Todo, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	s, _, _ := boardServer(t)
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	testbin.Write(t, filepath.Join(dir, "gh"), "#!/bin/sh\necho \"$*\" >> "+calls+"\ncase \"$1 $2\" in\n"+
		"\"pr merge\") "+ghMerge+" ;;\n"+
		"\"repo view\") echo '{\"viewerDefaultMergeMethod\":\"SQUASH\",\"squashMergeAllowed\":true}' ;;\n"+
		"\"api user\") echo octocat ;;\n"+
		"\"pr view\") echo '{\"state\":\"OPEN\",\"headRefOid\":\"\"}' ;;\n"+
		"esac\n")
	s.Git = gitops.New(s.Hub)
	s.Git.GhBin = filepath.Join(dir, "gh")
	s.Merges = queue.NewMergeQueue(raw(s), s.Git, s.Hub, &queue.TodoLog{Root: t.TempDir(), Pub: s.Hub}, 4)
	p, err := raw(s).CreateProject(model.Project{Name: "gh", OrgID: testOwner(t, s).Org, Path: repo, DefaultBranch: "main",
		MergeMode: model.MergePR, MergeModeSet: true})
	if err != nil {
		t.Fatal(err)
	}
	td := reviewTask(t, s, p, &model.PullRequest{Number: 7, URL: "https://github.com/o/r/pull/7", State: "OPEN", Waits: model.PRWaitMerge})
	return s, p, td, calls
}

func mergeReq(t *testing.T, s *Server, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("POST", "/api/todos/"+id+"/merge-pr", strings.NewReader(body))))
	return w
}

func ghLog(calls string) string {
	b, _ := os.ReadFile(calls)
	return string(b)
}

// Merge pull request merges it on GitHub with the method asked for, else
// the project's, else the repository's default, as the gh account here.
func TestMergePREndpoint(t *testing.T) {
	parallel(t)

	s, p, td, calls := mergePRServer(t, `echo "✓ Merged pull request o/r#7"`)
	w := mergeReq(t, s, td.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", w.Code, w.Body)
	}
	var res queue.Merged
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || res != (queue.Merged{Number: 7, Method: "squash", As: "octocat"}) {
		t.Fatalf("answer %s (%v)", w.Body, err)
	}
	if got := ghLog(calls); !strings.Contains(got, "pr merge 7 --squash") || strings.Contains(got, "--admin") {
		t.Fatalf("gh calls:\n%s", got)
	}

	if w := mergeReq(t, s, td.ID, `{"method":"rebase"}`); w.Code != http.StatusOK || !strings.Contains(ghLog(calls), "pr merge 7 --rebase") {
		t.Fatalf("merge with rebase: %d %s\n%s", w.Code, w.Body, ghLog(calls))
	}
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.MergeMethod = gitops.MergeCommit; return nil }); err != nil {
		t.Fatal(err)
	}
	if w := mergeReq(t, s, td.ID, `{}`); w.Code != http.StatusOK || !strings.Contains(ghLog(calls), "pr merge 7 --merge") {
		t.Fatalf("merge with the project's method: %d %s\n%s", w.Code, w.Body, ghLog(calls))
	}
	if w := mergeReq(t, s, td.ID, `{"method":"admin"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown method: %d %s", w.Code, w.Body)
	}
}

// A task without an open pull request has nothing to merge, and gh isn't
// asked.
func TestMergePRNeedsAnOpenPullRequest(t *testing.T) {
	parallel(t)

	s, p, _, calls := mergePRServer(t, `echo merged`)
	for _, pr := range []*model.PullRequest{nil, {Number: 8, URL: "https://github.com/o/r/pull/8", State: "CLOSED"}} {
		td := reviewTask(t, s, p, pr)
		if w := mergeReq(t, s, td.ID, ""); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no open pull request") {
			t.Fatalf("PR %+v: %d %s", pr, w.Code, w.Body)
		}
	}
	if got := ghLog(calls); strings.Contains(got, "pr merge") {
		t.Fatalf("gh asked to merge:\n%s", got)
	}
}

// GitHub refusing the merge (branch protection, a conflict…) shows its
// message, and the task stays as it was.
func TestMergePRRefusedByGitHub(t *testing.T) {
	parallel(t)

	s, _, td, _ := mergePRServer(t, `echo "X Pull request o/r#7 is not mergeable: the base branch policy prohibits the merge." >&2; exit 1`)
	w := mergeReq(t, s, td.ID, "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "the base branch policy prohibits the merge") {
		t.Fatalf("refused merge: %d %s", w.Code, w.Body)
	}
	if x, _ := raw(s).GetTodo(td.ID); x.Status != model.StatusReview || x.PR.State != "OPEN" || x.Error != "" {
		t.Fatalf("the task changed: %s %q %+v", x.Status, x.Error, *x.PR)
	}
}

// A merge queue: the pull request joins it, and the task waits as before.
func TestMergePRQueued(t *testing.T) {
	parallel(t)

	s, _, td, _ := mergePRServer(t, `echo "✓ Pull request o/r#7 will be added to the merge queue for main when ready"`)
	w := mergeReq(t, s, td.ID, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"queued":true`) {
		t.Fatalf("queued merge: %d %s", w.Code, w.Body)
	}
	if x, _ := raw(s).GetTodo(td.ID); x.Status != model.StatusReview || x.PR.State != "OPEN" {
		t.Fatalf("a queued pull request changed the task: %s %+v", x.Status, *x.PR)
	}
}

// The project setting takes the three methods and "" (the repository's
// default), and nothing else.
func TestMergeMethodSetting(t *testing.T) {
	parallel(t)

	s, p, _, _ := mergePRServer(t, `true`)
	patch := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("PATCH", "/api/projects/"+p.ID, strings.NewReader(body))))
		return w
	}
	if w := patch(`{"merge_method":"rebase"}`); w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	if x, _ := raw(s).GetProject(p.ID); x.MergeMethod != "rebase" {
		t.Fatalf("merge method %q", x.MergeMethod)
	}
	if w := patch(`{"merge_method":"fast-forward"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad method: %d %s", w.Code, w.Body)
	}
	if w := patch(`{"merge_method":""}`); w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body)
	}
	if x, _ := raw(s).GetProject(p.ID); x.MergeMethod != "" {
		t.Fatalf("merge method %q", x.MergeMethod)
	}
}
