package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// A draft Claude makes in a project chat shows as a card with Queue, Open
// and Discard, which act right away: Queue queues it and Discard deletes
// it, each swapping the buttons for what happened and noting it in the
// chat; the other device's card follows (a draft discarded elsewhere shows
// as deleted). Open opens the task (on a phone, closing the chat page).
// Loaded again, the cards show the tasks as they are now.
func TestBrowserChatDraftCards(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	wide, phone := newBrowser(t, chrome), newBrowser(t, chrome)
	wide.signIn(h)
	phone.signIn(h)
	phone.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	wide.topMenu("pc-open", `!document.querySelector("#chat-pane").hidden`)
	phone.until("open the chat", `!document.querySelector("#chat-pane").hidden`, chromedp.Click(chromedp.ID("nav-chat")))
	const panel = `#chat-pane .chat-panel[data-chat]:not([hidden])`
	card := func(title string) string {
		return fmt.Sprintf(`[...document.querySelectorAll(%q)].find((c) => c.querySelector(".chat-task-title")?.textContent === %q)`, panel+" .chat-task", title)
	}
	has := func(text string) string {
		return fmt.Sprintf(`!!document.querySelector(%q)?.textContent.includes(%q)`, panel, text)
	}
	buttons := func(title string) string {
		return fmt.Sprintf(`[...(%s)?.querySelectorAll(".chat-task-actions button") || []].map((b) => b.textContent).join(",")`, card(title))
	}
	state := func(title string) string {
		return fmt.Sprintf(`(%s)?.querySelector(".chat-task-state")?.textContent`, card(title))
	}
	// click presses a card's button (Queue, Open or Discard) until expr holds.
	click := func(b *browser, what, title, label, until string) {
		b.t.Helper()
		b.waitJS(title+"'s card", "!!("+card(title)+")")
		id, err := chromedp.Run(b.ctx, chromedp.Evaluate[string](`(`+card(title)+`).dataset.todo`))
		if err != nil {
			b.t.Fatal(err)
		}
		btn := map[string]string{"Queue": "button.primary", "Open": `button[title="Open the task"]`, "Discard": "button.danger"}[label]
		b.until(what, until, chromedp.Click(chromedp.CSS(fmt.Sprintf(`%s .chat-task[data-todo=%q] %s`, panel, id, btn))))
	}

	for _, title := range []string{"Fix login redirect", "Old idea"} {
		wide.do("ask for a draft", chromedp.SendKeys(chromedp.CSS(panel+` textarea`), "draft task: "+title+"\r"))
		wide.waitJS("its card, a draft", buttons(title)+` === "Queue,Open,Discard"`)
	}
	id, err := chromedp.Run(wide.ctx, chromedp.Evaluate[string](`(`+card("Fix login redirect")+`).dataset.todo`))
	if err != nil || id == "" {
		t.Fatalf("the card's task: %q %v", id, err)
	}
	var draft struct{ Status string }
	h.api.must("GET", "/api/todos/"+id, nil, http.StatusOK, &draft)
	if draft.Status != "draft" {
		t.Fatalf("made as %q, want a draft", draft.Status)
	}

	// Queue on the laptop: the card says so, the chat notes it, and the phone follows.
	click(wide, "queue it", "Fix login redirect", "Queue", state("Fix login redirect")+` === "Queued ✓" || `+buttons("Fix login redirect")+` === "Open"`)
	wide.waitJS("noted", has("You queued Fix login redirect."))
	var queued struct{ Status string }
	h.api.must("GET", "/api/todos/"+id, nil, http.StatusOK, &queued)
	if queued.Status == "draft" {
		t.Error("Queue left the task a draft")
	}
	phone.waitJS("the phone's card, queued", buttons("Fix login redirect")+` === "Open" && `+has("You queued Fix login redirect."))

	// Discard on the phone: the laptop's card says it's deleted.
	phone.waitJS("the second draft on the phone", buttons("Old idea")+` === "Queue,Open,Discard"`)
	click(phone, "discard it", "Old idea", "Discard", state("Old idea")+` === "Discarded"`)
	phone.waitJS("only the state, no Open", `(`+card("Old idea")+`).querySelector(".chat-task-actions").textContent === "Discarded"`)
	phone.waitJS("noted", has("You discarded Old idea."))
	wide.waitJS("deleted on the laptop", state("Old idea")+` === "Deleted" && `+buttons("Old idea")+` === ""`)
	phone.waitJS("the card fits the phone", `(() => { const c = `+card("Fix login redirect")+`; return c && c.scrollWidth <= c.clientWidth + 1 && c.getBoundingClientRect().right <= innerWidth; })()`)

	// Open on the phone: the task, full screen, without the chat page over it.
	// Its "Ready for review" toast sits under the chat's head, over this card's
	// Open, and a toast under the pointer stays: wait for it to be done, then
	// dismiss the toast, as a user would, so the click reaches Open.
	phone.waitJS("the task done", state("Fix login redirect")+` === "Ready for review"`)
	phone.run("dismiss the toast", `document.querySelectorAll("#toasts .toast-close").forEach((x) => x.click()); true`)
	phone.waitJS("no toast", `!document.querySelector("#toasts .toast")`)
	click(phone, "open it", "Fix login redirect", "Open", `!document.querySelector("#detail").hidden && document.querySelector("#chat-pane").hidden`)
	phone.waitJS("the task opened", `document.querySelector("#detail")?.textContent.includes("Fix login redirect")`)

	// Open on a laptop, the chat overlaid (1600px unpinned, 1100px pinned): the
	// task panel is modal, so it shows over the chat (inert, behind the scrim)
	// and takes the clicks there. Esc closes it and brings the chat back on top.
	overChat := func(sel string) string {
		return `(() => { const r = document.querySelector("#chat-pane").getBoundingClientRect();
			return !!document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)?.closest(` + fmt.Sprintf("%q", sel) + `); })()`
	}
	// elementFromPoint skips the inert chat, so also check that it is drawn under the scrim.
	const chatUnderScrim = `+getComputedStyle(document.querySelector("#chat-pane")).zIndex < +getComputedStyle(document.querySelector("#scrim")).zIndex`
	for _, size := range []struct {
		w      int64
		pinned bool
	}{{1600, false}, {1100, true}} {
		wide.do("resize", chromedp.EmulateViewport(size.w, 1000))
		if size.pinned {
			wide.until("pin the chat", `document.querySelector(".app").classList.contains("chat-pinned")`, chromedp.Click(chromedp.ID("pc-pin")))
		}
		wide.waitJS("the chat on top", overChat("#chat-pane"))
		click(wide, "open it", "Fix login redirect", "Open", `!document.querySelector("#detail").hidden && `+overChat("#detail")+` && `+chatUnderScrim)
		wide.until("close it with Esc", `document.querySelector("#detail").hidden && `+overChat("#chat-pane"), chromedp.KeyEvent(kb.Escape))
	}

	// Loaded again: the cards and notes come back, as the tasks are now.
	wide.do("reload", chromedp.Reload())
	wide.topMenu("pc-open", `!document.querySelector("#chat-pane").hidden`)
	wide.waitJS("the cards, as they are now", buttons("Fix login redirect")+` === "Open" && `+state("Old idea")+` === "Deleted" && `+
		has("You queued Fix login redirect.")+` && `+has("You discarded Old idea."))
}
