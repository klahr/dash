package main

import (
	"context"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// fakeSpeech stands in for the Web Speech API: each recognizer made is in
// window.e2eRecs, and say(text, final) hands the page a result as Chrome would.
const fakeSpeech = `(() => {
	window.e2eRecs = [];
	window.SpeechRecognition = class {
		constructor() { window.e2eRecs.push(this); this.state = "new"; }
		start() { this.state = "listening"; }
		stop() { this.state = "stopped"; setTimeout(() => this.onend?.()); }
		abort() { this.state = "aborted"; }
		say(text, final) { const r = [{ transcript: text }]; r.isFinal = final; this.onresult?.({ resultIndex: 0, results: [r] }); }
	};
	window.e2eRec = () => window.e2eRecs.at(-1);
})()`

// Dictation (docs/dictation.md): off until turned on in Account → Dictation;
// then a mic button on the task form's instructions and the chat types what
// is said at the cursor, shows the interim words, stops on Esc without
// closing the sheet, says why on an error, and fits a phone's composer.
func TestBrowserDictation(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	b := newBrowser(t, chrome)
	b.do("fake speech recognition", chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		_, err := cdp.Call(ctx, tg, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: fakeSpeech})
		return err
	}))
	b.signIn(h)
	shown := func(sel string) string {
		return `(() => { const b = document.querySelector(` + jsq(sel) + `); return !!b && getComputedStyle(b).display !== "none" && b.getClientRects().length > 0; })()`
	}
	b.waitJS("no mic button while it is off", `!!document.querySelector("#todo-form .dictate-btn") && !`+shown("#todo-form .dictate-btn"))

	// Turn it on in Account → Dictation.
	b.until("open the account menu", `document.querySelector("#foot-menu").open`, chromedp.Click(chromedp.CSS(`#foot-menu summary`)))
	b.until("open Account", `document.querySelector("#account-sheet").open`, chromedp.Click(chromedp.CSS(`#account-open`)))
	b.until("the Dictation section", `document.querySelector('#account-sheet [data-section=dictation]').classList.contains("current")`,
		chromedp.Click(chromedp.CSS(`#account-sheet .rail-item[data-rail=dictation]`)))
	b.waitJS("the privacy note", `document.querySelector('#account-sheet [data-section=dictation]').textContent.includes("Chrome sends the audio to Google")`)
	b.until("turn it on", `localStorage.getItem("dash.dictation") === "1"`,
		chromedp.Click(chromedp.CSS(`#account-sheet [data-section=dictation] label.toggle`)))
	b.until("close Account", `!document.querySelector("#account-sheet").open`, chromedp.Click(chromedp.CSS(`#account-sheet .sheet-head button`)))

	// The task form: the words go in after what is there, with a space.
	b.until("open the new task sheet", `document.querySelector("#quick-sheet").open`, chromedp.Click(chromedp.ID("new-todo")))
	b.do("write the task", chromedp.SendKeys(chromedp.CSS(`#quick-sheet[open] #quick-rows .quick-row`), "Fix the bug"))
	b.until("open the full form", `document.querySelector("#todo-sheet").open`, chromedp.Click(chromedp.ID("quick-more")))
	prompt := `document.querySelector("#todo-form").prompt.value`
	b.waitJS("the mic button", shown("#todo-form .dictate-btn"))
	b.do("the cursor at the end", chromedp.Click(chromedp.CSS(`#todo-form .cm-content`)), chromedp.KeyEvent(kb.End, chromedp.KeyModifiers(chromedp.ModifierCtrl)))
	b.until("start dictating", `document.querySelector("#todo-form .dictate-btn").getAttribute("aria-pressed") === "true" && e2eRec()?.state === "listening" && e2eRec().lang === navigator.language`,
		chromedp.Click(chromedp.CSS(`#todo-form .dictate-btn`)))
	b.waitJS("the editor kept the focus", `document.activeElement?.matches("#todo-form .cm-content")`)
	b.waitJS("the interim words", `(e2eRec().say("and add a", false), `+prompt+` === "Fix the bug and add a")`)
	b.waitJS("the final words", `(e2eRec().say("and add a test", true), `+prompt+` === "Fix the bug and add a test")`)
	b.waitJS("more words", `(e2eRec().say("please", true), `+prompt+` === "Fix the bug and add a test please")`)
	b.until("Esc stops it, the sheet stays", `document.querySelector("#todo-form .dictate-btn").getAttribute("aria-pressed") === "false" && document.querySelector("#todo-sheet").open`,
		chromedp.KeyEvent(kb.Escape))

	// An error says why and stops.
	b.until("start again", `e2eRec()?.state === "listening"`, chromedp.Click(chromedp.CSS(`#todo-form .dictate-btn`)))
	b.waitJS("the error toast", `(e2eRec().onerror({ error: "not-allowed" }), [...document.querySelectorAll(".toast")].some((t) => t.textContent.includes("microphone")) &&
		document.querySelector("#todo-form .dictate-btn").getAttribute("aria-pressed") === "false")`)
	// On a phone the form's tools, Dictate among them, fit across.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the tools fit", shown("#todo-form .dictate-btn")+` && (() => { const r = document.querySelector("#todo-form .prompt-tools"); return r.scrollWidth <= r.clientWidth; })()`)
	b.do("a wide screen again", chromedp.EmulateViewport(1600, 1000))
	b.until("close the sheet", `!document.querySelector("#todo-sheet").open`, chromedp.KeyEvent(kb.Escape))

	// The chat, on a phone: the button fits beside Send, and typing ends dictation keeping the words.
	composer := `#chat-pane .chat-panel:not([hidden]) form.composer`
	b.until("open the chat", `!document.querySelector("#chat-pane").hidden`, chromedp.Click(chromedp.ID("pc-open")))
	b.waitJS("the mic beside Send", shown(composer+" .dictate-btn"))
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the mic in the composer", shown(composer+" .dictate-btn")+` && document.querySelector(`+jsq(composer+" .dictate-btn")+`).getBoundingClientRect().right <= innerWidth`)
	b.waitJS("the toasts out of the way", `(document.querySelectorAll(".toast").forEach((t) => t.remove()), true)`)
	b.do("type", chromedp.SendKeys(chromedp.CSS(composer+` textarea`), "Hello"))
	b.until("dictate", `e2eRec()?.state === "listening"`, chromedp.Click(chromedp.CSS(composer+` .dictate-btn`)))
	b.waitJS("the interim words", `(e2eRec().say("world", false), document.querySelector(`+jsq(composer+" textarea")+`).value === "Hello world")`)
	b.do("type on", chromedp.SendKeys(chromedp.CSS(composer+` textarea`), "!"))
	b.waitJS("dictation ended, the words kept", `e2eRec().state === "aborted" && document.querySelector(`+jsq(composer+" textarea")+`).value === "Hello world!" &&
		document.querySelector(`+jsq(composer+" .dictate-btn")+`).getAttribute("aria-pressed") === "false"`)
}
