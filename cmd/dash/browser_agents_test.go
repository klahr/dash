package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// A runner's row in Runners shows the agents it found, and its page lists
// them, each with its version, sign-in, label and features, then the ones it
// didn't find; the project settings offer the agent its tasks run on, with
// no warning while a runner has it, and a warning and what it can't do for
// Codex, which no runner has here; Add project picks Claude Code, whose
// sign-in the fake claude doesn't say. On a wide screen and a phone's,
// none scrolls sideways.
func TestBrowserRunnerAgents(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	b := newBrowser(t, chrome)
	b.signIn(h)
	shot := func(name string) {
		if dir := os.Getenv("DASH_TEST_SHOTS"); dir != "" {
			buf, err := chromedp.Run(b.ctx, chromedp.FullScreenshot(90))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name+".jpg"), buf, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	b.do("a wide screen", chromedp.EmulateViewport(1280, 900))
	b.openStatus(`!!document.querySelector("#status-list .status-runner:not(.status-runner-more)")`)
	b.until("open the runner's page", `!!document.querySelector("#runner-detail .runner-agent")`,
		chromedp.Click(chromedp.CSS(`#status-list .status-runner:not(.status-runner-more)`)))
	b.waitJS("Claude Code with its features", `(() => {
		const a = document.querySelector("#runner-detail .runner-agent");
		return a.querySelector("strong").textContent === "Claude Code"
			&& a.querySelectorAll(".runner-agent-feature:not(.off)").length === 5;
	})()`)
	b.waitJS("its sign-in unknown, without a sign-in hint", `(() => {
		const a = document.querySelector("#runner-detail .runner-agent");
		return a.querySelector(".runner-agent-state").textContent === "Sign-in unknown"
			&& !a.querySelector(".runner-agent-signin.warn");
	})()`)
	b.waitJS("the agents it didn't find, with how to install them", `(() => {
		const m = document.querySelector("#runner-detail .runner-agents-missing");
		return !!m && m.textContent.includes("Codex") && m.textContent.includes("npm install -g @openai/codex");
	})()`)
	b.waitJS("Claude Code and its version on its row", `[...document.querySelectorAll("#runners-list .runner-row-agent")].some((c) => c.textContent === "Claude Code2.1.0")`)
	b.run("show its agents", `document.querySelector("#runner-detail .runner-agents-box").scrollIntoView(); true`)
	shot("runner-agents-wide")
	b.waitJS("its label among the default ones", `[...document.querySelectorAll("#runner-detail .runner-labels .runner-label.default")].some((l) => l.textContent === "claude")`)
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("no sideways scrolling", `document.querySelector("#runners-main").scrollWidth <= document.querySelector("#runners-main").clientWidth + 1`)
	b.run("show its agents", `document.querySelector("#runner-detail .runner-agents-box").scrollIntoView(); true`)
	shot("runner-agents-phone")
	b.run("show the overview", `document.querySelector('#runners-nav .rail-item[data-rail="overview"]').click(); true`)
	b.waitJS("its row's agents", `!!document.querySelector("#runners-list .runner-row-agent") && document.querySelector('#runners-main .rail-section[data-section="overview"]').classList.contains("current")`)
	shot("runners-overview-phone")
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
	b.run("pick Codex", `const sel = document.querySelector("#settings-agent");
		sel.value = "codex";
		sel.dispatchEvent(new Event("change", { bubbles: true }));
		true`)
	b.waitJS("a warning, and what Codex can't do", `(() => {
		const hint = document.querySelector("#settings-agent-hint");
		return !hint.hidden && hint.classList.contains("warn")
			&& hint.textContent.includes("No online runner has Codex") && hint.textContent.includes("can't ask you in Dash");
	})()`)
	shot("agent-codex-wide")
	b.waitJS("still on the Agent section", `document.querySelector('#settings-sheet .rail-item[data-rail="claude"]').getAttribute("aria-current") === "true"
		&& document.querySelector('#settings-sheet .rail-section[data-section="claude"]').classList.contains("current")`)
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("no sideways scrolling", `(() => { const m = document.querySelector("#settings-sheet .rail-main"); return m.scrollWidth <= m.clientWidth + 1; })()`)
	shot("agent-codex-phone")
	b.run("back to Claude Code", `const sel = document.querySelector("#settings-agent");
		sel.value = "claude";
		sel.dispatchEvent(new Event("change", { bubbles: true }));
		true`)
	b.waitJS("no warning again", `!document.querySelector("#settings-agent-hint").classList.contains("warn")`)
	b.until("close the settings", `!document.querySelector("#settings-sheet").open`, chromedp.KeyEvent(kb.Escape))

	b.do("a wide screen for Add project", chromedp.EmulateViewport(1280, 900))
	b.until("open Add project", `document.querySelector("#new-project-sheet").open`, chromedp.Click(chromedp.ID("side-new-project")))
	b.run("pick From URL", `document.querySelector('#new-project-sheet .rail-item[data-rail="url"]').click(); true`)
	b.waitJS("Claude Code picked, its sign-in unknown on the runner", `(() => {
		const sel = document.querySelector("#new-project-agent"), hint = document.querySelector("#new-project-agent-hint");
		return sel.value === "claude" && sel.options.length >= 3 && !hint.hidden && !hint.classList.contains("warn")
			&& hint.textContent.includes("laptop, which couldn't tell");
	})()`)
	b.run("pick Codex", `const sel = document.querySelector("#new-project-agent");
		sel.value = "codex";
		sel.dispatchEvent(new Event("change", { bubbles: true }));
		true`)
	b.waitJS("how to install Codex on the runner", `(() => {
		const hint = document.querySelector("#new-project-agent-hint");
		return hint.classList.contains("warn") && hint.textContent.includes("Not on laptop") && !!hint.querySelector("code");
	})()`)
	shot("add-project-agent-wide")
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("no sideways scrolling", `(() => { const m = document.querySelector("#new-project-sheet .rail-main"); return m.scrollWidth <= m.clientWidth + 1; })()`)
	shot("add-project-agent-phone")
	b.until("close Add project", `!document.querySelector("#new-project-sheet").open`, chromedp.KeyEvent(kb.Escape))
}
