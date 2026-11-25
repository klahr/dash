package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// The address bar follows what the page shows: switching views and opening
// a task push entries Back and Forward step through, closing a task goes
// back, and a reload or a link opens the same place (after signing in, for
// someone signed out).
func TestBrowserLinks(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	pid := h.project.ID
	var task, card struct{ ID string }
	h.api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Linked task", "prompt": "Share me"}, http.StatusCreated, &task)
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Linked card", "column": "planned"}, http.StatusCreated, &card)
	path := func(p string) string { return fmt.Sprintf(`location.pathname + location.search === %q`, p) }
	detailOpen := `!document.querySelector("#detail").hidden && document.querySelector("#d-title")?.textContent === "Linked task"`
	board, taskURL := "/p/"+pid+"/board", "/p/"+pid+"/tasks/"+task.ID

	// Signed out, a card's link goes through the sign-in and comes back to it.
	b := newBrowser(t, chrome)
	b.do("open a card's link", chromedp.Navigate(h.base+"/planning/cards/"+card.ID))
	b.do("sign in",
		chromedp.WaitVisible(chromedp.CSS(`#login input[name=username]`)),
		chromedp.SendKeys(chromedp.CSS(`#login input[name=username]`), h.username),
		chromedp.SendKeys(chromedp.CSS(`#login input[name=password]`), h.password),
		chromedp.Click(chromedp.CSS(`#login button.btn.primary`)),
	)
	b.waitJS("the card open on Planning", `document.querySelector("#view-planning.active") && !document.querySelector("#card-detail").hidden && document.querySelector("#pd-title")?.textContent === "Linked card"`)
	b.waitJS("its URL", path("/planning/cards/"+card.ID))
	b.waitJS("the Copy link button", `[...document.querySelectorAll("#pd-actions button")].some((e) => e.textContent === "Copy link")`)
	// Back closes it and stays on Planning, as the pane's Back button would.
	b.do("go back", chromedp.NavigateBack())
	b.waitJS("the card closed", `document.querySelector("#card-detail").hidden && document.querySelector("#view-planning.active")`)
	b.waitJS("Planning's URL", path("/planning"))
	b.do("go forward", chromedp.NavigateForward())
	b.waitJS("the card open again", `!document.querySelector("#card-detail").hidden && location.pathname === "/planning/cards/`+card.ID+`"`)
	b.until("close it with Esc", `document.querySelector("#card-detail").hidden`, chromedp.KeyEvent(kb.Escape))
	b.waitJS("Planning's URL again", path("/planning"))

	// Views: each one an entry.
	b.until("open the board", `document.querySelector("#view-board.active")`, chromedp.Click(chromedp.CSS(`.nav > button[data-view=board]`)))
	b.waitJS("the board's URL", path(board))
	b.until("open History", `document.querySelector("#view-history.active")`, chromedp.Click(chromedp.CSS(`.nav [data-view=history]`)))
	b.waitJS("History's URL", path("/p/"+pid+"/history"))
	b.until("open Goals", `document.querySelector("#view-goals.active")`, chromedp.Click(chromedp.CSS(`.nav > button[data-view=goals]`)))
	b.waitJS("Goals' URL", path("/p/"+pid+"/goals"))
	b.do("go back", chromedp.NavigateBack())
	b.waitJS("History again", `document.querySelector("#view-history.active") && `+path("/p/"+pid+"/history"))
	b.do("go back", chromedp.NavigateBack())
	b.waitJS("the board again", `document.querySelector("#view-board.active") && `+path(board))

	// A task: open, Back closes it, Forward opens it again, its close button goes back.
	b.waitCard(task.ID, "draft")
	b.openTask(task.ID)
	b.waitJS("the task's URL", path(taskURL))
	b.do("go back", chromedp.NavigateBack())
	b.waitJS("the task closed", `document.querySelector("#detail").hidden && `+path(board))
	b.do("go forward", chromedp.NavigateForward())
	b.waitJS("the task open again", detailOpen+` && `+path(taskURL))
	// Retried: the pane may still be sliding in (style.css's slide-in), moving the button under the click.
	b.until("close it", `document.querySelector("#detail").hidden`, chromedp.Click(chromedp.ID("d-close")))
	b.waitJS("the task closed, the board's URL", `document.querySelector("#detail").hidden && `+path(board))

	// A reload opens the same place: the task, over the board; a sheet with its section.
	b.do("open the task's link", chromedp.Navigate(h.base+taskURL))
	b.waitJS("the task open", detailOpen+` && document.querySelector("#view-board.active")`)
	b.do("go back", chromedp.NavigateBack())
	b.waitJS("Back closes it and stays in Dash", `document.querySelector("#detail").hidden && `+path(board))
	b.do("open Project settings → Board", chromedp.Navigate(h.base+board+"?sheet=settings&section=board"))
	b.waitJS("the sheet at its section", `document.querySelector("#settings-sheet").open && document.querySelector("#settings-sheet .rail-item[aria-current]")?.dataset.rail === "board"`)
	b.until("close the sheet", `!document.querySelector("#settings-sheet").open`, chromedp.KeyEvent(kb.Escape))
	b.waitJS("the board's URL", path(board))

	// A commit on History: its own URL; Back closes the pane.
	b.until("open History", `document.querySelector("#view-history.active")`, chromedp.Click(chromedp.CSS(`.nav [data-view=history]`)))
	b.until("open the commit", `!document.querySelector("#commit-detail").hidden`, chromedp.Click(chromedp.CSS(`#graph li:first-child .graph-commit`)))
	b.waitJS("the commit's URL", `location.pathname === "/p/`+pid+`/history/" + document.querySelector("#graph li:first-child").dataset.hash`)
	b.do("go back", chromedp.NavigateBack())
	b.waitJS("the commit closed", `document.querySelector("#commit-detail").hidden && `+path("/p/"+pid+"/history"))

	// A sheet: opening it is an entry, its section is in the URL.
	b.until("open the project menu", `document.querySelector("#project-menu").open`, chromedp.Click(chromedp.CSS(`#project-menu > summary`)))
	b.until("open Project settings", `document.querySelector("#settings-sheet").open`, chromedp.Click(chromedp.ID("pm-settings")))
	b.until("its Board section", `document.querySelector("#settings-sheet .rail-item[aria-current]")?.dataset.rail === "board"`,
		chromedp.Click(chromedp.CSS(`#settings-sheet .rail-item[data-rail=board]`)))
	b.waitJS("the sheet's URL", path("/p/"+pid+"/history?sheet=settings&section=board"))
	b.do("go back", chromedp.NavigateBack())
	b.waitJS("the sheet closed", `!document.querySelector("#settings-sheet").open && `+path("/p/"+pid+"/history"))

	// On a phone, Back closes the full-screen task, as its Back button does.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.do("open the task's link", chromedp.Navigate(h.base+taskURL))
	b.waitJS("the task full screen", detailOpen)
	b.do("go back", chromedp.NavigateBack())
	b.waitJS("the task closed, in Dash", `document.querySelector("#detail").hidden && `+path(board))
	b.do("go forward", chromedp.NavigateForward())
	b.waitJS("the task again", detailOpen)
	b.until("its Back button", `document.querySelector("#detail").hidden`, chromedp.Click(chromedp.ID("d-close")))
	b.waitJS("closed, the board's URL", `document.querySelector("#detail").hidden && `+path(board))
	b.do("a wide screen", chromedp.EmulateReset())

	// A task that isn't there: the board, and a word why.
	b.do("open a missing task's link", chromedp.Navigate(h.base+"/p/"+pid+"/tasks/nosuch"))
	b.waitJS("the board, the task closed and a message", `document.querySelector("#view-board.active") && document.querySelector("#detail").hidden && document.querySelector("#toasts")?.textContent.includes("That task isn't here")`)
	b.waitJS("the URL without it", path(board))
}
