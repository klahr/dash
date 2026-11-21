package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// Claude asks a question mid-task (AskUserQuestion): the board shows a
// question card, with radios, checkboxes for a multi-select question and an
// Other field, on a phone and on a desktop; its answers reach Claude.
func TestBrowserQuestionCard(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api := h.api
	b := newBrowser(t, chrome)
	b.signIn(h)
	shot := func(name string) {
		dir := os.Getenv("DASH_TEST_SHOTS")
		if dir == "" {
			return
		}
		time.Sleep(time.Second)
		ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
		defer cancel()
		if png, err := chromedp.Run(ctx, chromedp.FullScreenshot(90)); err == nil {
			_ = os.WriteFile(filepath.Join(dir, name+".png"), png, 0o644)
		}
	}

	// In a project of the runner's own, as in TestBrowserMainFlows.
	const peer = "/peer/laptop"
	var local struct{ ID string }
	api.must("POST", peer+"/api/projects", map[string]any{"name": "local"}, http.StatusCreated, &local)
	b.clickTextUntil(`#side-project-list .side-project`, "local", `document.querySelector("#project-name")?.textContent === "local"`)
	var task struct{ ID string }
	api.must("POST", peer+"/api/projects/"+local.ID+"/todos", map[string]any{"title": "Needs a choice", "prompt": "ask a question first", "enqueue": true},
		http.StatusCreated, &task)
	const card = `#perm-banner form.perm.ask`
	b.waitJS("the question card", `(() => { const c = document.querySelector("`+card+`"); return !!c && c.querySelectorAll("fieldset.ask-q").length === 2
		&& c.querySelectorAll("fieldset.ask-q")[0].querySelectorAll("input[type=radio]").length === 3
		&& c.querySelectorAll("fieldset.ask-q")[1].querySelectorAll("input[type=checkbox]").length === 3
		&& c.textContent.includes("Database") && c.textContent.includes("One file, no server")
		&& !c.querySelector("button[title^='Always']"); })()`)
	b.waitJS("the card says it waits for an answer", `document.querySelector(`+"`"+cardSel(task.ID)+"`"+`)?.textContent.includes("Waiting for your answer")
		&& document.querySelector("#review-badge").textContent.includes("1 question")`)
	shot("question-wide")

	// On a phone the card fits the screen.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the card fits the phone", `(() => { const c = document.querySelector("`+card+`"); return c && c.scrollWidth <= c.clientWidth + 1 && c.getBoundingClientRect().right <= innerWidth; })()`)
	shot("question-narrow")

	// Submitting with a question unanswered keeps the card.
	b.do("submit too soon", chromedp.Click(chromedp.CSS(card+` button[type=submit]`)))
	time.Sleep(300 * time.Millisecond)
	b.waitJS("the card still there", `!!document.querySelector("`+card+`") && !document.querySelector("`+card+` button[type=submit]").disabled`)

	// SQLite, then Unit and an answer of my own for the tests.
	b.clickIn(card+` fieldset.ask-q:nth-of-type(1) label.radio`, "SQLite", "input")
	b.clickIn(card+` fieldset.ask-q:nth-of-type(2) label.radio`, "Unit", "input")
	b.do("write my own answer", chromedp.SendKeys(chromedp.CSS(card+` fieldset.ask-q:nth-of-type(2) .ask-other-text`), "a smoke test"))
	b.waitJS("Other chosen as I type", `document.querySelector("`+card+` fieldset.ask-q:nth-of-type(2) .ask-other input[type=checkbox]").checked`)
	shot("question-answered-narrow")
	b.do("submit with Enter", chromedp.SendKeys(chromedp.CSS(card+` fieldset.ask-q:nth-of-type(2) .ask-other-text`), kb.Enter))
	waitStatus(t, api, peer+"/api/todos/"+task.ID, "review", nil)
	b.waitJS("the question to go", `!document.querySelector("#perm-banner .perm")`)
	_, diff := api.try("GET", peer+"/api/todos/"+task.ID+"/diff", nil)
	if !strings.Contains(diff, "question.txt") ||
		!strings.Contains(diff, `+allow {"Which database should the app use?":"SQLite","Which tests should I add?":"Unit, a smoke test"}`) {
		t.Fatalf("Claude didn't get the answers:\n%s", diff)
	}
}
