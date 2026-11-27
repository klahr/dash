package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
)

// With one runner online, a chat names it once: a project chat's place line,
// General's runner bar. With two of your runners online that work on a
// project, its chat shows a runner menu (on a wide screen and a phone's),
// which alone names the runner: the runner picked there is the one the next
// turn runs on, the menu shows the one a turn ran on, and the pick is
// remembered for that project, apart from General's.
func TestBrowserProjectChatRunnerMenu(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)

	const panel = `#chat-pane .chat-panel[data-chat]:not([hidden])`
	menu := panel + ` .chat-runner select`
	options := fmt.Sprintf(`[...document.querySelectorAll(%q)].map((o) => o.textContent).join(",")`, menu+" option")
	picked := fmt.Sprintf(`(document.querySelector(%q)?.selectedOptions[0]?.textContent)`, menu)
	bar := `document.querySelector(` + jsq(panel+" .chat-runner") + `)`
	place := fmt.Sprintf(`(document.querySelector(%q)?.textContent || "")`, panel+" .chat-cwd .chat-place")
	general := `document.querySelector("#chat-runner-text").textContent`

	wide, phone := newBrowser(t, chrome), newBrowser(t, chrome)
	wide.signIn(h)
	phone.signIn(h)
	phone.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))

	// One runner: no menu; the place line names it, and General's bar does.
	wide.topMenu("pc-open", `!document.querySelector("#chat-pane").hidden`)
	wide.waitJS("the place names the one runner", place+`.endsWith("· laptop") && `+bar+`.hidden`)
	wide.waitJS("General's bar names it", general+` === "Runs on laptop, with its Claude login" && document.querySelector("#chat-runner-pick").hidden`)
	shot(wide, "project-chat-one-runner-wide")

	// A second runner of the admin's, which works on the project too (it runs on "any").
	var code struct{ Code string }
	h.api.must("POST", "/api/runners/codes", map[string]any{}, http.StatusOK, &code)
	dir := procsTempDir(t)
	startDash(t, h.dash, h.runnerEnv, "runner", "-data", filepath.Join(dir, "runner"), "-projects", filepath.Join(dir, "projects"),
		"-name", "desktop", "-env", filepath.Join(dir, "none.env"), "--server", h.base, "--code", code.Code)
	waitFor(t, h.runner, "the second runner to come online", func() bool {
		var rs struct {
			Runners []struct {
				Name   string
				Online bool
			}
		}
		h.api.must("GET", "/api/runners", nil, http.StatusOK, &rs)
		n := 0
		for _, r := range rs.Runners {
			if r.Online {
				n++
			}
		}
		return n == 2
	})

	// Two runners: the menu names the runner, the place line and the bar's text don't.
	runsOn := `document.querySelector(` + jsq(panel+" .chat-runner-text") + `).textContent === "Runs on"`
	wide.waitJS("the project chat's runner menu", `!`+bar+`.hidden && `+options+` === "desktop,laptop" && `+picked+` === "desktop" && `+runsOn)
	wide.waitJS("the place leaves the runner out", `!`+place+`.includes("·")`)
	wide.waitJS("General's menu", general+` === "Runs on" && !document.querySelector("#chat-runner-pick").hidden`)
	shot(wide, "project-chat-runner-menu-wide")
	wide.waitJS("the menu sits beside its label", fmt.Sprintf(`(() => { const l = document.querySelector(%q).getBoundingClientRect(), s = document.querySelector(%q).getBoundingClientRect(); return s.left > l.right && s.top < l.bottom; })()`, panel+" .chat-runner-text", menu))

	// Pick laptop: the next turn runs there, and the menu says so.
	wide.run("pick laptop", fmt.Sprintf(`const s = document.querySelector(%q); s.value = [...s.options].find((o) => o.textContent === "laptop").value; s.dispatchEvent(new Event("change")); true`, menu))
	wide.waitJS("the menu says laptop", picked+` === "laptop" && `+runsOn)
	wide.do("ask", chromedp.SendKeys(chromedp.CSS(panel+` textarea`), "hello\r"))
	wide.waitJS("the turn ran on laptop", `!!document.querySelector(`+jsq(panel+" .msg.assistant .msg-actions")+`) && `+picked+` === "laptop" && !`+place+`.includes("·")`)
	pick, err := chromedp.Run(wide.ctx, chromedp.Evaluate[string](`localStorage.getItem("dash.chatRunner") || ""`))
	if err != nil || pick != "" {
		t.Errorf("the project's pick changed General's: %q %v", pick, err)
	}

	// The phone: the same menu, laid out across the narrow chat page.
	phone.until("open the chat", `!document.querySelector("#chat-pane").hidden`, chromedp.Click(chromedp.ID("nav-chat")))
	phone.waitJS("the phone's runner menu", `!`+bar+`.hidden && `+options+` === "desktop,laptop" && `+runsOn)
	phone.waitJS("the menu shows and fits the screen", `(() => { const r = document.querySelector(`+jsq(menu)+`).getBoundingClientRect(); return r.width > 0 && r.right <= innerWidth; })()`)
	shot(phone, "project-chat-runner-menu-phone")
}
