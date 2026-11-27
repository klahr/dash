package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
)

// The board shows who created each task: their face on the card once the
// org has more than one member, their name in the task's pane always. Mine
// filters the board to the signed-in user's own tasks, is kept across a
// reload, and is hidden in an org of one.
func TestBrowserTaskCreatorAndMine(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	var mine struct{ ID string }
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Mine to do", "prompt": "Stay a draft"}, http.StatusCreated, &mine)

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.waitCard(mine.ID, "draft")
	b.waitJS("no face and no Mine in an org of one", fmt.Sprintf(`!document.querySelector(%q) && document.querySelector("#mine-filter").hidden && document.querySelector("#goal-filter").hidden`, cardSel(mine.ID)+" .todo-creator"))
	b.openTask(mine.ID)
	b.waitJS("the pane names its creator", `document.querySelector("#d-meta .created-by")?.textContent.endsWith("Created by admin")`)
	b.do("close the task", chromedp.Click(chromedp.CSS(`#d-close`)))

	// A second member, who creates a task of their own.
	var me struct{ Org struct{ ID int64 } }
	api.must("GET", "/api/me", nil, http.StatusOK, &me)
	var inv struct{ Token string }
	api.must("POST", fmt.Sprintf("/api/orgs/%d/invitations", me.Org.ID), map[string]any{"email": "ana@example.com", "password": h.password}, http.StatusCreated, &inv)
	ana := newClient(t, h.base)
	ana.must("POST", "/api/invitations/register", map[string]any{"token": inv.Token, "username": "ana", "password": "another-long-password"}, http.StatusCreated, nil)
	var theirs struct{ ID string }
	ana.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Ana's", "prompt": "Also a draft"}, http.StatusCreated, &theirs)

	b.do("reload", chromedp.Reload())
	b.waitCard(theirs.ID, "draft")
	b.waitJS("each card shows its creator's face", fmt.Sprintf(`document.querySelector(%q)?.title === "Created by ana" && document.querySelector(%q)?.title === "Created by admin"`,
		cardSel(theirs.ID)+" .todo-creator", cardSel(mine.ID)+" .todo-creator"))
	b.waitJS("Mine, off", `document.querySelector("#mine-filter")?.offsetWidth > 0 && document.querySelector("#mine-filter").getAttribute("aria-pressed") === "false"`)
	b.until("turn Mine on", fmt.Sprintf(`document.querySelector("#mine-filter").getAttribute("aria-pressed") === "true" && !document.querySelector(%q)`, cardSel(theirs.ID)),
		chromedp.Click(chromedp.CSS(`#mine-filter`)))
	b.waitCard(mine.ID, "draft")

	b.do("reload", chromedp.Reload())
	b.waitCard(mine.ID, "draft")
	b.waitJS("Mine kept on", fmt.Sprintf(`document.querySelector("#mine-filter").getAttribute("aria-pressed") === "true" && !document.querySelector(%q)`, cardSel(theirs.ID)))

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the one-lane layout", `!matchMedia("(min-width: 1024px)").matches && [...document.querySelectorAll("#board .col")].filter((c) => c.offsetWidth).length === 1`)
	// The switch reflows the board head (a taller button for touch, the
	// filters row wrapping): click only once the button stays put between two
	// checks and is what a click at its centre hits, or the click can land on
	// a card where the button was.
	b.waitJS("Mine shown and settled on a phone", `(() => {
		const el = document.querySelector("#mine-filter");
		const r = el?.getBoundingClientRect();
		if (!r || !r.width) return false;
		const at = [r.left, r.top, r.width, r.height].join();
		const settled = window.__e2eMineRect === at;
		window.__e2eMineRect = at;
		return settled && el.contains(document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2));
	})()`)
	// Should a click still miss and open a task, its pane covers the board on
	// a phone: close it before the next try.
	b.until("turn Mine off", fmt.Sprintf(`(() => {
		if (document.querySelector("#mine-filter").getAttribute("aria-pressed") === "false" && !!document.querySelector(%q)) return true;
		if (!document.querySelector("#detail").hidden) document.querySelector("#d-close")?.click();
		return false;
	})()`, cardSel(theirs.ID)),
		chromedp.Click(chromedp.CSS(`#mine-filter`)))
	b.waitJS("the face on a phone's card", fmt.Sprintf(`document.querySelector(%q)?.offsetWidth > 0`, cardSel(theirs.ID)+" .todo-creator .avatar"))
	b.openTask(theirs.ID)
	b.waitJS("the pane names the other member", `document.querySelector("#d-meta .created-by")?.textContent.endsWith("Created by ana")`)
}
