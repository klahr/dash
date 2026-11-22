package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// Reported issues are drafts on the Board: scans' drafts below high wait
// behind From scans (N), cards show severity, ×N and tags (a tag filters
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
	b.waitJS("the scans' low drafts hidden behind From scans (2)", fmt.Sprintf(`!document.querySelector(%q) && !document.querySelector(%q) &&
		document.querySelector("#tag-filter .scan-filter")?.textContent === "From scans (2)" && !document.querySelector("#board-filters").hidden`, cardSel(low1), cardSel(low2)))
	b.waitJS("the high draft's severity, ×2 and tags", fmt.Sprintf(`(() => {
		const c = document.querySelector(%q);
		const tags = [...c.querySelectorAll(".todo-tags .tag-chip")].map((e) => e.textContent);
		return c.querySelector(".reports-tag")?.textContent === "×2" && tags.includes("security") && tags.includes("high") && tags.includes("scan:security") &&
			c.querySelector(".tag-chip.sev-high") !== null &&
			[...c.querySelectorAll(".tag-chip.hued")].some((e) => e.textContent === "security" && e.style.getPropertyValue("--tag-h") !== "");
	})()`, cardSel(high)))
	b.until("show the scans' drafts", fmt.Sprintf(`!!document.querySelector(%q) && !!document.querySelector(%q) && document.querySelector("#tag-filter .scan-filter").getAttribute("aria-pressed") === "true"`, cardSel(low1), cardSel(low2)),
		chromedp.Click(chromedp.CSS(`#tag-filter .scan-filter`)))

	// A tag on a card filters the Board by it.
	b.until("filter by backend", fmt.Sprintf(`!document.querySelector(%q) && !!document.querySelector(%q) && /Tag: backend/.test(document.querySelector("#tag-filter .tag-active.hued")?.textContent)`, cardSel(high), cardSel(plain.ID)),
		chromedp.Click(chromedp.CSS(cardSel(plain.ID)+` .todo-tags .tag-chip`)))
	b.until("clear the tag filter", fmt.Sprintf(`!!document.querySelector(%q) && !document.querySelector("#tag-filter .tag-active")`, cardSel(high)),
		chromedp.Click(chromedp.CSS(`#tag-filter .tag-active`)))

	// Pick two drafts and merge them. Cards glide to new places as the
	// Board redraws: clicks wait for them to settle.
	still := func() {
		b.waitJS("the cards settled", `document.getAnimations().every((a) => a.playState !== "running")`)
	}
	still()
	b.until("pick a low draft", `document.querySelector("#merge-bar")?.offsetWidth > 0 && /1 selected/.test(document.querySelector("#merge-bar").textContent) && document.querySelector("#merge-bar .btn.primary").disabled`,
		chromedp.Click(chromedp.CSS(cardSel(low1)+` .todo-pick input`)))
	still()
	b.until("pick the other", `/2 selected/.test(document.querySelector("#merge-bar").textContent) && !document.querySelector("#merge-bar .btn.primary").disabled`,
		chromedp.Click(chromedp.CSS(cardSel(low2)+` .todo-pick input`)))
	b.until("Esc clears the picks", `document.querySelector("#merge-bar").hidden`, chromedp.KeyEvent(kb.Escape))
	still()
	b.do("pick them again", chromedp.Click(chromedp.CSS(cardSel(low1)+` .todo-pick input`)))
	still()
	b.do("and the other", chromedp.Click(chromedp.CSS(cardSel(low2)+` .todo-pick input`)))
	b.until("merge them", fmt.Sprintf(`!document.querySelector(%q) && !!document.querySelector(%q) && document.querySelector("#merge-bar").hidden`, cardSel(low2), cardSel(low1)),
		chromedp.Click(chromedp.CSS(`#merge-bar .btn.primary`)))
	b.waitJS("the merged draft has both issues", fmt.Sprintf(`/Fix 2 issues/.test(document.querySelector(%q)?.textContent)`, cardSel(low1)))

	// The pane: the issues it has, and tags to edit.
	b.openTask(high)
	b.waitJS("the reported issue in the pane", `/Reported issue · reported 2 times/.test(document.querySelector("#d-finding")?.textContent) && /db\.go:9/.test(document.querySelector("#d-finding").textContent)`)
	b.do("type a tag", chromedp.Click(chromedp.CSS(`#d-tags .tag-input`)), chromedp.SendKeys(chromedp.CSS(`#d-tags .tag-input`), "urgent\r"))
	b.waitJS("the tag on the task and its card", fmt.Sprintf(`[...document.querySelectorAll("#d-tags .tag-chip")].some((e) => e.textContent.startsWith("urgent")) &&
		[...document.querySelectorAll(%q)].some((e) => e.textContent === "urgent")`, cardSel(high)+" .todo-tags .tag-chip"))
	b.until("remove it", `![...document.querySelectorAll("#d-tags .tag-chip")].some((e) => e.textContent.startsWith("urgent"))`,
		chromedp.Click(chromedp.CSS(`#d-tags .tag-x[aria-label="Remove tag urgent"]`)))
	b.do("close the task", chromedp.Click(chromedp.CSS(`#d-close`)))

	// A phone: the pick boxes show without hovering, the bar fits the screen.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.do("reload", chromedp.Reload())
	b.waitJS("the board", `document.querySelector("#project-name")?.textContent === "app"`)
	b.until("the drafts lane", `document.querySelector('#board .col[data-lane="draft"]')?.offsetWidth > 0`, chromedp.Click(chromedp.CSS(`.lane-tab[data-lane="draft"]`)))
	b.waitJS("From scans (1) on a phone", `document.querySelector("#tag-filter .scan-filter")?.offsetWidth > 0 && document.querySelector("#tag-filter .scan-filter").textContent === "From scans (1)"`)
	b.waitJS("a pick box shows without hovering", fmt.Sprintf(`getComputedStyle(document.querySelector(%q)).opacity === "1"`, cardSel(high)+" .todo-pick"))
	still()
	b.do("tap a pick box", chromedp.Click(chromedp.CSS(cardSel(high)+` .todo-pick input`)))
	still()
	b.do("tap another", chromedp.Click(chromedp.CSS(cardSel(plain.ID)+` .todo-pick input`)))
	b.waitJS("the bar on screen, above the nav", `(() => {
		const r = document.querySelector("#merge-bar").getBoundingClientRect(), nav = document.querySelector(".nav").getBoundingClientRect();
		return r.width > 0 && r.left >= 0 && r.right <= innerWidth && r.bottom <= nav.top && /2 selected/.test(document.querySelector("#merge-bar").textContent);
	})()`)
	b.do("clear", chromedp.Click(chromedp.CSS(`#merge-bar .link`)))
	b.do("a wide screen", chromedp.EmulateReset())

	// Old links to Issues open the Board, or the draft an issue became.
	b.do("an old Issues link", chromedp.Navigate(h.base+"/p/"+pid+"/issues"))
	b.waitJS("the Board", `document.querySelector("#view-board.active") && document.querySelector("#project-name")?.textContent === "app" && /\/board$/.test(location.pathname)`)
	b.do("an old link to one issue", chromedp.Navigate(h.base+"/p/"+pid+"/issues/"+high))
	b.waitJS("its draft, open", fmt.Sprintf(`document.querySelector("#detail") && !document.querySelector("#detail").hidden && location.pathname.endsWith("/tasks/%s")`, high))
}
