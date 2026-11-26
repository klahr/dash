package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/chromedp/chromedp"
)

// Code blocks in a chat reply are coloured (web/static/codehl.js): a block
// as soon as its closing fence has come, the one still open once the reply
// ends. The colours come from style.css, and the code's text (what Copy
// copies) stays the plain code.
func TestBrowserChatCodeColours(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	b := newBrowser(t, chrome)
	b.signIn(h)
	b.topMenu("pc-open", `!document.querySelector("#chat-pane").hidden`)
	const panel = `#chat-pane .chat-panel[data-chat]:not([hidden])`
	code := func(lang string) string {
		return fmt.Sprintf(`document.querySelector(%q)`, panel+` .msg.assistant pre > code.lang-`+lang)
	}

	hold := h.calls + ".chat-hold"
	if err := os.WriteFile(hold, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(hold) })
	b.do("ask for code", chromedp.SendKeys(chromedp.CSS(panel+` textarea`), "code reply\r"))

	// Streaming: the Go block is closed and coloured; the shell block isn't closed yet.
	b.waitJS("the Go block coloured while the reply streams",
		`!!document.querySelector("`+panel+` .msg.assistant.pending") && `+code("go")+`?.querySelector(".tok-keyword")?.textContent === "func"`)
	b.waitJS("the open shell block plain", code("sh")+`?.hasAttribute("data-open") && !`+code("sh")+`.querySelector("span")`)

	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	b.waitJS("the shell block coloured once closed",
		`!document.querySelector("`+panel+` .msg.assistant.pending") && `+code("sh")+`?.querySelector(".tok-string")?.textContent === '"hi"' && `+code("sh")+`.querySelector(".tok-comment")?.textContent === "# greet"`)
	b.waitJS("the Go block still coloured", code("go")+`?.querySelector(".tok-keyword")?.textContent === "func"`)
	b.waitJS("the code's text unchanged", code("go")+`.textContent === "func main() {}" && `+code("sh")+`.textContent === 'echo "hi" # greet'`)
	b.waitJS("keywords a colour of their own",
		`getComputedStyle(`+code("go")+`.querySelector(".tok-keyword")).color !== getComputedStyle(`+code("go")+`).color`)

	// Loaded again, the saved reply is coloured too.
	b.reload()
	b.topMenu("pc-open", `!document.querySelector("#chat-pane").hidden`)
	b.waitJS("coloured after a reload", code("go")+`?.querySelector(".tok-keyword")?.textContent === "func" && `+code("sh")+`?.querySelector(".tok-string")?.textContent === '"hi"'`)
}
