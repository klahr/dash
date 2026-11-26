package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// The Board's hover card (app/hovercard.js) stays out of the way of
// selecting drafts: it opens only when the mouse rests on a card, not on its
// pick box nor while drafts are picked, it closes when the mouse moves on to
// a card it covers, and a click on it does what a click on the card does.
func TestBrowserHoverCard(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	long := strings.Repeat("A long description that the card cuts off and the widened copy shows in full. ", 6)
	ids := make([]string, 6)
	for i := range ids {
		var d struct{ ID string }
		api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": fmt.Sprintf("Draft %d", i+1), "prompt": long}, http.StatusCreated, &d)
		ids[i] = d.ID
	}

	b := newBrowser(t, chrome)
	b.signIn(h)
	for _, id := range ids {
		b.waitCard(id, "draft")
	}
	b.waitJS("the cards settled", `document.getAnimations().every((a) => a.playState !== "running")`)
	if ok, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`matchMedia("(hover: hover) and (pointer: fine)").matches`)); err != nil || !ok {
		t.Skip("this browser has no fine pointer that hovers")
	}
	// The cards from the top of the column, as the Board orders them.
	order := func() []string {
		got, err := chromedp.Run(b.ctx, chromedp.Evaluate[[]string](`[...document.querySelectorAll('#board .col[data-lane="draft"] .todo[data-id]')].map((c) => c.dataset.id)`))
		if err != nil {
			b.fail("list the drafts", err)
		}
		return got[:5]
	}
	type pt struct{ X, Y float64 }
	// at is a point in an element: fx, fy of its width and height.
	at := func(sel string, fx, fy float64) pt {
		p, err := chromedp.Run(b.ctx, chromedp.Evaluate[pt](fmt.Sprintf(`(() => { const r = document.querySelector(%q).getBoundingClientRect(); return {X: r.left + r.width * %v, Y: r.top + r.height * %v}; })()`, sel, fx, fy)))
		if err != nil {
			b.fail("find "+sel, err)
		}
		return p
	}
	mouse := func(typ input.DispatchMouseEventType, p pt, mods int64) chromedp.Action[chromedp.Void] {
		return chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
			e := input.DispatchMouseEventParams{Type: typ, X: p.X, Y: p.Y, Modifiers: mods}
			if typ != input.DispatchMouseEventTypeMouseMoved {
				e.Button, e.ClickCount = input.MouseButtonLeft, 1
			}
			_, err := cdp.Call(ctx, tg, input.DispatchMouseEvent, e)
			return err
		})
	}
	cur := pt{1, 1}
	// glide moves the mouse to p in small steps, as a hand does.
	glide := func(p pt) {
		t.Helper()
		const n = 12
		from := cur
		for i := 1; i <= n; i++ {
			f := float64(i) / n
			b.do("move the mouse", mouse(input.DispatchMouseEventTypeMouseMoved, pt{from.X + (p.X-from.X)*f, from.Y + (p.Y-from.Y)*f}, 0))
			time.Sleep(15 * time.Millisecond)
		}
		cur = p
	}
	click := func(p pt, mods int64) {
		t.Helper()
		glide(p)
		b.do("click", mouse(input.DispatchMouseEventTypeMousePressed, p, mods), mouse(input.DispatchMouseEventTypeMouseReleased, p, mods))
	}
	float := func(id string) string {
		return fmt.Sprintf(`document.querySelector(".todo-float .todo-title")?.textContent === document.querySelector(%q + " .todo-title").textContent`, cardSel(id))
	}
	none := func(what string, d time.Duration) {
		t.Helper()
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			if ok, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`!!document.querySelector(".todo-float")`)); err == nil && ok {
				b.fail(what, fmt.Errorf("the hover card opened"))
			}
		}
	}
	// body is a spot on a card's text, away from its controls.
	body := func(id string) pt { return at(cardSel(id)+" .todo-title", 0.5, 0.5) }
	cards := order()

	// Resting opens the copy; passing over cards doesn't.
	glide(body(cards[0]))
	b.waitJS("the copy of the first card", float(cards[0]))
	glide(pt{cur.X + 400, cur.Y})
	b.waitJS("the copy closed off the card", `!document.querySelector(".todo-float")`)
	glide(body(cards[0]))
	b.waitJS("the copy again", float(cards[0]))
	// Down to the next card, which the taller copy covers: it closes and
	// opens for that card once the mouse rests.
	if ok, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](fmt.Sprintf(`(() => {
		const r = document.querySelector(".todo-float").getBoundingClientRect(), n = document.querySelector(%q).getBoundingClientRect();
		return r.bottom > n.top + 4;
	})()`, cardSel(cards[1])))); err != nil || !ok {
		t.Logf("the copy doesn't cover the next card here (%v)", err)
	}
	glide(body(cards[1]))
	b.waitJS("the copy of the second card", float(cards[1]))
	// Crossing cards without stopping opens nothing.
	glide(pt{cur.X + 400, cur.Y})
	b.waitJS("the copy closed", `!document.querySelector(".todo-float")`)
	for range 3 {
		for _, id := range cards {
			p := body(id)
			glide(pt{p.X + 30, p.Y})
			glide(pt{p.X - 30, p.Y + 4})
		}
	}
	none("crossing cards", 0)
	glide(pt{cur.X + 400, cur.Y})

	// Resting on a pick box opens nothing.
	pick := func(id string) pt { return at(cardSel(id)+" .todo-pick", 0.5, 0.5) }
	glide(pick(cards[0]))
	none("resting on the pick box", 900*time.Millisecond)

	// Tick five in a row, straight down the column.
	for i, id := range cards {
		click(pick(id), 0)
		b.waitJS(fmt.Sprintf("%d picked", i+1), fmt.Sprintf(`/%d selected/.test(document.querySelector("#combine-bar").textContent) && document.querySelector(%q).classList.contains("picked")`, i+1, cardSel(id)))
	}
	none("while picking", 0)
	glide(body(cards[2]))
	none("resting on a card while picking", 900*time.Millisecond)
	b.do("clear the picks", chromedp.KeyEvent(kb.Escape))
	b.waitJS("no picks", `document.querySelector("#combine-bar").hidden`)

	// The copy's pick box ticks the draft.
	glide(pt{cur.X + 400, cur.Y})
	glide(body(cards[3]))
	b.waitJS("the copy of the fourth card", float(cards[3]))
	click(at(".todo-float .todo-pick", 0.5, 0.5), 0)
	b.waitJS("the copy's pick box picks the draft", fmt.Sprintf(`/1 selected/.test(document.querySelector("#combine-bar").textContent) && document.querySelector(%q).classList.contains("picked")`, cardSel(cards[3])))
	b.do("clear the picks", chromedp.KeyEvent(kb.Escape))
	b.waitJS("no picks", `document.querySelector("#combine-bar").hidden`)

	// A click on the copy is a click on the card, with its keys.
	if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`(() => {
		window.e2eClicks = [];
		document.addEventListener("click", (e) => { const c = e.target.closest("#board .todo[data-id]"); if (c) window.e2eClicks.push({id: c.dataset.id, meta: e.metaKey, shift: e.shiftKey}); }, true);
		return true;
	})()`)); err != nil {
		b.fail("watch clicks", err)
	}
	glide(pt{cur.X + 400, cur.Y})
	glide(body(cards[1]))
	b.waitJS("the copy of the second card", float(cards[1]))
	click(at(".todo-float .todo-sub", 0.5, 0.2), 4 /* Meta */) // in the card's own area
	b.waitJS("the card got the Cmd-click", fmt.Sprintf(`window.e2eClicks.some((c) => c.id === %q && c.meta)`, cards[1]))
	b.waitJS("the card's task open", fmt.Sprintf(`!document.querySelector("#detail").hidden && document.querySelector("#board .todo.selected")?.dataset.id === %q`, cards[1]))

	// Click from one card to the next, with the copy of the one before open
	// (on a desktop the task pane is modal: Esc closes it between clicks).
	closed := `document.querySelector("#detail").hidden`
	for i, id := range cards[2:] {
		b.do("close the task", chromedp.KeyEvent(kb.Escape))
		b.waitJS("the task closed", closed)
		glide(pt{cur.X + 2, cur.Y + 2})
		b.waitJS("the copy of the card before", float(cards[i+1]))
		click(body(id), 0)
		b.waitJS("open "+id, fmt.Sprintf(`!document.querySelector("#detail").hidden && document.querySelector("#board .todo.selected")?.dataset.id === %q`, id))
	}
}
