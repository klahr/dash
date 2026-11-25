package main

import (
	"context"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// The web app's navigation: the project's pages Board · Goals · History ·
// Code, Planning set apart below a divider, Code's Files and Shell tabs (the
// Shell with a "main / commit…" picker; Try's old link opens it at a
// commit), History's Commits and Deploys tabs, Scans as a sheet over the
// Board, old links and keys opening where their pages moved, the Keyboard
// sheet's table per area, and the project's actions in groups, with Clean up
// saying what it removed. On a wide screen and a phone's.
func TestBrowserNavigation(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	pid := h.project.ID
	b := newBrowser(t, chrome)
	b.signIn(h)
	key := func(what, k, expr string) { b.until(what, expr, chromedp.KeyEvent(k)) }

	// The rail lists each page once, in order (the drawer's pages among the nav's own):
	// Board, Goals, History, Code; Planning comes last, after the Organization divider.
	b.waitJS("the pages in order, Planning below the divider", `(() => {
		const els = [...document.querySelectorAll(".nav [data-view], .nav > .nav-sep")].filter((e) => e.offsetHeight > 0)
			.sort((a, b) => a.getBoundingClientRect().top - b.getBoundingClientRect().top);
		const sep = els.find((e) => e.matches(".nav-sep"));
		return sep?.textContent === "Organization"
			&& els.map((e) => e.dataset.view || "|").join() === "board,goals,history,code,|,planning";
	})()`)
	// The sidebar has no Actions: Run tests, Push and Builds… are in Run's ▾ menu, Deploy on the Deploys page.
	b.waitJS("no Actions in the sidebar", `document.querySelector("#run-split") && !document.querySelector("#quick-actions, #quick-test, #push-main, #pm-builds")`)
	b.until("open Run's ▾", `document.querySelector(".pop-menu") && document.querySelector("#run-pick").getAttribute("aria-expanded") === "true"`,
		chromedp.Click(chromedp.ID("run-pick")))
	// The app's project has no test command, pushes through its runner and has no builds yet: Run and Configure… only.
	b.waitJS("Run, then Configure…", `[...document.querySelectorAll(".pop-menu .menu-label")].map((e) => e.textContent).join() === "Run,Configure…"`)
	b.until("close it", `!document.querySelector(".pop-menu")`, chromedp.KeyEvent(kb.Escape))

	// The Checklist's old link opens the board.
	b.do("open the Checklist's old link", chromedp.Navigate(h.base+"/p/"+pid+"/checklist"))
	b.waitJS("the board", `document.querySelector("#view-board.active") && location.pathname === "/p/`+pid+`/board"`)
	b.waitJS("no Checklist", `!document.querySelector("#view-checklist, [data-view=checklist]")`)

	// Old links open where their pages moved: Scans' the Scans sheet over the
	// board, Files' Code → Files, Deploys' History → Deploys.
	b.do("open Scans' old link", chromedp.Navigate(h.base+"/p/"+pid+"/scans"))
	b.waitJS("the Scans sheet over the board", `document.querySelector("#scans-sheet").open && document.querySelector("#view-board.active")
		&& location.pathname === "/p/`+pid+`/board" && new URLSearchParams(location.search).get("sheet") === "scans"`)
	b.until("close it", `!document.querySelector("#scans-sheet").open && !new URLSearchParams(location.search).has("sheet")`, chromedp.KeyEvent(kb.Escape))
	b.waitJS("From scans and Scans… on the board", `document.querySelector("#board-filters:not([hidden]) .scan-filter") && document.querySelector(".scans-open")`)
	b.until("Scans… opens the sheet", `document.querySelector("#scans-sheet").open`, chromedp.Click(chromedp.CSS(`.scans-open`)))
	b.until("close it", `!document.querySelector("#scans-sheet").open`, chromedp.KeyEvent(kb.Escape))
	b.do("open Files' old link", chromedp.Navigate(h.base+"/p/"+pid+"/files"))
	b.waitJS("Code → Files", `document.querySelector("#view-files.active") && location.pathname === "/p/`+pid+`/code"
		&& document.querySelector('.nav [data-view=code]').getAttribute("aria-current") === "page"
		&& document.querySelector('#view-files .page-tabs [data-tab=files]').getAttribute("aria-current") === "page"`)
	b.do("open Deploys' old link", chromedp.Navigate(h.base+"/p/"+pid+"/deploys"))
	b.waitJS("History → Deploys", `document.querySelector("#view-deploys.active")
		&& document.querySelector('.nav [data-view=history]').getAttribute("aria-current") === "page"
		&& document.querySelector('#view-deploys .page-tabs [data-tab=deploys]').getAttribute("aria-current") === "page"`)
	b.until("its Commits tab", `document.querySelector("#view-history.active") && location.pathname === "/p/`+pid+`/history"`,
		chromedp.Click(chromedp.CSS(`#view-deploys .page-tabs [data-tab=history]`)))

	// Keys: gn opens Scans, gs Code → Shell (no gS any more).
	b.run("leave any field", `document.activeElement?.blur(); true`)
	key("g", "g", `document.querySelector("#kbd-status").textContent.includes("g")`)
	key("n: Scans", "n", `document.querySelector("#scans-sheet").open && document.querySelector("#view-board.active")`)
	b.until("close it", `!document.querySelector("#scans-sheet").open`, chromedp.KeyEvent(kb.Escape))
	b.run("leave any field", `document.activeElement?.blur(); true`)
	key("g", "g", `document.querySelector("#kbd-status").textContent.includes("g")`)
	key("s: the Shell", "s", `document.querySelector("#view-run.active:not(.at-commit)")`)
	b.waitJS("the Shell at main", `document.querySelector("#shell-at-main").getAttribute("aria-pressed") === "true" && location.pathname === "/p/`+pid+`/shell"`)
	// Code opens on the tab shown last; the Files tab switches.
	b.until("Code → Files", `document.querySelector("#view-files.active") && location.pathname === "/p/`+pid+`/code"`,
		chromedp.Click(chromedp.CSS(`#view-run .page-tabs [data-tab=files]`)))
	b.until("the board", `document.querySelector("#view-board.active")`, chromedp.Click(chromedp.CSS(`.nav > button[data-view=board]`)))
	b.until("Code, on Files again", `document.querySelector("#view-files.active")`, chromedp.Click(chromedp.CSS(`.nav [data-view=code]`)))
	b.do("open Run's old link", chromedp.Navigate(h.base+"/p/"+pid+"/run"))
	b.waitJS("Code → Shell", `document.querySelector("#view-run.active:not(.at-commit)") && location.pathname === "/p/`+pid+`/shell"`)
	// Not a CLI project: commit… says why it can't.
	b.waitJS("commit… off, with why", `document.querySelector("#shell-at-commit").disabled && document.querySelector("#shell-at-commit").title.includes("CLI projects")`)
	b.waitJS("no Try in the sidebar", `!document.querySelector("#quick-try")`)

	// Try's old link opens the Shell at a commit, under the nav's Code.
	b.do("open Try's old link", chromedp.Navigate(h.base+"/p/"+pid+"/try"))
	b.waitJS("the Shell at a commit", `document.querySelector("#view-run.active.at-commit") && document.querySelector('.nav [data-view=code]').getAttribute("aria-current") === "page"`)
	b.waitJS("commit… pressed", `document.querySelector("#shell-at-commit").getAttribute("aria-pressed") === "true" && !document.querySelector("#try-status").hidden`)
	b.until("back to main", `document.querySelector("#view-run.active:not(.at-commit)") && location.pathname === "/p/`+pid+`/shell"`, chromedp.Click(chromedp.ID("shell-at-main")))

	// ? opens the Keyboard sheet at the page's area, a table per area. The
	// Shell's terminal focuses itself once it starts, which may be after a
	// blur: each try leaves it again before the key.
	b.until("?", `document.querySelector("#keys-sheet").open`, func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		if _, err := chromedp.Evaluate[bool](`document.activeElement?.blur(), true`)(ctx, t); err != nil {
			return chromedp.Void{}, err
		}
		return chromedp.KeyEvent("?")(ctx, t)
	})
	b.waitJS("at the Shell's keys", `document.querySelector("#keys-sheet .rail-item[aria-current]")?.dataset.rail === "shell" && document.querySelector('#keys-sheet .rail-section.current table.keys-table th kbd')`)
	b.waitJS("an area per table", `document.querySelectorAll("#keys-sheet .rail-section").length === document.querySelectorAll("#keys-sheet .rail-section > table.keys-table").length`)
	b.until("its Approvals: X denies", `document.querySelector("#keys-sheet .rail-section.current")?.dataset.section === "perms"`,
		chromedp.Click(chromedp.CSS(`#keys-sheet .rail-item[data-rail=perms]`)))
	b.waitJS("X, not n", `[...document.querySelectorAll("#keys-sheet .rail-section.current kbd")].map((k) => k.textContent).join() === "y,A,X"`)
	b.until("close it", `!document.querySelector("#keys-sheet").open`, chromedp.KeyEvent(kb.Escape))

	// The project menu: groups, no Clean up any more (it moved to Project settings → General).
	b.until("open the project menu", `document.querySelector("#project-menu").open`, chromedp.Click(chromedp.CSS(`#project-menu > summary`)))
	b.waitJS("grouped, without Builds or Clean up", `document.querySelectorAll("#pm-project .menu-sep").length === 3 && !document.querySelector("#project-menu #pm-builds, #project-menu #pm-cleanup")`)
	// Project settings → General: Clean up with its own icon; it says what it removed.
	b.until("open the settings", `document.querySelector("#settings-sheet").open`, chromedp.Click(chromedp.ID("pm-settings")))
	b.until("show its repository, in General", `document.querySelector('#settings-sheet .rail-section[data-section="general"]').classList.contains("current")`,
		chromedp.Click(chromedp.CSS(`#settings-sheet .rail-item[data-rail="general"]`)))
	b.waitJS("Clean up's icon is the broom, not a submit", `document.querySelector("#settings-cleanup svg") && document.querySelector("#settings-cleanup").type === "button"`)
	b.until("clean up", `document.querySelector("#toasts")?.textContent.match(/(Cleaned up|Nothing to clean up in) app/)`, chromedp.Click(chromedp.ID("settings-cleanup")))
	b.waitJS("what it removed", `/Removed |No leftover worktrees/.test(document.querySelector("#toasts").textContent)`)
	b.waitJS("enabled again, the sheet still open", `!document.querySelector("#settings-cleanup").disabled && document.querySelector("#settings-sheet").open`)
	b.until("close the settings", `!document.querySelector("#settings-sheet").open`, chromedp.KeyEvent(kb.Escape))
	// The router drops the sheet from the URL a turn later: the reload below
	// would open it again over the phone's page.
	b.waitJS("the URL without the sheet", `!new URLSearchParams(location.search).has("sheet")`)
	b.waitJS("Push isn't in the top bar", `!document.querySelector(".topbar #push-main")`)
	b.waitJS("the status icon is quiet and live", `(() => {
		const s = document.querySelector("#status-btn");
		return s.offsetWidth > 0 && s.dataset.live === "on" && s.dataset.tone === "ok" && getComputedStyle(s.querySelector(".status-dot")).display === "none";
	})()`)
	// It opens the popover under it, with the connection first; Esc closes it and the focus goes back.
	b.openStatus(`document.querySelector("#status-list .status-row[data-key=live]")?.textContent.includes("Connected")`)
	b.waitJS("the popover under the icon", `(() => {
		const p = document.querySelector("#status-pop").getBoundingClientRect(), s = document.querySelector("#status-btn").getBoundingClientRect();
		return p.top >= s.bottom - 8 && p.width < innerWidth / 2;
	})()`)
	b.until("Esc closes the status", `!document.querySelector("#status-pop").open && document.activeElement?.id === "status-btn"`, chromedp.KeyEvent(kb.Escape))

	// A phone: the bottom nav is Board · Chat · Planning · Goals, left to right, with no
	// divider; History and Code are in the menu drawer.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.do("reload", chromedp.Reload())
	b.waitJS("the board", `document.querySelector("#project-name")?.textContent === "app"`)
	b.waitJS("no divider in the bottom nav", `getComputedStyle(document.querySelector(".nav > .nav-sep")).display === "none"`)
	b.waitJS("the bottom nav's tabs in order", `[...document.querySelectorAll(".nav > button")].filter((e) => e.offsetWidth > 0)
		.sort((a, b) => a.getBoundingClientRect().left - b.getBoundingClientRect().left)
		.map((e) => e.querySelector("span").textContent).join() === "Board,Chat,Planning,Goals"`)
	b.until("open Goals", `document.querySelector('.nav > button[data-view=goals][aria-current=page].active') && document.querySelector("#view-goals.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=goals]`)))
	b.openDrawer("")
	b.waitJS("History and Code in the drawer, nothing else", `[...document.querySelectorAll(".menu-views .menu-view")].filter((e) => e.offsetHeight > 0)
		.map((e) => e.dataset.view).join() === "history,code"`)
	b.until("open Code from the drawer", `document.querySelector("#view-run.active, #view-files.active") && document.querySelector('.menu-view[data-view=code][aria-current=page]') && !document.querySelector(".app").classList.contains("menu-open")`,
		chromedp.Click(chromedp.CSS(`.menu-view[data-view=code]`)))
	b.waitJS("its tabs fit", `[...document.querySelectorAll(".view.active .page-tabs button")].every((e) => e.offsetWidth > 0 && e.getBoundingClientRect().right <= innerWidth)`)
	b.openDrawer("")
	b.until("History from the drawer, its tabs", `document.querySelector("#view-history.active .page-tabs [data-tab=deploys]")?.offsetWidth > 0`,
		chromedp.Click(chromedp.CSS(`.menu-view[data-view=history]`)))
	b.do("a wide screen", chromedp.EmulateReset())
}
