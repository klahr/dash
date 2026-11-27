package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// Reported issues are drafts on the Board: From scans (N) shows only what
// scans added, and scans' drafts below high show only there; cards show severity with its report count and tags (a tag filters
// the Board), drafts can be picked and merged, a task's tags are edited in
// its pane, and old links to Issues open the Board. On a wide screen and a
// phone's.
func TestBrowserFindingDrafts(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	type reported struct {
		Todo struct{ ID string }
	}
	report := func(want int, body map[string]any) string {
		var r reported
		api.must("POST", "/api/projects/"+pid+"/issues", body, want, &r)
		return r.Todo.ID
	}
	high := report(http.StatusCreated, map[string]any{"title": "SQL injection in search", "type": "vuln", "severity": "high", "file": "db.go", "line": 4, "source": "scan:security"})
	if again := report(http.StatusOK, map[string]any{"title": "SQL injection in search", "type": "vuln", "severity": "high", "file": "db.go", "line": 9, "source": "agent:chat"}); again != high {
		t.Fatalf("the same issue again made another draft: %s and %s", high, again)
	}
	low1 := report(http.StatusCreated, map[string]any{"title": "Unused helper", "type": "issue", "severity": "low", "file": "a.go", "source": "scan:security"})
	low2 := report(http.StatusCreated, map[string]any{"title": "Long function", "type": "issue", "severity": "info", "file": "b.go", "source": "scan:security"})
	var plain struct{ ID string }
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Plain draft", "prompt": "Stay a draft", "tags": []string{"backend"}}, http.StatusCreated, &plain)

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.waitCard(high, "draft")
	b.waitCard(plain.ID, "draft")
	b.waitJS("the scans' low drafts hidden, From scans (3)", fmt.Sprintf(`!document.querySelector(%q) && !document.querySelector(%q) &&
		document.querySelector("#tag-filter .scan-filter")?.textContent === "From scans (3)" && !document.querySelector("#board-filters").hidden`, cardSel(low1), cardSel(low2)))
	b.waitJS("the high draft's severity with its reports, and tags", fmt.Sprintf(`(() => {
		const c = document.querySelector(%q);
		const tags = [...c.querySelectorAll(".todo-tags .tag-chip")].map((e) => e.textContent);
		const sev = c.querySelector(".tag-chip.sev-high");
		return sev?.textContent === "high · 2 reports" && getComputedStyle(sev).textTransform === "none" && !tags.includes("high") &&
			tags.includes("security") && tags.includes("scan:security") &&
			[...c.querySelectorAll(".tag-chip.hued")].some((e) => e.textContent === "security" && e.style.getPropertyValue("--tag-h") !== "");
	})()`, cardSel(high)))
	scanFilter := func(on bool, expr string) {
		b.until(fmt.Sprintf("From scans %v", on), fmt.Sprintf(`%s && document.querySelector("#tag-filter .scan-filter").getAttribute("aria-pressed") === "%v"`, expr, on),
			chromedp.Click(chromedp.CSS(`#tag-filter .scan-filter`)))
	}
	scanFilter(true, fmt.Sprintf(`!!document.querySelector(%q) && !!document.querySelector(%q) && !!document.querySelector(%q) && !document.querySelector(%q)`, cardSel(low1), cardSel(low2), cardSel(high), cardSel(plain.ID)))
	scanFilter(false, fmt.Sprintf(`!!document.querySelector(%q) && !document.querySelector(%q)`, cardSel(plain.ID), cardSel(low1)))

	// A tag on a card filters the Board by it.
	b.until("filter by backend", fmt.Sprintf(`!document.querySelector(%q) && !!document.querySelector(%q) && /backend/.test(document.querySelector("#tag-filter .tag-active.hued")?.textContent)`, cardSel(high), cardSel(plain.ID)),
		chromedp.Click(chromedp.CSS(cardSel(plain.ID)+` .todo-tags .tag-chip`)))
	b.until("clear the tag filter", fmt.Sprintf(`!!document.querySelector(%q) && !document.querySelector("#tag-filter .tag-active")`, cardSel(high)),
		chromedp.Click(chromedp.CSS(`#tag-filter .tag-active`)))
	// Tag, in the filters, is a menu of the project's tags.
	b.until("open Tag", `[...document.querySelectorAll(".pop-menu [role=menuitem]")].some((e) => e.textContent.startsWith("backend"))`,
		chromedp.Click(chromedp.CSS(`#tag-filter .tag-filter-pick`)))
	b.clickTextUntil(".pop-menu [role=menuitem] .menu-label", "backend", fmt.Sprintf(`!document.querySelector(%q) && !!document.querySelector("#tag-filter .tag-active")`, cardSel(high)))
	b.until("clear it again", `!document.querySelector("#tag-filter .tag-active")`, chromedp.Click(chromedp.CSS(`#tag-filter .tag-active`)))

	// Pick two drafts and merge them. Cards glide to new places as the
	// Board redraws: clicks wait for them to settle.
	still := func() {
		b.waitJS("the cards settled", `document.getAnimations().every((a) => a.playState !== "running")`)
	}
	scanFilter(true, fmt.Sprintf(`!!document.querySelector(%q) && !document.querySelector(%q)`, cardSel(low1), cardSel(plain.ID)))
	still()
	b.until("pick a low draft", `document.querySelector("#combine-bar")?.offsetWidth > 0 && /1 selected/.test(document.querySelector("#combine-bar").textContent) && !document.querySelector("#combine-bar .combine")`,
		chromedp.Click(chromedp.CSS(cardSel(low1)+` .todo-pick input`)))
	still()
	b.until("pick the other", `/2 selected/.test(document.querySelector("#combine-bar").textContent) && document.querySelector("#combine-bar .combine")?.textContent === "Combine"`,
		chromedp.Click(chromedp.CSS(cardSel(low2)+` .todo-pick input`)))
	b.until("Esc clears the picks", `document.querySelector("#combine-bar").hidden`, chromedp.KeyEvent(kb.Escape))
	still()
	b.do("pick them again", chromedp.Click(chromedp.CSS(cardSel(low1)+` .todo-pick input`)))
	still()
	b.do("and the other", chromedp.Click(chromedp.CSS(cardSel(low2)+` .todo-pick input`)))
	b.until("combine them", fmt.Sprintf(`!document.querySelector(%q) && !!document.querySelector(%q) && document.querySelector("#combine-bar").hidden`, cardSel(low2), cardSel(low1)),
		chromedp.Click(chromedp.CSS(`#combine-bar .combine`)))
	b.waitJS("the merged draft has both issues", fmt.Sprintf(`/Fix 2 issues/.test(document.querySelector(%q)?.textContent)`, cardSel(low1)))

	// The pane: the issues it has, and tags to edit.
	b.openTask(high)
	b.waitJS("the reported issue in the pane", `/Reported issue · reported 2 times/.test(document.querySelector("#d-finding")?.textContent) && /db\.go:9/.test(document.querySelector("#d-finding").textContent)`)
	b.do("type a tag", chromedp.Click(chromedp.CSS(`#d-tags .tag-input`)), chromedp.SendKeys(chromedp.CSS(`#d-tags .tag-input`), "urgent\r"))
	b.waitJS("the tag on the task and its card", fmt.Sprintf(`[...document.querySelectorAll("#d-tags .tag-chip")].some((e) => e.textContent.startsWith("urgent")) &&
		[...document.querySelectorAll(%q)].some((e) => e.textContent === "urgent")`, cardSel(high)+" .todo-tags .tag-chip"))
	b.until("remove it", `![...document.querySelectorAll("#d-tags .tag-chip")].some((e) => e.textContent.startsWith("urgent"))`,
		chromedp.Click(chromedp.CSS(`#d-tags .tag-x[aria-label="Remove tag urgent"]`)))
	// Wait for the address to leave the task as well (closing goes Back, a
	// moment later), or the reload below can open it again over the board.
	b.until("close the task", `document.querySelector("#detail").hidden && !location.pathname.includes("/tasks/")`,
		chromedp.Click(chromedp.CSS(`#d-close`)))

	// A phone: Select at the foot of Drafts shows the pick boxes, the bar fits the screen.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.do("reload", chromedp.Reload())
	b.waitJS("the board", `document.querySelector("#project-name")?.textContent === "app"`)
	b.until("the drafts lane", `document.querySelector('#board .col[data-lane="draft"]')?.offsetWidth > 0`, chromedp.Click(chromedp.CSS(`.lane-tab[data-lane="draft"]`)))
	b.waitJS("From scans (2) on a phone", `document.querySelector("#tag-filter .scan-filter")?.offsetWidth > 0 && document.querySelector("#tag-filter .scan-filter").textContent === "From scans (2)"`)
	b.waitJS("no pick box or Queue before Select", fmt.Sprintf(`getComputedStyle(document.querySelector(%q)).visibility === "hidden"`, cardSel(high)+" .draft-actions"))
	b.until("Select shows them", fmt.Sprintf(`(() => { const s = getComputedStyle(document.querySelector(%q)); return s.visibility === "visible" && s.opacity === "1"; })() && /0 selected/.test(document.querySelector("#combine-bar").textContent)`, cardSel(high)+" .todo-pick"),
		chromedp.Click(chromedp.CSS(`#board .col[data-lane="draft"] .select-drafts`)))
	still()
	b.do("tap a pick box", chromedp.Click(chromedp.CSS(cardSel(high)+` .todo-pick input`)))
	still()
	b.do("tap another", chromedp.Click(chromedp.CSS(cardSel(plain.ID)+` .todo-pick input`)))
	b.waitJS("the bar on screen, above the nav", `(() => {
		const r = document.querySelector("#combine-bar").getBoundingClientRect(), nav = document.querySelector(".nav").getBoundingClientRect();
		return r.width > 0 && r.left >= 0 && r.right <= innerWidth && r.bottom <= nav.top && /2 selected/.test(document.querySelector("#combine-bar").textContent);
	})()`)
	b.do("clear", chromedp.Click(chromedp.CSS(`#combine-bar .clear-picks`)))
	b.do("a wide screen", chromedp.EmulateReset())

	// Old links to Issues open the Board, or the draft an issue became.
	b.do("an old Issues link", chromedp.Navigate(h.base+"/p/"+pid+"/issues"))
	b.waitJS("the Board", `document.querySelector("#view-board.active") && document.querySelector("#project-name")?.textContent === "app" && /\/board$/.test(location.pathname)`)
	b.do("an old link to one issue", chromedp.Navigate(h.base+"/p/"+pid+"/issues/"+high))
	b.waitJS("its draft, open", fmt.Sprintf(`document.querySelector("#detail") && !document.querySelector("#detail").hidden && location.pathname.endsWith("/tasks/%s")`, high))
}
