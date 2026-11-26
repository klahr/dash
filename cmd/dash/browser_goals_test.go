package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Ending a goal from its pane, on a wide screen and on a phone's: its main
// action, one beside it and ⋯ fit on one row; Stop is in ⋯ and shows the
// goal stopped at once, sent once its toast has gone without Undo; only a
// stopped goal offers Delete, which takes it from the list and closes the
// pane, and Undo brings it back. A goal deleted elsewhere (another window,
// the API) leaves the list and its open pane too.
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
	status := func(id string) string {
		var g struct{ Status string }
		code, body := api.try("GET", "/api/goals/"+id, nil)
		if code == http.StatusNotFound {
			return "gone"
		}
		_ = json.Unmarshal([]byte(body), &g)
		return g.Status
	}
	await := func(id, want string) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); status(id) != want; time.Sleep(100 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("goal %s is %s, not %s", id, status(id), want)
			}
		}
	}

	b := newBrowser(t, chrome)
	b.signIn(h)
	row := func(id string) string { return fmt.Sprintf(`#goal-list li[data-id=%q] .goal-open`, id) }
	gone := func(id string) string {
		return fmt.Sprintf(`!document.querySelector('#goal-list li[data-id=%q]') && document.querySelector("#goal-detail").hidden`, id)
	}
	const oneRow = `(() => {
		const bs = [...document.querySelectorAll("#gd-actions > *")];
		return bs.length >= 2 && bs.length <= 3 && bs.at(-1).id === "gd-more" && bs.every((e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.left >= 0 && r.right <= innerWidth && Math.abs(r.top - bs[0].getBoundingClientRect().top) < 4; });
	})()`
	open := func(id string) {
		b.t.Helper()
		b.until("open the goal", fmt.Sprintf(`!document.querySelector("#goal-detail").hidden && document.querySelector('#goal-list li[data-id=%q].selected')`, id),
			chromedp.Click(chromedp.CSS(row(id))))
		b.waitJS("its actions on one row, ⋯ last", oneRow)
	}
	menu := func(item, expr string) {
		b.t.Helper()
		b.until("the goal's menu", `!!document.querySelector(".pop-menu")`, chromedp.Click(chromedp.CSS(`#gd-more`)))
		b.clickTextUntil(".pop-menu [role=menuitem]", item, expr)
	}
	const noDelete = `![...document.querySelectorAll(".pop-menu [role=menuitem]")].some((e) => e.textContent.includes("Delete"))`
	stop := func(id string) {
		b.t.Helper()
		b.until("the goal's menu", `!!document.querySelector(".pop-menu")`, chromedp.Click(chromedp.CSS(`#gd-more`)))
		b.waitJS("no Delete while it runs", noDelete)
		b.clickTextUntil(".pop-menu [role=menuitem]", "Stop goal", `document.querySelector("#gd-meta .goal-status")?.textContent === "Stopped"`)
		await(id, "done")
	}
	del := func(id string) {
		b.t.Helper()
		menu("Delete goal", gone(id))
		await(id, "gone")
	}

	b.do("a wide screen", chromedp.EmulateViewport(1280, 900))
	b.until("open Goals", `document.querySelector("#view-goals.active") && document.querySelectorAll("#goal-list li").length === 3`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=goals]`)))
	open(wide)
	stop(wide)
	// Delete, then Undo: it comes back, and stays.
	menu("Delete goal", gone(wide))
	// It is back, under Done: the list shows the active goals.
	b.clickTextUntil(".toast-action", "Undo", `!document.querySelector("#goal-show").hidden && document.querySelector("#goal-n-done").textContent === "1"`)
	time.Sleep(7 * time.Second)
	if s := status(wide); s != "done" {
		t.Fatalf("undone delete: goal is %s", s)
	}
	b.until("the done goals", fmt.Sprintf(`!!document.querySelector('#goal-list li[data-id=%q]')`, wide),
		chromedp.Click(chromedp.CSS(`#goal-show [data-show=done]`)))
	open(wide)
	del(wide)

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	// The sidebar turns into the closed drawer at once: sliding out, it
	// covered the page and took the next click (Shell, Add project).
	if hidden, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`getComputedStyle(document.querySelector("#menu")).visibility === "hidden"`)); err != nil || !hidden {
		b.fail("the drawer hidden at once", fmt.Errorf("hidden: %v, err: %v", hidden, err))
	}
	open(phone)
	stop(phone)
	del(phone)

	// Deleted elsewhere while open here.
	b.until("open the last goal", `!document.querySelector("#goal-detail").hidden`, chromedp.Click(chromedp.CSS(row(elsewhere))))
	api.must("DELETE", "/api/goals/"+elsewhere, nil, http.StatusNoContent, nil)
	b.waitJS("it leaves the list and the pane closes", gone(elsewhere)+` && !document.querySelector("#goal-empty").hidden`)
}

// A goal opens beside the list, every row still clickable, what it is for
// first and rendered, its status once, its log with Dash's entries as
// Dash's; Answer opens its chat beside the goal (never over it or the list),
// with the coordinator's question in it and no wake-up of Dash's as yours;
// the chat follows the goal opened next, in one tab, and closes with it; a
// waiting plan is shown in the goal's tasks and accepted there, and the log
// names its tasks by title, never by id. On a phone the chat is a page with
// a Goal button back to the goal.
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
		return [...document.querySelectorAll("#goal-list .goal-open"), document.querySelector("#goal-new"), ...document.querySelectorAll("#gd-actions button"), document.querySelector("#gd-note:not([hidden])")].filter(Boolean).every(free);
	})()`
	const chatGoal = `document.querySelector("#chat-pane:not([hidden]) .chat-tab-goal [aria-selected=true]")`

	b.do("a wide screen", chromedp.EmulateViewport(1600, 900))
	b.until("open Goals", `document.querySelector("#view-goals.active") && document.querySelectorAll("#goal-list li").length === 2`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=goals]`)))
	b.until("open the goal that asks", open(asks), chromedp.Click(chromedp.CSS(row(asks))))
	b.waitJS("the list beside the goal", clear)
	b.waitJS("what it is for first, rendered; the question once; Dash's entries as Dash's", `(() => {
		const body = document.querySelector("#gd-body"), first = [...body.children].find((e) => !e.hidden);
		const status = document.querySelector("#gd-meta .goal-status")?.textContent;
		const who = [...document.querySelectorAll("#gd-log .goal-log-kind")].map((e) => e.textContent);
		const created = [...document.querySelectorAll("#gd-log .goal-log-entry")].find((e) => e.textContent.includes("Goal created"));
		return first?.id === "gd-desc-wrap" && document.querySelector("#gd-desc strong")?.textContent === "refunded" &&
			status === "Question for you" && !document.querySelector("#gd-body").textContent.includes("Waiting for you") &&
			document.querySelector("#gd-note strong")?.textContent === "refunded" &&
			created?.querySelector(".goal-log-kind")?.textContent === "Dash" && !who.includes("You") &&
			document.querySelector("#gd-actions .btn.primary")?.textContent.trim() === "Answer";
	})()`)

	b.clickTextUntil("#gd-actions button", "Answer", chatGoal)
	b.waitJS("the question in the chat, as the coordinator's, and no wake-up", `(() => {
		const log = [...document.querySelectorAll("#chat-pane .chat-panel:not([hidden]) .msg")];
		const text = log.map((m) => m.textContent).join("\n");
		return log.some((m) => m.classList.contains("assistant") && m.textContent.includes("refunded")) && !text.includes("propose_plan") && !text.includes("The goal was made");
	})()`)
	b.waitJS("the chat beside the goal and the list", clear+` && document.querySelector("#goal-detail").getBoundingClientRect().right <= document.querySelector("#chat-pane").getBoundingClientRect().left + 1`)

	// Another goal: the chat follows it, in the one goal tab.
	b.until("open the goal with a plan", open(plans), chromedp.Click(chromedp.CSS(row(plans))))
	b.waitJS("the chat on that goal", chatGoal+`?.textContent.includes("CSV export") && document.querySelectorAll("#chat-tabs .chat-tab-goal").length === 1`)
	b.waitJS("the plan in the goal's tasks; the list row agrees", fmt.Sprintf(`document.querySelectorAll("#gd-plan .proposed").length === 2 && document.querySelector("#gd-note").hidden &&
		document.querySelector("#gd-meta .goal-status")?.textContent === "Plan to accept" &&
		document.querySelector('#goal-list li[data-id=%q] .cl-count')?.textContent === "0 of 2 merged" &&
		document.querySelectorAll('#goal-list li[data-id=%q] .goal-status').length === 1 && !document.querySelector('#goal-list li[data-id=%q] .goal-wait')`, plans, plans, plans))
	b.clickTextUntil("#gd-actions button", "Accept plan", `![...document.querySelectorAll("#gd-actions button")].some((e) => e.textContent.includes("Accept plan"))`)
	var g struct {
		Status string
		Tasks  []struct{ ID, Status string }
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
	b.waitJS("the tasks in the goal, no ids in its log", fmt.Sprintf(`document.querySelectorAll("#gd-plan .goal-task:not(.proposed)").length === 2 &&
		!document.querySelector("#gd-log").textContent.includes(%q) && !document.querySelector("#gd-log").textContent.includes(%q)`, g.Tasks[0].ID, g.Tasks[1].ID))
	b.until("close the goal", `document.querySelector("#goal-detail").hidden && document.querySelector("#chat-pane").hidden && !document.querySelector("#chat-tabs .chat-tab-goal")`,
		chromedp.Click(chromedp.CSS("#gd-close")))

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.until("open the goal that asks", open(asks), chromedp.Click(chromedp.CSS(row(asks))))
	b.clickTextUntil("#gd-actions button", "Answer", chatGoal+` && getComputedStyle(document.querySelector("#goal-detail")).display === "none" && !document.querySelector("#chat-goal-back").hidden`)
	b.until("back to the goal", `document.querySelector("#chat-pane").hidden && document.querySelector("#goal-detail").getBoundingClientRect().width > 0`,
		chromedp.Click(chromedp.CSS("#chat-goal-back")))
}

// The Goals list takes the page's width (cards in columns, one on a phone),
// splits active goals from done ones, and lists every project's goals under
// All projects, each naming its project; one of another project opens with
// its project selected. The Board badge's "goals" leads there too. New goal
// asks only what to get done and how its tasks start, with no help texts,
// and the goal opens at once.
func TestBrowserGoalsList(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	var shop struct{ ID string }
	api.must("POST", "/api/projects", map[string]any{"name": "shop", "remote_url": "https://example.com/shop.git", "runs_on": []string{"any"}}, http.StatusCreated, &shop)
	goal := func(project, title string) string {
		var g struct{ ID string }
		api.must("POST", "/api/goals", map[string]any{"project": project, "title": title, "description": title, "mode": "ask"}, http.StatusCreated, &g)
		return g.ID
	}
	plans, _, ended := goal(pid, "Faster search"), goal(pid, "Smaller images"), goal(pid, "Old goal")
	other := goal(shop.ID, "Checkout in one step")
	api.must("POST", "/api/goals/"+ended+"/stop", map[string]any{}, http.StatusOK, nil)
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(100 * time.Millisecond) {
		var g struct {
			WaitingFor string `json:"waiting_for"`
		}
		api.must("GET", "/api/goals/"+plans, nil, http.StatusOK, &g)
		if g.WaitingFor == "plan" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goal %s waits for %q, not a plan", plans, g.WaitingFor)
		}
	}

	b := newBrowser(t, chrome)
	b.signIn(h)
	row := func(id string) string { return fmt.Sprintf(`#goal-list li[data-id=%q] .goal-open`, id) }
	const ids = `[...document.querySelectorAll("#goal-list li")].map((li) => li.dataset.id)`
	// No help texts: no explanations under fields, no placeholders, no tooltips on the page's buttons.
	const noHelp = `(() => {
		const sheet = document.querySelector("#goal-sheet");
		return !sheet.querySelector("em, .hint") && ![...sheet.querySelectorAll("input, textarea")].some((e) => e.placeholder) &&
			![...document.querySelectorAll("#view-goals [title]")].length && !document.querySelector("#goal-empty p:not(.empty-title)");
	})()`

	b.do("a wide screen", chromedp.EmulateViewport(1600, 900))
	b.until("open Goals", `document.querySelector("#view-goals.active") && document.querySelectorAll("#goal-list li").length === 2`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=goals]`)))
	b.waitJS("the active goals in columns, using the width; Done counts the stopped one", fmt.Sprintf(`(() => {
		const lis = [...document.querySelectorAll("#goal-list li")], list = document.querySelector("#goal-list").getBoundingClientRect();
		return list.width > 1000 && Math.abs(lis[0].getBoundingClientRect().top - lis[1].getBoundingClientRect().top) < 2 &&
			!%s.includes(%q) && document.querySelector("#goal-n-done").textContent === "1" && !document.querySelector("#goal-scope").hidden;
	})()`, ids, ended))
	b.until("the done goals", fmt.Sprintf(`%s.join() === %q`, ids, ended), chromedp.Click(chromedp.CSS(`#goal-show [data-show=done]`)))
	b.until("the active goals again", fmt.Sprintf(`%s.length === 2 && !%s.includes(%q)`, ids, ids, ended), chromedp.Click(chromedp.CSS(`#goal-show [data-show=active]`)))

	// All projects: shop's goal too, naming its project; opening it selects shop.
	b.until("every project's goals", fmt.Sprintf(`document.querySelector('#goal-list li[data-id=%q] .goal-where')?.textContent === "shop" && %s.length === 3`, other, ids),
		chromedp.Click(chromedp.CSS(`#goal-scope [data-scope=all]`)))
	b.until("shop's goal open, in shop", fmt.Sprintf(`!document.querySelector("#goal-detail").hidden && document.querySelector("#gd-title").textContent === "Checkout in one step" &&
		document.querySelector("#project-name").textContent.includes("shop") && document.querySelector('#goal-list li[data-id=%q].selected')`, other),
		chromedp.Click(chromedp.CSS(row(other))))
	b.until("close it", `document.querySelector("#goal-detail").hidden`, chromedp.Click(chromedp.CSS("#gd-close")))

	// New goal: what to get done and how its tasks start; the goal opens at once, named from it.
	b.until("New goal", `document.querySelector("#goal-sheet").open`, chromedp.Click(chromedp.CSS("#goal-new")))
	b.waitJS("only what to get done and how tasks start, no help texts", noHelp+` && document.querySelector("#goal-title-field").hidden && document.querySelector("#goal-max-field").hidden &&
		document.querySelector("#goal-form select[name=mode]").value === "ask" && !document.querySelector("#goal-form [name=weekly_share]")`)
	b.do("say what to get done", chromedp.SendKeys(`#goal-form textarea[name=description]`, "Ship the new checkout"))
	b.until("the goal opens", `!document.querySelector("#goal-detail").hidden && document.querySelector("#gd-title").textContent.includes("checkout") && !document.querySelector(".toast")?.textContent.includes("Goal started")`,
		chromedp.Click(chromedp.CSS("#goal-save")))
	b.until("close it", `document.querySelector("#goal-detail").hidden`, chromedp.Click(chromedp.CSS("#gd-close")))

	// The Board badge's goals: back in the first project, its goal waiting for you leads to every project's goals.
	b.until("the project's goals", `document.querySelector("#goal-scope [data-scope=project]").getAttribute("aria-current") === "page"`,
		chromedp.Click(chromedp.CSS(`#goal-scope [data-scope=project]`)))
	b.do("the first project's board", chromedp.Navigate(h.base+"/p/"+pid+"/board"))
	b.waitJS("the badge counts its goal", `/^\d+ goals?$/.test(document.querySelector("#review-badge .rb-goals")?.textContent || "")`)
	b.until("the badge's goals", `document.querySelector("#view-goals.active") && document.querySelector("#goal-scope [data-scope=all]").getAttribute("aria-current") === "page" && document.querySelectorAll("#goal-list li").length === 4`,
		chromedp.Click(chromedp.CSS(`#review-badge .rb-goals`)))

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("one column, nothing wider than the screen", `(() => {
		const lis = [...document.querySelectorAll("#goal-list li")];
		return lis.length >= 2 && lis[1].getBoundingClientRect().top > lis[0].getBoundingClientRect().bottom - 1 &&
			[...document.querySelectorAll("#view-goals .cl-head > *:not([hidden]), #goal-list li")].every((e) => e.getBoundingClientRect().right <= innerWidth);
	})()`)
}
