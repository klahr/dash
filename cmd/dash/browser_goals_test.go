package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
)

// Deleting a goal from its pane, on a wide screen and on a phone's: the
// confirm says its tasks stay, then the pane closes and the goal leaves the
// list. A goal deleted elsewhere (another window, the API) leaves the list
// and its open pane too.
func TestBrowserDeleteGoal(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	goal := func(title string) string {
		var g struct{ ID string }
		api.must("POST", "/api/goals", map[string]any{"project": pid, "title": title, "mode": "ask"}, http.StatusCreated, &g)
		return g.ID
	}
	wide, phone, elsewhere := goal("Wide goal"), goal("Phone goal"), goal("Elsewhere goal")

	b := newBrowser(t, chrome)
	b.signIn(h)
	row := func(id string) string { return fmt.Sprintf(`#goal-list li[data-id=%q] .goal-open`, id) }
	gone := func(id string) string {
		return fmt.Sprintf(`!document.querySelector('#goal-list li[data-id=%q]') && document.querySelector("#goal-detail").hidden`, id)
	}
	del := func(id string) {
		b.t.Helper()
		b.until("open the goal", fmt.Sprintf(`!document.querySelector("#goal-detail").hidden && document.querySelector('#goal-list li[data-id=%q].selected')`, id),
			chromedp.Click(chromedp.CSS(row(id))))
		b.waitJS("Delete beside Stop, on screen", `(() => {
			const bs = [...document.querySelectorAll("#gd-actions button")], i = bs.findIndex((e) => e.textContent.trim() === "Delete");
			const r = bs[i]?.getBoundingClientRect();
			return i > 0 && bs[i - 1].textContent.trim() === "Stop" && r.width > 0 && r.left >= 0 && r.right <= innerWidth;
		})()`)
		b.clickTextUntil("#gd-actions button", "Delete", `document.querySelector("#ask-sheet").open`)
		b.waitJS("the confirm says the tasks stay", `document.querySelector("#ask-title").textContent === "Delete the goal?" && document.querySelector("#ask-body").textContent.includes("tasks stay on the board")`)
		b.until("confirm", gone(id), chromedp.Click(chromedp.ID("ask-ok")))
		if code, body := api.try("GET", "/api/goals/"+id, nil); code != http.StatusNotFound {
			t.Fatalf("deleted goal: %d %s", code, body)
		}
	}

	b.do("a wide screen", chromedp.EmulateViewport(1280, 900))
	b.until("open Goals", `document.querySelector("#view-goals.active") && document.querySelectorAll("#goal-list li").length === 3`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=goals]`)))
	del(wide)

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	// The sidebar turns into the closed drawer at once: sliding out, it
	// covered the page and took the next click (Shell, Add project).
	if hidden, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`getComputedStyle(document.querySelector("#menu")).visibility === "hidden"`)); err != nil || !hidden {
		b.fail("the drawer hidden at once", fmt.Errorf("hidden: %v, err: %v", hidden, err))
	}
	del(phone)

	// Deleted elsewhere while open here.
	b.until("open the last goal", `!document.querySelector("#goal-detail").hidden`, chromedp.Click(chromedp.CSS(row(elsewhere))))
	api.must("DELETE", "/api/goals/"+elsewhere, nil, http.StatusNoContent, nil)
	b.waitJS("it leaves the list and the pane closes", gone(elsewhere)+` && !document.querySelector("#goal-empty").hidden`)
}
