package main

import (
	"net/http"
	"slices"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// Runner labels are picked as chips, each change saved at once. On the
// runner's page a typed label is added on Enter; in the project settings the labels that
// exist are offered, a chip's × removes it, a new label can still be typed
// (and taken off again with Backspace), and the runners that match show as
// the labels change. On a phone's screen too.
func TestBrowserLabelChips(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	b := newBrowser(t, chrome)
	b.signIn(h)

	// The runner's own labels: type one, Enter saves it.
	b.waitJS("the runner in the sidebar", `!!document.querySelector("#side-runner-list .side-runner:not(.side-runner-more)")`)
	b.until("open the runner's page", `!!document.querySelector("#runner-detail .runner-own-labels .label-picker-input")`,
		chromedp.Click(chromedp.CSS(`#side-runner-list .side-runner:not(.side-runner-more)`)))
	b.do("type a label", chromedp.SendKeys(`#runner-detail .runner-own-labels .label-picker-input`, "gpu"+kb.Enter))
	b.waitJS("its chip", `[...document.querySelectorAll("#runner-detail .runner-own-labels .label-chip")].map((c) => c.textContent).join() === "gpu"`)
	// The chip shows as soon as it's picked; the save follows it.
	waitFor(t, nil, "the runner to have gpu", func() bool {
		var list struct {
			Runners []struct {
				Labels []string `json:"labels"`
			} `json:"runners"`
		}
		h.api.must("GET", "/api/runners", nil, http.StatusOK, &list)
		if len(list.Runners) != 1 {
			t.Fatalf("runners after picking gpu: %+v", list.Runners)
		}
		return slices.Equal(list.Runners[0].Labels, []string{"gpu"})
	})
	b.until("close the Runners sheet", `!document.querySelector("#runners-sheet").open`, chromedp.KeyEvent(kb.Escape))

	// The project's Runs on: any, as it was made.
	b.until("open the project menu", `document.querySelector("#project-menu").open`, chromedp.Click(chromedp.CSS(`#project-menu > summary`)))
	b.until("open the settings", `document.querySelector("#settings-sheet").open`, chromedp.Click(chromedp.ID("pm-settings")))
	b.until("show its runners, in Agent & runners", `document.querySelector('#settings-sheet .rail-section[data-section="claude"]').classList.contains("current")`,
		chromedp.Click(chromedp.CSS(`#settings-sheet .rail-item[data-rail="claude"]`)))
	const field = `#settings-runners-field`
	b.waitJS("any picked", `[...document.querySelectorAll("`+field+` .label-chip")].map((c) => c.textContent).join() === "any" && !!document.querySelector("#settings-runners .runner-pick.on")`)
	// The chips and offers are redrawn when the runners reload: clicks retry.
	b.until("remove any", `document.querySelector("#settings-runs-on").value === ""`,
		chromedp.Click(chromedp.CSS(field+` .label-chip-x[aria-label="Remove any"]`)))
	b.waitJS("no runner matches no labels", `!document.querySelector("#settings-runners .runner-pick.on")`)
	b.clickTextUntil(field+` .label-option`, "gpu", `document.querySelector("#settings-runs-on").value === "gpu"`)
	b.waitJS("the runner matches", `!!document.querySelector("#settings-runners .runner-pick.on")`)
	b.do("type a new label", chromedp.SendKeys(field+` .label-picker-input`, "nope"+kb.Enter))
	b.waitJS("no runner has both", `document.querySelector("#settings-runs-on").value === "gpu, nope" && !document.querySelector("#settings-runners .runner-pick.on")`)
	b.do("take it off again", chromedp.SendKeys(field+` .label-picker-input`, kb.Backspace))
	b.waitJS("the runner matches again", `document.querySelector("#settings-runs-on").value === "gpu" && !!document.querySelector("#settings-runners .runner-pick.on")`)

	// On a phone's screen the chips wrap: nothing scrolls sideways.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("no sideways scrolling", `(() => { const m = document.querySelector("#settings-sheet .rail-main"); return m.scrollWidth <= m.clientWidth + 1; })()`)

	// Each change saved itself: the sheet has no Save button.
	b.until("close the settings", `!document.querySelector("#settings-sheet").open`, chromedp.KeyEvent(kb.Escape))
	waitFor(t, nil, "the project to run on gpu", func() bool {
		var p struct {
			RunsOn []string `json:"runs_on"`
		}
		h.api.must("GET", "/api/projects/"+h.project.ID, nil, http.StatusOK, &p)
		return slices.Equal(p.RunsOn, []string{"gpu"})
	})
}
