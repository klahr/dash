package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// The board's header is one row: no filters while there is nothing to
// filter, a search that filters the cards as you type (Esc clears it), and
// New task in it on a desktop, a floating button on a phone with room below
// the cards for it. An empty column says nothing. A minimized column stays in
// its place as a strip that expands on a click, and the minimize button shows
// without a hover. A task's pane lies beside the board on a desktop, the card
// opened still in sight and selected, and another card swaps it; on a phone
// it is full screen.
func TestBrowserBoardHeaderAndPane(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	var rv, alpha, beta struct{ ID string }
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Add a done file", "prompt": "add a done file", "enqueue": true}, http.StatusCreated, &rv)
	waitStatus(t, api, "/api/todos/"+rv.ID, "review", nil)
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Alpha draft", "prompt": "Stay a draft"}, http.StatusCreated, &alpha)
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Beta draft", "prompt": "Stay a draft about the parser"}, http.StatusCreated, &beta)

	b := newBrowser(t, chrome)
	b.do("a 1280 screen", chromedp.EmulateViewport(1280, 900))
	b.signIn(h)
	b.waitCard(rv.ID, "review")
	b.waitCard(alpha.ID, "draft")
	b.waitCard(beta.ID, "draft")

	// One row: the filters (Tag: the task reported an issue, a tagged draft), no
	// From scans at zero, no Scans…, New task in the header; empty columns say nothing.
	b.waitJS("a one-row header with New task", `(() => {
		const head = document.querySelector("#board-head"), r = head.getBoundingClientRect(), fab = document.querySelector("#new-todo").getBoundingClientRect();
		const f = document.querySelector("#board-filters").getBoundingClientRect();
		return !document.querySelector("#tag-filter .scan-filter, .scans-open, #lane-chips, select.tag-filter-pick") &&
			r.height < 60 && fab.top >= r.top && fab.bottom <= r.bottom && f.top >= r.top && f.bottom <= r.bottom &&
			getComputedStyle(document.querySelector("#new-todo")).position !== "fixed";
	})()`)
	b.waitJS("empty columns without words", `(() => {
		const run = document.querySelector('#board .col[data-lane="in_progress"] .col-body');
		return run.textContent.trim() === "" && !document.querySelector("#board .col-empty");
	})()`)

	// Search: the magnifier opens the field; typing filters, Esc clears it.
	b.until("open search", `!document.querySelector("#board-search-input").hidden && document.activeElement === document.querySelector("#board-search-input")`,
		chromedp.Click(chromedp.ID("board-search-open")))
	b.do("type", chromedp.SendKeys(chromedp.ID("board-search-input"), "parser"))
	b.waitJS("only the match", fmt.Sprintf(`!!document.querySelector(%q) && !document.querySelector(%q) && !document.querySelector(%q)`, cardSel(beta.ID), cardSel(alpha.ID), cardSel(rv.ID)))
	b.do("Esc", chromedp.KeyEvent(kb.Escape))
	b.waitJS("every card again, the field closed", fmt.Sprintf(`!!document.querySelector(%q) && !!document.querySelector(%q) && document.querySelector("#board-search-input").hidden && document.querySelector("#board-search-input").value === ""`, cardSel(alpha.ID), cardSel(rv.ID)))

	// Minimize: shown without a hover; the column stays as a strip that expands on a click.
	queued := `#board .col[data-lane="queued"]`
	b.waitJS("minimize shown at rest", fmt.Sprintf(`+getComputedStyle(document.querySelector(%q)).opacity > 0.3`, queued+" .col-toggle"))
	b.until("minimize Queued", fmt.Sprintf(`(() => {
		const c = document.querySelector(%q), s = c?.querySelector(".col-strip");
		return c.classList.contains("collapsed") && s?.offsetWidth > 0 && c.offsetWidth < 60 && s.textContent.includes("Queued");
	})()`, queued), chromedp.Click(chromedp.CSS(queued+" .col-toggle")))
	shot(b, "board-minimized-column")
	b.until("expand it", fmt.Sprintf(`!document.querySelector(%q).classList.contains("collapsed") && document.querySelector(%q).offsetWidth > 100`, queued, queued),
		chromedp.Click(chromedp.CSS(queued+" .col-strip")))

	// The pane beside the board: the card opened stays in sight, selected; another card swaps it.
	beside := func(id string) string {
		return fmt.Sprintf(`(() => {
			const c = document.querySelector(%q), d = document.querySelector("#detail");
			if (!c || d.hidden || !c.classList.contains("selected")) return false;
			const cr = c.getBoundingClientRect(), dr = d.getBoundingClientRect();
			return document.querySelector(".app").classList.contains("detail-beside") && document.querySelector("#scrim").hidden &&
				cr.width > 0 && cr.right <= dr.left + 1 && cr.left >= 0 && document.elementFromPoint(cr.left + 10, cr.top + 10)?.closest(".todo") === c;
		})()`, cardSel(id))
	}
	for _, w := range []int64{1600, 1280, 1100} {
		b.do(fmt.Sprintf("a %d screen", w), chromedp.EmulateViewport(w, 900))
		b.frames()
		b.openTask(rv.ID)
		b.waitJS(fmt.Sprintf("the review card beside the pane at %d", w), beside(rv.ID))
		shot(b, fmt.Sprintf("board-pane-%d", w))
		b.until("another card swaps the pane", beside(alpha.ID)+` && document.querySelector("#d-title")?.textContent.includes("Alpha draft") && !document.querySelector(".todo.selected[data-id='`+rv.ID+`']")`,
			clickCard(alpha.ID))
		b.until("Esc closes it", `document.querySelector("#detail").hidden && !document.querySelector(".app").classList.contains("detail-beside") && !document.querySelector(".todo.selected")`,
			chromedp.KeyEvent(kb.Escape))
	}

	// A phone: the lane tabs, the filters under them (here Tag); New task floats, with room under the cards for it.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.frames()
	b.until("the drafts lane", `document.querySelector('#board .col[data-lane="draft"]')?.offsetWidth > 0`, chromedp.Click(chromedp.CSS(`.lane-tab[data-lane="draft"]`)))
	b.waitJS("a one-row header, New task floating clear of the cards", `(() => {
		const head = document.querySelector("#board-head").getBoundingClientRect(), fab = document.querySelector("#new-todo"), fr = fab.getBoundingClientRect();
		const nav = document.querySelector(".nav").getBoundingClientRect(), pad = parseFloat(getComputedStyle(document.querySelector("#board")).paddingBottom);
		return head.height < 110 && getComputedStyle(fab).position === "fixed" && fr.bottom <= nav.top && pad >= nav.top - fr.top;
	})()`)
	shot(b, "board-phone-header")
	b.until("search takes a row of its own", `(() => {
		const i = document.querySelector("#board-search-input"), tabs = document.querySelector("#lane-tabs");
		return !i.hidden && i.getBoundingClientRect().bottom <= tabs.getBoundingClientRect().top + 1 && tabs.offsetWidth > 300;
	})()`, chromedp.Click(chromedp.ID("board-search-open")))
	b.do("Esc", chromedp.KeyEvent(kb.Escape))
	b.waitJS("search closed", `document.querySelector("#board-search-input").hidden`)
	b.openTask(alpha.ID)
	b.waitJS("the pane full screen", `(() => {
		const d = document.querySelector("#detail").getBoundingClientRect();
		return d.left === 0 && Math.abs(d.width - innerWidth) < 1 && !document.querySelector(".app").classList.contains("detail-beside") && document.querySelector("#d-close").getAttribute("aria-label") !== null;
	})()`)
	b.until("Back closes it", `document.querySelector("#detail").hidden`, chromedp.Click(chromedp.ID("d-close")))
}
