package main

import (
	"context"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// fakeHandsFree is a SpeechRecognition that ends as a browser's does (abort
// fires "aborted", then end), and can fail; e2eSay(key, text) says text once
// (polled by waitJS), when voice mode listens and Dash is quiet. Each call
// ends what Dash says (fakeSynth's e2eEnd), as if it had finished.
const fakeHandsFree = `(() => {
	window.e2eRecs = [];
	window.SpeechRecognition = class {
		constructor() { window.e2eRecs.push(this); this.state = "new"; }
		start() { this.state = "listening"; }
		stop() { this.abort(); }
		abort() { if (this.state !== "listening") return; this.state = "aborted"; setTimeout(() => { this.onerror?.({ error: "aborted" }); this.onend?.(); }); }
		say(text) { const r = [{ transcript: text }]; r.isFinal = true; this.onresult?.({ resultIndex: 0, results: [r] }); }
		fail(error) { this.state = "ended"; this.onerror?.({ error }); this.onend?.(); }
	};
	window.e2eRec = () => window.e2eRecs.at(-1);
	const done = new Set();
	window.e2eSay = (key, text) => {
		e2eEnd();
		if (done.has(key)) return true;
		if (e2eRec()?.state !== "listening" || document.querySelector("#voice-mode").dataset.state === "speaking") return false;
		done.add(key);
		e2eRec().say(text);
		return true;
	};
})()`

// Voice mode (docs/dictation.md#voice-mode): started from the ⋮ menu, it
// listens until left. "new task", the words, "done" read the task back, and
// only "yes" queues it; pause ignores everything but resume; the browser
// ending a listen on an error starts another; a microphone that isn't
// allowed is shown and said; Esc and "exit voice mode" leave and stop
// listening. It fits a phone.
func TestBrowserVoiceMode(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	b := newBrowser(t, chrome)
	b.do("fake speech", chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		for _, src := range []string{fakeSynth, fakeHandsFree} {
			if _, err := cdp.Call(ctx, tg, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: src}); err != nil {
				return err
			}
		}
		return nil
	}))
	b.signIn(h)
	const vm = `document.querySelector("#voice-mode")`
	st := func(s string) string { return vm + `.dataset.state === "` + s + `"` }
	text := func(sel string) string { return `document.querySelector("` + sel + `").textContent` }

	// No setting needed: the ⋮ menu has it, and Dash speaks in it.
	b.topMenu("voicemode-open", vm+`.open`)
	b.waitJS("it greets and listens", `e2eHeard("Voice mode on") && e2eRec()?.state === "listening" && `+text("#vm-where")+`.includes("app")`)
	b.waitJS("new task", `e2eSay("a", "New task.") && `+st("dictating")+` && `+text("#vm-hints")+`.includes("“done”")`)
	b.waitJS("dictate", `e2eSay("b", "fix the login redirect") && `+text("#vm-heard")+` === "fix the login redirect"`)
	b.waitJS("done reads it back, then waits for yes", `e2eSay("c", "done") && `+text("#vm-said")+` === "Queue a new task: fix the login redirect. Say yes to confirm." &&
		e2eHeard("Say yes to confirm") && `+st("confirming")+` && `+text("#vm-state")+` === "Say yes or no"`)
	b.waitJS("not yes: nothing happens", `e2eSay("d", "maybe later") && `+st("confirming")+` && `+text("#vm-note")+` === "Say yes or no."`)
	b.waitJS("yes", `e2eSay("e", "Yes.") && e2eHeard("Queued.")`)
	waitTodo(t, api, pid, "fix the login redirect")

	// Paused, only resume is heard.
	b.waitJS("pause", `e2eSay("f", "pause") && (e2eEnd(), `+st("paused")+`) && document.querySelector("#vm-pause").textContent === "Resume"`)
	b.waitJS("ignored while paused", `e2eSay("g", "new task") && `+st("paused"))
	b.until("resume with the button", `(e2eEnd(), `+st("listening")+`)`, chromedp.Click(chromedp.ID("vm-pause")))

	// A network error: it listens again (after a backoff), without a word.
	b.waitJS("a network error", `(window.e2eFailed ||= (e2eEnd(), e2eRec().fail("network"), e2eRecs.length)) && e2eRecs.length > e2eFailed && e2eRec().state === "listening" && `+vm+`.open && document.querySelector("#vm-error").hidden`)
	// The microphone not allowed: shown and said, and no more listening.
	b.waitJS("not allowed", `(window.e2eDenied ||= (e2eRec().fail("not-allowed"), e2eRecs.length)) && !document.querySelector("#vm-error").hidden &&
		`+text("#vm-error")+`.includes("microphone") && e2eHeard("Allow it in the browser") && `+st("off"))
	b.until("Esc leaves", `!`+vm+`.open && e2eSaid.length > 0 && !window.speechSynthesis.speaking`, chromedp.KeyEvent(kb.Escape))
	b.waitJS("nothing listens after", `e2eRecs.length === e2eDenied && e2eRecs.every((r) => r.state !== "listening")`)

	// On a phone: the item and the view fit; "exit voice mode" leaves.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.topMenu("voicemode-open", vm+`.open`)
	b.waitJS("the view fits", `(() => { const l = document.querySelector("#vm-leave").getBoundingClientRect(), d = `+vm+`.getBoundingClientRect();
		return l.right <= innerWidth && d.width === innerWidth && document.documentElement.scrollWidth <= innerWidth; })()`)
	b.waitJS("exit voice mode", `e2eSay("h", "exit voice mode") && !`+vm+`.open && e2eRec().state !== "listening"`)
}
