package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// History's Preview on a web project's commit: a warning the first time,
// then the app at that commit starting on the runner, its start-up log in
// the preview pane beside the commit (full screen on a phone). Esc hides
// the pane, then the commit; Preview again reattaches to the running one;
// Stop stops it.
func TestBrowserCommitPreview(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHubWith(t, []string{"DASH_PREVIEW_DOMAIN=preview.dash.test"})
	api, pid := h.api, h.project.ID
	// It never opens its port: the preview stays starting, its log growing.
	api.must("PATCH", "/api/projects/"+pid, map[string]any{"setup_command": "echo installing-deps",
		"run_command": "echo starting-the-app; sleep 120"}, http.StatusOK, nil)
	waitFor(t, nil, "the runner's clone", func() bool {
		api.try("POST", "/api/projects/"+pid+"/remote/pull", nil)
		code, body := api.try("GET", "/api/projects/"+pid+"/graph", nil)
		var g struct{ Commits []struct{ Subject string } }
		return code == http.StatusOK && json.Unmarshal([]byte(body), &g) == nil && len(g.Commits) >= 1
	})

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open History", `document.querySelector("#view-history.active")`,
		chromedp.Click(chromedp.CSS(`.nav [data-view=history]`)))
	b.waitJS("the commit", `document.querySelectorAll("#graph li").length >= 1`)
	b.until("open the commit", `!document.querySelector("#commit-detail").hidden`, chromedp.Click(chromedp.CSS(`#graph li:first-child .graph-commit`)))
	previewBtn := `[...document.querySelectorAll("#cd-actions button")].find((e) => /preview/i.test(e.textContent))`
	b.waitJS("Preview in the commit's actions", previewBtn+`?.disabled === false`)

	// The first time, a warning about the shared dev database.
	runJS(t, b, previewBtn+`.click(), true`) // click Preview
	b.waitJS("the warning", `document.querySelector("#ask-sheet").open && /dev database/.test(document.querySelector("#ask-body").textContent)`)
	b.until("accept it", `!document.querySelector("#ask-sheet").open`, chromedp.Click(chromedp.ID("ask-ok")))

	// The pane, beside the commit, shows the start-up and its log.
	inPane := `(() => { const p = document.querySelector("#d-preview"); return !!p && !p.hidden && p.parentElement.id === "commit-detail" && document.querySelector("#commit-detail").classList.contains("previewing"); })()`
	b.waitJS("the preview pane in the commit pane", inPane)
	b.waitJS("starting, with the setup and the server in its log",
		`/Setting up and starting the server/.test(document.querySelector("#pv-status").textContent) && /installing-deps/.test(document.querySelector("#pv-status").textContent) && /starting-the-app/.test(document.querySelector("#pv-log-text").textContent)`)
	b.waitJS("beside the commit, wide", `(() => { const p = document.querySelector("#d-preview").getBoundingClientRect(), c = document.querySelector("#cd-title").getBoundingClientRect(); return p.left >= c.right - 1 && p.width > 400; })()`)
	b.waitJS("no Draw for a commit", `getComputedStyle(document.querySelector("#pv-draw-btn")).display === "none"`)
	b.waitJS("the button says Hide preview", previewBtn+`?.textContent.includes("Hide preview")`)
	var st struct {
		Session *struct{ Kind, State, URL, OpenURL string } `json:"session"`
	}
	api.must("GET", "/api/projects/"+pid+"/try", nil, http.StatusOK, &st)
	if st.Session == nil || st.Session.Kind != "web" || !strings.Contains(st.Session.URL, pid+"--laptop.preview.dash.test") {
		t.Fatalf("session: %+v", st.Session)
	}

	// Esc hides the preview, then closes the commit.
	runJS(t, b, `document.activeElement?.blur(), true`) // blur
	b.do("press Esc", chromedp.KeyEvent(kb.Escape))
	b.waitJS("the preview hidden, the commit open", `document.querySelector("#d-preview").hidden && document.querySelector("#d-preview").parentElement.id === "detail" && !document.querySelector("#commit-detail").hidden && !document.querySelector("#commit-detail").classList.contains("previewing")`)
	b.do("press Esc again", chromedp.KeyEvent(kb.Escape))
	b.waitJS("the commit closed", `document.querySelector("#commit-detail").hidden`)

	// Preview again: the running one, no warning, on a phone full screen.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.until("open the commit", `!document.querySelector("#commit-detail").hidden`, chromedp.Click(chromedp.CSS(`#graph li:first-child .graph-commit`)))
	b.waitJS("Preview, marked starting", previewBtn+`?.querySelector(".pv-dot.starting")`)
	runJS(t, b, previewBtn+`.click(), true`) // click Preview
	b.waitJS("the pane, no warning", inPane+` && !document.querySelector("#ask-sheet").open`)
	b.waitJS("full screen", `(() => { const r = document.querySelector("#d-preview").getBoundingClientRect(); return Math.abs(r.width - innerWidth) < 2 && r.height > innerHeight * .9; })()`)
	b.waitJS("the actions button", `document.querySelector("#pv-fab").getClientRects().length > 0`)

	// Stop, from the wide screen's bar.
	b.do("a wide screen", chromedp.EmulateViewport(1400, 900))
	b.until("stop it", `/isn't running/.test(document.querySelector("#pv-status").textContent)`, chromedp.Click(chromedp.ID("pv-stop")))
	st.Session = nil
	api.must("GET", "/api/projects/"+pid+"/try", nil, http.StatusOK, &st)
	if st.Session != nil {
		t.Fatalf("session after Stop: %+v", st.Session)
	}
}

// runJS evaluates expr (which ends in a value) in the page.
func runJS(t *testing.T, b *browser, expr string) {
	t.Helper()
	if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[any](expr)); err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
}
