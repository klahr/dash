package main

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Deleting a goal from its pane, on a wide screen and on a phone's: the
// confirm says its tasks stay, then the pane closes and the goal leaves the
// list. A goal deleted elsewhere (another window, the API) leaves the list
// and its open pane too.
func TestBrowserDeleteGoal(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	goal := func(title string) string {
		var g struct{ ID string }
		api.must("POST", "/api/goals", map[string]any{"project": pid, "title": title, "mode": "ask"}, http.StatusCreated, &g)
		return g.ID
	}
	wide, phone, elsewhere := goal("Wide goal"), goal("Phone goal"), goal("Elsewhere goal")

	b := newBrowser(t, chrome)
	b.signIn(h)
	row := func(id string) string { return fmt.Sprintf(`#goal-list li[data-id=%q] .goal-open`, id) }
	gone := func(id string) string {
		return fmt.Sprintf(`!document.querySelector('#goal-list li[data-id=%q]') && document.querySelector("#goal-detail").hidden`, id)
	}
	del := func(id string) {
		b.t.Helper()
		b.until("open the goal", fmt.Sprintf(`!document.querySelector("#goal-detail").hidden && document.querySelector('#goal-list li[data-id=%q].selected')`, id),
			chromedp.Click(chromedp.CSS(row(id))))
		b.waitJS("Delete beside Stop, on screen", `(() => {
			const bs = [...document.querySelectorAll("#gd-actions button")], i = bs.findIndex((e) => e.textContent.trim() === "Delete");
			const r = bs[i]?.getBoundingClientRect();
			return i > 0 && bs[i - 1].textContent.trim() === "Stop" && r.width > 0 && r.left >= 0 && r.right <= innerWidth;
		})()`)
		b.clickTextUntil("#gd-actions button", "Delete", `document.querySelector("#ask-sheet").open`)
		b.waitJS("the confirm says the tasks stay", `document.querySelector("#ask-title").textContent === "Delete the goal?" && document.querySelector("#ask-body").textContent.includes("tasks stay on the board")`)
		b.until("confirm", gone(id), chromedp.Click(chromedp.ID("ask-ok")))
		if code, body := api.try("GET", "/api/goals/"+id, nil); code != http.StatusNotFound {
			t.Fatalf("deleted goal: %d %s", code, body)
		}
	}

	b.do("a wide screen", chromedp.EmulateViewport(1280, 900))
	b.until("open Goals", `document.querySelector("#view-goals.active") && document.querySelectorAll("#goal-list li").length === 3`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=goals]`)))
	del(wide)

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	// The sidebar turns into the closed drawer at once: sliding out, it
	// covered the page and took the next click (Shell, Add project).
	if hidden, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`getComputedStyle(document.querySelector("#menu")).visibility === "hidden"`)); err != nil || !hidden {
		b.fail("the drawer hidden at once", fmt.Errorf("hidden: %v, err: %v", hidden, err))
	}
	del(phone)

	// Deleted elsewhere while open here.
	b.until("open the last goal", `!document.querySelector("#goal-detail").hidden`, chromedp.Click(chromedp.CSS(row(elsewhere))))
	api.must("DELETE", "/api/goals/"+elsewhere, nil, http.StatusNoContent, nil)
	b.waitJS("it leaves the list and the pane closes", gone(elsewhere)+` && !document.querySelector("#goal-empty").hidden`)
}

// A goal opens beside the list, every row still clickable; Talk to
// coordinator opens its chat beside the goal (never over it or the list),
// with the coordinator's question in it and no wake-up of Dash's as yours;
// the chat follows the goal opened next, in one tab, and closes with it; a
// waiting plan is shown in the goal and accepted there. On a phone the chat
// is a page with a Goal button back to the goal.
func TestBrowserGoalPaneAndChat(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	goal := func(title, desc string) string {
		var g struct{ ID string }
		api.must("POST", "/api/goals", map[string]any{"project": pid, "title": title, "description": desc, "mode": "ask"}, http.StatusCreated, &g)
		return g.ID
	}
	asks := goal("Billing export", "Export invoices.\nAsk first: Should the export include **refunded** invoices?")
	plans := goal("CSV export", "Export invoices as CSV.")
	waits := func(id, what string) {
		t.Helper()
		for deadline := time.Now().Add(time.Minute); ; time.Sleep(100 * time.Millisecond) {
			var g struct {
				WaitingFor string `json:"waiting_for"`
			}
			api.must("GET", "/api/goals/"+id, nil, http.StatusOK, &g)
			if g.WaitingFor == what {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("goal %s waits for %q, not %q", id, g.WaitingFor, what)
			}
		}
	}
	waits(asks, "question")
	waits(plans, "plan")

	b := newBrowser(t, chrome)
	b.signIn(h)
	row := func(id string) string { return fmt.Sprintf(`#goal-list li[data-id=%q] .goal-open`, id) }
	open := func(id string) string {
		return fmt.Sprintf(`!document.querySelector("#goal-detail").hidden && document.querySelector('#goal-list li[data-id=%q].selected') && document.querySelector("#gd-actions button")`, id)
	}
	// Nothing lies over the list's rows, New goal… or the goal's buttons.
	const clear = `(() => {
		const free = (el) => { const r = el.getBoundingClientRect(); const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2); return r.width > 0 && el.contains(at); };
		return [...document.querySelectorAll("#goal-list .goal-open"), document.querySelector("#goal-new"), ...document.querySelectorAll("#gd-actions button"), document.querySelector("#gd-note")].every(free);
	})()`
	const chatGoal = `document.querySelector("#chat-pane:not([hidden]) .chat-tab-goal [aria-selected=true]")`

	b.do("a wide screen", chromedp.EmulateViewport(1600, 900))
	b.until("open Goals", `document.querySelector("#view-goals.active") && document.querySelectorAll("#goal-list li").length === 2`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=goals]`)))
	b.until("open the goal that asks", open(asks), chromedp.Click(chromedp.CSS(row(asks))))
	b.waitJS("the list beside the goal", clear)

	b.clickTextUntil("#gd-actions button", "Talk to coordinator", chatGoal)
	b.waitJS("the question in the chat, as the coordinator's, and no wake-up", `(() => {
		const log = [...document.querySelectorAll("#chat-pane .chat-panel:not([hidden]) .msg")];
		const text = log.map((m) => m.textContent).join("\n");
		return log.some((m) => m.classList.contains("assistant") && m.textContent.includes("refunded")) && !text.includes("propose_plan") && !text.includes("The goal was made");
	})()`)
	b.waitJS("the chat beside the goal and the list", clear+` && document.querySelector("#goal-detail").getBoundingClientRect().right <= document.querySelector("#chat-pane").getBoundingClientRect().left + 1`)

	// Another goal: the chat follows it, in the one goal tab.
	b.until("open the goal with a plan", open(plans), chromedp.Click(chromedp.CSS(row(plans))))
	b.waitJS("the chat on that goal", chatGoal+`?.textContent.includes("CSV export") && document.querySelectorAll("#chat-tabs .chat-tab-goal").length === 1`)
	b.waitJS("the plan in the goal", `[...document.querySelectorAll("#gd-plan .proposed")].length === 2 && document.querySelector("#gd-note").textContent.includes("accept")`)
	b.clickTextUntil("#gd-actions button", "Accept plan", `![...document.querySelectorAll("#gd-actions button")].some((e) => e.textContent.includes("Accept plan"))`)
	var g struct {
		Status string
		Tasks  []struct{ Status string }
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		api.must("GET", "/api/goals/"+plans, nil, http.StatusOK, &g)
		if len(g.Tasks) == 2 || time.Now().After(deadline) {
			break
		}
	}
	if len(g.Tasks) != 2 || g.Tasks[0].Status == "draft" {
		t.Fatalf("accepted plan: %+v", g)
	}
	b.until("close the goal", `document.querySelector("#goal-detail").hidden && document.querySelector("#chat-pane").hidden && !document.querySelector("#chat-tabs .chat-tab-goal")`,
		chromedp.Click(chromedp.CSS("#gd-close")))

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.until("open the goal that asks", open(asks), chromedp.Click(chromedp.CSS(row(asks))))
	b.clickTextUntil("#gd-actions button", "Talk to coordinator", chatGoal+` && getComputedStyle(document.querySelector("#goal-detail")).display === "none" && !document.querySelector("#chat-goal-back").hidden`)
	b.until("back to the goal", `document.querySelector("#chat-pane").hidden && document.querySelector("#goal-detail").getBoundingClientRect().width > 0`,
		chromedp.Click(chromedp.CSS("#chat-goal-back")))
}
