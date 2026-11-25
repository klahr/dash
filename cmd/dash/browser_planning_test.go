package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// Planning is laid out as the task board: its columns side by side on a
// desktop, each card a bordered card (no rail) and Add card at the bottom of
// each column; on a phone, a strip of lanes and one column at a time.
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
	b.waitJS("the cards as bordered cards", fmt.Sprintf(`document.querySelector('#view-planning .col[data-col=planned] .pl-cards .pl-card[data-id=%q] .todo .todo-title')?.textContent === "Export reports as PDF"`, c.ID))
	b.waitJS("no rail line and no node", `!document.querySelector("#view-planning .rail, #view-planning .pl-card .node") && getComputedStyle(document.querySelector("#view-planning .pl-card .todo")).borderTopStyle === "solid"`)
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

	// Switching project in the sidebar shows that project's cards here too;
	// loading the page (above) left the filter on all projects.
	b.clickTextUntil(`#side-project-list .side-project`, "shop", `document.querySelector("#pl-filter .pl-filter-name")?.textContent === "shop" && !document.querySelector("#view-planning .pl-card")`)
	b.do("open the project filter", chromedp.Click(chromedp.CSS(`#pl-filter`)))
	b.until("all projects again", `document.querySelector("#view-planning .pl-card")`,
		chromedp.Click(chromedp.CSS(`#pl-filter-list [role=option][data-value=""]`)))

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("one column at a time", `[...document.querySelectorAll("#view-planning .board .col")].filter((c) => c.offsetWidth).length === 1`)
	b.waitJS("Ideas first", `document.querySelector("#view-planning .col.active")?.dataset.col === "ideas"`)
	b.until("the Planned lane", `document.querySelector("#view-planning .col.active")?.dataset.col === "planned"`,
		chromedp.Click(chromedp.CSS(`#view-planning .lane-tab[data-col=planned]`)))
	b.waitJS("its card", fmt.Sprintf(`document.querySelector('#view-planning .col.active .pl-card[data-id=%q]')?.offsetWidth > 0`, c.ID))
	// The board reaches down to the nav, so a swipe to the next column works
	// on the empty space below a short column's cards too.
	b.waitJS("the board below the cards", `!!document.elementFromPoint(innerWidth / 2, innerHeight - 100)?.closest("#pl-board")`)
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

// A planner's Planning page has the app's shell: on a desktop the sidebar,
// with Planning as its one section and the account menu at its foot (whose
// Account opens the sections a planner can use); on a phone no bottom nav,
// and the menu button opens the drawer with the same.
func TestBrowserPlannerShell(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Dark mode", "column": "ideas"}, http.StatusCreated, nil)
	var me struct{ Org struct{ ID int64 } }
	h.api.must("GET", "/api/me", nil, http.StatusOK, &me)
	var inv struct{ Token string }
	h.api.must("POST", fmt.Sprintf("/api/orgs/%d/invitations", me.Org.ID), map[string]any{"email": "pia@example.com", "role": "planner", "password": h.password}, http.StatusCreated, &inv)
	newClient(t, h.base).must("POST", "/api/invitations/register", map[string]any{"token": inv.Token, "username": "pia", "password": "another-long-password"}, http.StatusCreated, nil)

	b := newBrowser(t, chrome)
	b.do("sign in as the planner",
		chromedp.Navigate(h.base+"/"),
		chromedp.WaitVisible(chromedp.CSS(`#login input[name=username]`)),
		chromedp.SendKeys(chromedp.CSS(`#login input[name=username]`), "pia"),
		chromedp.SendKeys(chromedp.CSS(`#login input[name=password]`), "another-long-password"),
		chromedp.Click(chromedp.CSS(`#login button.btn.primary`)),
	)
	b.waitJS("Planning with its card", `location.pathname === "/planning.html" && [...document.querySelectorAll(".pl-card .todo-title")].some((t) => t.textContent === "Dark mode")`)
	b.waitJS("the sidebar: Planning, and the account at its foot", `document.querySelector(".sidebar").offsetWidth > 100 &&
		document.querySelector('.nav > button[data-view=planning][aria-current=page]')?.offsetWidth > 0 &&
		document.querySelector("#foot-user")?.textContent === "pia" && document.querySelector("#foot-menu").offsetWidth > 0`)
	b.until("collapse the sidebar", `document.querySelector(".app").classList.contains("side-collapsed")`,
		chromedp.Click(chromedp.CSS(`#menu-open`)))
	b.until("expand it again", `!document.querySelector(".app").classList.contains("side-collapsed")`,
		chromedp.Click(chromedp.CSS(`#menu-open`)))
	b.until("open the account menu", `document.querySelector("#foot-menu").open`,
		chromedp.Click(chromedp.CSS(`#foot-menu summary`)))
	b.until("open Account", `document.querySelector("#account-sheet").open`,
		chromedp.Click(chromedp.CSS(`#account-open`)))
	b.waitJS("the planner's sections", `[...document.querySelectorAll("#account-sheet .rail-item")].filter((x) => !x.hidden).map((x) => x.dataset.rail).join() === "profile,notifications,signin"`)
	b.until("two-factor sign-in", `document.querySelector("#twofactor-body button")?.textContent.includes("Set up")`,
		chromedp.Click(chromedp.CSS(`#account-sheet .rail-item[data-rail=signin]`)))
	// Closed for sure: an Account still open (modal) takes every click below.
	b.until("close Account", `!document.querySelector("#account-sheet").open`,
		chromedp.Click(chromedp.CSS(`#account-sheet .sheet-head button`)))

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("no bottom nav, the drawer closed", `!matchMedia("(min-width: 1024px)").matches &&
		!document.querySelector('.nav > button[data-view=planning]').offsetWidth && !document.querySelector(".app").classList.contains("menu-open")`)
	b.openDrawer(`document.querySelector("#planning-main").inert &&
		document.querySelector(".menu-view[data-view=planning]").offsetWidth > 0 && document.querySelector("#foot-menu").offsetWidth > 0`)
	b.until("Esc closes it", `!document.querySelector(".app").classList.contains("menu-open") && !document.querySelector("#planning-main").inert`,
		chromedp.KeyEvent(kb.Escape))
}

// A card's pane has its Members (add one from the org, take one off), and
// a description box that is always there and saves when it loses focus. A
// card with comments shows how many, with a speech bubble.
func TestBrowserPlanningCardPane(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	var c struct{ ID string }
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Export reports as PDF", "column": "ideas"}, http.StatusCreated, &c)
	h.api.must("POST", "/api/planning/cards/"+c.ID+"/comments", map[string]any{"text": "Finance asked twice."}, http.StatusCreated, nil)
	var me struct{ Org struct{ ID int64 } }
	h.api.must("GET", "/api/me", nil, http.StatusOK, &me)
	var inv struct{ Token string }
	h.api.must("POST", fmt.Sprintf("/api/orgs/%d/invitations", me.Org.ID), map[string]any{"email": "ana@example.com", "password": h.password}, http.StatusCreated, &inv)
	newClient(t, h.base).must("POST", "/api/invitations/register", map[string]any{"token": inv.Token, "username": "ana", "password": "another-long-password"}, http.StatusCreated, nil)

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open Planning", `document.querySelector("#view-planning.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=planning]`)))
	card := fmt.Sprintf(`#view-planning .pl-card[data-id=%q]`, c.ID)
	b.waitJS("the comment count, with a speech bubble", fmt.Sprintf(`document.querySelector(%q)?.textContent.trim() === "1" && !!document.querySelector(%q)`,
		card+" .pl-comments", card+" .pl-comments .ico"))
	b.until("open the card", `!document.querySelector("#card-detail").hidden && !!document.querySelector("#pd-desc")`,
		chromedp.Click(chromedp.CSS(card+" .pl-open")))
	b.waitJS("Members, nobody on it yet, and an empty description box", `document.querySelector("#pd-members")?.textContent === "Members" &&
		!document.querySelector(".pl-member") && document.querySelector("#pd-desc").tagName === "TEXTAREA" && document.querySelector("#pd-desc").value === ""`)
	b.run("add ana", `const s = document.querySelector(".pl-member-list select");
		s.value = [...s.options].find((o) => o.textContent === "ana").value;
		s.dispatchEvent(new Event("change", { bubbles: true }));
		true`)
	b.waitJS("ana on the card and in the pane", fmt.Sprintf(`document.querySelector(".pl-member .pl-member-name")?.textContent === "ana" &&
		document.querySelector(%q)?.title === "Members: ana"`, card+" .pl-members"))
	b.run("write a description", `const d = document.querySelector("#pd-desc");
		d.focus();
		d.value = "So finance can file them.";
		d.blur();
		d.dispatchEvent(new Event("change", { bubbles: true }));
		true`)
	b.waitJS("saved: the card shows it", fmt.Sprintf(`document.querySelector(%q)?.textContent === "So finance can file them."`, card+" .todo-sub"))
	b.until("take ana off", `!document.querySelector(".pl-member") && !document.querySelector(".pl-members")`,
		chromedp.Click(chromedp.CSS(`.pl-member .icon-btn`)))
}

// Ask in chat on a card opens the chat about it: a card of no project in the
// general chat, its message box started with the card's title and id, and
// nothing sent.
func TestBrowserPlanningAskInChat(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	var c struct{ ID string }
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Dark mode", "column": "ideas"}, http.StatusCreated, &c)

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open Planning", `document.querySelector("#view-planning.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=planning]`)))
	card := fmt.Sprintf(`#view-planning .pl-card[data-id=%q]`, c.ID)
	b.until("open the card", `!document.querySelector("#card-detail").hidden && !!document.querySelector("#pd-desc")`,
		chromedp.Click(chromedp.CSS(card+" .pl-open")))
	want := fmt.Sprintf(`About the planning card "Dark mode" (card %s): `, c.ID)
	b.until("the general chat, about the card", fmt.Sprintf(`!document.querySelector("#chat-pane").hidden && !document.querySelector("#chat-general").hidden &&
		document.querySelector("#chat-general textarea")?.value === %q`, want),
		chromedp.Click(chromedp.CSS(`#pd-actions button[title^="Ask Claude about this card"]`)))
	b.waitJS("nothing sent", `!document.querySelector("#chat-general .chat-log .msg")`)

	// On a phone, a card of a project opens that project's chat, full screen.
	var pr struct{ ID string }
	h.api.must("POST", "/api/projects", map[string]any{"name": "shop", "remote_url": "https://example.com/shop.git", "runs_on": []string{"any"}}, http.StatusCreated, &pr)
	var c2 struct{ ID string }
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Gift cards", "column": "ideas", "project": pr.ID}, http.StatusCreated, &c2)
	b.until("close the chat", `document.querySelector("#chat-pane").hidden`, chromedp.Click(chromedp.CSS(`#pc-close`)))
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.run("open the shop card", fmt.Sprintf(`location.href = "/planning/cards/%s"; true`, c2.ID))
	b.waitJS("the card, full screen", `!document.querySelector("#card-detail").hidden && document.querySelector("#pd-title")?.textContent === "Gift cards" &&
		document.querySelector("#card-detail").offsetWidth >= innerWidth - 1`)
	want = fmt.Sprintf(`About the planning card "Gift cards" (card %s): `, c2.ID)
	b.until("shop's chat, full screen, about the card", fmt.Sprintf(`(() => {
		const pane = document.querySelector("#chat-pane");
		const panel = [...pane.querySelectorAll(".chat-panel[data-chat]")].find((p) => !p.hidden);
		return !pane.hidden && pane.offsetWidth >= innerWidth - 1 && panel?.querySelector("textarea")?.value === %q;
	})()`, want),
		chromedp.Click(chromedp.CSS(`#pd-actions button[title^="Ask Claude about this card"]`)))
}

// A card's pane adds a card it depends on from a list of the org's cards;
// the board then shows that it waits, until that card is in Done.
func TestBrowserPlanningCardDeps(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	var first, second struct{ ID string }
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Accounts", "column": "planned"}, http.StatusCreated, &first)
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Billing", "column": "ideas"}, http.StatusCreated, &second)

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open Planning", `document.querySelector("#view-planning.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=planning]`)))
	card := fmt.Sprintf(`#view-planning .pl-card[data-id=%q]`, second.ID)
	b.until("open Billing", `!document.querySelector("#card-detail").hidden && !!document.querySelector("#pd-deps")`,
		chromedp.Click(chromedp.CSS(card+" .pl-open")))
	b.until("the list of cards", `!!document.querySelector("dialog .pl-pick-item")`,
		chromedp.Click(chromedp.CSS(`.pl-dep-add`)))
	b.waitJS("Billing itself isn't in it", `![...document.querySelectorAll("dialog .pl-pick-item .pl-link-title")].some((x) => x.textContent === "Billing")`)
	b.until("pick Accounts", `document.querySelector("#pd-deps + .pl-links .pl-link-title")?.textContent === "Accounts"`,
		chromedp.Click(chromedp.CSS(`dialog .pl-pick-item`)))
	b.waitJS("Billing waits on the board", fmt.Sprintf(`document.querySelector(%q)?.title === "Waits for Accounts"`, card+" .pl-waits"))
	h.api.must("POST", "/api/planning/cards/"+first.ID+"/move", map[string]any{"column": "done"}, http.StatusOK, nil)
	b.waitJS("no longer, once Accounts is done", fmt.Sprintf(`!document.querySelector(%q) && document.querySelector("#pd-deps + .pl-links .pl-state")?.textContent === "Done"`, card+" .pl-waits"))
}

// A card's tags show on it on the board. In its pane, Enter adds a tag and
// a tag's name shows only the cards with it, until × at the top.
func TestBrowserPlanningTags(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	var c struct{ ID string }
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Dark mode", "tags": []string{"ux"}}, http.StatusCreated, &c)
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Faster sync"}, http.StatusCreated, nil)

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open Planning", `document.querySelector("#view-planning.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=planning]`)))
	card := fmt.Sprintf(`#view-planning .pl-card[data-id=%q]`, c.ID)
	b.waitJS("the tag on the card", fmt.Sprintf(`document.querySelector('%s .pl-tag')?.textContent === "ux"`, card))
	b.until("open the card", `!document.querySelector("#card-detail").hidden && document.querySelector("#card-detail .pl-tag-input")`,
		chromedp.Click(chromedp.CSS(card+` .pl-open`)))
	b.do("type a tag and press Enter", chromedp.SendKeys(chromedp.CSS(`#card-detail .pl-tag-input`), "Q3"), chromedp.KeyEvent("\r"))
	b.waitJS("two tags on the card", fmt.Sprintf(`[...document.querySelectorAll('%s .pl-tag')].map((e) => e.textContent).join() === "ux,Q3"`, card))
	b.until("show only the cards tagged ux", `document.querySelectorAll("#view-planning .pl-card").length === 1 && document.querySelector("#pl-tag-filter .pl-tag-name")?.textContent === "ux"`,
		chromedp.Click(chromedp.CSS(`#card-detail .pl-tags-field button.pl-tag-name`)))
	b.until("all cards again", `document.querySelectorAll("#view-planning .pl-card").length === 2 && document.querySelector("#pl-tag-filter").hidden`,
		chromedp.Click(chromedp.CSS(`#pl-tag-filter .icon-btn`)))

	// Filter… picks tags, more than one: the cards with all of them show.
	pick := func(v string) {
		t.Helper()
		js := fmt.Sprintf(`(() => { const s = document.querySelector("#pl-more-filter"); s.value = %q; s.dispatchEvent(new Event("change")); return true; })()`, v)
		if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](js)); err != nil {
			t.Fatalf("pick %s: %v", v, err)
		}
	}
	pick("t:Q3")
	b.waitJS("tagged Q3", `document.querySelectorAll("#view-planning .pl-card").length === 1 && document.querySelectorAll("#pl-tag-filter .pl-tag").length === 1`)
	pick("t:ux")
	b.waitJS("tagged Q3 and ux", `document.querySelectorAll("#view-planning .pl-card").length === 1 && [...document.querySelectorAll("#pl-tag-filter .pl-tag-name")].map((e) => e.textContent).join() === "Q3,ux" && location.search.includes("tag=Q3&tag=ux")`)
	// Mine shows only the cards you are on: none yet.
	b.until("Mine", `document.querySelector("#pl-mine").getAttribute("aria-pressed") === "true" && document.querySelectorAll("#view-planning .pl-card").length === 0 && location.search.includes("mine=1")`,
		chromedp.Click(chromedp.CSS(`#pl-mine`)))
	// Each click re-renders the filters, so each is its own step: a click
	// queued behind another can find its node replaced.
	b.until("Mine off", `document.querySelector("#pl-mine").getAttribute("aria-pressed") === "false" && !location.search.includes("mine=1")`,
		chromedp.Click(chromedp.CSS(`#pl-mine`)))
	b.until("one tag off", `document.querySelectorAll("#pl-tag-filter .pl-tag").length === 1`,
		chromedp.Click(chromedp.CSS(`#pl-tag-filter .icon-btn`)))
	b.until("the other tag off", `document.querySelector("#pl-tag-filter").hidden`,
		chromedp.Click(chromedp.CSS(`#pl-tag-filter .icon-btn`)))
	b.waitJS("all cards again", `document.querySelectorAll("#view-planning .pl-card").length === 2 && document.querySelector("#pl-tag-filter").hidden`)

	// On a phone, the pane's tags and their box fit the screen.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the tags fit", `(() => { const f = document.querySelector("#card-detail .pl-tags-field"); const r = f?.getBoundingClientRect(); const i = f?.querySelector(".pl-tag-input")?.getBoundingClientRect();
		return r && i && r.width > 0 && r.right <= innerWidth && i.right <= innerWidth && i.width > 60; })()`)
}

// An org's boards show above the columns when there is more than one; a
// click shows one, and the board shown is in the address. A new card goes on
// it; an open card moves to another board; a card's link opens its board.
// The Boards sheet adds, renames, reorders and deletes boards (a deleted
// board's cards move to the default one, after a confirm). On a phone the
// boards are a strip across the top.
func TestBrowserPlanningBoards(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	var mobile struct{ ID string }
	h.api.must("POST", "/api/planning/boards", map[string]any{"name": "Mobile"}, http.StatusCreated, &mobile)
	var onMain, onMobile struct{ ID string }
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Web checkout"}, http.StatusCreated, &onMain)
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Push alerts", "board": mobile.ID}, http.StatusCreated, &onMobile)
	cardOf := func(id string) string { return fmt.Sprintf(`#view-planning .pl-card[data-id=%q]`, id) }
	titles := func(board string) []string {
		var cs []struct{ Title string }
		h.api.must("GET", "/api/planning/cards?board="+board, nil, http.StatusOK, &cs)
		var out []string
		for _, c := range cs {
			out = append(out, c.Title)
		}
		return out
	}

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open Planning", `document.querySelector("#view-planning.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=planning]`)))
	b.waitJS("a tab for each board, the default one shown", `(() => { const t = [...document.querySelectorAll("#pl-boards .pl-board-tab")]; return !document.querySelector("#pl-boards").hidden && t.length === 2 && t[0].getAttribute("aria-selected") === "true" && t[0].querySelector(".pl-board-name").textContent === "Main"; })()`)
	b.waitJS("only the default board's cards", fmt.Sprintf(`!!document.querySelector(%q) && !document.querySelector(%q)`, cardOf(onMain.ID), cardOf(onMobile.ID)))
	b.until("show Mobile", fmt.Sprintf(`!!document.querySelector(%q) && !document.querySelector(%q)`, cardOf(onMobile.ID), cardOf(onMain.ID)),
		chromedp.Click(chromedp.CSS(fmt.Sprintf(`#pl-boards .pl-board-tab[data-board=%q]`, mobile.ID))))
	b.waitJS("the board in the address", fmt.Sprintf(`location.pathname === "/planning" && new URLSearchParams(location.search).get("board") === %q`, mobile.ID))

	// A new card goes on the board shown.
	const box = `#view-planning .col[data-col=ideas] .pl-composer textarea`
	b.until("Add card", fmt.Sprintf(`document.activeElement === document.querySelector(%q)`, box),
		chromedp.Click(chromedp.CSS(`#view-planning .pl-add[data-col=ideas]`)))
	b.do("type a card", chromedp.SendKeys(box, "Offline mode"), chromedp.KeyEvent(kb.Enter))
	b.waitJS("the card on the board", `[...document.querySelectorAll("#view-planning .pl-card .todo-title")].some((x) => x.textContent === "Offline mode")`)
	b.do("close the new card", chromedp.KeyEvent(kb.Escape))
	if got := titles(mobile.ID); len(got) != 2 || got[1] != "Offline mode" {
		t.Fatalf("Mobile's cards: %v", got)
	}

	// An open card moves to another board, and leaves the one shown.
	b.until("open Push alerts", `!document.querySelector("#card-detail").hidden && !!document.querySelector("#pd-body select[aria-label=Board]")`,
		chromedp.Click(chromedp.CSS(cardOf(onMobile.ID)+" .pl-open")))
	b.run("move it to Main", `(() => { const s = document.querySelector("#pd-body select[aria-label=Board]"); s.value = s.options[0].value; s.dispatchEvent(new Event("change", { bubbles: true })); return true; })()`)
	b.waitJS("gone from Mobile", fmt.Sprintf(`!document.querySelector(%q)`, cardOf(onMobile.ID)))
	if got := titles(""); len(got) != 2 || got[1] != "Push alerts" {
		t.Fatalf("Main's cards after the move: %v", got)
	}

	// A card's link opens its board.
	b.do("open a card's link", chromedp.Navigate(h.base+"/planning/cards/"+onMain.ID+"?board="+mobile.ID))
	b.waitJS("the card open, on its board", fmt.Sprintf(`!document.querySelector("#card-detail").hidden && document.querySelector("#pl-boards .pl-board-tab[aria-selected=true]")?.dataset.board !== %q && !!document.querySelector(%q)`, mobile.ID, cardOf(onMain.ID)))
	b.waitJS("its board in the address", `!new URLSearchParams(location.search).get("board")`)
	b.do("close the card", chromedp.KeyEvent(kb.Escape))

	// The Boards sheet.
	b.until("open Boards", `!!document.querySelector("dialog.pl-boards-sheet[open] .pl-board-row")`,
		chromedp.Click(chromedp.CSS(`#pl-boards-btn`)))
	b.do("add Web", chromedp.SendKeys(`dialog.pl-boards-sheet .pl-board-new`, "Web"), chromedp.KeyEvent(kb.Enter))
	b.waitJS("Web added, last", `[...document.querySelectorAll("dialog.pl-boards-sheet .pl-board-name-edit")].map((x) => x.value).join() === "Main,Mobile,Web" && document.querySelectorAll("#pl-boards .pl-board-tab").length === 3`)
	b.run("rename Web", `(() => { const i = document.querySelectorAll("dialog.pl-boards-sheet .pl-board-name-edit")[2]; i.value = "Website"; i.dispatchEvent(new Event("change", { bubbles: true })); return true; })()`)
	b.waitJS("renamed", `[...document.querySelectorAll("#pl-boards .pl-board-name")].map((x) => x.textContent).join() === "Main,Mobile,Website"`)
	b.until("move Website up", `[...document.querySelectorAll("dialog.pl-boards-sheet .pl-board-name-edit")].map((x) => x.value).join() === "Main,Website,Mobile"`,
		chromedp.Click(chromedp.CSS(`dialog.pl-boards-sheet .pl-board-row:last-child [data-act=up]`)))
	b.waitJS("the default board has no Delete", `!document.querySelector("dialog.pl-boards-sheet .pl-board-row:first-child [data-act=delete]")`)
	b.until("delete Mobile asks first", `document.querySelector("#ask-sheet")?.open && document.querySelector("#ask-body").textContent.includes("to Main")`,
		chromedp.Click(chromedp.CSS(fmt.Sprintf(`dialog.pl-boards-sheet .pl-board-row[data-board=%q] [data-act=delete]`, mobile.ID))))
	b.until("deleted", `[...document.querySelectorAll("dialog.pl-boards-sheet .pl-board-name-edit")].map((x) => x.value).join() === "Main,Website"`,
		chromedp.Click(chromedp.CSS(`#ask-ok`)))
	if got := titles(""); len(got) != 3 || got[2] != "Offline mode" {
		t.Fatalf("Main's cards after deleting Mobile: %v", got)
	}
	b.do("close Boards", chromedp.KeyEvent(kb.Escape))

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the boards across the top, above the lanes", `(() => { const s = document.querySelector("#pl-boards"), l = document.querySelector("#view-planning .pl-lanes"); return s.offsetWidth > 0 && s.getBoundingClientRect().bottom <= l.getBoundingClientRect().top + 1 && s.scrollWidth >= s.clientWidth; })()`)
}
