package main

import (
	"context"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// fakeSynth stands in for speechSynthesis: what is spoken is in
// window.e2eSaid, utterances stay "speaking" until e2eEnd() ends them, and
// cancel() counts in e2eCancels.
const fakeSynth = `(() => {
	window.e2eSaid = [];
	window.e2eCancels = 0;
	let q = [];
	window.SpeechSynthesisUtterance = class { constructor(text) { this.text = text; } };
	Object.defineProperty(window, "speechSynthesis", { configurable: true, value: {
		get speaking() { return q.length > 0; }, pending: false,
		speak(u) { q.push(u); window.e2eSaid.push(u.text); },
		cancel() { window.e2eCancels++; const old = q; q = []; old.forEach((u) => u.onerror?.({ error: "canceled" })); },
		resume() {},
		getVoices() { return [{ name: "Here", lang: navigator.language, voiceURI: "here", localService: true, default: true }, { name: "Cloud", lang: navigator.language, voiceURI: "cloud", localService: false }]; },
		addEventListener() {},
	} });
	window.e2eEnd = () => { const old = q; q = []; old.forEach((u) => u.onend?.()); };
	window.e2eHeard = (s) => window.e2eSaid.join(" ").includes(s);
})()`

// Spoken replies (docs/dictation.md): off until turned on in Account →
// Spoken replies; then a finished chat reply is read aloud with its speaker
// button pressed, Esc stops it without closing anything, the button reads
// it again or stops, dictation listening stops it, and the button fits on a
// phone.
func TestBrowserSpokenReplies(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	b := newBrowser(t, chrome)
	b.do("fake speech synthesis", chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		_, err := cdp.Call(ctx, tg, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: fakeSynth})
		return err
	}))
	b.signIn(h)
	section := `#account-sheet [data-section=voice]`

	// Turn it on in Account → Voice.
	b.until("open the account menu", `document.querySelector("#foot-menu").open`, chromedp.Click(chromedp.CSS(`#foot-menu summary`)))
	b.until("open Account", `document.querySelector("#account-sheet").open`, chromedp.Click(chromedp.CSS(`#account-open`)))
	b.until("the Voice section", `document.querySelector(`+jsq(section)+`).classList.contains("current")`,
		chromedp.Click(chromedp.CSS(`#account-sheet .rail-item[data-rail=voice]`)))
	b.waitJS("off, its switches waiting", `!document.querySelector("#prefs-form").speech.checked && document.querySelector("#prefs-form").speechReplies.disabled`)
	b.waitJS("the online note and the voices", `document.querySelector(`+jsq(section)+`).textContent.includes("online voice service") &&
		!document.querySelector("#speech-voice-field").hidden && [...document.querySelector("#prefs-form").speechVoice.options].some((o) => o.textContent.includes("Cloud") && o.textContent.includes("online"))`)
	b.until("turn it on", `localStorage.getItem("dash.speech") === "1" && !document.querySelector("#prefs-form").speechReplies.disabled && e2eHeard("This is how Dash")`,
		chromedp.Click(chromedp.CSS(section+` input[name=speech] + span`)))
	b.waitJS("the sample ended", `(e2eEnd(), true)`)
	b.until("close Account", `!document.querySelector("#account-sheet").open`, chromedp.Click(chromedp.CSS(`#account-sheet .sheet-head button`)))

	// A finished reply in the project chat is read aloud.
	const panel = `#chat-pane .chat-panel[data-chat]:not([hidden])`
	btn := `document.querySelector(` + jsq(panel+" .msg.assistant:not(.pending) .speak-btn") + `)`
	b.topMenu("pc-open", `!document.querySelector("#chat-pane").hidden`)
	b.do("ask", chromedp.SendKeys(chromedp.CSS(panel+` textarea`), "Hi there\r"))
	b.waitJS("the reply read aloud, its button pressed", `!!`+btn+` && e2eHeard("Hello from the runner") && `+btn+`.getAttribute("aria-pressed") === "true"`)
	b.until("Esc stops it, the chat stays", btn+`.getAttribute("aria-pressed") === "false" && e2eCancels > 0 && !document.querySelector("#chat-pane").hidden`,
		chromedp.KeyEvent(kb.Escape))

	// The button reads it on demand, and stops it.
	b.until("read it again", btn+`.getAttribute("aria-pressed") === "true"`, chromedp.Click(chromedp.CSS(panel+` .msg.assistant:not(.pending) .speak-btn`)))
	b.until("stop it with the button", btn+`.getAttribute("aria-pressed") === "false"`, chromedp.Click(chromedp.CSS(panel+` .msg.assistant:not(.pending) .speak-btn`)))
	b.until("read it once more", btn+`.getAttribute("aria-pressed") === "true"`, chromedp.Click(chromedp.CSS(panel+` .msg.assistant:not(.pending) .speak-btn`)))
	b.waitJS("dictation listening stops it", `(document.dispatchEvent(new Event("dash:listening")), `+btn+`.getAttribute("aria-pressed") === "false")`)

	// On a phone the button fits under the reply.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the speaker button on screen", `(() => { const r = `+btn+`.getBoundingClientRect(); return r.width > 0 && r.right <= innerWidth; })()`)

	// The settings fit a phone: the rail turns into a strip across the top.
	b.do("a wide screen", chromedp.EmulateViewport(1400, 900))
	b.until("open the account menu", `document.querySelector("#foot-menu").open`, chromedp.Click(chromedp.CSS(`#foot-menu summary`)))
	b.until("open Account", `document.querySelector("#account-sheet").open`, chromedp.Click(chromedp.CSS(`#account-open`)))
	b.until("the Voice section", `document.querySelector(`+jsq(section)+`).classList.contains("current")`,
		chromedp.Click(chromedp.CSS(`#account-sheet .rail-item[data-rail=voice]`)))
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("it fits across", `(() => { const s = document.querySelector(`+jsq(section)+`); return s.classList.contains("current") && s.scrollWidth <= s.clientWidth && s.getBoundingClientRect().right <= innerWidth; })()`)
	b.until("close Account", `!document.querySelector("#account-sheet").open`, chromedp.Click(chromedp.CSS(`#account-sheet .sheet-head button`)))

	// Turned off, the buttons go.
	b.waitJS("off again", `(localStorage.setItem("dash.speech", "0"), dispatchEvent(new StorageEvent("storage", { key: "dash.speech" })), getComputedStyle(`+btn+`).display === "none")`)
}
