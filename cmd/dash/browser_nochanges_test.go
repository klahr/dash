package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// A task Claude finishes without changing files (on the runner) waits in
// Review with a "No changes" badge, Claude's answer and Close instead of
// Approve, on a wide screen and a phone; Close closes it, and the Merged
// lane says "Closed · no changes". With the project's "Close tasks that
// change no files" on, the next one is closed without Review.
func TestBrowserCloseNoChanges(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
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
	// lane shows a lane on a phone, where one lane shows at a time.
	lane := func(key string) {
		b.until("show the "+key+" lane", fmt.Sprintf(`document.querySelector("#board .col.active")?.dataset.lane === %q || !document.querySelector(".lane-tab")?.offsetParent`, key),
			chromedp.Click(chromedp.CSS(fmt.Sprintf(`.lane-tab[data-lane=%q]`, key))))
	}

	for _, size := range []struct {
		name  string
		vp    chromedp.Action[chromedp.Void]
		phone bool
	}{
		{"wide", chromedp.EmulateViewport(1280, 900), false},
		{"phone", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch), true},
	} {
		b.do("a "+size.name+" screen", size.vp)
		var td struct{ ID string }
		api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Question " + size.name, "prompt": "answer only: what is it?", "enqueue": true}, http.StatusCreated, &td)
		waitStatus(t, api, "/api/todos/"+td.ID, "review", nil)
		if size.phone {
			lane("review")
		}
		b.waitCard(td.ID, "review")
		card := cardSel(td.ID)
		var sum struct{ Summary string }
		waitFor(t, nil, "Claude's summary", func() bool {
			api.must("GET", "/api/todos/"+td.ID, nil, http.StatusOK, &sum)
			return sum.Summary != ""
		})
		b.waitJS("the No changes badge, the summary and Close", fmt.Sprintf(`(() => {
			const c = document.querySelector(%q);
			const chips = [...c.querySelectorAll(".card-actions button")].map((e) => e.textContent.trim());
			return c.querySelector(".nochange-tag")?.textContent === "No changes" &&
				c.querySelector(".todo-sub")?.textContent === %q &&
				chips.includes("Close") && !chips.includes("Approve") &&
				c.querySelector(".review-chip").textContent === "Close" && c.querySelector(".review-chip").getBoundingClientRect().right <= innerWidth;
		})()`, card, sum.Summary))
		shot("nochanges-card-" + size.name)

		// The task: Ask for changes… and Close, not Rework… and Approve.
		b.openTask(td.ID)
		b.waitJS("Ask for changes and Close", `(() => {
			const btns = [...document.querySelectorAll("#d-actions button")].map((e) => e.textContent.trim());
			return btns.includes("Ask for changes…") && btns.includes("Close") && !btns.includes("Approve") && !btns.includes("Rework…");
		})()`)
		b.waitJS("the timeline says it changed nothing", `document.querySelector("#d-chat-log")?.textContent.includes("finished without changing any files")`)
		shot("nochanges-detail-" + size.name)
		b.clickText("#d-actions button", "Close")
		waitStatus(t, api, "/api/todos/"+td.ID, "merged", nil)
		b.waitJS("the toast", `[...document.querySelectorAll(".toast")].some((e) => e.textContent.includes("Closed"))`)
		b.waitJS("Closed · no changes in the task", `document.querySelector("#d-meta .status")?.textContent === "Closed · no changes"`)
		b.waitJS("no Revert", `![...document.querySelectorAll("#d-actions button")].some((e) => e.textContent.includes("Revert"))`)
		b.until("close the task", `document.querySelector("#detail").hidden`, chromedp.KeyEvent(kb.Escape))
		if size.phone {
			lane("done")
		}
		b.waitCard(td.ID, "done")
		b.waitJS("Closed · no changes on the card", fmt.Sprintf(`document.querySelector(%q)?.querySelector(".nochange-tag")?.textContent === "Closed · no changes"`, card))
		shot("nochanges-closed-" + size.name)
	}

	// The setting, in Project settings → Board.
	b.do("a wide screen", chromedp.EmulateViewport(1280, 900))
	b.until("open the project menu", `document.querySelector("#project-menu").open`, chromedp.Click(chromedp.CSS(`#project-menu > summary`)))
	b.until("open the settings", `document.querySelector("#settings-sheet").open`, chromedp.Click(chromedp.ID("pm-settings")))
	b.until("show Board", `document.querySelector("#settings-sheet-section-board").classList.contains("current")`,
		chromedp.Click(chromedp.ID("settings-sheet-rail-board")))
	b.waitJS("the setting, off", `(() => { const el = document.querySelector("#settings-form [name=close_no_changes]"); return el && !el.checked && el.getBoundingClientRect().width > 0; })()`)
	shot("nochanges-settings")
	// Clicked until checked: a click lost while the sheet settles would
	// leave nothing to save. Checking it saves it (its change event).
	b.until("turn it on", `document.querySelector("#settings-form [name=close_no_changes]").checked`,
		chromedp.Click(chromedp.CSS(`#settings-form [name=close_no_changes]`)))
	waitFor(t, nil, "the setting saved", func() bool {
		var p struct {
			CloseNoChanges bool `json:"close_no_changes"`
		}
		api.must("GET", "/api/projects/"+pid, nil, http.StatusOK, &p)
		return p.CloseNoChanges
	})
	b.until("close the settings", `!document.querySelector("#settings-sheet").open`, chromedp.KeyEvent(kb.Escape))

	// The next question is closed on the runner without Review.
	var td struct{ ID string }
	api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Auto question", "prompt": "answer only: and now?", "enqueue": true}, http.StatusCreated, &td)
	waitStatus(t, api, "/api/todos/"+td.ID, "merged", nil)
	var got struct {
		NoChanges    bool   `json:"no_changes"`
		MergedCommit string `json:"merged_commit"`
	}
	api.must("GET", "/api/todos/"+td.ID, nil, http.StatusOK, &got)
	if !got.NoChanges || got.MergedCommit != "" {
		t.Fatalf("want it closed with no changes and no commit, got %+v", got)
	}
	b.waitCard(td.ID, "done")
	b.waitJS("Closed · no changes on the card", fmt.Sprintf(`document.querySelector(%q)?.querySelector(".nochange-tag")?.textContent === "Closed · no changes"`, cardSel(td.ID)))
}
