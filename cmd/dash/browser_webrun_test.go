package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// The header's Run on a web project: ▶ Run previews the latest commit of
// the default branch on the runner, as History's Preview does, opening
// History on that commit with the preview pane beside it (no warning: it is
// today's code). It is ■ Terminate while it runs, with the commit and runner
// beside it opening the pane again (full screen on a phone); Terminate stops
// it. ▾ has the runners, Pick a commit… and Run in checkout. With commit
// previews off, Run is greyed and says why.
func TestBrowserWebRunPreviewsTheLatestCommit(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHubWith(t, []string{"DASH_PREVIEW_DOMAIN=preview.dash.test"})
	api, pid := h.api, h.project.ID
	// It never opens its port: the preview stays starting.
	api.must("PATCH", "/api/projects/"+pid, map[string]any{"run_command": "echo starting-the-app; sleep 120"}, http.StatusOK, nil)
	waitFor(t, nil, "the runner's clone", func() bool {
		api.try("POST", "/api/projects/"+pid+"/remote/pull", nil)
		code, body := api.try("GET", "/api/projects/"+pid+"/graph", nil)
		var g struct{ Commits []struct{ Subject string } }
		return code == http.StatusOK && json.Unmarshal([]byte(body), &g) == nil && len(g.Commits) >= 1
	})

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.do("a wide screen", chromedp.EmulateViewport(1400, 900), chromedp.Navigate(h.base+"/p/"+pid+"/board"))
	b.waitJS("▶ Run, previewing the latest main on laptop", `(() => {
		const m = document.querySelector("#run-main");
		return !document.querySelector("#run-split").hidden && !m.disabled && !m.classList.contains("stop") && !m.classList.contains("off")
			&& m.textContent === "Run" && m.title === "Preview the latest main on laptop";
	})()`)

	// ▾: the runner, Pick a commit…, Run in checkout, then Configure….
	b.until("open ▾", `!!document.querySelector(".pop-menu")`, chromedp.Click(chromedp.ID("run-pick")))
	b.waitJS("its rows", `(() => {
		const rows = [...document.querySelectorAll(".pop-menu .menu-label")].map((e) => e.textContent);
		return rows.length === 4 && rows[0].startsWith("laptop") && rows[1] === "Pick a commit…" && rows[2] === "Run in checkout" && rows[3] === "Configure…";
	})()`)
	b.until("close ▾", `!document.querySelector(".pop-menu")`, chromedp.KeyEvent(kb.Escape))

	// ▶ Run: History, the newest commit, its preview beside it, starting; no warning.
	b.until("Run", `document.querySelector("#run-main").classList.contains("stop")`, chromedp.Click(chromedp.ID("run-main")))
	inPane := `(() => { const p = document.querySelector("#d-preview"); return !!p && !p.hidden && p.parentElement.id === "commit-detail" && document.querySelector("#commit-detail").classList.contains("previewing"); })()`
	b.waitJS("History, with the preview pane in the commit pane", `document.querySelector("#view-history.active") && !document.querySelector("#commit-detail").hidden && `+inPane+
		` && !document.querySelector("#ask-sheet").open`)
	b.waitJS("the newest commit, starting", `document.querySelector("#graph li:first-child").classList.contains("selected")
		&& /starting-the-app/.test(document.querySelector("#pv-log-text").textContent)`)
	b.waitJS("■ Terminate, the commit and runner beside it", `(() => {
		const m = document.querySelector("#run-main"), a = document.querySelector("#run-at");
		return m.textContent === "Terminate" && !a.hidden && /^[0-9a-f]{7}/.test(a.textContent) && a.textContent.includes("on laptop")
			&& a.getAttribute("href") === "/p/`+pid+`/history";
	})()`)
	var st struct {
		Session *struct{ Kind, Ref, Commit string } `json:"session"`
	}
	api.must("GET", "/api/projects/"+pid+"/try", nil, http.StatusOK, &st)
	if st.Session == nil || st.Session.Kind != "web" || st.Session.Ref != "" {
		t.Fatalf("session after Run: %+v", st.Session)
	}
	b.waitJS("the pane is the session's commit", `document.querySelector("#graph li.selected")?.dataset.hash === "`+st.Session.Commit+`"`)

	// A newer commit on main (pushed elsewhere: History → Refresh fetches it):
	// New commit · Update, which runs it on the same runner at the same width.
	b.waitJS("no Update while it runs the newest", `document.querySelector("#pv-update").hidden`)
	b.until("Phone width", `document.querySelector(".pv-widths [aria-pressed=true]")?.dataset.width === "390"`,
		chromedp.Click(chromedp.CSS(`.pv-widths [data-width="390"]`)))
	next := strings.TrimSpace(run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "commit-tree", "main^{tree}", "-p", "main", "-m", "the fix"))
	run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "update-ref", "refs/heads/main", next)
	b.until("History → Refresh", `(() => { const u = document.querySelector("#pv-update");
		return !u.hidden && u.textContent === "New commit `+next[:7]+` · Update" && u.getBoundingClientRect().height === document.querySelector("#pv-draw-btn").getBoundingClientRect().height; })()`,
		chromedp.Click(chromedp.ID("graph-refresh")))
	b.until("Update", `document.querySelector("#pv-update").hidden`, chromedp.Click(chromedp.ID("pv-update")))
	b.waitJS("no warning; the new commit, starting, at the same width", `!document.querySelector("#ask-sheet").open
		&& document.querySelector("#graph li.selected")?.dataset.hash === "`+next+`" && `+inPane+`
		&& document.querySelector(".pv-widths [aria-pressed=true]")?.dataset.width === "390"
		&& /starting-the-app/.test(document.querySelector("#pv-log-text").textContent)`)
	api.must("GET", "/api/projects/"+pid+"/try", nil, http.StatusOK, &st)
	if st.Session == nil || st.Session.Commit != next || st.Session.Ref != "" {
		t.Fatalf("session after Update: %+v, want %s", st.Session, next)
	}
	b.waitJS("still no Update", `document.querySelector("#pv-update").hidden`)

	// Esc hides the preview; it keeps running.
	runJS(t, b, `document.activeElement?.blur(), true`)
	b.do("press Esc", chromedp.KeyEvent(kb.Escape))
	b.waitJS("the preview hidden", `document.querySelector("#d-preview").hidden && document.querySelector("#run-main").textContent === "Terminate"`)

	// On a phone, the commit beside Terminate opens it again, full screen.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch),
		chromedp.Navigate(h.base+"/p/"+pid+"/board"))
	b.waitJS("■ Terminate", `document.querySelector("#run-main").textContent === "Terminate" && !document.querySelector("#run-at").hidden`)
	b.until("open its preview", inPane, chromedp.Click(chromedp.ID("run-at")))
	b.waitJS("full screen, with the actions button", `(() => { const r = document.querySelector("#d-preview").getBoundingClientRect();
		return Math.abs(r.width - innerWidth) < 2 && r.height > innerHeight * .9 && document.querySelector("#pv-fab").getClientRects().length > 0; })()`)
	b.until("open its actions", `!!document.querySelector(".pop-menu")`, chromedp.Click(chromedp.ID("pv-fab")))
	b.run("Back to the commit", `[...document.querySelectorAll(".pop-menu button")].find((e) => e.textContent.includes("Back to the commit")).click(); true`)
	b.waitJS("the commit, the preview hidden", `document.querySelector("#d-preview").hidden && !document.querySelector("#commit-detail").hidden`)

	// ■ Terminate stops it.
	b.do("a wide screen", chromedp.EmulateViewport(1400, 900))
	b.until("Terminate", `document.querySelector("#run-main").textContent === "Run"`, chromedp.Click(chromedp.ID("run-main")))
	st.Session = nil
	api.must("GET", "/api/projects/"+pid+"/try", nil, http.StatusOK, &st)
	if st.Session != nil {
		t.Fatalf("session after Terminate: %+v", st.Session)
	}

	// Commit previews off: Run is greyed and says why; Run in checkout stays.
	api.must("PATCH", "/api/projects/"+pid, map[string]any{"commit_previews": false}, http.StatusOK, nil)
	b.do("the board", chromedp.Navigate(h.base+"/p/"+pid+"/board"))
	b.waitJS("a greyed Run, saying why", `(() => {
		const m = document.querySelector("#run-main");
		return m.disabled && m.textContent === "Run" && m.title.startsWith("Commit previews are off") && m.title.includes("Run in checkout");
	})()`)
	b.until("open ▾", `!!document.querySelector(".pop-menu")`, chromedp.Click(chromedp.ID("run-pick")))
	b.waitJS("the runner and Pick a commit… greyed, Run in checkout not", `(() => {
		const rows = [...document.querySelectorAll(".pop-menu button")];
		const row = (s) => rows.find((e) => e.textContent.includes(s));
		return row("laptop")?.disabled && row("Pick a commit")?.disabled && row("Run in checkout") && !row("Run in checkout").disabled;
	})()`)
	b.until("close ▾", `!document.querySelector(".pop-menu")`, chromedp.KeyEvent(kb.Escape))
}
