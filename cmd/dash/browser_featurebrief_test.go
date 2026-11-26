package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

// Write a feature brief on a card: the project guide's planner in its own
// sheet, with a feature's steps and topics; the card's description is the
// idea. Start puts the brief on the card and queues a task from it, linked
// to the card. On a phone, New task's Write a feature brief carries what
// was typed over as the idea, for a new card.
func TestBrowserFeatureBrief(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	var c struct{ ID string }
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Export reports", "description": "Finance wants the reports as files.",
		"column": "ideas", "project": h.project.ID}, http.StatusCreated, &c)

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open Planning", `document.querySelector("#view-planning.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=planning]`)))
	card := fmt.Sprintf(`#view-planning .pl-card[data-id=%q]`, c.ID)
	b.until("open the card", `!document.querySelector("#card-detail").hidden && !!document.querySelector("#pd-desc")`,
		chromedp.Click(chromedp.CSS(card+" .pl-open")))
	b.until("write a feature brief", `document.querySelector("#feature-sheet").open && !document.querySelector("#feature-slot #plan-work").hidden`,
		chromedp.Click(chromedp.CSS(`#pd-brief`)))
	b.waitJS("the card's words, its project, and a feature's steps", `document.querySelector("#plan-idea").value === "Finance wants the reports as files." &&
		document.querySelector("#plan-name").value === "Export reports" && document.querySelector("#plan-project").selectedOptions[0]?.textContent === "app" &&
		[...document.querySelectorAll("#plan-stepper .plan-st-title")].map((e) => e.textContent).join() === "Idea,Interview,Brief,Start" &&
		!document.querySelector("#feature-actions #plan-next").hidden && document.querySelector("#plan-card").textContent.includes("Export reports")`)
	shot(b, "feature-idea-wide")
	b.until("start the interview", `document.querySelector(".plan-q-card")`, chromedp.Click(chromedp.CSS(`#plan-next:not(:disabled)`)))
	b.waitJS("a feature's question and topics, the brief beside them", `document.querySelector(".plan-q-card.is-missing") &&
		document.querySelector("#plan-live h1")?.textContent === "Fake feature" &&
		[...document.querySelectorAll(".plan-topic > span:not(.sr-only)")].map((e) => e.textContent).join().startsWith("Problem & who it's for,Behaviour & scope") &&
		document.querySelector(".plan-topic.is-done")?.textContent.includes("Behaviour")`)
	shot(b, "feature-interview-wide")
	b.clickIn(".plan-q-card .plan-chip", "Everyone", "span")
	b.until("send the answer", `document.querySelector("#plan-talk .plan-qa-a")?.textContent === "Everyone"`,
		chromedp.Click(chromedp.CSS(`.plan-compose .btn.primary:not(:disabled)`)))
	b.until("review the brief", `document.querySelector("#plan-verdict.is-ready")`, chromedp.Click(chromedp.CSS(`#plan-next:not(:disabled)`)))
	b.waitJS("Next is how to start", `document.querySelector("#plan-next").textContent.trim() === "Choose how to start"`)
	b.until("choose how to start", `!document.querySelector('.plan-pane[data-step="start"]').hidden`, chromedp.Click(chromedp.CSS(`#plan-next:not(:disabled)`)))
	b.waitJS("a task by default", `document.querySelector("input[name=plan-start]:checked")?.value === "task" &&
		document.querySelector("#plan-next").textContent.trim() === "Start the task" && document.querySelector("#plan-start-summary").textContent.includes("app")`)
	shot(b, "feature-start-wide")
	b.until("start the task", `!document.querySelector("#feature-sheet").open`, chromedp.Click(chromedp.CSS(`#plan-next:not(:disabled)`)))
	b.waitJS("the card, with the brief and the task", `!document.querySelector("#card-detail").hidden && document.querySelector("#pd-desc.md h1")?.textContent === "Fake feature" &&
		document.querySelectorAll("#pd-body .pl-link").length === 1`)

	var drafts []json.RawMessage
	h.api.must("GET", "/api/plans", nil, http.StatusOK, &drafts)
	if len(drafts) != 0 {
		t.Errorf("drafts after starting: %s", drafts)
	}

	// On a phone: from New task, what was typed is the idea.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.run("to the board", `document.querySelector(".nav > button[data-view=board]").click(); true`)
	b.waitJS("the board", `document.querySelector("#view-board.active") && !document.querySelector("#new-todo").hidden`)
	b.until("open New task", `document.querySelector("#quick-sheet").open`, chromedp.Click(chromedp.ID("new-todo")))
	b.do("type a task", chromedp.SendKeys(chromedp.CSS(".quick-row"), "Let people export the board as CSV"))
	b.until("write a feature brief instead", `document.querySelector("#feature-sheet").open && !document.querySelector("#quick-sheet").open`,
		chromedp.Click(chromedp.ID("quick-brief")))
	b.waitJS("the text as the idea, for a new card, the compact steps", `document.querySelector("#plan-idea").value === "Let people export the board as CSV" &&
		document.querySelector("#plan-card").textContent.includes("a new card") && document.querySelector("#plan-stepper-compact").textContent === "Step 1 of 4 · Idea" &&
		document.querySelector("#feature-sheet").offsetWidth >= innerWidth - 1`)
	shot(b, "feature-idea-phone")
	b.until("start the interview", `document.querySelector(".plan-q-card")`, chromedp.Click(chromedp.CSS(`#plan-next:not(:disabled)`)))
	shot(b, "feature-interview-phone")
	b.until("close it", `!document.querySelector("#feature-sheet").open`, chromedp.Click(chromedp.CSS(`#feature-sheet .sheet-head .icon-btn`)))
	h.api.must("GET", "/api/plans", nil, http.StatusOK, &drafts)
	if len(drafts) != 1 || !strings.Contains(string(drafts[0]), `"kind":"feature"`) {
		t.Errorf("the phone's draft: %s", drafts)
	}

	// Planning's New card: what was typed is the idea, and no card is
	// added until the guide saves it; its draft is listed as a feature's.
	b.run("to Planning", `document.querySelector(".nav > button[data-view=planning]").click(); true`)
	b.menuPick("#pl-head-more", "New card", `document.querySelector(".pl-composer .pl-compose-text")`, "") // a phone's: under ⋯
	b.do("type a card", chromedp.SendKeys(chromedp.CSS(".pl-compose-text"), "Dark mode"))
	b.until("write a feature brief instead", `document.querySelector("#feature-sheet").open && !document.querySelector(".pl-composer")`,
		chromedp.Click(chromedp.CSS(".pl-compose-brief")))
	b.waitJS("the text as the idea", `document.querySelector("#plan-idea").value === "Dark mode"`)
	b.until("all briefs", `!document.querySelector("#plan-home").hidden`, chromedp.Click(chromedp.ID("plan-all")))
	b.waitJS("the draft, a feature's", `document.querySelector("#plan-drafts .plan-draft-meta")?.textContent.startsWith("Feature · app · Interview")`)
	shot(b, "feature-drafts-phone")
	b.until("close it", `!document.querySelector("#feature-sheet").open`, chromedp.Click(chromedp.CSS(`#feature-sheet .sheet-head .icon-btn`)))
	var cards []json.RawMessage
	h.api.must("GET", "/api/planning/cards", nil, http.StatusOK, &cards)
	if len(cards) != 2 { // Export reports, and the phone's
		t.Errorf("cards: %s", cards)
	}
}

// shot saves a screenshot to $DASH_TEST_SHOTS, when set, to look at.
func shot(b *browser, name string) {
	dir := os.Getenv("DASH_TEST_SHOTS")
	if dir == "" {
		return
	}
	if buf, err := chromedp.Run(b.ctx, chromedp.CaptureScreenshot()); err == nil {
		_ = os.WriteFile(filepath.Join(dir, name+".png"), buf, 0o644)
	}
}
