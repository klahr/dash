package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// A planner without a runner sees, where Claude would be (a card's Write a
// feature brief, the Chat), "Install a runner to use Claude here", which
// opens the setup steps with a code. Once a runner registered with it
// connects, the planner chats on it and writes a feature brief there, and
// the brief's Start offers a task or Planned (a goal needs its owner). On a
// phone, the drawer has Chat, and the chat fills the screen.
func TestBrowserPlannerClaude(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	var c struct{ ID string }
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Export reports", "description": "Finance wants the reports as files.",
		"column": "ideas", "project": h.project.ID}, http.StatusCreated, &c)
	var me struct{ Org struct{ ID int64 } }
	h.api.must("GET", "/api/me", nil, http.StatusOK, &me)
	var inv struct{ Token string }
	h.api.must("POST", fmt.Sprintf("/api/orgs/%d/invitations", me.Org.ID), map[string]any{"email": "pia@example.com", "role": "planner", "password": h.password}, http.StatusCreated, &inv)
	newClient(t, h.base).must("POST", "/api/invitations/register", map[string]any{"token": inv.Token, "username": "pia", "password": "another-long-password"}, http.StatusCreated, nil)

	b := newBrowser(t, chrome)
	b.do("sign in as the planner",
		chromedp.Navigate(h.base+"/"),
		chromedp.WaitVisible(chromedp.CSS(`#login input[name=username]`)),
		chromedp.SendKeys(chromedp.CSS(`#login input[name=username]`), "pia"),
		chromedp.SendKeys(chromedp.CSS(`#login input[name=password]`), "another-long-password"),
		chromedp.Click(chromedp.CSS(`#login button.btn.primary`)),
	)
	card := fmt.Sprintf(`.pl-card[data-id=%q]`, c.ID)
	b.waitJS("Planning with the card", `location.pathname === "/planning.html" && !!document.querySelector(`+jsq(card)+`)`)

	// Without a runner: what Claude needs, in place of its buttons.
	b.until("open the card", `!document.querySelector("#card-detail").hidden && !!document.querySelector("#pd-desc")`,
		chromedp.Click(chromedp.CSS(card+" .pl-open")))
	b.waitJS("Install a runner, no feature brief button", `document.querySelector("#pd-actions .claude-needed")?.textContent === "Install a runner to use Claude here" &&
		![...document.querySelectorAll("#pd-actions button")].some((x) => x.textContent.includes("Write a feature brief"))`)
	shot(b, "planner-card-no-runner-wide")
	b.run("close the card", `document.querySelector("#pd-close")?.click(); true`)
	b.until("the Chat section", `!document.querySelector("#chat-main").hidden && document.querySelector("#planning-main").hidden`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=chat]`)))
	b.waitJS("what the chat needs, no composer", `!document.querySelector("#pl-chat-needed").hidden && document.querySelector("#pl-chat-form").hidden &&
		document.querySelector("#pl-chat-needed .claude-needed")?.textContent === "Install a runner to use Claude here"`)
	shot(b, "planner-chat-no-runner-wide")
	b.until("the setup steps, with a code", `document.querySelector("#runners-sheet").open && /--code \S+/.test(document.querySelector("#pl-runner-add pre")?.textContent || "")`,
		chromedp.Click(chromedp.CSS(`#pl-chat-needed .claude-needed`)))
	shot(b, "planner-runner-add-wide")
	code, err := chromedp.Run(b.ctx, chromedp.Evaluate[string](`document.querySelector("#pl-runner-add pre").textContent`))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`--code (\S+)`).FindStringSubmatch(code)
	if m == nil {
		t.Fatalf("no code in %q", code)
	}

	// pia's runner: the install command's code, the fake claude.
	dir := procsTempDir(t)
	startDash(t, h.dash, h.runnerEnv, "runner", "-data", filepath.Join(dir, "runner"), "-projects", filepath.Join(dir, "projects"),
		"-name", "pias-mac", "-env", filepath.Join(dir, "none.env"), "--server", h.base, "--code", m[1])
	b.waitJS("the runner, online, in the overview", `[...document.querySelectorAll("#pl-runners-list .item-row")].some((r) => r.textContent.includes("pias-mac") && !!r.querySelector(".runner-dot.on")) &&
		document.querySelector("#runners-sheet .rail-section[data-section=overview]").classList.contains("current")`)
	shot(b, "planner-runners-wide")
	b.until("close Runners", `!document.querySelector("#runners-sheet").open`, chromedp.Click(chromedp.CSS(`#runners-sheet .sheet-head button`)))
	// "pias-mac is connected" sits bottom right, over the chat's Send and the
	// brief's Next, and a toast under the pointer stays: dismiss it, as a
	// user would, so the clicks below reach their buttons.
	b.run("dismiss the toast", `document.querySelectorAll("#toasts .toast-close").forEach((x) => x.click()); true`)
	b.waitJS("no toast", `!document.querySelector("#toasts .toast")`)

	// The chat, on it.
	b.waitJS("the composer, on the runner", `!document.querySelector("#pl-chat-form").hidden && document.querySelector("#pl-chat-runner-text").textContent === "On your runner pias-mac"`)
	// Send shows the question in the log at once; a click that missed (or a
	// failed send, which puts the question back) is tried again.
	b.do("ask", chromedp.SendKeys(chromedp.CSS(`#pl-chat-form textarea`), "What's planned?"))
	b.until("send", `[...document.querySelectorAll("#pl-chat-log .msg.user")].some((m) => m.textContent.includes("What's planned?"))`,
		chromedp.Click(chromedp.CSS(`#pl-chat-send`)))
	b.waitJS("the runner's answer", `[...document.querySelectorAll("#pl-chat-log .msg.assistant .reply")].some((r) => r.textContent.includes("Hello from the runner")) &&
		!document.querySelector("#pl-chat-log .msg.pending")`)
	shot(b, "planner-chat-wide")

	// A feature brief, on it: a task or Planned to start.
	b.until("back to Planning", `!document.querySelector("#planning-main").hidden`, chromedp.Click(chromedp.CSS(`.nav > button[data-view=planning]`)))
	b.until("open the card", `!document.querySelector("#card-detail").hidden && !!document.querySelector("#pd-desc")`,
		chromedp.Click(chromedp.CSS(card+" .pl-open")))
	b.until("write a feature brief", `document.querySelector("#feature-sheet").open && !document.querySelector("#plan-work").hidden`,
		chromedp.Click(chromedp.CSS(`#pd-brief`)))
	b.waitJS("the card's idea, on the runner", `document.querySelector("#plan-idea").value === "Finance wants the reports as files." &&
		document.querySelector("#plan-where").textContent.includes("pias-mac")`)
	b.until("start the interview", `document.querySelector(".plan-q-card")`, chromedp.Click(chromedp.CSS(`#plan-next:not(:disabled)`)))
	shot(b, "planner-feature-interview-wide")
	b.waitJS("Start offers a task or Planned", `[...document.querySelectorAll("input[name=plan-start]")].map((r) => r.value).join() === "task,planned"`)
	b.until("close it", `!document.querySelector("#feature-sheet").open`, chromedp.Click(chromedp.CSS(`#feature-sheet .sheet-head .icon-btn`)))

	// On a phone: Chat in the drawer, the chat full screen.
	b.run("close the card", `document.querySelector("#pd-close")?.click(); true`)
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.openDrawer(`document.querySelector(".menu-view[data-view=chat]").offsetWidth > 0`)
	b.until("Chat", `!document.querySelector("#chat-main").hidden && !document.querySelector(".app").classList.contains("menu-open")`,
		chromedp.Click(chromedp.CSS(`.menu-view[data-view=chat]`)))
	b.waitJS("the chat fills the screen, its composer in view", `(() => {
		const f = document.querySelector("#pl-chat-form").getBoundingClientRect();
		return document.querySelector("#chat-main").offsetWidth >= innerWidth - 1 && f.bottom <= innerHeight + 1 && f.height > 0;
	})()`)
	b.do("the drawer slides away", chromedp.Sleep(500*time.Millisecond))
	shot(b, "planner-chat-phone")
}

// jsq quotes s for JavaScript.
func jsq(s string) string { return fmt.Sprintf("%q", s) }
