package main

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// A runner's page lists the agents it reported, each with its version,
// login, label and features; the project settings offer the agent its
// tasks run on, with no warning while a runner has it. On a wide screen
// and a phone's, neither scrolls sideways.
func TestBrowserRunnerAgents(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	b := newBrowser(t, chrome)
	b.signIn(h)

	b.do("a wide screen", chromedp.EmulateViewport(1280, 900))
	b.waitJS("the runner in the sidebar", `!!document.querySelector("#side-runner-list .side-runner:not(.side-runner-more)")`)
	b.until("open the runner's page", `!!document.querySelector("#runner-detail .runner-agent")`,
		chromedp.Click(chromedp.CSS(`#side-runner-list .side-runner:not(.side-runner-more)`)))
	b.waitJS("Claude Code with its features", `(() => {
		const a = document.querySelector("#runner-detail .runner-agent");
		return a.querySelector("strong").textContent === "Claude Code"
			&& a.querySelectorAll(".runner-agent-feature:not(.off)").length === 4;
	})()`)
	b.waitJS("its label among the default ones", `[...document.querySelectorAll("#runner-detail .runner-labels .runner-label.default")].some((l) => l.textContent === "claude")`)
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("no sideways scrolling", `document.querySelector("#runners-main").scrollWidth <= document.querySelector("#runners-main").clientWidth + 1`)
	b.until("close the Runners sheet", `!document.querySelector("#runners-sheet").open`, chromedp.KeyEvent(kb.Escape))

	b.do("a wide screen again", chromedp.EmulateViewport(1280, 900))
	b.until("open the project menu", `document.querySelector("#project-menu").open`, chromedp.Click(chromedp.CSS(`#project-menu > summary`)))
	b.until("open the settings", `document.querySelector("#settings-sheet").open`, chromedp.Click(chromedp.ID("pm-settings")))
	b.until("show its agent", `document.querySelector('#settings-sheet .rail-section[data-section="claude"]').classList.contains("current")`,
		chromedp.Click(chromedp.CSS(`#settings-sheet .rail-item[data-rail="claude"]`)))
	b.waitJS("Claude Code picked, and no warning", `(() => {
		const sel = document.querySelector("#settings-agent");
		return sel.value === "claude" && sel.selectedOptions[0].textContent === "Claude Code"
			&& !document.querySelector("#settings-agent-hint").classList.contains("warn");
	})()`)
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("no sideways scrolling", `(() => { const m = document.querySelector("#settings-sheet .rail-main"); return m.scrollWidth <= m.clientWidth + 1; })()`)
	b.until("close the settings", `!document.querySelector("#settings-sheet").open`, chromedp.KeyEvent(kb.Escape))
}
