package main

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// The web app's navigation: Planning set apart below a divider, page
// headings that match the nav, one Shell page with a "main / commit…"
// picker (Try's old link opens it at a commit), the shortcut keys and the
// Keyboard sheet's table per area, and the project's actions in groups,
// with Clean up saying what it removed. On a wide screen and a phone's.
func TestBrowserNavigation(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	pid := h.project.ID
	b := newBrowser(t, chrome)
	b.signIn(h)
	key := func(what, k, expr string) { b.until(what, expr, chromedp.KeyEvent(k)) }

	// Planning comes last, after the Organization divider; the project's sections before it.
	b.waitJS("Planning below the divider", `(() => {
		const els = [...document.querySelectorAll(".nav > button[data-view], .nav > .nav-sep")];
		const sep = els.findIndex((e) => e.matches(".nav-sep"));
		return sep > 0 && getComputedStyle(els[sep]).display !== "none" && els[sep].textContent === "Organization"
			&& els.slice(sep + 1).map((e) => e.dataset.view).join() === "planning";
	})()`)

	// Checklist's heading is the nav's label.
	b.until("open Checklist", `document.querySelector("#view-checklist.active")`, chromedp.Click(chromedp.CSS(`.nav > button[data-view=checklist]`)))
	b.waitJS("its heading", `document.querySelector("#view-checklist h2").textContent === "Checklist"`)

	// Keys: gn is Scans, gs the Shell (no gS any more).
	b.run("leave any field", `document.activeElement?.blur(); true`)
	key("g", "g", `document.querySelector("#kbd-status").textContent.includes("g")`)
	key("n: Scans", "n", `document.querySelector("#view-scans.active")`)
	key("g", "g", `document.querySelector("#kbd-status").textContent.includes("g")`)
	key("s: the Shell", "s", `document.querySelector("#view-run.active:not(.at-commit)")`)
	b.waitJS("the Shell at main", `document.querySelector("#shell-at-main").getAttribute("aria-pressed") === "true" && location.pathname === "/p/`+pid+`/run"`)
	// Not a CLI project: commit… says why it can't.
	b.waitJS("commit… off, with why", `document.querySelector("#shell-at-commit").disabled && document.querySelector("#shell-at-commit").title.includes("CLI projects")`)
	b.waitJS("no Try in the sidebar", `!document.querySelector("#quick-try")`)

	// Try's old link opens the Shell page at a commit, under the nav's Shell.
	b.do("open Try's old link", chromedp.Navigate(h.base+"/p/"+pid+"/try"))
	b.waitJS("the Shell at a commit", `document.querySelector("#view-run.active.at-commit") && document.querySelector('.nav > button[data-view=run]').getAttribute("aria-current") === "page"`)
	b.waitJS("commit… pressed", `document.querySelector("#shell-at-commit").getAttribute("aria-pressed") === "true" && !document.querySelector("#try-status").hidden`)
	b.until("back to main", `document.querySelector("#view-run.active:not(.at-commit)") && location.pathname === "/p/`+pid+`/run"`, chromedp.Click(chromedp.ID("shell-at-main")))

	// ? opens the Keyboard sheet at the page's area, a table per area.
	b.run("leave the terminal", `document.activeElement?.blur(); true`)
	key("?", "?", `document.querySelector("#keys-sheet").open`)
	b.waitJS("at the Shell's keys", `document.querySelector("#keys-sheet .rail-item[aria-current]")?.dataset.rail === "shell" && document.querySelector('#keys-sheet .rail-section.current table.keys-table th kbd')`)
	b.waitJS("an area per table", `document.querySelectorAll("#keys-sheet .rail-section").length === document.querySelectorAll("#keys-sheet .rail-section > table.keys-table").length`)
	b.until("its Approvals: X denies", `document.querySelector("#keys-sheet .rail-section.current")?.dataset.section === "perms"`,
		chromedp.Click(chromedp.CSS(`#keys-sheet .rail-item[data-rail=perms]`)))
	b.waitJS("X, not n", `[...document.querySelectorAll("#keys-sheet .rail-section.current kbd")].map((k) => k.textContent).join() === "y,A,X"`)
	b.until("close it", `!document.querySelector("#keys-sheet").open`, chromedp.KeyEvent(kb.Escape))

	// The project menu: groups, Clean up with its own icon; it says what it removed.
	b.until("open the project menu", `document.querySelector("#project-menu").open`, chromedp.Click(chromedp.CSS(`#project-menu > summary`)))
	b.waitJS("grouped, without Builds", `document.querySelectorAll("#project-menu .menu-sep").length === 2 && !document.querySelector("#project-menu #pm-builds")`)
	b.waitJS("Clean up's icon isn't Delete's", `document.querySelector("#pm-cleanup svg").innerHTML !== document.querySelector("#pm-delete svg").innerHTML`)
	b.until("clean up", `document.querySelector("#toasts")?.textContent.match(/(Cleaned up|Nothing to clean up in) app/)`, chromedp.Click(chromedp.ID("pm-cleanup")))
	b.waitJS("what it removed", `/Removed |No leftover worktrees/.test(document.querySelector("#toasts").textContent)`)
	b.waitJS("Push isn't in the top bar", `!document.querySelector(".topbar #push-main")`)
	b.waitJS("the live dot says so", `document.querySelector("#live-label").offsetWidth > 0 && document.querySelector("#live-label").textContent === "Live"`)

	// A phone: the bottom nav has no divider; the menu drawer has Planning below it.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.do("reload", chromedp.Reload())
	b.waitJS("the board", `document.querySelector("#project-name")?.textContent === "app"`)
	b.waitJS("no divider in the bottom nav", `getComputedStyle(document.querySelector(".nav > .nav-sep")).display === "none"`)
	b.until("open the menu", `document.querySelector(".app").classList.contains("menu-open")`, chromedp.Click(chromedp.ID("menu-open")))
	b.waitJS("Planning below the drawer's divider", `document.querySelector(".menu-views .nav-sep + [data-view=planning]")?.offsetHeight > 0`)
	b.do("a wide screen", chromedp.EmulateReset())
}
