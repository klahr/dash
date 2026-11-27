package main

import (
	"testing"

	"github.com/chromedp/chromedp"
)

// A runner's Doctor section in the Runners sheet: the real runner reports
// its checks with its heartbeat, the page lists them by group, and Check
// again runs them anew through the tunnel. On a phone the section fits
// without sideways scrolling.
func TestBrowserRunnerDoctor(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	b := newBrowser(t, chrome)
	b.signIn(h)

	open := func(b *browser) {
		b.openStatus(`!!document.querySelector("#status-list .status-runner:not(.status-runner-more)")`)
		b.until("open the runner's page", `document.querySelector("#runners-sheet").open && !!document.querySelector("#runners-main .current .runner-title")`,
			chromedp.Click(chromedp.CSS(`#status-list .status-runner:not(.status-runner-more)`)))
		b.waitJS("its doctor's checks", `[...document.querySelectorAll("#runner-detail .runner-doctor-group")].map((g) => g.textContent).join(",").startsWith("Required,Features")
			&& /^Checked /.test(document.querySelector("#runner-detail .runner-doctor-head .hint")?.textContent || "")
			&& !!document.querySelector('#runner-detail .runner-doctor-check strong')`)
	}
	open(b)
	shot(b, "doctor-wide")
	b.clickText("#runner-detail .runner-doctor-head button", "Check again")
	b.waitJS("checked again", `[...document.querySelectorAll(".toast")].some((t) => / checked$/.test(t.textContent))`)

	phone := newBrowser(t, chrome)
	phone.signIn(h)
	phone.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	open(phone)
	phone.waitJS("no sideways scrolling", `(() => { const m = document.querySelector("#runners-main"); return m.scrollWidth <= m.clientWidth + 1; })()`)
	phone.do("scroll to the doctor", chromedp.ScrollIntoView(`#runner-detail .runner-doctor`))
	shot(phone, "doctor-phone")
}
