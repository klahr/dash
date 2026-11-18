package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// Planning looks like the task board: its columns side by side on a
// desktop, each card a row on the rail and Add card at the bottom of each
// column; on a phone, a strip of lanes and one column at a time.
func TestBrowserPlanningBoard(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	var c struct{ ID string }
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Export reports as PDF", "column": "planned", "description": "So **finance** can file them."}, http.StatusCreated, &c)
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Dark mode", "column": "ideas"}, http.StatusCreated, nil)
	for _, name := range []string{"shop", "wiki"} {
		h.api.must("POST", "/api/projects", map[string]any{"name": name, "remote_url": "https://example.com/" + name + ".git", "runs_on": []string{"any"}}, http.StatusCreated, nil)
	}

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open Planning", `document.querySelector("#view-planning.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=planning]`)))
	b.waitJS("four columns side by side", `[...document.querySelectorAll("#view-planning .board .col")].filter((c) => c.offsetWidth).length === 4`)
	b.waitJS("the cards as rows on the rail", fmt.Sprintf(`document.querySelector('#view-planning .col[data-col=planned] .rail .pl-card[data-id=%q] .todo .todo-title')?.textContent === "Export reports as PDF"`, c.ID))
	b.waitJS("Add card at the bottom of each column", `[...document.querySelectorAll("#view-planning .col")].every((c) => c.querySelector(".col-body").lastElementChild.matches(".pl-add"))`)

	// The project filter lists the projects with their icons; picking one
	// shows its cards.
	b.until("open the project filter", `!document.querySelector("#pl-filter-list").hidden`,
		chromedp.Click(chromedp.CSS(`#pl-filter`)))
	b.waitJS("each project with its icon", `(() => { const o = [...document.querySelectorAll("#pl-filter-list [role=option]")]; return o.length >= 3 && o.slice(1).every((li) => li.querySelector(".side-project-icon")?.offsetWidth > 0); })()`)
	b.until("pick wiki", `document.querySelector("#pl-filter .pl-filter-name")?.textContent === "wiki" && document.querySelector("#pl-filter .side-project-icon")?.textContent === "W"`,
		chromedp.Click(chromedp.CSS(`#pl-filter-list [role=option]:last-child`)))
	b.waitJS("only wiki's cards", `!document.querySelector("#view-planning .pl-card")`)
	b.do("open the project filter", chromedp.Click(chromedp.CSS(`#pl-filter`)))
	b.until("all projects again", `document.querySelector("#view-planning .pl-card")`,
		chromedp.Click(chromedp.CSS(`#pl-filter-list [role=option][data-value=""]`)))

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("one column at a time", `[...document.querySelectorAll("#view-planning .board .col")].filter((c) => c.offsetWidth).length === 1`)
	b.waitJS("Ideas first", `document.querySelector("#view-planning .col.active")?.dataset.col === "ideas"`)
	b.until("the Planned lane", `document.querySelector("#view-planning .col.active")?.dataset.col === "planned"`,
		chromedp.Click(chromedp.CSS(`#view-planning .lane-tab[data-col=planned]`)))
	b.waitJS("its card", fmt.Sprintf(`document.querySelector('#view-planning .col.active .pl-card[data-id=%q]')?.offsetWidth > 0`, c.ID))
}

// Add card turns into a new card to type on: Enter adds it (the first line
// the title, the rest the description) and starts the next one, Esc cancels,
// and empty text adds nothing.
func TestBrowserPlanningAddCard(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open Planning", `document.querySelector("#view-planning.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=planning]`)))
	const box = `#view-planning .col[data-col=planned] .pl-composer textarea`
	const ideasBox = `#view-planning .col[data-col=ideas] .pl-composer textarea`
	b.until("Add card opens a card to type on", fmt.Sprintf(`document.activeElement === document.querySelector(%q) && document.activeElement.getAttribute("aria-label") === "New card in Planned"`, box),
		chromedp.Click(chromedp.CSS(`#view-planning .pl-add[data-col=planned]`)))
	b.waitJS("no modal", `!document.querySelector("dialog[open]")`)

	b.do("type a title and a description",
		chromedp.SendKeys(chromedp.CSS(box), "Export reports as PDF"),
		chromedp.KeyEvent("\r", chromedp.KeyModifiers(kb.ModifierShift)),
		chromedp.SendKeys(chromedp.CSS(box), "So **finance** can file them."))
	b.waitJS("Shift+Enter is a new line", fmt.Sprintf(`document.querySelector(%q).value === "Export reports as PDF\nSo **finance** can file them."`, box))
	b.do("press Enter", chromedp.KeyEvent("\r"))
	b.waitJS("the card is added", `[...document.querySelectorAll("#view-planning .col[data-col=planned] .pl-card:not(.pl-composer) .todo-title")].map((x) => x.textContent).join() === "Export reports as PDF"`)
	b.waitJS("an empty one under it, focused", fmt.Sprintf(`document.activeElement === document.querySelector(%q) && document.activeElement.value === "" && document.querySelector("#view-planning .col[data-col=planned] .pl-cards").lastElementChild.matches(".pl-composer")`, box))
	b.waitJS("its pane stays closed", `document.querySelector("#card-detail").hidden`)
	var cards []struct{ Title, Description, Column string }
	h.api.must("GET", "/api/planning/cards", nil, http.StatusOK, &cards)
	if len(cards) != 1 || cards[0].Title != "Export reports as PDF" || cards[0].Description != "So **finance** can file them." || cards[0].Column != "planned" {
		t.Fatalf("cards = %+v", cards)
	}

	b.do("press Enter on nothing", chromedp.SendKeys(chromedp.CSS(box), "   "), chromedp.KeyEvent("\r"))
	b.waitJS("nothing is added", `document.querySelectorAll("#view-planning .pl-card:not(.pl-composer)").length === 1`)

	b.do("type and press Esc", chromedp.SendKeys(chromedp.CSS(box), "Throw this away"), chromedp.KeyEvent(kb.Escape))
	b.waitJS("Esc closes it, back on Add card", `!document.querySelector("#view-planning .pl-composer") && document.activeElement === document.querySelector("#view-planning .pl-add[data-col=planned]")`)
	b.waitJS("and adds nothing", `document.querySelectorAll("#view-planning .pl-card").length === 1`)

	b.until("New card opens one in Ideas", fmt.Sprintf(`document.activeElement === document.querySelector(%q)`, ideasBox),
		chromedp.Click(chromedp.CSS(`#view-planning .pl-new`)))
	b.do("type a card", chromedp.SendKeys(chromedp.CSS(ideasBox), "Dark mode"))
	b.until("opening one in another column adds it", fmt.Sprintf(`!document.querySelector(%q) && document.activeElement === document.querySelector(%q)`, ideasBox, box),
		chromedp.Click(chromedp.CSS(`#view-planning .pl-add[data-col=planned]`)))
	b.waitJS("Dark mode in Ideas", `document.querySelector("#view-planning .col[data-col=ideas] .pl-card .todo-title")?.textContent === "Dark mode"`)
	b.do("Esc", chromedp.KeyEvent(kb.Escape))

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.until("the Planned lane", `document.querySelector("#view-planning .col.active")?.dataset.col === "planned"`,
		chromedp.Click(chromedp.CSS(`#view-planning .lane-tab[data-col=planned]`)))
	b.until("New card shows Ideas, with the new card in it", fmt.Sprintf(`document.querySelector("#view-planning .col.active")?.dataset.col === "ideas" && document.querySelector(%q)?.offsetWidth > 0`, ideasBox),
		chromedp.Click(chromedp.CSS(`#view-planning .pl-new`)))
}
