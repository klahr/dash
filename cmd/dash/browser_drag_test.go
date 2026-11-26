package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
)

// dragJS defines fire(type, x, y, target): a mouse pointer event, which is
// what the board's and Planning's drags listen to.
const dragJS = `const fire = (type, x, y, t = document) => t.dispatchEvent(new PointerEvent(type, { bubbles: true, clientX: x, clientY: y, pointerType: "mouse", button: 0 }));
const glides = (els) => els.filter((el) => el.getAnimations().some((a) => a.id === "glide")).length;
const shown = (list) => [...list.children].filter((el) => el.offsetHeight > 0);`

// run runs script in the page (in a block of its own), which must end true.
func (b *browser) run(what, script string) {
	b.t.Helper()
	ok, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool]("{\n"+script+"\n}"))
	if err == nil && !ok {
		err = fmt.Errorf("the script ended false")
	}
	if err != nil {
		b.fail(what, err)
	}
}

// Dragging a card on the task board leaves a gap where it was, which moves
// to where it would land, and the cards around it glide aside to make room.
// Dropping it there puts it there.
func TestBrowserBoardDragMakesRoom(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	var ids []string
	for _, title := range []string{"First", "Second", "Third"} {
		var td struct{ ID string }
		h.api.must("POST", "/api/projects/"+h.project.ID+"/todos", map[string]any{"title": title, "prompt": "Stay a draft"}, http.StatusCreated, &td)
		ids = append(ids, td.ID)
	}

	b := newBrowser(t, chrome)
	b.signIn(h)
	for _, id := range ids {
		b.waitCard(id, "draft")
	}
	b.waitJS("three drafts", `document.querySelectorAll('#board .col[data-lane=draft] .cards > li').length === 3`)
	// The last card, dragged to the top of its lane.
	b.run("drag the last draft to the top", dragJS+`
		const list = document.querySelector('#board .col[data-lane=draft] .cards');
		const lis = [...list.children];
		const src = lis.at(-1), r = src.getBoundingClientRect();
		fire("pointerdown", r.left + 60, r.top + 20, src.querySelector(".todo"));
		fire("pointermove", r.left + 70, r.top + 24);
		window.e2e = { home: src.offsetHeight === 0 && shown(list).at(-1)?.matches(".drop-slot") };
		const top = lis[0].getBoundingClientRect();
		fire("pointermove", top.left + 70, top.top + 4);
		window.e2e.first = list.firstElementChild.matches(".drop-slot");
		window.e2e.glides = glides(lis.slice(0, 2));
		true`)
	b.waitJS("the gap opened where the card was", `window.e2e.home`)
	b.waitJS("the gap moved to the top", `window.e2e.first`)
	b.waitJS("the other two glided down", `window.e2e.glides === 2`)
	b.run("drop it", dragJS+`
		const top = document.querySelector('#board .col[data-lane=draft] .cards .drop-slot').getBoundingClientRect();
		fire("pointerup", top.left + 70, top.top + 4);
		true`)
	b.waitJS("it's first", fmt.Sprintf(`!document.querySelector(".drop-slot, .drag-src") &&
		document.querySelector('#board .col[data-lane=draft] .cards > li:first-child .todo')?.dataset.id === %q`, ids[2]))
}

// On a phone, a card held still near the bottom of the screen (just above
// the drop bar) scrolls the page down, and the gap follows; held under the
// lane tabs, it scrolls back up.
func TestBrowserBoardDragScrollsAtEdges(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	var ids []string
	for i := range 14 {
		var td struct{ ID string }
		h.api.must("POST", "/api/projects/"+h.project.ID+"/todos", map[string]any{"title": fmt.Sprintf("Draft %d", i+1), "prompt": "Stay a draft"}, http.StatusCreated, &td)
		ids = append(ids, td.ID)
	}

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	for _, id := range ids {
		b.waitCard(id, "draft")
	}
	b.until("show Drafts", `document.querySelector('#board .col[data-lane=draft]')?.offsetHeight > 0`,
		chromedp.Click(chromedp.CSS(`.lanes .lane-tab[data-lane=draft]`)))
	b.waitJS("a page taller than the screen", `scrollY === 0 && document.scrollingElement.scrollHeight > innerHeight + 300`)
	b.run("pick up the first draft", dragJS+`
		const src = document.querySelector('#board .col[data-lane=draft] .cards > li .todo'), r = src.getBoundingClientRect();
		fire("pointerdown", r.left + 60, r.top + 20, src);
		fire("pointermove", r.left + 70, r.top + 30);
		window.e2eX = r.left + 70;
		true`)
	// Held just above the drop bar, once it has slid in.
	b.waitJS("the drop bar in place", `(() => { const bar = document.querySelector("#drop-bar"); return bar && !bar.getAnimations().length; })()`)
	b.run("hold it near the bottom", dragJS+`
		fire("pointermove", e2eX, document.querySelector("#drop-bar").getBoundingClientRect().top - 20);
		true`)
	b.waitJS("the page scrolled down by itself", `scrollY > 200`)
	b.run("the gap followed", `const list = document.querySelector('#board .col[data-lane=draft] .cards');
		[...list.children].indexOf(document.querySelector(".drop-slot")) > 4`)
	b.run("hold it under the lane tabs", dragJS+`
		const tabs = document.querySelector(".lanes").getBoundingClientRect();
		fire("pointermove", e2eX, tabs.bottom + 10);
		true`)
	b.waitJS("the page scrolled back up", `scrollY === 0`)
	b.run("let go", dragJS+`fire("pointerup", e2eX, 300); true`)
	b.waitJS("the drag over", `!document.body.classList.contains("is-dragging")`)
}

// Planning's cards make room the same way, within a column and in another.
func TestBrowserPlanningDragMakesRoom(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	var ids []string
	for _, title := range []string{"One", "Two", "Three"} {
		var c struct{ ID string }
		h.api.must("POST", "/api/planning/cards", map[string]any{"title": title, "column": "ideas"}, http.StatusCreated, &c)
		ids = append(ids, c.ID)
	}
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Planned one", "column": "planned"}, http.StatusCreated, nil)

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open Planning", `document.querySelector("#view-planning.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=planning]`)))
	b.waitJS("the cards", `document.querySelectorAll('#view-planning .pl-cards[data-col=ideas] .pl-card[data-id]').length === 3`)
	// Until the switch to Planning (a view transition) is over, nothing on the page can be hit.
	b.waitJS("the view settled", `(() => { const r = document.querySelector('#view-planning .pl-card[data-id]').getBoundingClientRect();
		return document.elementFromPoint(r.left + 70, r.top + 20)?.closest(".pl-card"); })()`)
	b.run("drag the first card below the second", dragJS+`
		const ol = document.querySelector('#view-planning .pl-cards[data-col=ideas]');
		const cards = [...ol.querySelectorAll(".pl-card[data-id]")];
		const src = cards[0], r = src.getBoundingClientRect();
		fire("pointerdown", r.left + 60, r.top + 20, src.querySelector(".todo"));
		fire("pointermove", r.left + 70, r.top + 24);
		window.e2e = { home: src.offsetHeight === 0 && shown(ol)[0]?.matches(".pl-slot") };
		const two = cards[1].getBoundingClientRect();
		fire("pointermove", two.left + 70, two.bottom - 4);
		window.e2e.below = shown(ol)[1]?.matches(".pl-slot");
		window.e2e.glides = glides([cards[1]]);
		true`)
	b.waitJS("the gap opened where the card was", `window.e2e.home`)
	b.waitJS("the gap moved below the second", `window.e2e.below`)
	b.waitJS("the second glided up", `window.e2e.glides === 1`)
	b.run("over the Planned column", dragJS+`
		const ideas = [...document.querySelectorAll('#view-planning .pl-cards[data-col=ideas] > :not(.pl-slot)')];
		const p = document.querySelector('#view-planning .pl-cards[data-col=planned] .pl-card[data-id]').getBoundingClientRect();
		fire("pointermove", p.left + 70, p.top + 4);
		window.e2e.moved = document.querySelector('#view-planning .pl-cards[data-col=planned]').firstElementChild.matches(".pl-slot") &&
			!document.querySelector('#view-planning .pl-cards[data-col=ideas] .pl-slot');
		window.e2e.closed = glides(ideas) > 0;
		fire("pointerup", p.left + 70, p.top + 4);
		true`)
	b.waitJS("the gap went to Planned, and Ideas closed up", `window.e2e.moved && window.e2e.closed`)
	b.waitJS("dropped first in Planned", fmt.Sprintf(`!document.querySelector(".pl-slot, .pl-card.dragging") &&
		document.querySelector('#view-planning .pl-cards[data-col=planned] .pl-card[data-id]')?.dataset.id === %q`, ids[0]))
}
