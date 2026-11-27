package main

import (
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
)

// The first-review tips (app/hints.js): on a task in Review they show one
// at a time beside what they are about (the Changes tab, the diff's lines,
// the action bar), each until it is dismissed. Dismissed, they stay away
// after a reload; Account → General → Show review tips again brings them
// back. On a phone they fit the screen and leave the diff below them.
func TestBrowserReviewTips(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	b := newBrowser(t, chrome)
	b.signIn(h)

	var td struct{ ID string }
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Tips", "prompt": "Add a done file", "enqueue": true}, http.StatusCreated, &td)
	waitStatus(t, api, "/api/todos/"+td.ID, "review", nil)
	b.waitCard(td.ID, "review")
	b.openTask(td.ID)

	tips := `document.querySelectorAll("#detail .tip").length`
	b.waitJS("the Changes tip under the tabs, alone", `!document.querySelector("#d-tip-tabs").hidden && !!document.querySelector("#d-tip-tabs .tip-diff") && `+tips+` === 1`)
	// Got it: the next tip that fits the Timeline is the action bar's.
	b.until("dismiss the Changes tip", `!document.querySelector("#detail .tip-diff")`, chromedp.Click(chromedp.CSS(`#d-tip-tabs .tip-diff .tip-actions button`)))
	b.waitJS("the action bar's tip", `!!document.querySelector("#d-tip-actions .tip-actions") && document.querySelector("#d-tip-actions").textContent.includes("Approve")`)
	// On Changes the comment tip comes first, above the files.
	b.until("show the changes", `!!document.querySelector("#d-diff .diff-lines > div[data-ln]")`,
		chromedp.Click(chromedp.CSS(`.pane-tabs button[data-pane=diff]`)))
	b.waitJS("the comment tip above the first file", `(() => {
		const tip = document.querySelector("#d-diff .tip-comment"), file = document.querySelector("#d-diff .diff-file");
		return tip && file && tip.compareDocumentPosition(file) & Node.DOCUMENT_POSITION_FOLLOWING && `+tips+` === 1;
	})()`)
	// A phone: the tip fits the width, and the diff shows below it.
	b.do("a phone", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the tip fits the phone, the diff in view below it", `(() => {
		const tip = document.querySelector("#d-diff .tip-comment").getBoundingClientRect();
		const file = document.querySelector("#d-diff .diff-file").getBoundingClientRect();
		return tip.right <= innerWidth + 1 && tip.left >= -1 && tip.height < innerHeight / 3 && file.top >= tip.bottom && file.top < innerHeight;
	})()`)
	b.do("a wide screen", chromedp.EmulateViewport(1280, 900))
	b.until("hide all tips", tips+` === 0`, chromedp.Click(chromedp.CSS(`#d-diff .tip-comment .tip-skip`)))

	// Gone for good: not after a reload either.
	b.do("reload", chromedp.Reload())
	b.waitCard(td.ID, "review")
	b.openTask(td.ID)
	b.until("show the changes again", `!!document.querySelector("#d-diff .diff-lines > div[data-ln]")`,
		chromedp.Click(chromedp.CSS(`.pane-tabs button[data-pane=diff]`)))
	b.waitJS("no tips", tips+` === 0`)

	// Account → General brings them back.
	b.until("open the account menu", `document.querySelector("#foot-menu").open`, chromedp.Click(chromedp.CSS(`#foot-menu summary`)))
	b.until("open Account", `document.querySelector("#account-sheet").open`, chromedp.Click(chromedp.CSS(`#account-open`)))
	b.until("the General section", `document.querySelector('#account-sheet [data-section=general]').classList.contains("current")`,
		chromedp.Click(chromedp.CSS(`#account-sheet .rail-item[data-rail=general]`)))
	b.until("show the tips again", tips+` === 1`, chromedp.Click(chromedp.CSS(`#tips-reset`)))
	b.waitJS("the Changes tip again", `!!document.querySelector("#d-tip-tabs .tip-diff")`)
}
