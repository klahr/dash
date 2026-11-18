package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
)

// Planning looks like the task board: its columns side by side on a
// desktop, each card a row on the rail and Add card at the bottom of each
// column; on a phone, a strip of lanes and one column at a time.
func TestBrowserPlanningBoard(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	var c struct{ ID string }
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Export reports as PDF", "column": "planned", "description": "So **finance** can file them."}, http.StatusCreated, &c)
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Dark mode", "column": "ideas"}, http.StatusCreated, nil)

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open Planning", `document.querySelector("#view-planning.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=planning]`)))
	b.waitJS("four columns side by side", `[...document.querySelectorAll("#view-planning .board .col")].filter((c) => c.offsetWidth).length === 4`)
	b.waitJS("the cards as rows on the rail", fmt.Sprintf(`document.querySelector('#view-planning .col[data-col=planned] .rail .pl-card[data-id=%q] .todo .todo-title')?.textContent === "Export reports as PDF"`, c.ID))
	b.waitJS("Add card at the bottom of each column", `[...document.querySelectorAll("#view-planning .col")].every((c) => c.querySelector(".col-body").lastElementChild.matches(".pl-add"))`)

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("one column at a time", `[...document.querySelectorAll("#view-planning .board .col")].filter((c) => c.offsetWidth).length === 1`)
	b.waitJS("Ideas first", `document.querySelector("#view-planning .col.active")?.dataset.col === "ideas"`)
	b.until("the Planned lane", `document.querySelector("#view-planning .col.active")?.dataset.col === "planned"`,
		chromedp.Click(chromedp.CSS(`#view-planning .lane-tab[data-col=planned]`)))
	b.waitJS("its card", fmt.Sprintf(`document.querySelector('#view-planning .col.active .pl-card[data-id=%q]')?.offsetWidth > 0`, c.ID))
}
