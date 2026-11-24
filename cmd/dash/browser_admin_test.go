package main

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// The Admin sheet (account menu → Admin, platform admins): every account
// with search, a user's detail in a pane beside the list that Esc closes,
// a reset link shown once; and on a phone the pane in the list's place,
// with Back. Organizations list the default org, which can't be deleted.
func TestBrowserAdminSheet(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)

	// A second account, bea, a member of the default org.
	var me struct{ Org struct{ ID int64 } }
	h.api.must("GET", "/api/me", nil, http.StatusOK, &me)
	var inv struct{ Token string }
	h.api.must("POST", "/api/orgs/"+strconv.FormatInt(me.Org.ID, 10)+"/invitations",
		map[string]any{"email": "bea@example.com", "role": "member", "password": h.password}, http.StatusCreated, &inv)
	newClient(t, h.base).must("POST", "/api/invitations/register",
		map[string]string{"token": inv.Token, "username": "bea", "password": "bea-long-password"}, http.StatusCreated, nil)

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open the account menu", `document.querySelector("#foot-menu").open`, chromedp.Click(chromedp.CSS(`#foot-menu > summary`)))
	b.until("open Admin", `document.querySelector("#admin-sheet").open`, chromedp.Click(chromedp.ID("admin-open")))
	b.waitJS("every account, counted", `document.querySelectorAll("#admin-users-list .admin-row").length === 2 &&
		document.querySelector("#admin-users-count").textContent.startsWith("2 accounts")`)

	// Search.
	b.do("search for bea", chromedp.SendKeys(chromedp.ID("admin-users-q"), "bea"))
	b.waitJS("one match", `document.querySelectorAll("#admin-users-list .admin-row").length === 1 &&
		document.querySelector("#admin-users-count").textContent.includes("1 matching")`)

	// Her detail, beside the list on a wide screen.
	b.until("open bea", `!document.querySelector("#admin-user-detail").hidden`, chromedp.Click(chromedp.CSS(`#admin-users-list .admin-row`)))
	b.waitJS("beside the list", `document.querySelector("#admin-user-detail h3")?.textContent.endsWith("bea") &&
		document.querySelector("#admin-users .admin-list").offsetWidth > 0 &&
		document.querySelector("#admin-users-list .admin-row[aria-current=true]") &&
		document.querySelector("#admin-user-detail").textContent.includes("Reset password")`)

	// A reset link, shown once with a Copy button.
	b.until("reset her password", `[...document.querySelectorAll("dialog[open] h2")].some((x) => x.textContent === "Reset bea's password?")`,
		chromedp.Click(chromedp.CSS(`#admin-user-detail .admin-actions button:first-child`)))
	b.do("confirm with the password",
		chromedp.SendKeys(chromedp.CSS(`dialog[open] input[name=password]`), h.password),
		chromedp.Click(chromedp.CSS(`dialog[open] .sheet-actions .btn.primary`)))
	b.waitJS("the link", `[...document.querySelectorAll("dialog[open] .copy-text")].some((x) => x.textContent.includes("/login.html#reset="))`)
	b.until("done", `![...document.querySelectorAll("dialog[open] h2")].some((x) => x.textContent.startsWith("Reset link"))`,
		chromedp.Click(chromedp.CSS(`dialog[open] .sheet-actions .btn.primary`)))

	// Esc closes the pane, then the sheet.
	b.until("Esc closes the pane", `document.querySelector("#admin-user-detail").hidden && document.querySelector("#admin-sheet").open`, chromedp.KeyEvent(kb.Escape))
	b.until("Esc closes the sheet", `!document.querySelector("#admin-sheet").open`, chromedp.KeyEvent(kb.Escape))

	// On a phone: the pane in the list's place, with Back.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	// On a phone the account menu is in the drawer, off screen until Menu opens it.
	b.openDrawer("")
	b.until("open the account menu", `document.querySelector("#foot-menu").open`, chromedp.Click(chromedp.CSS(`#foot-menu > summary`)))
	b.until("open Admin", `document.querySelector("#admin-sheet").open`, chromedp.Click(chromedp.ID("admin-open")))
	b.until("open a user", `!document.querySelector("#admin-user-detail").hidden`, chromedp.Click(chromedp.CSS(`#admin-users-list .admin-row`)))
	b.waitJS("full screen", `document.querySelector("#admin-users .admin-list").offsetWidth === 0 &&
		document.querySelector("#admin-sheet .rail-nav").offsetWidth === 0 && document.querySelector("#admin-user-detail .back-word").offsetWidth > 0`)
	b.until("Back", `document.querySelector("#admin-user-detail").hidden && document.querySelector("#admin-users .admin-list").offsetWidth > 0`,
		chromedp.Click(chromedp.CSS(`#admin-user-detail .admin-back`)))

	// Organizations: the default org, which can't be deleted.
	b.until("show Organizations", `document.querySelector('#admin-sheet .rail-section[data-section="orgs"]').classList.contains("current")`,
		chromedp.Click(chromedp.CSS(`#admin-sheet .rail-item[data-rail="orgs"]`)))
	b.waitJS("the default org", `document.querySelector("#admin-orgs-list .admin-row")?.textContent.includes("2 members")`)
	b.until("open it", `!document.querySelector("#admin-org-detail").hidden`, chromedp.Click(chromedp.CSS(`#admin-orgs-list .admin-row`)))
	b.waitJS("not deletable", `[...document.querySelectorAll("#admin-org-detail button")].find((x) => x.textContent.includes("Delete organization"))?.disabled &&
		document.querySelector("#admin-org-detail").textContent.includes("records without an organization")`)
}
