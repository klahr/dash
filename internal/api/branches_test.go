package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func TestBranchSwitcher(t *testing.T) {
	parallel(t)

	rr := newReleaseRepo(t, "")
	rgit(t, rr.repo, "push", "-q", "origin", "main:refs/heads/from-origin")

	var list branchList
	w := rr.call(t, "GET", "/branches?fetch=1", "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil {
		t.Fatalf("branches: %d %s", w.Code, w.Body)
	}
	if list.Default != "main" || list.Current != "main" || len(list.Branches) != 2 || list.Branches[0].Name != "from-origin" || list.Branches[0].Local {
		t.Fatalf("branches: %+v", list)
	}

	// Creating a branch makes it from the current one and pushes it.
	var p model.Project
	w = rr.call(t, "PUT", "/branch", `{"branch":"feature","create":true}`)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.WorkBranch != "feature" || p.Base() != "feature" {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if out := rgit(t, rr.origin, "branch", "--list", "feature"); !strings.Contains(out, "feature") {
		t.Error("feature wasn't pushed to origin")
	}
	if w = rr.call(t, "GET", "/graph", ""); !strings.Contains(w.Body.String(), `"branch":"feature"`) {
		t.Errorf("graph: %s", w.Body)
	}

	// A branch only origin has is checked out from it; an unknown one isn't made without create.
	if w = rr.call(t, "PUT", "/branch", `{"branch":"from-origin"}`); w.Code != http.StatusOK {
		t.Fatalf("from origin: %d %s", w.Code, w.Body)
	}
	if w = rr.call(t, "PUT", "/branch", `{"branch":"nope"}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown branch: %d %s", w.Code, w.Body)
	}
	if w = rr.call(t, "PUT", "/branch", `{"branch":"dash/todo-1","create":true}`); w.Code != http.StatusBadRequest {
		t.Errorf("task branch: %d %s", w.Code, w.Body)
	}

	// Back on the default branch there's nothing to open a pull request for.
	w = rr.call(t, "PUT", "/branch", `{"branch":"main"}`)
	p = model.Project{}
	if json.Unmarshal(w.Body.Bytes(), &p); w.Code != http.StatusOK || p.WorkBranch != "" || p.Base() != "main" {
		t.Fatalf("back to main: %d %s", w.Code, w.Body)
	}
	if w = rr.call(t, "POST", "/branch/pr", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("pr on main: %d %s", w.Code, w.Body)
	}
}
