package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
)

// With two of your runners online that work on a project, its chat shows a
// runner menu (on a wide screen and a phone's): the runner picked there is
// the one the next turn runs on, the place line says so, and the pick is
// remembered for that project, apart from General's.
func TestBrowserProjectChatRunnerMenu(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)

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

	const panel = `#chat-pane .chat-panel[data-chat]:not([hidden])`
	menu := panel + ` .chat-runner select`
	options := fmt.Sprintf(`[...document.querySelectorAll(%q)].map((o) => o.textContent).join(",")`, menu+" option")
	place := fmt.Sprintf(`document.querySelector(%q)?.textContent || ""`, panel+" .chat-cwd .chat-place")

	wide, phone := newBrowser(t, chrome), newBrowser(t, chrome)
	wide.signIn(h)
	phone.signIn(h)
	phone.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))

	wide.topMenu("pc-open", `!document.querySelector("#chat-pane").hidden`)
	wide.waitJS("the project chat's runner menu", `!document.querySelector(`+jsq(panel+" .chat-runner")+`).hidden && `+options+` === "desktop,laptop"`)
	wide.waitJS("the place says the runner it will run on", place+`.endsWith("· desktop")`)
	shot(wide, "project-chat-runner-menu-wide")

	// Pick laptop: the next turn runs there, and the place says so.
	wide.run("pick laptop", fmt.Sprintf(`const s = document.querySelector(%q); s.value = [...s.options].find((o) => o.textContent === "laptop").value; s.dispatchEvent(new Event("change")); true`, menu))
	wide.waitJS("the bar says laptop", `document.querySelector(`+jsq(panel+" .chat-runner-text")+`).textContent === "Runs on laptop, with its Claude login"`)
	wide.do("ask", chromedp.SendKeys(chromedp.CSS(panel+` textarea`), "hello\r"))
	wide.waitJS("the turn ran on laptop", place+`.endsWith("· laptop")`)
	general, err := chromedp.Run(wide.ctx, chromedp.Evaluate[string](`localStorage.getItem("dash.chatRunner") || ""`))
	if err != nil || general != "" {
		t.Errorf("the project's pick changed General's: %q %v", general, err)
	}

	// The phone: the same menu, laid out across the narrow chat page.
	phone.until("open the chat", `!document.querySelector("#chat-pane").hidden`, chromedp.Click(chromedp.ID("nav-chat")))
	phone.waitJS("the phone's runner menu", `!document.querySelector(`+jsq(panel+" .chat-runner")+`).hidden && `+options+` === "desktop,laptop"`)
	phone.waitJS("the menu fits the screen", `document.querySelector(`+jsq(menu)+`).getBoundingClientRect().right <= innerWidth`)
	shot(phone, "project-chat-runner-menu-phone")
}
