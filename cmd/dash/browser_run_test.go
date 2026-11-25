package main

import (
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// The header's Run button on a CLI project: ▶ Run runs the latest commit on
// the runner and opens its terminal, turning into ■ Terminate (the same
// width, keeping focus) with the commit and runner beside it; Terminate stops
// it and it is Run again. ▾ lists the runners, then Pick a commit…, and works
// with the keyboard; it stays while a run goes, and has Run tests with its key and how it went. On a wide screen and a phone's. A web project with no
// run command shows it greyed, and clicking it opens Configure….
func TestBrowserRunButton(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	pid := h.project.ID
	b := newBrowser(t, chrome)
	b.signIn(h)

	// A web project with no run command: Run shows, greyed and saying why; a click
	// still opens Project settings → Commands.
	b.waitJS("the board", `document.querySelector("#project-name")?.textContent === "app"`)
	b.waitJS("a greyed Run on a web project", `(() => {
		const m = document.querySelector("#run-main");
		return !document.querySelector("#run-split").hidden && !m.disabled && m.classList.contains("off")
			&& m.getAttribute("aria-disabled") === "true" && m.title.startsWith("No run command set");
	})()`)
	b.until("click the greyed Run", `document.querySelector("#settings-sheet").open`, chromedp.Click(chromedp.ID("run-main")))
	b.waitJS("at Commands", `document.querySelector('#settings-sheet .rail-section[data-section="commands"]').classList.contains("current")`)
	b.until("close the settings", `!document.querySelector("#settings-sheet").open`, chromedp.KeyEvent(kb.Escape))
	// Closing the sheet goes Back (router.js) a moment later; navigating before
	// that lands has Chrome abort the load (net::ERR_ABORTED).
	b.waitJS("the sheet gone from the address", `!new URLSearchParams(location.search).has("sheet")`)
	h.api.must("PATCH", "/api/projects/"+pid, map[string]any{"preview_kind": "terminal", "preview_command": "echo run-button-ok; exec sh"}, http.StatusOK, nil)

	for _, size := range []struct {
		name string
		view chromedp.Action[chromedp.Void]
	}{
		{"wide", chromedp.EmulateViewport(1280, 900)},
		{"phone", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch)},
	} {
		b.do(size.name+": its screen", size.view, chromedp.Navigate(h.base+"/p/"+pid+"/board"))
		b.waitJS(size.name+": ▶ Run, ready", `(() => {
			const m = document.querySelector("#run-main");
			return !document.querySelector("#run-split").hidden && !m.disabled && !m.classList.contains("stop") && !m.classList.contains("off")
				&& m.textContent === "Run" && m.title.includes("laptop") && !document.querySelector("#run-pick").hidden;
		})()`)
		b.run(size.name+": remember Run's width", `window.runWidth = document.querySelector("#run-main").offsetWidth; true`)

		// ▾: the runners, online, then Pick a commit…, then Configure… last; arrows move, Esc closes it back on ▾.
		b.until(size.name+": open the list", `!!document.querySelector(".pop-menu")`, chromedp.Click(chromedp.ID("run-pick")))
		b.waitJS(size.name+": laptop online, then Pick a commit…, then Configure…", `(() => {
			const rows = [...document.querySelectorAll(".pop-menu button")];
			return rows.length === 3 && rows[0].textContent.includes("laptop") && rows[0].textContent.includes("online") && !rows[0].disabled
				&& rows[1].textContent.includes("Pick a commit") && rows[2].textContent.includes("Configure…")
				&& rows[2].previousElementSibling?.matches("hr.menu-sep") && document.activeElement === rows[0];
		})()`)
		b.until(size.name+": ArrowDown", `document.activeElement?.textContent.includes("Pick a commit")`, chromedp.KeyEvent(kb.ArrowDown))
		b.until(size.name+": Esc", `!document.querySelector(".pop-menu") && document.activeElement?.id === "run-pick"`, chromedp.KeyEvent(kb.Escape))

		// Configure…: the arrows reach it, Enter opens Project settings → Commands; then back.
		b.until(size.name+": open the list again", `!!document.querySelector(".pop-menu")`, chromedp.Click(chromedp.ID("run-pick")))
		b.until(size.name+": ArrowUp to Configure…", `document.activeElement?.textContent.includes("Configure…")`, chromedp.KeyEvent(kb.ArrowUp))
		b.until(size.name+": Enter", `document.querySelector("#settings-sheet").open`, chromedp.KeyEvent(kb.Enter))
		b.waitJS(size.name+": at Commands", `document.querySelector('#settings-sheet .rail-section[data-section="commands"]').classList.contains("current")
			&& new URLSearchParams(location.search).get("section") === "commands" && !document.querySelector(".pop-menu")`)
		b.until(size.name+": close the settings", `!document.querySelector("#settings-sheet").open`, chromedp.KeyEvent(kb.Escape))
		b.waitJS(size.name+": the sheet gone from the address", `!new URLSearchParams(location.search).has("sheet")`)

		// ▶ Run: Terminate, as wide, with the commit and runner beside it, and the terminal open.
		b.until(size.name+": Run", `document.querySelector("#run-main").classList.contains("stop")`, chromedp.Click(chromedp.ID("run-main")))
		b.waitJS(size.name+": ■ Terminate", `(() => {
			const m = document.querySelector("#run-main");
			return m.textContent === "Terminate" && m.offsetWidth === window.runWidth && !document.querySelector("#run-pick").hidden
				&& document.activeElement === m;
		})()`)
		b.waitJS(size.name+": the terminal", `document.querySelector("#view-run.active.at-commit") && !document.querySelector("#try-term-wrap").hidden
			&& [...document.querySelectorAll("#try-term .xterm-rows > div")].some((r) => r.textContent.includes("run-button-ok"))`)
		b.waitJS(size.name+": the commit beside it, linking to the terminal", `(() => {
			const a = document.querySelector("#run-at");
			return !a.hidden && /^[0-9a-f]{7}/.test(a.textContent) && a.textContent.includes("on laptop") && a.getAttribute("href") === "/p/`+pid+`/try";
		})()`)

		// ▾ stays, for the project's other actions: Terminate first instead of the runners.
		b.until(size.name+": ▾ while it runs", `!!document.querySelector(".pop-menu")`, chromedp.Click(chromedp.ID("run-pick")))
		b.waitJS(size.name+": Terminate, then Configure…", `[...document.querySelectorAll(".pop-menu .menu-label")].map((e) => e.textContent).join() === "Terminate,Configure…"`)
		b.until(size.name+": close ▾", `!document.querySelector(".pop-menu")`, chromedp.KeyEvent(kb.Escape))

		// ■ Terminate: stopped, Run again, focus still on it.
		b.until(size.name+": Terminate", `!document.querySelector("#run-main").classList.contains("stop")`, chromedp.Click(chromedp.ID("run-main")))
		b.waitJS(size.name+": ▶ Run again", `(() => {
			const m = document.querySelector("#run-main");
			return m.textContent === "Run" && m.offsetWidth === window.runWidth && document.activeElement === m
				&& !document.querySelector("#run-pick").hidden && document.querySelector("#run-at").hidden;
		})()`)
		var st struct{ Session any }
		h.api.must("GET", "/api/projects/"+pid+"/try", nil, http.StatusOK, &st)
		if st.Session != nil {
			t.Fatalf("%s: Terminate left the session running: %+v", size.name, st.Session)
		}
	}

	// With a test command, ▾ has Run tests with its key; it runs them, and says how they went
	// there and with a dot on ▾.
	h.api.must("PATCH", "/api/projects/"+pid, map[string]any{"test_command": "exit 3"}, http.StatusOK, nil)
	b.do("reload", chromedp.Reload())
	b.until("open ▾", `[...document.querySelectorAll(".pop-menu .menu-label")].some((e) => e.textContent === "Run tests")`,
		chromedp.Click(chromedp.ID("run-pick")))
	b.waitJS("Run tests, with T", `[...document.querySelectorAll(".pop-menu button")].find((e) => e.textContent.includes("Run tests"))?.querySelector("kbd.menu-key")?.textContent === "T"`)
	b.run("Run tests", `[...document.querySelectorAll(".pop-menu button")].find((e) => e.textContent.includes("Run tests")).click(); true`)
	b.waitJS("a red dot on ▾", `document.querySelector("#run-pick").dataset.state === "bad" && document.querySelector("#run-pick").title.includes("Tests failed")`)
	b.until("open ▾ again", `!!document.querySelector(".pop-menu")`, chromedp.Click(chromedp.ID("run-pick")))
	b.waitJS("Run tests says failed", `document.querySelector(".pop-menu .tone-bad .menu-note")?.textContent === "failed"`)
	b.until("close ▾", `!document.querySelector(".pop-menu")`, chromedp.KeyEvent(kb.Escape))
}
