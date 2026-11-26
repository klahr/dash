package main

import (
	"fmt"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// The two new-task dialogs, quick add and the full form, on a wide screen
// and a phone's: both name the project they file into and end with Queue
// and Save draft (the primary button), New goal… and Write a feature brief
// are in one "Something bigger?" menu, the full form starts at Instructions
// (Title after it, Model under Advanced), and Clear in it can be undone,
// tags and all.
func TestBrowserNewTaskDialogs(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	b := newBrowser(t, chrome)
	b.signIn(h)
	buttons := func(form string) string {
		return fmt.Sprintf(`[...document.querySelectorAll("%s .sheet-actions .btn:not([hidden])")].map((e) => e.textContent.trim() + (e.matches(".primary") ? "*" : "")).join()`, form)
	}
	menu := `[...document.querySelectorAll(".pop-menu button .menu-label")].map((e) => e.textContent).join()`

	for _, size := range []struct {
		name string
		vp   chromedp.Action[chromedp.Void]
	}{
		{"wide", chromedp.EmulateViewport(1280, 900)},
		{"phone", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch)},
	} {
		b.do("a "+size.name+" screen", size.vp)

		// Quick add.
		b.until("open the new task sheet", `document.querySelector("#quick-sheet").open`, chromedp.Click(chromedp.ID("new-todo")))
		b.waitJS("titled with the project, Save draft the primary button", `/^New task in \S/.test(document.querySelector("#quick-title").textContent) &&
			!!document.querySelector("#quick-title strong").textContent && `+buttons("#quick-form")+` === "Queue,Save draft*"`)
		b.waitJS("three side links, no goal or brief among them", `[...document.querySelectorAll("#quick-form .task-links button:not([hidden])")].map((e) => e.textContent.trim()).join() === "Another task,More options…,Something bigger?" &&
			document.querySelector("#quick-bigger").getBoundingClientRect().right <= innerWidth`)
		b.until("open Something bigger?", `!!document.querySelector(".pop-menu")`, chromedp.Click(chromedp.ID("quick-bigger")))
		b.waitJS("New goal… and Write a feature brief in it", menu+` === "New goal…,Write a feature brief"`)
		shot(b, "newtask-quick-"+size.name)
		b.until("Esc closes the menu, not the sheet", `!document.querySelector(".pop-menu")`, chromedp.KeyEvent(kb.Escape))
		b.waitJS("the sheet still open", `document.querySelector("#quick-sheet").open`)
		b.do("write the task", chromedp.SendKeys(chromedp.CSS(`#quick-sheet[open] #quick-rows .quick-row`), "Tidy the header #ui"))
		b.until("open the full form", `document.querySelector("#todo-sheet").open`, chromedp.Click(chromedp.ID("quick-more")))

		// The full form: Instructions first and focused, then Title; the same buttons.
		f := `document.querySelector("#todo-form")`
		b.waitJS("titled with the project, Instructions focused, the same buttons", `/^New task in \S/.test(document.querySelector("#todo-sheet-title").textContent) &&
			`+f+`.prompt.closest(".field").contains(document.activeElement) && `+buttons("#todo-form")+` === "Clear,Queue,Save draft*"`)
		b.waitJS("Instructions before Title, not optional; Model folded under Advanced", `(() => {
			const p = `+f+`.prompt, t = `+f+`.title;
			return !!(p.compareDocumentPosition(t) & Node.DOCUMENT_POSITION_FOLLOWING) &&
				!document.querySelector("#todo-prompt-label").textContent.includes("optional") &&
				!document.querySelector("#todo-advanced").open && `+f+`.model.closest("details") === document.querySelector("#todo-advanced");
		})()`)
		b.waitJS("no goal or brief in the instructions' tools", `!document.querySelector(".prompt-tools").textContent.includes("goal") && !document.querySelector(".prompt-tools").textContent.includes("brief") && !document.querySelector("#todo-bigger").hidden`)
		b.until("open Something bigger?", `!!document.querySelector(".pop-menu")`, chromedp.Click(chromedp.ID("todo-bigger")))
		b.waitJS("the same menu", menu+` === "New goal…,Write a feature brief"`)
		shot(b, "newtask-form-"+size.name)
		b.until("Esc closes the menu, not the form", `!document.querySelector(".pop-menu")`, chromedp.KeyEvent(kb.Escape))
		b.waitJS("the form still open", `document.querySelector("#todo-sheet").open`)

		// Clear, then Undo: the instructions and tags come back.
		b.waitJS("the text and tag carried over", f+`.prompt.value === "Tidy the header" && [...document.querySelectorAll("#todo-tags .tag-chip")].length === 1`)
		b.until("clear the form", f+`.prompt.value === "" && !document.querySelector("#todo-tags .tag-chip")`, chromedp.Click(chromedp.ID("todo-clear")))
		b.clickTextUntil(".toast .toast-action", "Undo", f+`.prompt.value === "Tidy the header" && [...document.querySelectorAll("#todo-tags .tag-chip")].length === 1 && document.querySelector("#todo-sheet").open`)
		// Clear again, so no draft is left for the next screen size.
		b.until("clear it again", f+`.prompt.value === ""`, chromedp.Click(chromedp.ID("todo-clear")))
		b.until("close the form", `!document.querySelector("#todo-sheet").open`, chromedp.Click(chromedp.CSS(`#todo-sheet .sheet-head .icon-btn`)))
	}
}
