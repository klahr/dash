package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
)

// A project with more than one board shows a switcher above the lanes: the
// board shows one board's tasks, new tasks go on it, and the board shown is
// kept across a reload, on a phone too.
func TestBrowserProjectBoards(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	var main struct{ ID string }
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "On Main", "prompt": "Stay a draft"}, http.StatusCreated, &main)

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.waitCard(main.ID, "draft")
	b.waitJS("no switcher with one board", `document.querySelector("#board-switch").hidden`)

	var web struct{ Board struct{ ID string } }
	api.must("POST", "/api/projects/"+pid+"/boards", map[string]any{"name": "Website"}, http.StatusCreated, &web)
	var onWeb struct{ ID string }
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "On Website", "prompt": "Stay a draft", "board": web.Board.ID}, http.StatusCreated, &onWeb)

	tab := func(name string) string {
		return fmt.Sprintf(`[...document.querySelectorAll("#board-switch .board-tab")].find((b) => b.textContent.startsWith(%q))`, name)
	}
	b.waitJS("the switcher, on Main", `!document.querySelector("#board-switch").hidden && `+tab("Main")+`?.getAttribute("aria-selected") === "true"`)
	b.waitJS("only Main's task", fmt.Sprintf(`!!document.querySelector(%q) && !document.querySelector(%q)`, cardSel(main.ID), cardSel(onWeb.ID)))
	b.until("show Website", fmt.Sprintf(`!!document.querySelector(%q) && !document.querySelector(%q)`, cardSel(onWeb.ID), cardSel(main.ID)),
		chromedp.Click(chromedp.CSS(`#board-switch .board-tab[data-board="`+web.Board.ID+`"]`)))

	b.do("reload", chromedp.Reload())
	b.waitCard(onWeb.ID, "draft")
	b.waitJS("Website kept", tab("Website")+`?.getAttribute("aria-selected") === "true" && !document.querySelector(`+fmt.Sprintf("%q", cardSel(main.ID))+`)`)

	// Opening a task on another board shows that board.
	b.run("open Main's task", fmt.Sprintf(`import("/app/detail.js").then((m) => m.openDetail(%q)); true`, main.ID))
	b.waitJS("Main shown with it", tab("Main")+`?.getAttribute("aria-selected") === "true" && !!document.querySelector(`+fmt.Sprintf("%q", cardSel(main.ID))+`)`)
	b.do("close the task", chromedp.Click(chromedp.CSS(`#d-close`)))

	// A task made on the board shown goes there.
	b.until("show Website again", tab("Website")+`?.getAttribute("aria-selected") === "true"`,
		chromedp.Click(chromedp.CSS(`#board-switch .board-tab[data-board="`+web.Board.ID+`"]`)))
	b.run("add a task", `import("/app/api.js").then(({ api }) => import("/app/boards.js").then(({ boardField }) => api("POST", "/api/projects/`+pid+`/todos", { title: "New here", prompt: "x", ...boardField() }))); true`)
	b.waitJS("the new task on Website", `[...document.querySelectorAll("#board .todo")].some((c) => c.textContent.includes("New here")) && `+tab("Website")+`?.textContent.endsWith("2")`)

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the switcher on a phone", tab("Website")+`?.offsetWidth > 0 && `+tab("Main")+`?.offsetWidth > 0`)
}
