package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
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
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	b := newBrowser(t, chrome)
	b.signIn(h)

	// Create a task from the quick sheet, queued right away.
	b.do("open the new task sheet", chromedp.Click(chromedp.ID("new-todo")))
	b.do("wait for the sheet", chromedp.WaitVisible(chromedp.CSS(`#quick-sheet[open] #quick-rows .quick-row`)))
	b.waitJS("the task's row focused", `document.activeElement?.matches("#quick-rows .quick-row")`)
	b.do("write the task", chromedp.SendKeys(chromedp.CSS(`#quick-rows .quick-row`), "Add a done file"))
	b.waitJS("the task written", `document.querySelector("#quick-rows .quick-row").value === "Add a done file"`)
	b.do("tick Queue right away", chromedp.Click(chromedp.CSS(`label.toggle:has(#quick-queue) span`))) // a new profile: unticked
	b.waitJS("Queue right away ticked", `document.querySelector("#quick-queue").checked`)
	b.waitJS("the Queue task button", `document.querySelector("#quick-submit-label").textContent === "Queue task"`)
	b.do("submit the task", chromedp.Click(chromedp.CSS(`#quick-form button[value=add]`)))
	b.waitJS("the sheet to close", `!document.querySelector("#quick-sheet").open`)
	first := waitTodo(t, api, pid, "Add a done file")

	// See it run: it lands in Review, and its timeline shows the agent's work.
	b.waitCard(first, "review")
	b.openTask(first)
	b.waitJS("the agent's output in the timeline", `document.querySelector("#d-chat-log")?.textContent.includes("Working on: Add a done file")`)

	// Review: comment on a line of the diff, and send the comment for rework.
	b.do("show the changes", chromedp.Click(chromedp.CSS(`.pane-tabs button[data-pane=diff]`)))
	b.waitJS("the diff", `document.querySelector("#d-diff")?.textContent.includes("done-by-runner.txt")`)
	b.clickText(`#d-diff details.diff-file > summary`, "done-by-runner.txt")
	b.do("comment on a line",
		chromedp.Click(chromedp.CSS(`#d-diff .diff-lines > div[data-ln][data-file="done-by-runner.txt"]`)),
		chromedp.WaitVisible(chromedp.CSS(`#d-diff .comment-box textarea`)),
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

	// Approve and merge: the change reaches origin's main.
	b.clickText("#d-actions button", "Approve")
	waitStatus(t, api, "/api/todos/"+first, "merged", nil)
	b.waitCard(first, "done")
	files := run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "ls-tree", "--name-only", "main")
	if !strings.Contains(files, "done-by-runner.txt") || !strings.Contains(files, "rework-by-runner.txt") {
		t.Fatalf("origin's main after the merge: %s", files)
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

// A CLI project's terminal preview: open it on a task in review, type in
// it, and send the task back with the terminal's transcript as the rework
// notes. In a project of the runner's own, whose runner opens terminals for
// the hub (-hub-commands): the preview runs there, reached through the hub.
func TestBrowserTerminalPreview(t *testing.T) {
	chrome := needChrome(t)
	h := startRunnerHub(t, "-hub-commands")
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
	b.do("open the + picker", chromedp.Click(chromedp.ID("chat-tab-add")))
	b.waitJS("the picker with lib and web", `!document.querySelector("#chat-tab-picker").hidden && `+options+` === "lib,web" && document.activeElement?.id === "chat-tab-search"`)
	b.do("search for we", chromedp.SendKeys(chromedp.ID("chat-tab-search"), "we"))
	b.waitJS("only web", options+` === "web"`)
	b.do("pick it", chromedp.SendKeys(chromedp.ID("chat-tab-search"), "\r"))
	b.waitJS("the picker closed", `document.querySelector("#chat-tab-picker").hidden`)
	tabs("This project,General,web")
	b.waitJS("web's chat shown", `document.querySelector('#chat-tabs [aria-selected="true"] .chat-tab-name')?.textContent === "web"`)
	b.do("open the + picker again", chromedp.Click(chromedp.ID("chat-tab-add")))
	b.waitJS("only lib left", options+` === "lib"`)
	b.do("close it with Escape", chromedp.SendKeys(chromedp.ID("chat-tab-search"), "\x1b"))
	b.waitJS("the picker closed", `document.querySelector("#chat-tab-picker").hidden`)

	// Another project has its own tabs.
	b.clickTextUntil(`#side-project-list .side-project`, "lib", `document.querySelector("#project-name")?.textContent === "lib"`)
	tabs("This project,General")
	b.do("open the + picker in lib", chromedp.Click(chromedp.ID("chat-tab-add")))
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
	b.clickTextUntil("#chat-tabs .chat-pane-tab-btn", "This project", `document.querySelector('#chat-tabs [aria-selected="true"] .chat-tab-name')?.textContent === "This project"`)
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
	b.do("close web's tab", chromedp.Click(chromedp.CSS(`#chat-tabs .chat-tab-close`)))
	tabs("This project,General")
	b.do("reload", chromedp.Reload())
	b.waitJS("app selected", `document.querySelector("#project-name")?.textContent === "app"`)
	tabs("This project,General")
}

// needChrome is the Chrome to test with, or skips the test (see
// TestBrowserMainFlows).
func needChrome(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs dash and Chrome")
	}
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
// again.
func (b *browser) try(step chromedp.Action[chromedp.Void]) {
	ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
	defer cancel()
	if err := chromedp.Do(ctx, step); err != nil {
		b.t.Logf("retrying after: %v", err)
	}
}

// until runs step until a JavaScript expression is true: for a click a
// re-render may swallow, where a second one does no harm.
func (b *browser) until(what, expr string, step chromedp.Action[chromedp.Void]) {
	b.t.Helper()
	for range 20 {
		b.try(step)
		for range 20 {
			if ok, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`!!(`+expr+`)`)); err == nil && ok {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	b.fail(what, fmt.Errorf("%s stayed false", expr))
}

// waitJS waits for a JavaScript expression to be true, across page loads.
func (b *browser) waitJS(what, expr string) {
	b.t.Helper()
	var last error
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
		ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`!!(`+expr+`)`))
		cancel()
		if err == nil && ok {
			return
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("timed out")
	}
	b.fail("wait for "+what, last)
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
		b.try(chromedp.Click(chromedp.CSS(`[data-e2e-click]`)))
		for range 20 {
			if ok, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`!!(`+expr+`)`)); err == nil && ok {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	b.fail("click "+text, fmt.Errorf("%s stayed false", expr))
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
