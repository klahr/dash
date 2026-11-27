package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/testbin"
)

// waitUntil polls until ok, or fails saying what never happened.
func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
	}
}

// A comment added in Changes on a task whose pull request is open goes to
// GitHub right away, naming its author; editing and deleting it does the
// same there; a failed post shows on the comment and Retry posts it; Send
// rework asks Claude to answer the posted ones and doesn't post them again.
// A task without a pull request keeps its comments in Dash only.
func TestCommentsPostedToPullRequest(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	s, _, _ := boardServer(t)
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	dir := t.TempDir()
	calls, down := filepath.Join(dir, "calls"), filepath.Join(dir, "down")
	testbin.Write(t, filepath.Join(dir, "gh"), "#!/bin/sh\necho \"$*\" >> "+calls+"\n[ -f "+down+" ] && exit 1\ncase \"$*\" in\n"+
		"\"pr view\"*) echo '{\"state\":\"OPEN\",\"headRefOid\":\"abc\"}' ;;\n"+
		"*\"-X POST repos/{owner}/{repo}/pulls/7/comments \"*) echo '{\"id\":501,\"pull_request_review_id\":601,\"html_url\":\"https://github.com/o/r/pull/7#discussion_r501\"}' ;;\n"+
		"esac\n")
	s.Git = gitops.New(s.Hub)
	s.Git.GhBin = filepath.Join(dir, "gh")
	s.Merges = queue.NewMergeQueue(raw(s), s.Git, s.Hub, &queue.TodoLog{Root: t.TempDir(), Pub: s.Hub}, 4)
	p, err := raw(s).CreateProject(model.Project{Name: "gh", OrgID: testOwner(t, s).Org, Path: repo, DefaultBranch: "main",
		MergeMode: model.MergePR, MergeModeSet: true})
	if err != nil {
		t.Fatal(err)
	}
	td := reviewTask(t, s, p, &model.PullRequest{Number: 7, URL: "https://github.com/o/r/pull/7", State: "OPEN"})
	h := asOwnerHandler(t, s)
	base := "/api/todos/" + td.ID + "/comments"
	add := func(id, body string) model.Todo {
		t.Helper()
		w := do(t, h, "POST", "/api/todos/"+id+"/comments", body)
		if w.Code != http.StatusOK {
			t.Fatalf("add: %d %s", w.Code, w.Body)
		}
		var got model.Todo
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	comment := func(i int) model.ReviewComment {
		x, _ := raw(s).GetTodo(td.ID)
		if i >= len(x.Comments) {
			return model.ReviewComment{}
		}
		return x.Comments[i]
	}

	got := add(td.ID, `{"file":"a.go","line":3,"end_line":4,"code":"+x","text":"Why?"}`)
	if c := got.Comments[0]; c.Author != "u" || c.GitHub == nil || c.GitHub.PR != 7 || c.GitHub.Posted() {
		t.Fatalf("answered with %+v, want it being posted", c)
	}
	waitUntil(t, "posted", func() bool { return comment(0).GitHub.Posted() })
	if g := comment(0).GitHub; g.ID != 501 || g.URL != "https://github.com/o/r/pull/7#discussion_r501" {
		t.Fatalf("posted as %+v", g)
	}
	if log := ghLog(calls); !strings.Contains(log, "-f body=<sub>From **u** in Dash</sub>\n\nWhy?") ||
		!strings.Contains(log, "-f commit_id=abc -f path=a.go -F line=4 -f side=RIGHT -F start_line=3") {
		t.Fatalf("gh calls:\n%s", log)
	}

	cid := comment(0).ID
	if w := do(t, h, "PATCH", base+"/"+cid, `{"text":"Make it a constant."}`); w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	waitUntil(t, "edited on GitHub", func() bool {
		return strings.Contains(ghLog(calls), "-X PATCH repos/{owner}/{repo}/pulls/comments/501 -f body=<sub>From **u** in Dash</sub>\n\nMake it a constant.")
	})

	// GitHub down: the comment is kept, marked, and Retry posts it.
	os.WriteFile(down, nil, 0o644)
	add(td.ID, `{"file":"b.go","line":1,"text":"Later."}`)
	waitUntil(t, "marked not posted", func() bool { g := comment(1).GitHub; return g != nil && g.Error != "" })
	cid2 := comment(1).ID
	if w := do(t, h, "POST", base+"/"+cid2+"/github", ""); w.Code == http.StatusOK {
		t.Fatalf("retry with GitHub down: %d", w.Code)
	}
	os.Remove(down)
	if w := do(t, h, "POST", base+"/"+cid2+"/github", ""); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}
	if g := comment(1).GitHub; !g.Posted() || g.Error != "" {
		t.Fatalf("after retry: %+v", g)
	}

	// Deleted in Dash: deleted on GitHub.
	if w := do(t, h, "DELETE", base+"/"+cid2, ""); w.Code != http.StatusOK {
		t.Fatalf("delete: %d", w.Code)
	}
	waitUntil(t, "deleted on GitHub", func() bool {
		return strings.Contains(ghLog(calls), "-X DELETE repos/{owner}/{repo}/pulls/comments/501")
	})

	// Send rework: one note for Claude, asking it to answer the posted
	// comment; nothing posted again.
	before := strings.Count(ghLog(calls), "-X POST")
	if w := do(t, h, "POST", "/api/todos/"+td.ID+"/rework", `{"comments":true}`); w.Code != http.StatusOK {
		t.Fatalf("rework: %d %s", w.Code, w.Body)
	}
	x, _ := raw(s).GetTodo(td.ID)
	if len(x.Comments) != 0 || len(x.Feedback) != 1 || !strings.Contains(x.Feedback[0], "### 1. a.go, lines 3–4") ||
		!strings.Contains(x.Feedback[0], "```dash-review") {
		t.Fatalf("after rework: comments %v, feedback %q", x.Comments, x.Feedback)
	}
	if r := x.PR.Reply; r == nil || !r.Dash || len(r.Items) != 1 || r.Items[0].Thread != 501 || r.Items[0].Author != "u" {
		t.Fatalf("reply owed: %+v", x.PR.Reply)
	}
	if n := strings.Count(ghLog(calls), "-X POST"); n != before {
		t.Fatalf("rework posted to GitHub:\n%s", ghLog(calls))
	}

	// No pull request: as before, in Dash only.
	other := reviewTask(t, s, p, nil)
	plain := add(other.ID, `{"file":"a.go","line":1,"text":"Fine."}`)
	if plain.Comments[0].GitHub != nil {
		t.Fatalf("a task without a pull request: %+v", plain.Comments[0].GitHub)
	}
	if w := do(t, h, "POST", "/api/todos/"+other.ID+"/comments/"+plain.Comments[0].ID+"/github", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("retry without a pull request: %d", w.Code)
	}
}
