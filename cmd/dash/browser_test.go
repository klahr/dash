package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/klahr/dash/internal/model"
)

// The web client's main flows, clicked through in headless Chrome against
// a real hub whose work runs on a real runner with testdata/fakeclaude for
// Claude Code: sign in, create a task and watch it run, comment on its diff
// and send it back for rework, approve it and see it merged into origin,
// drag a draft into the queue, and allow a command a task asks permission
// for.
//
// It needs Chrome or Chromium: $DASH_TEST_CHROME, or one chromedp finds on
// $PATH. Without one it is skipped, unless $DASH_TEST_BROWSER=require (as
// in CI), when it fails instead.
func TestBrowserMainFlows(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	b := newBrowser(t, chrome)
	b.signIn(h)

	// The sidebar lists the runner with its tasks running of its slots, and
	// opens its page in the Runners sheet.
	b.waitJS("the runner in the sidebar", `/^\d+\/\d+$/.test(document.querySelector("#side-runner-list .side-runner .side-runner-count")?.textContent || "")`)
	b.until("open the runner's page", `document.querySelector("#runners-sheet").open && !!document.querySelector("#runners-main .current .runner-title")`,
		chromedp.Click(chromedp.CSS(`#side-runner-list .side-runner:not(.side-runner-more)`)))
	b.until("close the Runners sheet", `!document.querySelector("#runners-sheet").open`, chromedp.KeyEvent(kb.Escape))

	// Create a task from the quick sheet, queued right away.
	// The sheet's clicks retry until they take: under load a click can land
	// while the page is still settling and miss. Each has its effect at once
	// (the sheet opens, the box ticks, the sheet closes as the task is sent),
	// so a click is only repeated when the one before it didn't land.
	b.until("open the new task sheet", `document.querySelector("#quick-sheet").open`, chromedp.Click(chromedp.ID("new-todo")))
	b.do("wait for the sheet", chromedp.WaitVisible(chromedp.CSS(`#quick-sheet[open] #quick-rows .quick-row`)))
	b.waitJS("the task's row focused", `document.activeElement?.matches("#quick-rows .quick-row")`)
	b.do("write the task", chromedp.SendKeys(chromedp.CSS(`#quick-rows .quick-row`), "Add a done file"))
	b.waitJS("the task written", `document.querySelector("#quick-rows .quick-row").value === "Add a done file"`)
	// A new profile: unticked.
	b.until("tick Queue right away", `document.querySelector("#quick-queue").checked`, chromedp.Click(chromedp.CSS(`label.toggle:has(#quick-queue) span`)))
	b.waitJS("the Queue task button", `document.querySelector("#quick-submit-label").textContent === "Queue task"`)
	// The sheet closes as the task is sent; a failed send opens it again with
	// the text kept, so a repeated click sends the task again.
	b.until("submit the task and the sheet to close", `!document.querySelector("#quick-sheet").open`,
		chromedp.Click(chromedp.CSS(`#quick-form button[value=add]:not(:disabled)`)))
	first := waitTodo(t, api, pid, "Add a done file")

	// See it run: it lands in Review, and its timeline shows the agent's work.
	b.waitCard(first, "review")
	b.openTask(first)
	b.waitJS("the agent's output in the timeline", `document.querySelector("#d-chat-log")?.textContent.includes("Working on: Add a done file")`)

	// Review: comment on a line of the diff, and send the comment for rework.
	b.do("show the changes", chromedp.Click(chromedp.CSS(`.pane-tabs button[data-pane=diff]`)))
	b.waitJS("the diff", `document.querySelector("#d-diff")?.textContent.includes("done-by-runner.txt")`)
	b.clickText(`#d-diff details.diff-file > summary`, "done-by-runner.txt")
	// From the keyboard: each line's comment button opens the box on Enter.
	b.do("comment on a line",
		chromedp.Focus(chromedp.CSS(`#d-diff .diff-lines > div[data-ln][data-file="done-by-runner.txt"] button.ln-comment`)),
		chromedp.KeyEvent("\r"),
		chromedp.WaitVisible(chromedp.CSS(`#d-diff .comment-box textarea`)),
	)
	b.waitJS("the comment box focused", `document.activeElement?.matches("#d-diff .comment-box textarea")`)
	b.do("write the comment",
		chromedp.SendKeys(chromedp.CSS(`#d-diff .comment-box textarea`), "Say it louder"),
		chromedp.Click(chromedp.CSS(`#d-diff .comment-box button.primary`)),
	)
	b.clickText("#d-actions button", "Send 1 comment")
	b.do("add a note",
		chromedp.WaitVisible(chromedp.ID("ask-input")),
		chromedp.SendKeys(chromedp.ID("ask-input"), "And add a rework file"),
	)
	b.waitJS("the note written", `document.querySelector("#ask-input").value === "And add a rework file"`)
	b.until("send it back", `!document.querySelector("#ask-sheet").open`, chromedp.Click(chromedp.ID("ask-ok")))
	waitStatus(t, api, "/api/todos/"+first, "review", func(td todo) bool { return td.Status == "review" && td.Runs > 1 })
	if _, diff := api.try("GET", "/api/todos/"+first+"/diff", nil); !strings.Contains(diff, "rework-by-runner.txt") ||
		!strings.Contains(diff, "Say it louder") || !strings.Contains(diff, "And add a rework file") {
		t.Fatalf("the rework didn't get the comment and the note:\n%s", diff)
	}
	b.waitJS("the reworked diff", `document.querySelector("#d-diff")?.textContent.includes("rework-by-runner.txt")`)

	// Edit a file of the change before approving: committed on the task's
	// branch on the runner, and merged with the rest.
	b.until("open the file to edit", `document.querySelector("#diff-edit-sheet").open`,
		chromedp.Click(chromedp.CSS(`#d-diff details.diff-file > summary button.diff-edit[aria-label="Edit done-by-runner.txt"]`)))
	b.do("edit it",
		chromedp.WaitVisible(chromedp.CSS(`#diff-edit-sheet .cm-content`)),
		chromedp.Click(chromedp.CSS(`#diff-edit-sheet .cm-content`)),
		chromedp.KeyEvent("Edited in review\r"),
	)
	b.waitJS("the edit typed", `document.querySelector("#diff-edit-text").value.includes("Edited in review")`)
	b.until("save the edit", `!document.querySelector("#diff-edit-sheet").open`, chromedp.Click(chromedp.CSS(`#diff-edit-save:not(:disabled)`)))
	b.waitJS("the edit in the diff", `document.querySelector("#d-diff")?.textContent.includes("Edited in review")`)

	// Approve and merge: the change reaches origin's main.
	b.clickText("#d-actions button", "Approve")
	waitStatus(t, api, "/api/todos/"+first, "merged", nil)
	b.waitCard(first, "done")
	files := run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "ls-tree", "--name-only", "main")
	if !strings.Contains(files, "done-by-runner.txt") || !strings.Contains(files, "rework-by-runner.txt") {
		t.Fatalf("origin's main after the merge: %s", files)
	}
	if got := run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "show", "main:done-by-runner.txt"); !strings.Contains(got, "Edited in review") {
		t.Fatalf("the edit made in review didn't land: %q", got)
	}
	b.do("close the task", chromedp.Click(chromedp.ID("d-close")))

	// Drag and drop: a draft dragged to Queued runs.
	var second struct{ ID string }
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Dragged", "prompt": "Run when dragged"},
		http.StatusCreated, &second)
	b.waitCard(second.ID, "draft")
	b.drag(second.ID, "draft", "queued")
	waitStatus(t, api, "/api/todos/"+second.ID, "review", nil)
	b.waitCard(second.ID, "review")

	// Permission approval: a task asks to run a command; allow it on the
	// board. In a project of the runner's own (the platform admin sees the
	// runner's projects, through the hub): the hub's projects can't show
	// their runners' permission requests yet.
	const peer = "/peer/laptop"
	var local struct{ ID string }
	api.must("POST", peer+"/api/projects", map[string]any{"name": "local"}, http.StatusCreated, &local)
	b.clickTextUntil(`#side-project-list .side-project`, "local", `document.querySelector("#project-name")?.textContent === "local"`)
	var third struct{ ID string }
	api.must("POST", peer+"/api/projects/"+local.ID+"/todos", map[string]any{"title": "Needs a command", "prompt": "ask permission to run a command", "enqueue": true},
		http.StatusCreated, &third)
	b.waitJS("the permission request", `[...document.querySelectorAll("#perm-banner .perm")].some((p) => p.textContent.includes("echo fake-permission-check"))`)
	b.do("allow the command", chromedp.Click(chromedp.CSS(`#perm-banner .perm button.allow`)))
	waitStatus(t, api, peer+"/api/todos/"+third.ID, "review", nil)
	b.waitCard(third.ID, "review")
	b.waitJS("the permission request to go", `!document.querySelector("#perm-banner .perm")`)
	if _, diff := api.try("GET", peer+"/api/todos/"+third.ID+"/diff", nil); !strings.Contains(diff, "permission.txt") || !strings.Contains(diff, "+allow") {
		t.Fatalf("the run wasn't allowed its command:\n%s", diff)
	}
}

// The checklist (TODO.md) of a hub's project, whose checkout is on its
// runner: create TODO.md from the empty state, add items, tick one, move
// one to a draft task and drag one to the top. Each change reaches origin's main as a commit, with
// no task for it.
func TestBrowserChecklist(t *testing.T) {
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	b := newBrowser(t, chrome)
	b.signIn(h)
	onOrigin := func(what string, ok func(string) bool) {
		t.Helper()
		waitFor(t, nil, what, func() bool {
			out, err := exec.Command("git", "--git-dir", h.origin, "show", "main:TODO.md").CombinedOutput()
			return err == nil && ok(string(out))
		})
	}

	b.until("open the checklist", `document.querySelector("#view-checklist.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=checklist]`)))
	b.waitJS("the empty state", `!document.querySelector("#cl-empty").hidden`)
	b.until("create TODO.md", `document.querySelector("#ask-sheet").open`, chromedp.Click(chromedp.ID("cl-create")))
	b.do("write the first item", chromedp.SendKeys(chromedp.ID("ask-input"), "First thing"))
	b.waitJS("the item written", `document.querySelector("#ask-input").value === "First thing"`)
	b.until("save it", `!document.querySelector("#ask-sheet").open`, chromedp.Click(chromedp.ID("ask-ok")))
	b.waitJS("the saved item", `[...document.querySelectorAll("#cl-body .cl-item:not(.pending)")].some((e) => e.textContent.includes("First thing"))`)
	onOrigin("TODO.md created on origin", func(s string) bool { return strings.Contains(s, "- [ ] First thing") })

	// Two quick additions are saved together.
	for _, item := range []string{"Second thing", "Third thing"} {
		b.do("add "+item, chromedp.SendKeys(chromedp.ID("cl-add-text"), item))
		b.waitJS(item+" written", fmt.Sprintf(`document.querySelector("#cl-add-text").value === %q`, item))
		b.do("submit "+item, chromedp.Click(chromedp.CSS(`#cl-add button[type=submit]`)))
	}
	b.waitJS("both saved", `document.querySelectorAll("#cl-body .cl-item:not(.pending)").length === 3`)
	onOrigin("both items on origin", func(s string) bool {
		return strings.Contains(s, "- [ ] Second thing\n- [ ] Third thing")
	})

	// Tick one.
	b.clickIn("#cl-body .cl-item", "First thing", ".cl-check input")
	onOrigin("the tick on origin", func(s string) bool { return strings.Contains(s, "- [x] First thing") })
	b.waitJS("the tick saved", `!document.querySelector("#cl-body .cl-item.pending") && document.querySelector("#cl-body .cl-item.done")?.textContent.includes("First thing")`)

	// Move one to a draft.
	b.clickIn("#cl-body .cl-item", "Second thing", ".cl-move button")
	b.waitJS("the item gone", `![...document.querySelectorAll("#cl-body .cl-item")].some((e) => e.textContent.includes("Second thing"))`)
	b.waitJS("the toast", `[...document.querySelectorAll("#toasts .toast")].some((e) => e.textContent.includes("Moved to draft"))`)
	onOrigin("the item gone from origin", func(s string) bool { return !strings.Contains(s, "Second thing") && strings.Contains(s, "Third thing") })
	var todos []todo
	api.must("GET", "/api/projects/"+pid+"/todos", nil, http.StatusOK, &todos)
	if len(todos) != 1 || todos[0].Title != "Second thing" || todos[0].Status != "draft" {
		t.Fatalf("tasks: %+v", todos)
	}

	// A change made elsewhere (another tab, the board's MCP tools) shows
	// up by itself: the runner commits it, the hub hears of it in the
	// runner's report and tells the page, which reads TODO.md again.
	api.must("POST", "/api/projects/"+pid+"/checklist", map[string]any{
		"ops": []map[string]any{{"op": "add", "text": "Made elsewhere"}},
	}, http.StatusOK, nil)
	b.waitJS("the change made elsewhere", `[...document.querySelectorAll("#cl-body .cl-item")].some((e) => e.textContent.includes("Made elsewhere"))`)
	b.waitJS("no Reload button", `!document.querySelector("#cl-reload")`)

	// Drag an item by its grip onto the section's heading: to the top.
	b.waitJS("the item to drag", markText("#cl-body .cl-item", "Made elsewhere"))
	b.do("drag it to the top", chromedp.DragAndDrop(chromedp.CSS(`[data-e2e-click] .cl-grip`), chromedp.CSS(`#cl-body .cl-section > h3`)))
	onOrigin("the item on top on origin", func(s string) bool { return strings.Contains(s, "- [ ] Made elsewhere\n- [x] First thing") })
	b.waitJS("on top", `document.querySelector("#cl-body .cl-item:not(.pending)")?.textContent.includes("Made elsewhere")`)

	// And with keys: gg to the first item, - moves it down a place.
	b.do("move it down", chromedp.KeyEvent("gg-"))
	onOrigin("the item moved down on origin", func(s string) bool { return strings.Contains(s, "- [x] First thing\n- [ ] Made elsewhere") })
}

// Split a large task: from the full task form, Claude (on the user's
// runner) proposes tasks; one is edited, another added, and saving makes
// them drafts, the second waiting for the first.
func TestBrowserSplitTask(t *testing.T) {
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	b := newBrowser(t, chrome)
	b.signIn(h)

	b.until("open the new task sheet", `document.querySelector("#quick-sheet").open`, chromedp.Click(chromedp.ID("new-todo")))
	b.do("write the task", chromedp.SendKeys(chromedp.CSS(`#quick-sheet[open] #quick-rows .quick-row`), "Add accounts"))
	b.waitJS("the task written", `document.querySelector("#quick-rows .quick-row").value === "Add accounts"`)
	b.until("open the full form", `document.querySelector("#todo-sheet").open`, chromedp.Click(chromedp.ID("quick-more")))
	b.waitJS("the instructions carried over", `document.querySelector("#todo-form").prompt.value === "Add accounts"`)
	b.until("split it", `document.querySelector("#split-sheet").open`, chromedp.Click(chromedp.CSS(`#todo-split:not(:disabled)`)))
	b.waitJS("Claude's two tasks", `document.querySelectorAll("#split-parts .split-part").length === 2 &&
		[...document.querySelectorAll("#split-parts .split-part:nth-child(2) .split-dep[aria-pressed=true]")].map((e) => e.textContent).join() === "1"`)
	b.do("edit the first title", chromedp.SendKeys(chromedp.CSS(`#split-parts .split-part:first-child .split-title`), " first"))
	b.waitJS("the title edited", `document.querySelector("#split-parts .split-title").value === "Add the users table first"`)
	b.do("add a task", chromedp.Click(chromedp.ID("split-add")))
	b.do("write it", chromedp.SendKeys(chromedp.CSS(`#split-parts .split-part:nth-child(3) .split-prompt`), "Document accounts"))
	b.waitJS("three tasks", `document.querySelector("#split-draft-label").textContent === "Save 3 drafts"`)
	b.until("save them", `!document.querySelector("#todo-sheet").open`, chromedp.Click(chromedp.CSS(`#split-form button[value=draft]`)))
	b.waitJS("the toast", `[...document.querySelectorAll("#toasts .toast")].some((e) => e.textContent.includes("Saved 3 drafts"))`)

	var todos []struct {
		ID, Title, Prompt, Status string
		DependsOn                 []string `json:"depends_on"`
	}
	api.must("GET", "/api/projects/"+pid+"/todos", nil, http.StatusOK, &todos)
	byPrompt := map[string]int{}
	for i, td := range todos {
		byPrompt[td.Prompt] = i
		if td.Status != "draft" {
			t.Errorf("task %q is %s, want a draft", td.Prompt, td.Status)
		}
	}
	first, ok1 := byPrompt["Create the users table."]
	second, ok2 := byPrompt["Build the signup form on the users table."]
	_, ok3 := byPrompt["Document accounts"]
	if len(todos) != 3 || !ok1 || !ok2 || !ok3 {
		t.Fatalf("tasks after the split: %+v", todos)
	}
	if todos[first].Title != "Add the users table first" || !slices.Equal(todos[second].DependsOn, []string{todos[first].ID}) {
		t.Errorf("tasks after the split: %+v", todos)
	}
}

// A personal webhook, from the account menu: add one, send it a test and
// read its delivery history.
func TestBrowserPersonalWebhook(t *testing.T) {
	chrome := needChrome(t)
	got := make(chan string, 10)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Dash-Event")
	}))
	t.Cleanup(recv.Close)
	h := startRunnerHub(t)
	b := newBrowser(t, chrome)
	b.signIn(h)

	b.until("open the account menu", `document.querySelector("#foot-menu").open`, chromedp.Click(chromedp.CSS(`#foot-menu > summary`)))
	b.until("open Account at Editors", `document.querySelector("#account-sheet").open && document.querySelector("#account-sheet-section-editors").classList.contains("current")`, chromedp.Click(chromedp.ID("account-open")))
	b.until("pick My webhooks", `document.querySelector("#account-sheet-section-webhooks").classList.contains("current")`, chromedp.Click(chromedp.ID("account-sheet-rail-webhooks")))
	b.waitJS("the empty list", `document.querySelector("#me-webhooks").textContent.includes("No webhooks yet")`)
	b.until("add one", `document.querySelector("#webhook-sheet").open`, chromedp.Click(chromedp.ID("me-webhook-add")))
	b.waitJS("the personal form", `document.querySelector("#webhook-title").textContent === "Add personal webhook"`)
	b.do("fill it in",
		chromedp.Click(chromedp.CSS(`#webhook-form input[name=format][value=json]`)),
		chromedp.SendKeys(chromedp.CSS(`#webhook-form input[name=url]`), recv.URL+"/mine"),
		chromedp.SendKeys(chromedp.CSS(`#webhook-form input[name=name]`), "My receiver"),
	)
	b.until("save it", `!document.querySelector("#webhook-sheet").open`, chromedp.Click(chromedp.CSS(`#webhook-form button[value=save]`)))
	b.waitJS("the webhook and its secret", `document.querySelector("#me-webhooks").textContent.includes("My receiver") && !document.querySelector("#me-webhook-secret").hidden`)

	b.do("send a test", chromedp.Click(chromedp.CSS(`#me-webhooks .webhook-actions button`)))
	select {
	case ev := <-got:
		if ev != "webhook.test" {
			t.Fatalf("received %q", ev)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no test delivery")
	}
	b.waitJS("its status", `document.querySelector("#me-webhooks .item-sub")?.textContent.includes("Test delivered")`)
	b.waitJS("the History button", markText("#me-webhooks .webhook-actions button", "History"))
	b.do("open its history", chromedp.Click(chromedp.CSS(`[data-e2e-click]`)))
	b.waitJS("the test delivery in it", `[...document.querySelectorAll("#me-webhooks .webhook-history li")].some((e) => e.textContent.includes("Test") && e.textContent.includes("HTTP 200"))`)

	// It isn't the project's.
	var hooks struct {
		Webhooks []json.RawMessage `json:"webhooks"`
	}
	h.api.must("GET", "/api/projects/"+h.project.ID+"/webhooks", nil, http.StatusOK, &hooks)
	if len(hooks.Webhooks) != 0 {
		t.Fatalf("the project's webhooks: %s", hooks.Webhooks)
	}
}

// The History view's commit pane, on a project whose clone is on a runner:
// select a commit, open a changed file's diff, move with j/k and the arrow
// keys, follow a parent link and close the pane with Esc.
func TestBrowserHistoryCommitPane(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID

	// A second commit on origin, pulled into the runner's clone.
	work := filepath.Join(t.TempDir(), "work")
	run(t, "", h.gitEnv, "git", "clone", "-q", h.origin, work)
	if err := os.WriteFile(filepath.Join(work, "notes.txt"), []byte("hello history\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, h.gitEnv, "git", "add", ".")
	run(t, work, h.gitEnv, "git", "commit", "-q", "-m", "Add notes\n\nA body line.")
	run(t, work, h.gitEnv, "git", "push", "-q", "origin", "main")
	waitFor(t, nil, "the runner's clone to pull the commit", func() bool {
		api.try("POST", "/api/projects/"+pid+"/remote/pull", nil)
		code, body := api.try("GET", "/api/projects/"+pid+"/graph", nil)
		var g struct{ Commits []struct{ Subject string } }
		return code == http.StatusOK && json.Unmarshal([]byte(body), &g) == nil && len(g.Commits) == 2
	})

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open History", `document.querySelector("#view-history.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=history]`)))
	b.waitJS("both commits", `document.querySelectorAll("#graph li").length === 2`)

	title := func(s string) string {
		return fmt.Sprintf(`!document.querySelector("#commit-detail").hidden && document.querySelector("#cd-title").textContent === %q`, s)
	}
	selected := func(i int) string {
		return fmt.Sprintf(`document.querySelectorAll("#graph li")[%d].classList.contains("selected") && document.querySelectorAll("#graph li.selected").length === 1`, i)
	}
	b.until("select the newest commit", title("Add notes"), chromedp.Click(chromedp.CSS(`#graph li:first-child .graph-commit`)))
	b.waitJS("its row selected", selected(0))
	b.waitJS("its message and files", `document.querySelector("#cd-message").textContent === "A body line." && document.querySelector("#cd-files").textContent.includes("notes.txt")`)
	b.waitJS("the actions in the head", `[...document.querySelectorAll("#cd-actions button")].map((e) => e.textContent).join("|").includes("Copy SHA")`)
	// On a desktop the files are listed beside the diff, the first one selected.
	b.waitJS("the first file selected", `document.querySelector("#cd-files [role=option][aria-selected=true]")?.dataset.path === "notes.txt"`)
	b.waitJS("the file's diff", `document.querySelector("#cd-diff .diff-lines")?.textContent.includes("+hello history")`)
	// n steps through the files (just the one here) instead of opening the new-task sheet.
	b.do("press n", chromedp.KeyEvent("n"))
	b.waitJS("still the file, no sheet", `!document.querySelector("dialog[open]") && document.querySelector("#cd-files [aria-selected=true]")?.dataset.path === "notes.txt"`)

	// j moves to the older commit (focus is on the row), k and ArrowDown back and forth.
	b.do("press j", chromedp.KeyEvent("j"))
	b.waitJS("the older commit shown", title("first")+` && document.querySelector("#cd-files").textContent.includes("README.md")`)
	b.waitJS("its first file's diff", `document.querySelector("#cd-diff").dataset.path === "README.md" && document.querySelector("#cd-diff .diff-lines div")`)
	b.waitJS("its row selected", selected(1))
	b.do("press k", chromedp.KeyEvent("k"))
	b.waitJS("the newer commit again", title("Add notes")+` && document.querySelector("#cd-files").textContent.includes("notes.txt")`)
	b.do("press ArrowDown", chromedp.KeyEvent(kb.ArrowDown))
	b.waitJS("the older commit again", title("first"))
	b.do("press ArrowUp", chromedp.KeyEvent(kb.ArrowUp))
	b.waitJS("back to the newest", title("Add notes")+` && document.querySelector("#cd-facts").textContent.includes("Parent")`)

	// The parent link selects the parent.
	b.do("follow the parent", chromedp.Click(chromedp.CSS(`#cd-facts .commit-parent`)))
	b.waitJS("the parent shown", title("first")+" && "+selected(1))

	// The selection survives a refresh of the graph.
	b.do("refresh the graph", chromedp.Click(chromedp.CSS(`.nav > button[data-view=history]`)))
	b.waitJS("still selected", selected(1))

	// Esc closes the pane and clears the selection.
	// (Focus off the History nav button, whose list takes Esc for itself.)
	if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`document.activeElement?.blur(), true`)); err != nil {
		t.Fatal(err)
	}
	b.do("press Esc", chromedp.KeyEvent(kb.Escape))
	b.waitJS("the pane closed", `document.querySelector("#commit-detail").hidden && !document.querySelector("#graph li.selected")`)
}

// A CLI project's terminal preview: open it on a task in review, type in
// it, and send the task back with the terminal's transcript as the rework
// notes. In a project of the runner's own, whose runner opens terminals for
// the hub: the preview runs there, reached through the hub.
func TestBrowserTerminalPreview(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api := h.api
	b := newBrowser(t, chrome)
	b.signIn(h)

	const peer = "/peer/laptop"
	var cli struct{ ID string }
	api.must("POST", peer+"/api/projects", map[string]any{"name": "cli"}, http.StatusCreated, &cli)
	api.must("PATCH", peer+"/api/projects/"+cli.ID, map[string]any{"preview_kind": "terminal"}, http.StatusOK, nil)
	var task struct{ ID string }
	api.must("POST", peer+"/api/projects/"+cli.ID+"/todos", map[string]any{"title": "CLI change", "prompt": "Change the CLI", "enqueue": true},
		http.StatusCreated, &task)
	waitStatus(t, api, peer+"/api/todos/"+task.ID, "review", nil)

	b.clickTextUntil(`#side-project-list .side-project`, "cli", `document.querySelector("#project-name")?.textContent === "cli"`)
	b.waitCard(task.ID, "review")
	b.openTask(task.ID)

	// The Preview button opens a terminal instead of a page; Draw is gone.
	b.clickText("#d-actions button", "Preview")
	b.waitJS("the terminal preview", `!document.querySelector("#pv-term").hidden && !!document.querySelector("#pv-term .xterm") && document.querySelector("#d-preview").dataset.state === "live"`)
	b.waitJS("no Draw button", `getComputedStyle(document.querySelector("#pv-draw-btn")).display === "none" && document.querySelector("#pv-iframe").hidden`)
	b.waitJS("the Live badge", `document.querySelector("#pv-state").textContent.includes("Live")`)
	b.waitJS("the Previews (1) button", `!document.querySelector("#previews-btn").hidden && document.querySelector("#previews-label").textContent.includes("(1)")`)

	// Type a command: its output shows in the terminal.
	rows := `[...document.querySelectorAll("#pv-term .xterm-rows > div")].map((r) => r.textContent.trimEnd())`
	b.waitJS("the shell's prompt", rows+`.some((r) => r.trim() !== "")`)
	// SendKeys' synthetic key and char events would each reach xterm.js as
	// input (typing every letter twice): insert the text, then press Enter.
	b.do("type echo hi",
		chromedp.Focus(chromedp.CSS(`#pv-term textarea.xterm-helper-textarea`)),
		chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
			if _, err := cdp.Call(ctx, t, input.InsertText, input.InsertTextParams{Text: "echo hi"}); err != nil {
				return err
			}
			for _, typ := range []input.DispatchKeyEventType{input.DispatchKeyEventTypeRawKeyDown, input.DispatchKeyEventTypeKeyUp} {
				if _, err := cdp.Call(ctx, t, input.DispatchKeyEvent, input.DispatchKeyEventParams{
					Type: typ, Key: "Enter", Code: "Enter", WindowsVirtualKeyCode: 13, NativeVirtualKeyCode: 13}); err != nil {
					return err
				}
			}
			return nil
		}))
	b.waitJS("hi in the terminal", rows+`.some((r) => r.trim() === "hi")`)

	// Send back with transcript: the terminal's text goes into the rework notes.
	b.do("send back with transcript", chromedp.Click(chromedp.ID("pv-transcript")))
	b.waitJS("the transcript in the notes", `document.querySelector("#ask-sheet").open && /echo hi/.test(document.querySelector("#ask-input").value) && /^hi$/m.test(document.querySelector("#ask-input").value)`)
	b.until("send it back", `!document.querySelector("#ask-sheet").open`, chromedp.Click(chromedp.ID("ask-ok")))
	waitStatus(t, api, peer+"/api/todos/"+task.ID, "review", func(td todo) bool { return td.Runs > 1 })
	if _, diff := api.try("GET", peer+"/api/todos/"+task.ID+"/diff", nil); !strings.Contains(diff, "echo hi") {
		t.Fatalf("the rework didn't get the transcript:\n%s", diff)
	}
}

// The chat pane's tabs: This project and General, plus the chats added with
// + in each project (each project its own), plus another project's chat
// while its reply streams or is unread.
func TestBrowserChatTabs(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api := h.api
	b := newBrowser(t, chrome)
	// Stale tabs from an older version (a shared list) and junk don't break the pane.
	b.do("open the hub", chromedp.Navigate(h.base+"/"))
	if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`localStorage.setItem("dash.chatTabs", '["laptop/x"]');
		localStorage.setItem("dash.chatPane", '{"open":false,"tab":"laptop/x","tabs":["laptop/x"],"projectTabs":{"a":"nope","b":[1,null,"b"],"c":["laptop/x"]}}'); true`)); err != nil {
		t.Fatal(err)
	}
	b.signIn(h)
	const peer = "/peer/laptop"
	for _, name := range []string{"lib", "web"} {
		api.must("POST", peer+"/api/projects", map[string]any{"name": name}, http.StatusCreated, nil)
	}
	b.do("reload", chromedp.Reload())
	b.waitJS("every project", `document.querySelectorAll("#side-project-list .side-project").length === 3`)
	b.clickTextUntil(`#side-project-list .side-project`, "app", `document.querySelector("#project-name")?.textContent === "app"`)

	tabs := func(want string) {
		b.t.Helper()
		b.waitJS("the tabs "+want, `[...document.querySelectorAll("#chat-tabs .chat-tab-name")].map((n) => n.textContent).join(",") === `+fmt.Sprintf("%q", want))
	}
	options := `[...document.querySelectorAll("#chat-tab-options button .project-name")].map((n) => n.textContent).join(",")`
	b.until("open the chat", `!document.querySelector("#chat-pane").hidden`, chromedp.Click(chromedp.ID("pc-open")))
	tabs("This project,General")
	if stored, _ := chromedp.Run(b.ctx, chromedp.Evaluate[string](`localStorage.getItem("dash.chatPane")`)); strings.Contains(stored, `"tabs"`) || strings.Contains(stored, "nope") {
		t.Fatalf("the old tabs are still stored: %s", stored)
	}

	// + lists the other projects, filters them, and adds the pick as a tab.
	b.until("open the + picker", `!document.querySelector("#chat-tab-picker").hidden`, chromedp.Click(chromedp.ID("chat-tab-add")))
	b.waitJS("the picker with lib and web", `!document.querySelector("#chat-tab-picker").hidden && `+options+` === "lib,web" && document.activeElement?.id === "chat-tab-search"`)
	b.do("search for we", chromedp.SendKeys(chromedp.ID("chat-tab-search"), "we"))
	b.waitJS("only web", options+` === "web"`)
	b.do("pick it", chromedp.SendKeys(chromedp.ID("chat-tab-search"), "\r"))
	b.waitJS("the picker closed", `document.querySelector("#chat-tab-picker").hidden`)
	tabs("This project,General,web")
	b.waitJS("web's chat shown", `document.querySelector('#chat-tabs [aria-selected="true"] .chat-tab-name')?.textContent === "web"`)
	b.until("open the + picker again", `!document.querySelector("#chat-tab-picker").hidden`, chromedp.Click(chromedp.ID("chat-tab-add")))
	b.waitJS("only lib left", options+` === "lib"`)
	b.do("close it with Escape", chromedp.SendKeys(chromedp.ID("chat-tab-search"), "\x1b"))
	b.waitJS("the picker closed", `document.querySelector("#chat-tab-picker").hidden`)

	// Another project has its own tabs.
	b.clickTextUntil(`#side-project-list .side-project`, "lib", `document.querySelector("#project-name")?.textContent === "lib"`)
	tabs("This project,General")
	b.until("open the + picker in lib", `!document.querySelector("#chat-tab-picker").hidden`, chromedp.Click(chromedp.ID("chat-tab-add")))
	b.waitJS("app and web", options+` === "app,web"`)
	b.clickText("#chat-tab-options button", "app")
	tabs("This project,General,app")
	b.clickTextUntil(`#side-project-list .side-project`, "app", `document.querySelector("#project-name")?.textContent === "app"`)
	tabs("This project,General,web")

	// They survive a reload.
	b.do("reload", chromedp.Reload())
	b.waitJS("app selected", `document.querySelector("#project-name")?.textContent === "app"`)
	tabs("This project,General,web")

	// A reply streaming in a chat you leave keeps its tab until you've seen it.
	hold := h.calls + ".chat-hold"
	if err := os.WriteFile(hold, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(hold) })
	// App's own chat was last picked as lib's "app" tab, so This project already
	// looks selected here: wait for the click itself, or a missed one leaves
	// that tab picked, and it follows us to web below (where it'd be on screen,
	// so never unread).
	b.clickTextUntil("#chat-tabs .chat-pane-tab-btn", "This project", `JSON.parse(localStorage.getItem("dash.chatPane"))?.tab === "project"`)
	panel := `#chat-pane .chat-panel[data-chat]:not([hidden])`
	b.do("ask app's chat", chromedp.SendKeys(chromedp.CSS(panel+` textarea`), "Hi\r"))
	b.waitJS("the reply streaming", `document.querySelector("`+panel+`")?.textContent.includes("Hello from the runner")`)
	b.clickTextUntil(`#side-project-list .side-project`, "web", `document.querySelector("#project-name")?.textContent === "web"`)
	tabs("This project,General,app")
	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	b.waitJS("app's tab unread", `[...document.querySelectorAll("#chat-tabs .chat-pane-tab-btn.unread .chat-tab-name")].map((n) => n.textContent).join() === "app"`)
	b.do("reload", chromedp.Reload())
	b.waitJS("web selected", `document.querySelector("#project-name")?.textContent === "web"`)
	tabs("This project,General,app") // unread survives a reload
	b.clickTextUntil("#chat-tabs .chat-pane-tab-btn", "app", `document.querySelector('#chat-tabs [aria-selected="true"] .chat-tab-name')?.textContent === "app"`)
	b.waitJS("app's tab read", `!document.querySelector("#chat-tabs .chat-pane-tab-btn.unread")`)
	tabs("This project,General,app") // still there while you read it
	b.clickText("#chat-tabs .chat-pane-tab-btn", "General")
	tabs("This project,General")

	// Closing a tab.
	b.clickTextUntil(`#side-project-list .side-project`, "app", `document.querySelector("#project-name")?.textContent === "app"`)
	tabs("This project,General,web")
	b.until("close web's tab", `![...document.querySelectorAll("#chat-tabs .chat-tab-name")].some((n) => n.textContent === "web")`,
		chromedp.Click(chromedp.CSS(`#chat-tabs .chat-tab-close`)))
	tabs("This project,General")
	b.do("reload", chromedp.Reload())
	b.waitJS("app selected", `document.querySelector("#project-name")?.textContent === "app"`)
	tabs("This project,General")
}

// A user's project chat is the same in each of their browsers: here one wide
// and one phone-sized, signed in as the same user. While a reply streams on
// one, the other says Claude is answering there; it shows the reply once it
// is done, and a message sent from either shows on both. Clearing it on one
// clears it on the other.
func TestBrowserProjectChatOnTwoDevices(t *testing.T) {
	chrome := needChrome(t)
	h := startRunnerHub(t)
	wide, phone := newBrowser(t, chrome), newBrowser(t, chrome)
	wide.signIn(h)
	phone.signIn(h)
	phone.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	wide.until("open the chat", `!document.querySelector("#chat-pane").hidden`, chromedp.Click(chromedp.ID("pc-open")))
	phone.until("open the chat", `!document.querySelector("#chat-pane").hidden`, chromedp.Click(chromedp.ID("nav-chat")))
	const panel = `#chat-pane .chat-panel[data-chat]:not([hidden])`
	has := func(texts ...string) string {
		var out []string
		for _, s := range texts {
			out = append(out, fmt.Sprintf(`document.querySelector(%q)?.textContent.includes(%q)`, panel, s))
		}
		return strings.Join(out, " && ")
	}
	replies := fmt.Sprintf(`document.querySelectorAll(%q).length`, panel+" .msg.assistant:not(.pending)")
	remote := fmt.Sprintf(`document.querySelector(%q)`, panel+" .chat-remote")
	for _, b := range []*browser{wide, phone} {
		b.waitJS("the project's chat, empty", `!!document.querySelector("`+panel+` .chat-empty:not([hidden])")`)
	}

	hold := h.calls + ".chat-hold"
	if err := os.WriteFile(hold, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(hold) })
	wide.do("ask on the laptop", chromedp.SendKeys(chromedp.CSS(panel+` textarea`), "Hi from the laptop\r"))
	wide.waitJS("the reply streaming", has("Hello from the runner"))
	wide.waitJS("no busy note where it runs", remote+`.hidden`)
	phone.waitJS("the phone says Claude is answering", `!`+remote+`.hidden`)
	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	phone.waitJS("the reply on the phone", has("Hi from the laptop", "Hello from the runner")+` && `+replies+` === 1 && `+remote+`.hidden`)

	// The phone asks next (a touch keyboard's Enter adds a line: Send sends);
	// the laptop follows.
	phone.do("write on the phone", chromedp.SendKeys(chromedp.CSS(panel+` textarea`), "And from the phone"))
	phone.waitJS("written", `document.querySelector("`+panel+` textarea").value === "And from the phone"`)
	phone.until("send from the phone", has("And from the phone"), chromedp.Click(chromedp.CSS(panel+` form button.primary`)))
	phone.waitJS("the phone's reply", replies+` === 2`)
	wide.waitJS("the phone's turn on the laptop", has("Hi from the laptop", "And from the phone")+` && `+replies+` === 2`)

	// Cleared on the laptop: on the phone too.
	wide.do("clear on the laptop", chromedp.SendKeys(chromedp.CSS(panel+` textarea`), "/clear\r"))
	wide.waitJS("cleared", `!(`+has("Hi from the laptop")+`)`)
	phone.waitJS("cleared on the phone", `!(`+has("Hi from the laptop")+`) && !!document.querySelector("`+panel+` .chat-empty:not([hidden])")`)
}

// needChrome is the Chrome to test with, or skips the test (see
// TestBrowserMainFlows).
func needChrome(t *testing.T) string {
	t.Helper()
	chrome := findChrome()
	if chrome == "" {
		if os.Getenv("DASH_TEST_BROWSER") == "require" {
			t.Fatal("no Chrome or Chromium found: set DASH_TEST_CHROME")
		}
		t.Skip("needs Chrome or Chromium (set DASH_TEST_CHROME)")
	}
	return chrome
}

// signIn signs in to the hub and waits for the board with its project.
func (b *browser) signIn(h *runnerHub) {
	b.t.Helper()
	b.do("open the hub", chromedp.Navigate(h.base+"/"))
	b.do("sign in",
		chromedp.WaitVisible(chromedp.CSS(`#login input[name=username]`)),
		chromedp.SendKeys(chromedp.CSS(`#login input[name=username]`), h.username),
		chromedp.SendKeys(chromedp.CSS(`#login input[name=password]`), h.password),
		chromedp.Click(chromedp.CSS(`#login button.btn.primary`)),
	)
	b.waitJS("the board with the project", `document.querySelector("#project-name")?.textContent === "app" && !!document.querySelector('#board .col[data-lane="review"]')`)
}

// findChrome is $DASH_TEST_CHROME, or a Chrome or Chromium on $PATH.
func findChrome() string {
	if p := os.Getenv("DASH_TEST_CHROME"); p != "" {
		return p
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "headless_shell", "headless-shell"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// browser is a headless Chrome tab that fails its test on the first step
// that doesn't work, with a screenshot and the page's text in the log.
type browser struct {
	t   *testing.T
	ctx context.Context
	dir string
}

func newBrowser(t *testing.T, chrome string) *browser {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := chromedp.NewContext(context.Background(), chromedp.WithAllocatorOptions(
		chromedp.ExecPath(chrome),
		chromedp.UserDataDir(filepath.Join(dir, "profile")),
		// Wide enough for the desktop board, every lane side by side.
		chromedp.WindowSize(1600, 1000),
		chromedp.Flag("no-sandbox", true), // CI containers
	))
	t.Cleanup(cancel)
	b := &browser{t: t, ctx: ctx, dir: dir}
	// The first Run starts the browser for the life of ctx (no timeout here).
	if err := chromedp.Do(ctx, chromedp.Navigate("about:blank")); err != nil {
		t.Fatalf("start %s: %v", chrome, err)
	}
	return b
}

func (b *browser) do(what string, steps ...chromedp.Action[chromedp.Void]) {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
	defer cancel()
	if err := chromedp.Do(ctx, steps...); err != nil {
		b.fail(what, err)
	}
}

// try runs a step that a re-render may defeat (its node replaced between
// the query and the input), for a caller that checks the outcome and tries
// again, giving up on it after wait.
func (b *browser) try(what string, wait time.Duration, step chromedp.Action[chromedp.Void]) error {
	ctx, cancel := context.WithTimeout(b.ctx, wait)
	defer cancel()
	err := chromedp.Do(ctx, step)
	if err != nil {
		b.t.Logf("%s: retrying after: %v", what, err)
	}
	return err
}

// until runs step until a JavaScript expression is true: for a click a
// re-render may swallow, where a second one does no harm.
func (b *browser) until(what, expr string, step chromedp.Action[chromedp.Void]) {
	b.t.Helper()
	start := time.Now()
	var last error
	for range 20 {
		if err := b.try(what, 10*time.Second, step); err != nil {
			last = err
		}
		for range 20 {
			if ok, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`!!(`+expr+`)`)); err == nil && ok {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	err := fmt.Errorf("still false after 20 tries in %s: %s", time.Since(start).Round(time.Second), expr)
	if last != nil {
		err = fmt.Errorf("%w (the last failed try: %v)", err, last)
	}
	b.fail(what, err)
}

// waitJS waits for a JavaScript expression to be true, across page loads.
func (b *browser) waitJS(what, expr string) {
	b.t.Helper()
	const wait = 60 * time.Second
	var last error
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
		ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`!!(`+expr+`)`))
		cancel()
		if err == nil && ok {
			return
		}
		last = err
	}
	err := fmt.Errorf("timed out after %s: %s", wait, expr)
	if last != nil {
		err = fmt.Errorf("%w (the last check: %v)", err, last)
	}
	b.fail("wait for "+what, err)
}

// waitCard waits for a task's card in a lane of the board.
func (b *browser) waitCard(id, lane string) {
	b.t.Helper()
	b.waitJS(fmt.Sprintf("task %s in %s", id, lane), fmt.Sprintf(`!!document.querySelector('#board .col[data-lane=%q] .todo[data-id=%q]')`, lane, id))
}

// openTask clicks a task's card until its detail opens: a click while the
// board is still settling after the sheet closed can miss.
func (b *browser) openTask(id string) {
	b.t.Helper()
	open := fmt.Sprintf(`!document.querySelector("#detail").hidden && document.querySelector("#board .todo.selected")?.dataset.id === %q`, id)
	b.until("open task "+id, open, chromedp.Click(chromedp.CSS(cardSel(id)+" .todo-title")))
}

// drag drags a task's card from one lane to another until it leaves the
// first: like a click, a drag while the board re-renders can miss.
func (b *browser) drag(id, from, to string) {
	b.t.Helper()
	left := fmt.Sprintf(`!document.querySelector('#board .col[data-lane=%q] .todo[data-id=%q]')`, from, id)
	b.until("drag task "+id+" to "+to, left, chromedp.DragAndDrop(chromedp.CSS(cardSel(id)+" .todo-title"),
		chromedp.CSS(fmt.Sprintf(`#board .col[data-lane=%q] .col-head`, to))))
}

// clickText clicks the first element matching sel whose text has text.
func (b *browser) clickText(sel, text string) {
	b.t.Helper()
	b.waitJS(fmt.Sprintf("%q in %s", text, sel), markText(sel, text))
	b.do("click "+text, chromedp.Click(chromedp.CSS(`[data-e2e-click]`)))
}

// clickTextUntil clicks the first element matching sel whose text has text
// until a JavaScript expression is true: for a list a re-render may replace
// (the sidebar's projects, reloaded as the hub links one), taking the
// marked node, and the click, with it.
func (b *browser) clickTextUntil(sel, text, expr string) {
	b.t.Helper()
	for range 20 {
		b.waitJS(fmt.Sprintf("%q in %s", text, sel), markText(sel, text))
		// The node was just marked: if the click can't find it, a re-render
		// replaced it, and waiting won't bring it back; mark the new one.
		b.try("click "+text, 3*time.Second, chromedp.Click(chromedp.CSS(`[data-e2e-click]`)))
		for range 20 {
			if ok, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`!!(`+expr+`)`)); err == nil && ok {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	b.fail("click "+text, fmt.Errorf("%s stayed false", expr))
}

// clickIn clicks inner inside the first element matching sel whose text has
// text, for a list a re-render may replace (the checklist, read again when
// the runner reports a commit) and a click that mustn't be repeated: it
// clicks again only when the marked node went before the click found it.
func (b *browser) clickIn(sel, text, inner string) {
	b.t.Helper()
	var err error
	for range 20 {
		b.waitJS(fmt.Sprintf("%q in %s", text, sel), markText(sel, text))
		if err = b.try("click "+inner+" in "+text, 3*time.Second, chromedp.Click(chromedp.CSS(`[data-e2e-click] `+inner))); err == nil {
			return
		}
	}
	b.fail("click "+inner+" in "+text, err)
}

// markText marks the first element matching sel whose text has text with
// data-e2e-click, reporting whether there is one.
func markText(sel, text string) string {
	return fmt.Sprintf(`(() => {
		document.querySelectorAll("[data-e2e-click]").forEach((e) => e.removeAttribute("data-e2e-click"));
		const el = [...document.querySelectorAll(%q)].find((e) => e.textContent.includes(%q) && !e.disabled);
		if (el) el.setAttribute("data-e2e-click", "");
		return !!el;
	})()`, sel, text)
}

func (b *browser) fail(what string, err error) {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
	defer cancel()
	if png, err := chromedp.Run(ctx, chromedp.FullScreenshot(90)); err == nil {
		shot := filepath.Join(os.TempDir(), "dash-browser-test.png")
		if os.WriteFile(shot, png, 0o644) == nil {
			b.t.Logf("screenshot: %s", shot)
		}
	}
	if text, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.body?.innerText ?? ""`)); err == nil {
		b.t.Logf("the page's text:\n%s", text)
	}
	b.t.Fatalf("%s: %v", what, err)
}

func cardSel(id string) string { return fmt.Sprintf(`#board .todo[data-id=%q]`, id) }

type todo struct {
	ID, Title, Prompt, Status, Error string
	Runs                             int
}

// waitTodo waits for the project's task with this prompt and returns its id.
func waitTodo(t *testing.T, api *client, pid, prompt string) string {
	t.Helper()
	var id string
	waitFor(t, nil, "the task "+prompt, func() bool {
		var todos []todo
		api.must("GET", "/api/projects/"+pid+"/todos", nil, http.StatusOK, &todos)
		for _, td := range todos {
			if strings.TrimSpace(td.Prompt) == prompt {
				id = td.ID
			}
		}
		return id != ""
	})
	return id
}

// waitStatus waits for the task at path (/api/todos/<id>, maybe under a
// /peer/ prefix) to reach a status (and ok, if given), failing at once if
// it fails.
func waitStatus(t *testing.T, api *client, path, status string, ok func(todo) bool) {
	t.Helper()
	var td todo
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		td = todo{}
		api.must("GET", path, nil, http.StatusOK, &td)
		if td.Status == status && (ok == nil || ok(td)) {
			return
		}
		if td.Status == "failed" && status != "failed" || time.Now().After(deadline) {
			_, log := api.try("GET", path+"/log", nil)
			raw, _ := json.Marshal(td)
			t.Fatalf("%s isn't %s: %s\n%s", path, status, raw, log)
		}
	}
}

// A project's board moved to an external tracker (a fake one, in the hub)
// in its settings: after the token, the board is picked from a dropdown
// loaded from the tracker, its columns are mapped by name, and saving starts
// the sync, which exports nothing and imports the board's queued card.
func TestBrowserTrackerBoard(t *testing.T) {
	chrome := needChrome(t)
	seed, err := json.Marshal(map[string]any{"name": "tracker", "boards": []map[string]any{
		{"name": "Roadmap", "token": "tok-SOMEONE-ELSE", "columns": []string{"Ideas"}},
		{"name": "Sprint", "token": "tok-ADA-1", "columns": []string{"Backlog", "Ready", "In progress", "Code review", "Shipped"},
			"cards": []map[string]string{{"title": "From the tracker", "column": "Ready"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DASH_TEST_TRACKER", string(seed))
	h := startRunnerHub(t)
	b := newBrowser(t, chrome)
	b.signIn(h)

	b.until("open the project menu", `document.querySelector("#project-menu").open`, chromedp.Click(chromedp.CSS(`#project-menu > summary`)))
	b.until("open the settings", `document.querySelector("#settings-sheet").open`, chromedp.Click(chromedp.ID("pm-settings")))
	b.waitJS("the board backends", `[...document.querySelectorAll("#board-backend option")].some((o) => o.value === "tracker")`)
	set := func(what, sel, value string) {
		t.Helper()
		js := fmt.Sprintf(`(() => { const el = document.querySelector(%q); el.value = %q; el.dispatchEvent(new Event("change", { bubbles: true })); return el.value === %q; })()`, sel, value, value)
		if ok, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](js)); err != nil || !ok {
			b.fail(what, fmt.Errorf("set %s = %q: %v", sel, value, err))
		}
	}
	set("pick the tracker", "#board-backend", "tracker")
	b.waitJS("the tracker's form", `!!document.querySelector('#board-fields [data-board-field="board"]')`)
	set("type the token", `#board-fields [data-board-field="token"]`, "tok-ADA-1")
	b.waitJS("the boards the token may use", `[...document.querySelectorAll('[data-board-field="board"] option')].map((o) => o.textContent).join() === "Pick the board,Sprint"`)
	set("pick the board", `#board-fields [data-board-field="board"]`, "Sprint")
	b.waitJS("the columns mapped by name", `document.querySelector('[data-board-status="queued"]').value === "Ready" && document.querySelector('[data-board-status="review"]').value === "Code review" && document.querySelector('[data-board-status="merged"]').value === "Shipped"`)
	if ok, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`(document.querySelector('input[name=board_export_choice][value=no]').checked = true)`)); err != nil || !ok {
		b.fail("leave the tasks in Dash", err)
	}
	if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`document.querySelector("#board-save").scrollIntoView(), document.querySelector("#board-save").click(), true`)); err != nil {
		b.fail("save the board", err)
	}
	b.waitJS("it syncs", `document.querySelector("#board-status").textContent.includes("Synced with Fake tracker")`)

	var st struct {
		Config model.TrackerConfig `json:"config"`
	}
	h.api.must("GET", "/api/projects/"+h.project.ID+"/tracker", nil, http.StatusOK, &st)
	var cfg map[string]string
	_ = json.Unmarshal(st.Config.Config, &cfg)
	if st.Config.Backend != "tracker" || cfg["board"] != "Sprint" || st.Config.StatusMap[model.StatusQueued] != "Ready" || !st.Config.HasCredentials {
		t.Fatalf("saved: %+v %s", st.Config, st.Config.Config)
	}
	if strings.Contains(string(st.Config.Config), "tok-ADA-1") {
		t.Fatal("the token is in the settings")
	}
	// The board takes only its token: the card came through with it.
	b.waitJS("the imported card's task", `[...document.querySelectorAll("#board .todo")].some((e) => e.textContent.includes("From the tracker"))`)
}

// On a phone the board shows one lane at a time, and a sideways swipe moves
// to the next or previous shown lane, as the tabs do. Not from the screen's
// edge, not mostly vertical, not in something that scrolls sideways, not
// past the first or last lane, and not once a long-press dragged a card.
func TestBrowserSwipeLanes(t *testing.T) {
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	b := newBrowser(t, chrome)
	b.signIn(h)
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the one-lane layout", `!matchMedia("(min-width: 1024px)").matches && document.querySelectorAll("#board .col").length && [...document.querySelectorAll("#board .col")].filter((c) => c.offsetWidth).length === 1`)
	var draft struct{ ID string }
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Hold me", "prompt": "Stay a draft"}, http.StatusCreated, &draft)
	b.waitCard(draft.ID, "draft")

	// Count the slide-ins of a lane, and note how far the page got wider than
	// the screen while one ran: a phone grows its layout viewport to fit, which
	// pushes the fixed bottom nav off-screen.
	if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`(() => {
		window.e2eSlides = 0;
		window.e2eOverflow = 0;
		const animate = Element.prototype.animate;
		Element.prototype.animate = function (...a) {
			if (!this.matches(".col")) return animate.apply(this, a);
			window.e2eSlides++;
			const r = animate.apply(this, a);
			r.pause();
			r.currentTime = 0;
			window.e2eOverflow = Math.max(window.e2eOverflow, document.documentElement.scrollWidth - document.documentElement.clientWidth);
			r.play();
			return r;
		};
		return true;
	})()`)); err != nil {
		b.fail("count the slide-ins", err)
	}

	lane := func(want string) {
		t.Helper()
		b.waitJS("the "+want+" lane", fmt.Sprintf(`document.querySelector("#board .col.active")?.dataset.lane === %q && document.querySelector(".lane-tab[aria-selected=true]")?.dataset.lane === %q`, want, want))
	}
	// swipe drags one finger from (x0, y0) to (x1, y1), holding it down
	// first for hold.
	swipe := func(what string, x0, y0, x1, y1 float64, hold time.Duration) {
		t.Helper()
		b.do(what, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
			touch := func(typ input.DispatchTouchEventType, x, y float64) error {
				p := input.DispatchTouchEventParams{Type: typ, TouchPoints: []*input.TouchPoint{}}
				if typ != input.DispatchTouchEventTypeTouchEnd {
					p.TouchPoints = []*input.TouchPoint{{X: x, Y: y}}
				}
				_, err := cdp.Call(ctx, tg, input.DispatchTouchEvent, p)
				return err
			}
			if err := touch(input.DispatchTouchEventTypeTouchStart, x0, y0); err != nil {
				return err
			}
			time.Sleep(hold)
			for i := 1; i <= 6; i++ {
				f := float64(i) / 6
				if err := touch(input.DispatchTouchEventTypeTouchMove, x0+(x1-x0)*f, y0+(y1-y0)*f); err != nil {
					return err
				}
				time.Sleep(16 * time.Millisecond)
			}
			return touch(input.DispatchTouchEventTypeTouchEnd, 0, 0)
		}))
	}
	still := func(what, want string) {
		t.Helper()
		time.Sleep(300 * time.Millisecond)
		lane(want)
	}

	b.until("show the drafts", `document.querySelector("#board .col.active")?.dataset.lane === "draft"`, chromedp.Click(chromedp.CSS(`.lane-tab[data-lane=draft]`)))
	still("swipe right on the first lane", "draft")
	swipe("swipe right on the first lane", 120, 500, 300, 510, 0)
	still("swipe right on the first lane", "draft")

	swipe("swipe left", 300, 500, 120, 510, 0)
	lane("queued")
	b.waitJS("a slide-in", `window.e2eSlides === 1`)
	b.waitJS("a slide-in within the screen's width", `window.e2eOverflow === 0`)
	swipe("swipe right", 120, 500, 300, 520, 0)
	lane("draft")

	swipe("a short swipe", 300, 500, 260, 500, 0)
	still("a short swipe", "draft")
	swipe("a mostly vertical swipe", 300, 400, 220, 600, 0)
	still("a mostly vertical swipe", "draft")
	swipe("a swipe from the screen's edge", 385, 500, 150, 500, 0)
	still("a swipe from the screen's edge", "draft")

	// In something that scrolls sideways (a code block, a row of chips).
	if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`(() => {
		const s = document.createElement("div");
		s.id = "e2e-scroller";
		s.style.cssText = "overflow-x: auto; position: fixed; left: 0; right: 0; top: 300px; height: 80px; z-index: 50";
		s.innerHTML = '<div style="width: 2000px; height: 80px"></div>';
		document.querySelector("#board .col.active .col-body").append(s);
		return true;
	})()`)); err != nil {
		b.fail("add a sideways scroller", err)
	}
	swipe("swipe in a sideways scroller", 300, 340, 100, 340, 0)
	still("swipe in a sideways scroller", "draft")
	b.waitJS("the scroller still there", `!!document.querySelector("#e2e-scroller")`)

	// A long-press drags the card instead.
	b.do("scroll the card into view", chromedp.ScrollIntoView(chromedp.CSS(cardSel(draft.ID))))
	r, err := chromedp.Run(b.ctx, chromedp.Evaluate[struct{ X, Y float64 }](fmt.Sprintf(`(() => { const r = document.querySelector(%q).getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`, cardSel(draft.ID))))
	if err != nil {
		b.fail("find the card", err)
	}
	if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`(() => {
		window.e2eDragged = false;
		new MutationObserver(() => { if (document.body.classList.contains("is-dragging")) window.e2eDragged = true; })
			.observe(document.body, { attributes: true, attributeFilter: ["class"] });
		return true;
	})()`)); err != nil {
		b.fail("watch for a drag", err)
	}
	swipe("long-press and drag the card sideways", r.X+80, r.Y, r.X-100, r.Y+5, 500*time.Millisecond)
	b.waitJS("the card dragged", `window.e2eDragged`)
	still("long-press and drag the card sideways", "draft")
	b.waitJS("the drag over", `!document.body.classList.contains("is-dragging")`)

	// The last lane, and a hidden one: with auto approve, Review isn't shown.
	b.until("open the project menu", `document.querySelector("#project-menu").open`, chromedp.Click(chromedp.CSS(`#project-menu > summary`)))
	b.until("open the settings", `document.querySelector("#settings-sheet").open`, chromedp.Click(chromedp.ID("pm-settings")))
	if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`(() => {
		document.querySelector("#settings-form [name=auto_approve]").checked = true;
		document.querySelector("#settings-sheet").close("save");
		return true;
	})()`)); err != nil {
		b.fail("approve automatically", err)
	}
	b.waitJS("Review hidden", `document.querySelector(".lane-tab[data-lane=review]")?.hidden && document.querySelector('#board .col[data-lane="review"]').hidden`)
	b.until("show Running", `document.querySelector("#board .col.active")?.dataset.lane === "in_progress"`, chromedp.Click(chromedp.CSS(`.lane-tab[data-lane=in_progress]`)))
	swipe("swipe left past Review", 300, 500, 120, 500, 0)
	lane("merging")
	swipe("swipe right past Review", 120, 500, 300, 500, 0)
	lane("in_progress")
	b.until("show Done", `document.querySelector("#board .col.active")?.dataset.lane === "done"`, chromedp.Click(chromedp.CSS(`.lane-tab[data-lane=done]`)))
	swipe("swipe left on the last lane", 300, 500, 120, 500, 0)
	still("swipe left on the last lane", "done")

	// No slide-in when the user asks for less motion.
	b.do("reduce motion", chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		_, err := cdp.Call(ctx, tg, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{
			Features: []*emulation.MediaFeature{{Name: "prefers-reduced-motion", Value: "reduce"}}})
		return err
	}))
	b.waitJS("reduced motion", `matchMedia("(prefers-reduced-motion: reduce)").matches`)
	slides := `window.e2eSlides`
	before, err := chromedp.Run(b.ctx, chromedp.Evaluate[int](slides))
	if err != nil {
		b.fail("count the slide-ins", err)
	}
	swipe("swipe right with reduced motion", 120, 500, 300, 500, 0)
	lane("merging")
	b.waitJS("no slide-in", fmt.Sprintf(`window.e2eSlides === %d`, before))
}

// Create a custom scan from the Scans page: the card after the list opens
// the editor with only a title and a prompt showing, Advanced closed, and a
// scan saved without a name gets one made from its title.
func TestBrowserCustomScan(t *testing.T) {
	chrome := needChrome(t)
	h := startRunnerHub(t)
	b := newBrowser(t, chrome)
	b.signIn(h)
	shot := func(name string) {
		if dir := os.Getenv("DASH_TEST_SHOTS"); dir != "" {
			buf, err := chromedp.Run(b.ctx, chromedp.FullScreenshot(90))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name+".jpg"), buf, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	b.until("open the scans", `document.querySelector("#view-scans.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=scans]`)))
	b.waitJS("the create card after the list", `(() => {
		const c = document.querySelector("#scan-create");
		return !c.hidden && document.querySelector("#scan-list").children.length > 0 &&
			c.previousElementSibling.id === "scan-list" && c.querySelector("#scan-new").textContent.includes("Create custom scan") &&
			!document.querySelector(".scan-bar #scan-new");
	})()`)
	b.do("scroll to the card", chromedp.ScrollIntoView(chromedp.ID("scan-create")))
	shot("scans")
	b.until("open the editor", `document.querySelector("#scan-sheet").open`, chromedp.Click(chromedp.ID("scan-new")))
	b.waitJS("only the essentials", `!document.querySelector("#scan-advanced").open && document.activeElement === document.querySelector("#scan-form [name=title]")`)
	shot("editor")
	b.do("write the scan",
		chromedp.SendKeys(chromedp.CSS(`#scan-form [name=title]`), "API error handling"),
		chromedp.SendKeys(chromedp.CSS(`#scan-form [name=prompt]`), "Find handlers that leak internal errors."))
	b.until("save it", `!document.querySelector("#scan-sheet").open`, chromedp.Click(chromedp.CSS(`#scan-form button[value=save]`)))
	b.waitJS("the new scan listed", `!!document.querySelector('.scan-card[data-scan="api-error-handling"]')`)
	var res struct {
		Scans []struct {
			Name, Title, Kind, Scope, Source, Prompt string
		} `json:"scans"`
	}
	h.api.must("GET", "/api/projects/"+h.project.ID+"/scans", nil, http.StatusOK, &res)
	i := slices.IndexFunc(res.Scans, func(s struct{ Name, Title, Kind, Scope, Source, Prompt string }) bool {
		return s.Name == "api-error-handling"
	})
	if i < 0 || res.Scans[i].Title != "API error handling" || res.Scans[i].Kind != "custom" || res.Scans[i].Scope != "repo" || res.Scans[i].Source != "dash" {
		t.Fatalf("scans: %+v", res.Scans)
	}

	// Editing it opens Advanced only when something there is set. The edit
	// actions are in the card's details.
	b.clickIn(".scan-card", "API error handling", ".scan-more")
	b.clickIn(".scan-card", "API error handling", ".scan-actions .link")
	b.waitJS("the editor, Advanced closed", `document.querySelector("#scan-sheet").open && !document.querySelector("#scan-advanced").open && document.querySelector("#scan-form [name=name]").readOnly`)
}

// On a desktop a task is edited in its detail pane: the title in place,
// the instructions in the Markdown editor with Ctrl+Enter / Esc, priority,
// model and dependencies from the controls under the title. Esc never closes
// the pane while a field is being edited. A phone keeps the task form.
func TestBrowserInlineEdit(t *testing.T) {
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	b := newBrowser(t, chrome)
	b.signIn(h)

	var base, task struct{ ID string }
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Base", "prompt": "Lay the ground"}, http.StatusCreated, &base)
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Rename me", "prompt": "Old instructions"}, http.StatusCreated, &task)
	b.waitCard(task.ID, "draft")
	b.openTask(task.ID)
	b.waitJS("the editable pane", `document.querySelector("#d-title").classList.contains("editable") && !document.querySelector("#d-meta-edit").hidden &&
		document.querySelector("#d-prompt-wrap").open && !!document.querySelector("#d-prompt .edit-prompt") &&
		![...document.querySelectorAll("#d-actions .btn")].some((e) => e.textContent.trim() === "Edit")`)
	var td struct {
		Title, Prompt, Model string
		Priority             *int
		DependsOn            []string `json:"depends_on"`
	}
	get := func() { api.must("GET", "/api/todos/"+task.ID, nil, http.StatusOK, &td) }
	paneOpen := `!document.querySelector("#detail").hidden`

	// The title: Esc cancels (the pane stays), Enter saves.
	b.until("edit the title", `!document.querySelector("#d-title-input").hidden`, chromedp.Click(chromedp.ID("d-title")))
	b.do("type and cancel", chromedp.KeyEvent("Nope"), chromedp.KeyEvent(kb.Escape))
	b.waitJS("the title back", paneOpen+` && document.querySelector("#d-title-input").hidden && document.querySelector("#d-title").textContent === "Rename me"`)
	b.until("edit the title again", `!document.querySelector("#d-title-input").hidden`, chromedp.Click(chromedp.ID("d-title")))
	b.do("type and save", chromedp.KeyEvent("Renamed\r"))
	b.waitJS("the new title", `document.querySelector("#d-title-input").hidden && document.querySelector("#d-title").textContent === "Renamed"`)
	if get(); td.Title != "Renamed" {
		t.Fatalf("title after saving: %q", td.Title)
	}

	// The instructions: Esc cancels, Ctrl+Enter saves.
	editor := `#d-prompt-edit .cm-content`
	b.until("edit the instructions", `!document.querySelector("#d-prompt-edit").hidden`, chromedp.Click(chromedp.CSS(`#d-prompt .edit-prompt`)))
	b.waitJS("the editor focused", `document.activeElement?.matches(`+fmt.Sprintf("%q", editor)+`)`)
	b.do("type and cancel", chromedp.KeyEvent(" discarded"), chromedp.KeyEvent(kb.Escape))
	b.waitJS("the instructions back", paneOpen+` && document.querySelector("#d-prompt-edit").hidden && document.querySelector("#d-prompt .md").textContent.trim() === "Old instructions"`)
	b.until("click the instructions", `!document.querySelector("#d-prompt-edit").hidden`, chromedp.Click(chromedp.CSS(`#d-prompt .md`)))
	b.waitJS("the editor focused", `document.activeElement?.matches(`+fmt.Sprintf("%q", editor)+`)`)
	b.do("type and save", chromedp.KeyEvent(" and new ones"), chromedp.KeyEvent("\r", chromedp.KeyModifiers(chromedp.ModifierCtrl)))
	b.waitJS("the saved instructions", paneOpen+` && document.querySelector("#d-prompt-edit").hidden && document.querySelector("#d-prompt .md").textContent.trim() === "Old instructions and new ones"`)
	if get(); td.Prompt != "Old instructions and new ones" {
		t.Fatalf("instructions after saving: %q", td.Prompt)
	}

	// Priority, model and dependencies save as they change.
	b.do("set a priority", chromedp.Click(chromedp.CSS(`#d-meta-edit .meta-input`)), chromedp.KeyEvent("3\r"))
	waitFor(t, nil, "the priority saved", func() bool { get(); return td.Priority != nil && *td.Priority == 3 })
	if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`(() => {
		const s = document.querySelector("#d-meta-edit .meta-select");
		s.value = "haiku";
		return s.dispatchEvent(new Event("change"));
	})()`)); err != nil {
		t.Fatalf("pick a model: %v", err)
	}
	waitFor(t, nil, "the model saved", func() bool { get(); return td.Model == "haiku" })
	b.until("open the dependencies", `!document.querySelector("#d-meta-edit .dep-pop").hidden`, chromedp.Click(chromedp.CSS(`#d-meta-edit .meta-btn`)))
	b.do("tick Base", chromedp.Click(chromedp.CSS(fmt.Sprintf(`#d-meta-edit .dep-pop input[value=%q]`, base.ID))))
	waitFor(t, nil, "the dependency saved", func() bool { get(); return slices.Equal(td.DependsOn, []string{base.ID}) })
	b.do("close the list", chromedp.KeyEvent(kb.Escape))
	b.waitJS("the list closed, the pane open", paneOpen+` && !document.querySelector("#d-meta-edit .dep-pop:not([hidden])") && document.querySelector("#d-meta-edit .meta-btn").textContent === "Depends on 1 task"`)

	// A phone keeps the task form.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the Edit button, nothing in place", `[...document.querySelectorAll("#d-actions .btn")].some((e) => e.textContent.trim() === "Edit") &&
		!document.querySelector("#d-title").classList.contains("editable") && document.querySelector("#d-meta-edit").hidden && !document.querySelector("#d-prompt .edit-prompt")`)
}

// A runner low on disk space: with thresholds that make its real disk
// "low", the board shows the banner, and the runner's page in Runners its
// Disk section with the warning and the thresholds to change; set back to
// the defaults, the warning goes. On a wide screen and a phone's.
func TestBrowserRunnerDiskWarning(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	b := newBrowser(t, chrome)
	b.signIn(h)
	path := "/api/runners/" + h.runnerID
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		var list struct {
			Runners []struct {
				Reported *struct {
					Disk *struct{ Volumes []json.RawMessage } `json:"disk"`
				} `json:"reported"`
			} `json:"runners"`
		}
		h.api.must("GET", "/api/runners", nil, http.StatusOK, &list)
		if len(list.Runners) == 1 && list.Runners[0].Reported != nil && list.Runners[0].Reported.Disk != nil && len(list.Runners[0].Reported.Disk.Volumes) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the runner didn't report its disk space")
		}
	}
	h.api.must("PATCH", path, map[string]any{"disk": map[string]any{"warn_percent": 99, "warn_gb": 100000, "min_free_gb": 0.01}}, http.StatusOK, nil)
	b.waitJS("the banner on the board", `!document.querySelector("#disk-banner").hidden && document.querySelector("#disk-banner").textContent.includes("free of")`)
	b.until("open the runner from the banner", `document.querySelector("#runners-sheet").open && !!document.querySelector("#runners-main .current .runner-disk-warn")`,
		chromedp.Click(chromedp.CSS(`#disk-banner button`)))
	b.waitJS("its thresholds", `document.querySelector("#runner-detail .runner-disk-settings input[aria-label^='Warn below percent']")?.value === "99"`)
	b.waitJS("the rail marks it", `document.querySelector("#runners-nav .runner-rail-warn")?.title === "Disk nearly full"`)
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("no sideways scrolling", `document.querySelector("#runners-main").scrollWidth <= document.querySelector("#runners-main").clientWidth + 1`)
	b.until("close the Runners sheet", `!document.querySelector("#runners-sheet").open`, chromedp.KeyEvent(kb.Escape))
	// Too full to start a task: the banner says so.
	h.api.must("PATCH", path, map[string]any{"disk": map[string]any{"min_free_gb": 100000}}, http.StatusOK, nil)
	b.waitJS("the banner says full", `document.querySelector("#disk-banner").classList.contains("full") && document.querySelector("#disk-banner").textContent.includes("starts no tasks")`)

	h.api.must("PATCH", path, map[string]any{"disk": map[string]any{"warn_percent": 0.01, "warn_gb": 0.001, "min_free_gb": 0.01}}, http.StatusOK, nil)
	b.waitJS("the banner gone", `document.querySelector("#disk-banner").hidden`)
}

// Build on: a merged task is listed under Build on in the full task form;
// the task saved with it waits on nothing and shows "Builds on #id title"
// in its detail. On a wide screen and a phone's.
func TestBrowserBuildOn(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	b := newBrowser(t, chrome)
	b.signIn(h)
	shot := func(name string) {
		if dir := os.Getenv("DASH_TEST_SHOTS"); dir != "" {
			buf, err := chromedp.Run(b.ctx, chromedp.FullScreenshot(90))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name+".jpg"), buf, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	var base struct{ ID string }
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Lay the ground", "prompt": "add a done file", "enqueue": true}, http.StatusCreated, &base)
	waitStatus(t, api, "/api/todos/"+base.ID, "review", nil)
	api.must("POST", "/api/todos/"+base.ID+"/approve", nil, http.StatusAccepted, nil)
	waitStatus(t, api, "/api/todos/"+base.ID, "merged", nil)

	for _, size := range []struct {
		name string
		vp   chromedp.Action[chromedp.Void]
	}{
		{"wide", chromedp.EmulateViewport(1280, 900)},
		{"phone", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch)},
	} {
		b.do("a "+size.name+" screen", size.vp)
		b.until("open the new task sheet", `document.querySelector("#quick-sheet").open`, chromedp.Click(chromedp.ID("new-todo")))
		b.do("write the task", chromedp.SendKeys(chromedp.CSS(`#quick-sheet[open] #quick-rows .quick-row`), "Follow up "+size.name))
		b.waitJS("the task written", `document.querySelector("#quick-rows .quick-row").value === "Follow up `+size.name+`"`)
		b.until("open the full form", `document.querySelector("#todo-sheet").open`, chromedp.Click(chromedp.ID("quick-more")))
		b.waitJS("Build on lists the merged task", fmt.Sprintf(`(() => {
			const f = document.querySelector("#todo-buildon-field"), s = document.querySelector("#todo-form").builds_on;
			return !f.hidden && f.getBoundingClientRect().width > 0 && s.options[0].value === "" &&
				[...s.options].some((o) => o.value === %q && o.textContent.includes("Lay the ground"));
		})()`, base.ID))
		if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](fmt.Sprintf(`(() => {
			const s = document.querySelector("#todo-form").builds_on;
			s.value = %q;
			s.scrollIntoView();
			return s.dispatchEvent(new Event("change"));
		})()`, base.ID))); err != nil {
			b.fail("pick the task to build on", err)
		}
		shot("buildon-form-" + size.name)
		b.until("save the draft", `!document.querySelector("#todo-sheet").open`, chromedp.Click(chromedp.CSS(`#todo-form button[value=draft]`)))
		id := waitTodo(t, api, pid, "Follow up "+size.name)
		var td struct {
			BuildsOn string `json:"builds_on"`
		}
		api.must("GET", "/api/todos/"+id, nil, http.StatusOK, &td)
		if td.BuildsOn != base.ID {
			t.Fatalf("%s: builds_on %q, want %q", size.name, td.BuildsOn, base.ID)
		}
		b.openTask(id)
		b.waitJS("Builds on in the detail", fmt.Sprintf(`[...document.querySelectorAll("#detail .deps")].some((e) =>
			e.textContent === "Builds on #%s Lay the ground" && e.getBoundingClientRect().width > 0)`, base.ID))
		shot("buildon-detail-" + size.name)
		if size.name == "wide" {
			// Edited in place: Build on among the controls under the title.
			b.waitJS("the inline Build on", fmt.Sprintf(`document.querySelector("#d-meta-edit .meta-buildon")?.value === %q`, base.ID))
			if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`(() => {
				const s = document.querySelector("#d-meta-edit .meta-buildon");
				s.value = "";
				return s.dispatchEvent(new Event("change"));
			})()`)); err != nil {
				b.fail("build on nothing", err)
			}
			waitFor(t, nil, "builds_on cleared", func() bool {
				td.BuildsOn = "" // omitted when empty
				api.must("GET", "/api/todos/"+id, nil, http.StatusOK, &td)
				return td.BuildsOn == ""
			})
			b.waitJS("no Builds on line", `![...document.querySelectorAll("#detail .deps")].some((e) => e.textContent.startsWith("Builds on"))`)
		}
		b.until("close the task", `document.querySelector("#detail").hidden`, chromedp.KeyEvent(kb.Escape))
	}
}

// Plan a new project: an idea, Claude's interview (one question, answered
// with its suggestion), the brief, a new repository from a URL picked with
// "Use for the plan", and Launch, which adds the project, commits BRIEF.md
// and CLAUDE.md to it and opens the goal it started.
func TestBrowserPlanProject(t *testing.T) {
	chrome := needChrome(t)
	h := startRunnerHub(t)
	// An empty repository on the same origin for the planned project.
	plan := filepath.Join(filepath.Dir(h.origin), "plan.git")
	run(t, "", h.gitEnv, "git", "init", "-q", "--bare", "-b", "main", plan)
	run(t, plan, h.gitEnv, "git", "config", "http.receivepack", "true")
	var app struct {
		RemoteURL string `json:"remote_url"`
	}
	h.api.must("GET", "/api/projects/"+h.project.ID, nil, http.StatusOK, &app)
	remote := strings.TrimSuffix(app.RemoteURL, "app.git") + "plan.git"
	b := newBrowser(t, chrome)
	b.signIn(h)

	b.until("open Add project", `document.querySelector("#new-project-sheet").open`, chromedp.Click(chromedp.ID("side-new-project")))
	b.until("choose Plan a new project", `!document.querySelector("#plan-home").hidden`, chromedp.Click(chromedp.CSS(`#new-project-nav [data-rail=plan]`)))
	b.until("start a plan", `!document.querySelector("#plan-work").hidden`, chromedp.Click(chromedp.ID("plan-new")))
	b.do("describe the idea",
		chromedp.SendKeys(chromedp.ID("plan-idea"), "A recipe box for my family"),
		chromedp.SendKeys(chromedp.ID("plan-name"), "recipes"))
	b.until("start the interview", `document.querySelector(".plan-q-card")`, chromedp.Click(chromedp.CSS(`#plan-next:not(:disabled)`)))
	b.waitJS("the question, flagged as not mentioned, with the brief beside it", `document.querySelector(".plan-q-card.is-missing") &&
		document.querySelector("#plan-live h1")?.textContent === "Fake plan" && document.querySelector("#plan-live .plan-open-mark") &&
		document.querySelector(".plan-st.is-current")?.textContent.includes("Interview") &&
		document.querySelector(".plan-topic.is-done")?.textContent.includes("Users")`)
	b.clickIn(".plan-q-card .plan-chip", "Single user", "span")
	b.waitJS("the suggestion picked", `document.querySelector(".plan-q-card .plan-chip[aria-pressed=true]")?.textContent.includes("Single user")`)
	b.until("send the answer", `document.querySelector("#plan-talk .plan-qa-a")?.textContent === "Single user"`,
		chromedp.Click(chromedp.CSS(`.plan-compose .btn.primary:not(:disabled)`)))
	b.until("review the brief", `document.querySelector("#plan-verdict.is-ready")`, chromedp.Click(chromedp.CSS(`#plan-next:not(:disabled)`)))
	b.waitJS("the brief to edit", `document.querySelector("#plan-brief").value.includes("Single user.")`)
	b.until("choose the repository", `document.querySelector(".plan-source")`, chromedp.Click(chromedp.CSS(`#plan-next:not(:disabled)`)))
	b.clickIn(".plan-source", "From URL", ".plan-source-name")
	b.waitJS("the URL source, for the plan", `document.querySelector("#new-project-nav [data-rail=url]").getAttribute("aria-current") === "true" &&
		!document.querySelector("#new-project-plan-note").hidden && document.querySelector("#new-project").name.value === "recipes"`)
	b.do("paste the remote", chromedp.SendKeys(chromedp.CSS(`#new-project input[name=remote_url]`), remote))
	b.until("use it for the plan", `!document.querySelector('.plan-pane[data-step="launch"]').hidden && document.querySelector("#plan-summary")?.textContent.includes("plan.git")`,
		chromedp.Click(chromedp.CSS(`#new-project-go:not(:disabled)`)))
	b.waitJS("the launch summary", `document.querySelector("#plan-summary").textContent.includes("Build the first version of recipes") &&
		document.querySelector("#plan-launch-claude").textContent.includes("Go.")`)
	b.until("launch", `!document.querySelector("#new-project-sheet").open || document.querySelector(".np-step.is-run, .np-step.is-ok")`,
		chromedp.Click(chromedp.CSS(`#plan-next:not(:disabled)`)))
	b.waitJS("the goal it started, open", `!document.querySelector("#new-project-sheet").open && document.querySelector("#project-name")?.textContent === "recipes" &&
		!document.querySelector("#goal-detail").hidden && document.querySelector("#goal-detail").textContent.includes("Build the first version of recipes")`)

	if got := run(t, plan, h.gitEnv, "git", "show", "main:BRIEF.md"); !strings.Contains(got, "Single user.") {
		t.Errorf("BRIEF.md on main: %q", got)
	}
	if got := run(t, plan, h.gitEnv, "git", "show", "main:CLAUDE.md"); !strings.Contains(got, "# recipes: rules for Claude") {
		t.Errorf("CLAUDE.md on main: %q", got)
	}
	var drafts []json.RawMessage
	h.api.must("GET", "/api/plans", nil, http.StatusOK, &drafts)
	if len(drafts) != 0 {
		t.Errorf("drafts after launching: %s", drafts)
	}
}
