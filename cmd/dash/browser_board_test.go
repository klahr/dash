package main

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// The board's columns fit from 1100px wide, with Merging folded away while
// nothing merges, and where they don't, the edge with more board is shaded.
// A card in Review is its title, one line of what it is and one row of
// actions, Review first, at every width. Approve on the card and dropping
// it on Drafts both wait for an Undo before anything is sent; while it is
// dragged, the columns say what a drop does. Picked drafts are combined with
// Combine, which shows from two on, or queued with the bar's Queue.
func TestBrowserBoardRiskyActions(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	var rv struct{ ID string }
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Add a done file to the repository root", "prompt": "add a done file", "enqueue": true}, http.StatusCreated, &rv)
	waitStatus(t, api, "/api/todos/"+rv.ID, "review", nil)
	card := cardSel(rv.ID)
	status := func() string {
		var x struct{ Status string }
		api.must("GET", "/api/todos/"+rv.ID, nil, http.StatusOK, &x)
		return x.Status
	}

	b := newBrowser(t, chrome)
	b.signIn(h)

	// In Review the card shows what Claude did, not the prompt; with one
	// runner it doesn't name it.
	var sum struct{ Summary string }
	waitFor(t, nil, "Claude's summary", func() bool {
		api.must("GET", "/api/todos/"+rv.ID, nil, http.StatusOK, &sum)
		return sum.Summary != ""
	})
	b.waitCard(rv.ID, "review")
	b.waitJS("the summary, no runner", fmt.Sprintf(`(() => {
		const c = document.querySelector(%q);
		return c.querySelector(".todo-sub")?.textContent === %q && !c.querySelector(".runner-tag");
	})()`, card, sum.Summary))

	// The card's layout: the title on the card's width, a single line under
	// it, and the actions in one row, Review first and filled, Approve
	// quieter, Ask Claude an icon.
	layout := fmt.Sprintf(`(() => {
		const c = document.querySelector(%q);
		if (!c || !c.offsetWidth) return false;
		const side = c.querySelector(":scope > .todo-side"), row = c.querySelector(":scope > .review-actions");
		const btns = [...row.children].filter((e) => e.offsetWidth);
		const top = btns[0].getBoundingClientRect().top;
		const cr = c.getBoundingClientRect(), main = c.querySelector(".todo-main").getBoundingClientRect();
		return btns[0].matches(".review-chip") && btns[0].textContent.trim() === "Review" &&
			btns.every((e) => Math.abs(e.getBoundingClientRect().top - top) < 2 && e.getBoundingClientRect().right <= cr.right + 1) &&
			getComputedStyle(btns[0]).backgroundColor !== getComputedStyle(row.querySelector(".approve-chip")).backgroundColor &&
			row.querySelector(".ask-claude-chip").textContent.trim() === "" && row.querySelector(".ask-claude-chip").title === "Ask Claude" &&
			side.getBoundingClientRect().height < 26 && main.right > cr.right - 40;
	})()`, card)
	for _, size := range []struct {
		name string
		vp   chromedp.Action[chromedp.Void]
	}{
		{"1600", chromedp.EmulateViewport(1600, 900)},
		{"1280", chromedp.EmulateViewport(1280, 900)},
		{"1100", chromedp.EmulateViewport(1100, 800)},
		{"phone", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch)},
	} {
		b.do("a "+size.name+" screen", size.vp)
		if size.name == "phone" {
			b.until("show Review", `document.querySelector("#board .col.active")?.dataset.lane === "review"`,
				chromedp.Click(chromedp.CSS(`.lane-tab[data-lane=review]`)))
			b.waitJS("no Merging tab", `document.querySelector(".lane-tab[data-lane=merging]").hidden`)
		} else {
			b.waitJS("five columns that fit, no Merging", `(() => {
				const board = document.querySelector("#board"), cols = [...board.querySelectorAll(".col")].filter((c) => c.offsetWidth);
				return cols.length === 5 && !cols.some((c) => c.dataset.lane === "merging") &&
					board.scrollWidth <= board.clientWidth && !board.classList.contains("more-right");
			})()`)
		}
		b.waitCard(rv.ID, "review")
		b.waitJS("the review card's layout at "+size.name, layout)
		shot(b, "board-review-card-"+size.name)
	}

	// Too narrow for five columns: the board scrolls, and says so.
	b.do("a narrow desktop", chromedp.EmulateViewport(1024, 800))
	b.waitJS("the right edge shaded", `document.querySelector("#board").classList.contains("more-right") && getComputedStyle(document.querySelector("#board"), "::after").opacity === "1"`)
	b.run("scroll to the end", `const bd = document.querySelector("#board"); bd.scrollLeft = bd.scrollWidth; true`)
	b.waitJS("the left edge shaded instead", `(() => { const c = document.querySelector("#board").classList; return c.contains("more-left") && !c.contains("more-right"); })()`)

	// Approve on the card: it shows in Merging at once, with Undo, and nothing is sent.
	b.do("a wide screen", chromedp.EmulateViewport(1280, 900))
	b.waitCard(rv.ID, "review")
	b.do("approve on the card", chromedp.Click(chromedp.CSS(card+" .approve-chip")))
	b.waitCard(rv.ID, "merging")
	b.waitJS("Undo in the toast", `[...document.querySelectorAll(".toast")].some((e) => e.textContent.includes("Approved") && e.querySelector(".toast-action")?.textContent === "Undo")`)
	b.clickText(".toast .toast-action", "Undo")
	b.waitCard(rv.ID, "review")
	b.waitJS("Merging folded away again", `document.querySelector('#board .col[data-lane=merging]').hidden`)
	time.Sleep(6 * time.Second) // past the toast's time: an Undo that failed would have sent it by now
	if s := status(); s != "review" {
		t.Fatalf("after Undo the task is %s, want review", s)
	}

	// Dragged, the columns say what a drop does; dropped on Drafts, it waits for Undo too.
	b.run("drag the card over Drafts", dragJS+fmt.Sprintf(`
		const c = document.querySelector(%q), r = c.getBoundingClientRect();
		fire("pointerdown", r.left + 40, r.top + 12, c);
		fire("pointermove", r.left + 50, r.top + 20);
		const d = document.querySelector('#board .col[data-lane=draft] .col-body').getBoundingClientRect();
		fire("pointermove", d.left + 60, d.top + 80);
		window.e2e = {
			draft: document.querySelector('#board .col[data-lane=draft] .drop-label')?.textContent,
			danger: document.querySelector('#board .col[data-lane=draft] .drop-label')?.classList.contains("danger"),
			queued: document.querySelector('#board .col[data-lane=queued] .drop-label')?.textContent,
			done: document.querySelector('#board .col[data-lane=done] .drop-label')?.textContent,
			over: document.querySelector('#board .col[data-lane=draft]').classList.contains("drop-over"),
			x: d.left + 60, y: d.top + 80,
		};
		true`, card))
	b.waitJS("the drop labels", `e2e.draft === "Cancel task" && e2e.danger && e2e.queued === "Send back" && e2e.done === "Approve" && e2e.over`)
	shot(b, "board-drag-labels")
	b.run("drop it on Drafts", dragJS+`fire("pointerup", e2e.x, e2e.y); true`)
	b.waitJS("no labels after the drop", `!document.querySelector(".drop-label")`)
	b.waitCard(rv.ID, "draft")
	b.waitJS("Undo in the toast", `[...document.querySelectorAll(".toast")].some((e) => e.textContent.includes("Moved to drafts") && e.querySelector(".toast-action")?.textContent === "Undo")`)
	b.clickText(".toast .toast-action", "Undo")
	b.waitCard(rv.ID, "review")
	time.Sleep(6 * time.Second)
	if s := status(); s != "review" {
		t.Fatalf("after Undo the task is %s, want review", s)
	}

	// Approved and left alone, it merges once the toast has gone.
	b.do("approve on the card", chromedp.Click(chromedp.CSS(card+" .approve-chip")))
	b.waitCard(rv.ID, "merging")
	if s := status(); s != "review" {
		t.Fatalf("while Undo shows the task is %s, want review", s)
	}
	waitStatus(t, api, "/api/todos/"+rv.ID, "merged", nil)
	b.waitCard(rv.ID, "done")

	// Combine shows from two picked drafts on.
	var d1, d2 struct{ ID string }
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "One draft", "prompt": "Stay a draft"}, http.StatusCreated, &d1)
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Another draft", "prompt": "Stay a draft"}, http.StatusCreated, &d2)
	b.waitCard(d1.ID, "draft")
	b.waitCard(d2.ID, "draft")
	// Select shows every draft's box. A box also shows on hover, but not
	// here: the viewport changes above turned Chrome's touch emulation off,
	// which gives the page the machine's own pointer, and a CI machine has no
	// mouse, so (hover: none) hides the boxes and a click opens the card.
	// TestBrowserFindingDrafts picks with hover.
	b.until("Select", `!document.querySelector("#combine-bar").hidden && /0 selected/.test(document.querySelector("#combine-bar").textContent)`,
		chromedp.Click(chromedp.CSS(`#board .col[data-lane="draft"] .select-drafts`)))
	b.until("pick a draft", `/1 selected/.test(document.querySelector("#combine-bar").textContent) && !document.querySelector("#combine-bar .combine")`,
		chromedp.Click(chromedp.CSS(cardSel(d1.ID)+" .todo-pick input")))
	b.until("pick the other", `document.querySelector("#combine-bar .combine")?.textContent === "Combine"`,
		chromedp.Click(chromedp.CSS(cardSel(d2.ID)+" .todo-pick input")))
	shot(b, "board-combine")
	b.do("combine them", chromedp.Click(chromedp.CSS(`#combine-bar .combine`)))
	b.waitJS("one draft left", fmt.Sprintf(`!document.querySelector(%q) && !!document.querySelector(%q) && document.querySelector("#combine-bar").hidden`, cardSel(d2.ID), cardSel(d1.ID)))
	b.waitJS("the toast", `[...document.querySelectorAll(".toast")].some((e) => e.textContent.includes("Combined 2 drafts"))`)

	// A draft's Queue shows only on hover; the bar queues what is picked.
	b.waitJS("Queue hidden at rest", fmt.Sprintf(`getComputedStyle(document.querySelector(%q)).opacity === "0"`, cardSel(d1.ID)+" .draft-actions"))
	b.until("Select again", `!document.querySelector("#combine-bar").hidden`,
		chromedp.Click(chromedp.CSS(`#board .col[data-lane="draft"] .select-drafts`)))
	b.until("pick the draft left", `/1 selected/.test(document.querySelector("#combine-bar").textContent) && !!document.querySelector("#combine-bar .queue-picked")`,
		chromedp.Click(chromedp.CSS(cardSel(d1.ID)+" .todo-pick input")))
	b.do("queue it from the bar", chromedp.Click(chromedp.CSS(`#combine-bar .queue-picked`)))
	b.waitJS("it left Drafts", fmt.Sprintf(`!document.querySelector('#board .col[data-lane="draft"] .todo[data-id=%q]') && document.querySelector("#combine-bar").hidden`, d1.ID))

	// A queued task waiting for a draft says so calmly, with Queue both.
	var dep, waiter struct{ ID string }
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Groundwork", "prompt": "Stay a draft"}, http.StatusCreated, &dep)
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Needs groundwork", "prompt": "add a later file", "depends_on": []string{dep.ID}, "enqueue": true}, http.StatusCreated, &waiter)
	b.waitCard(waiter.ID, "queued")
	b.waitJS("waits for the draft, with Queue both", fmt.Sprintf(`(() => {
		const c = document.querySelector(%q), line = c?.querySelector(".todo-sub.deps");
		return line?.textContent === "waits for Groundwork" && !line.classList.contains("failed") &&
			getComputedStyle(line.querySelector(".dep-link")).color === getComputedStyle(c.querySelector(".todo-title")).color &&
			[...c.querySelectorAll(".enqueue-chip")].some((e) => e.textContent.trim() === "Queue both");
	})()`, cardSel(waiter.ID)))
	shot(b, "board-waits-for-draft")
	b.clickTextUntil(cardSel(waiter.ID)+" .enqueue-chip", "Queue both", fmt.Sprintf(`!document.querySelector('#board .col[data-lane="draft"] .todo[data-id=%q]')`, dep.ID))
}
