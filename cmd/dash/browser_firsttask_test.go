package main

import (
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// A project with no tasks: the board shows how to start (New task, example
// tasks that fill in the new-task sheet, what happens next) instead of empty
// columns, on a wide screen and a phone's; it goes once there is a task.
func TestBrowserFirstTaskScreen(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	b := newBrowser(t, chrome)
	b.signIn(h)

	screen := `(() => {
		const e = document.querySelector("#board-empty");
		return !e.hidden && e.querySelector(".empty-title")?.textContent === "Add your first task" &&
			e.querySelectorAll(".first-task").length >= 3 && !document.querySelector("#board .col") &&
			[...e.querySelectorAll(".empty-actions .btn")].some((x) => x.textContent.includes("New task") && x.getBoundingClientRect().width > 0) &&
			e.textContent.includes("worktree") && e.textContent.includes("Review") && e.textContent.includes("Approve");
	})()`
	for _, size := range []struct {
		name string
		vp   chromedp.Action[chromedp.Void]
	}{
		{"wide", chromedp.EmulateViewport(1280, 900)},
		{"phone", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch)},
	} {
		b.do("a "+size.name+" screen", size.vp)
		b.waitJS("the first-task screen", screen)
		b.waitJS("it fits the screen", `[...document.querySelectorAll("#board-empty .first-task")].every((x) => { const r = x.getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth; })`)
		shot(b, "first-task-"+size.name)

		// An example fills in the new-task sheet.
		b.until("open an example", `document.querySelector("#quick-sheet").open`, chromedp.Click(chromedp.CSS(`#board-empty .first-task`)))
		b.waitJS("its text in the sheet", `[...document.querySelectorAll("#quick-rows .quick-row")].some((r) => r.value === document.querySelector("#board-empty .first-task").textContent)`)
		b.until("close the sheet", `!document.querySelector("#quick-sheet").open`, chromedp.KeyEvent(kb.Escape))
	}

	// A task: the board again.
	var td struct{ ID string }
	h.api.must("POST", "/api/projects/"+h.project.ID+"/todos", map[string]any{"title": "First", "prompt": "Stay a draft"}, http.StatusCreated, &td)
	b.waitCard(td.ID, "draft")
	b.waitJS("no first-task screen", `document.querySelector("#board-empty").hidden`)
}
