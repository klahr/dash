package main

import (
	"context"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// Voice commands (docs/dictation.md#voice-commands): off until turned on in
// Account → Voice; then a mic in the top bar or Ctrl-Shift-, listens for
// commands, shows a bar while it does, runs "send" in the chat it was started
// from with a toast of what it heard, opens sections, ignores other speech,
// and stops on Esc and on "stop listening". While dictating, "send" said on
// its own sends; in a sentence it is typed. It fits a phone's screen.
func TestBrowserVoiceCommands(t *testing.T) {
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
	b.waitJS("no mic in the top bar while it is off", `!!document.querySelector("#voice-open") && !`+shown("#voice-open"))

	// Turn voice commands and dictation on in Account → Voice.
	section := `#account-sheet [data-section=voice]`
	b.until("open the account menu", `document.querySelector("#foot-menu").open`, chromedp.Click(chromedp.CSS(`#foot-menu summary`)))
	b.until("open Account", `document.querySelector("#account-sheet").open`, chromedp.Click(chromedp.CSS(`#account-open`)))
	b.until("the Voice section", `document.querySelector(`+jsq(section)+`).classList.contains("current")`,
		chromedp.Click(chromedp.CSS(`#account-sheet .rail-item[data-rail=voice]`)))
	b.until("turn voice commands on", `localStorage.getItem("dash.voice") === "1"`,
		chromedp.Click(chromedp.CSS(section+` input[name=voice] + span`)))
	b.until("turn dictation on", `localStorage.getItem("dash.dictation") === "1"`,
		chromedp.Click(chromedp.CSS(section+` input[name=dictation] + span`)))
	b.until("close Account", `!document.querySelector("#account-sheet").open`, chromedp.Click(chromedp.CSS(`#account-sheet .sheet-head button`)))
	b.waitJS("the mic in the top bar", shown("#voice-open"))

	// Start from the chat with Ctrl-Shift-,: "send" sends what is in it.
	composer := `#chat-pane .chat-panel:not([hidden]) form.composer`
	box := `document.querySelector(` + jsq(composer+" textarea") + `)`
	listening := `e2eRec()?.state === "listening" && document.querySelector("#voice-open").getAttribute("aria-pressed") === "true" && !!document.querySelector("#toasts .voice-bar")`
	b.until("open the chat", `!document.querySelector("#chat-pane").hidden`, chromedp.Click(chromedp.ID("pc-open")))
	b.do("type", chromedp.SendKeys(chromedp.CSS(composer+` textarea`), "Hello voice"))
	b.until("start voice commands", listening+` && e2eRec().lang === navigator.language`,
		chromedp.KeyEvent(",", chromedp.KeyModifiers(chromedp.ModifierCtrl|chromedp.ModifierShift)))
	b.waitJS("speech that isn't a command does nothing", `(e2eRec().say("hello there", true),
		document.querySelector(".voice-bar .voice-status").textContent.includes("not a command") && `+box+`.value === "Hello voice")`)
	b.waitJS("send", `(e2eRec().say("Send.", true), `+box+`.value === "" &&
		[...document.querySelectorAll("#chat-pane .msg.user")].some((m) => m.textContent.includes("Hello voice")) &&
		[...document.querySelectorAll(".toast")].some((t) => t.textContent.includes("Heard “Send.”")))`)
	b.waitJS("open history", `(e2eRec().say("open the history", true), document.querySelector("#view-history").classList.contains("active"))`)
	b.waitJS("approve with nothing waiting", `(e2eRec().say("approve", true),
		[...document.querySelectorAll(".toast.error")].some((t) => t.textContent.includes("No permission request waiting")))`)
	b.waitJS("open the board", `(e2eRec().say("go to board", true), document.querySelector("#view-board").classList.contains("active"))`)
	b.until("Esc stops", `e2eRec().state === "aborted" && !document.querySelector(".voice-bar") && document.querySelector("#voice-open").getAttribute("aria-pressed") === "false"`,
		chromedp.KeyEvent(kb.Escape))

	// The button, on a phone: the mic and the bar fit; "stop listening" stops.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the toasts out of the way", `(document.querySelectorAll(".toast").forEach((t) => t.remove()), true)`)
	b.waitJS("the mic fits the top bar", shown("#voice-open")+` && document.querySelector("#voice-open").getBoundingClientRect().right <= innerWidth`)
	b.until("start from the mic", listening, chromedp.Click(chromedp.ID("voice-open")))
	b.waitJS("the bar fits", `(() => { const r = document.querySelector(".voice-bar").getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth; })()`)
	b.waitJS("stop listening", `(e2eRec().say("stop listening", true), e2eRec().state === "aborted" && !document.querySelector(".voice-bar"))`)
	b.do("a wide screen again", chromedp.EmulateViewport(1600, 1000))

	// While dictating: "send" in a sentence is typed; on its own it sends.
	b.waitJS("the chat open", `!document.querySelector("#chat-pane").hidden || (document.querySelector("#pc-open").click(), false)`)
	b.do("type", chromedp.SendKeys(chromedp.CSS(composer+` textarea`), "Hi"))
	b.until("dictate", `e2eRec()?.state === "listening" && document.querySelector(`+jsq(composer+" .dictate-btn")+`).getAttribute("aria-pressed") === "true"`,
		chromedp.Click(chromedp.CSS(composer+` .dictate-btn`)))
	b.waitJS("send in a sentence is a word", `(e2eRec().say("and then send", true), `+box+`.value === "Hi and then send")`)
	b.waitJS("send on its own sends", `(e2eRec().say("send", true), `+box+`.value === "" &&
		document.querySelector(`+jsq(composer+" .dictate-btn")+`).getAttribute("aria-pressed") === "false" &&
		[...document.querySelectorAll("#chat-pane .msg.user")].some((m) => m.textContent.includes("Hi and then send")))`)
}
