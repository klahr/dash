package main

import (
	"fmt"
	"net/http"
	"testing"
	"time"

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
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Export reports as PDF", "column": "planned", "description": "## Why\nSo **finance** can file them.",
		"priority": "high", "target": "1.4", "tags": []string{"reports"}}, http.StatusCreated, &c)
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
	// Its chips say only their values, each kind in a look of its own; the
	// preview is the description's first sentence, without its heading.
	b.waitJS("chips without kind words", fmt.Sprintf(`(() => { const card = document.querySelector('#view-planning .pl-card[data-id=%q]');
		const prio = card.querySelector(".pl-prio"), target = card.querySelector(".pl-target"), tag = card.querySelector(".pl-tag");
		return prio?.textContent === "High" && target?.textContent === "1.4" && !!target.querySelector(".ico") && tag?.textContent === "reports" && tag.classList.contains("hued") &&
			[prio, target, tag].every((x) => getComputedStyle(x, "::before").content === "none") &&
			card.querySelector(".todo-sub")?.textContent === "So finance can file them."; })()`, c.ID))
	b.waitJS("no rail line and no node", `!document.querySelector("#view-planning .rail, #view-planning .pl-card .node") && getComputedStyle(document.querySelector("#view-planning .pl-card .todo")).borderTopStyle === "solid"`)
	b.waitJS("Add card at the bottom of each column", `[...document.querySelectorAll("#view-planning .col")].every((c) => c.querySelector(".col-body").lastElementChild.matches(".pl-add"))`)

	// The head is one row: the board's tab, +, search, Filter and New card;
	// no sentence explains the page, and an empty column says nothing.
	b.waitJS("the head in one row", `(() => { const h = document.querySelector("#pl-head"); const kids = [...h.children].filter((x) => x.offsetWidth);
		const tops = new Set(kids.map((x) => Math.round(x.getBoundingClientRect().top + x.offsetHeight / 2)));
		return h.offsetHeight < 64 && tops.size === 1 && ["#pl-boards", "#pl-board-add", "#pl-search", "#pl-filter", ".pl-new"].every((q) => h.querySelector(q)?.offsetWidth > 0); })()`)
	b.waitJS("no help texts", `!document.querySelector("#view-planning .hint, #view-planning .col-empty") && !document.querySelector("#view-planning [placeholder]")`)
	// The priority is the task board's quiet label, not the failed colour.
	b.waitJS("a calm priority", fmt.Sprintf(`(() => { const p = document.querySelector('#view-planning .pl-card[data-id=%q] .pl-prio');
		const red = getComputedStyle(document.documentElement).getPropertyValue("--failed").trim();
		return p.classList.contains("meta-chip") && !!p.querySelector(".ico") && getComputedStyle(p).backgroundColor === "rgba(0, 0, 0, 0)" && getComputedStyle(p).color !== red; })()`, c.ID))

	// Filter lists the projects (each with its icon), the tags and the
	// targets, in groups; picking a project shows its cards.
	b.menuPick("#pl-filter", "wiki", `document.querySelector("#pl-tag-filter .pl-project-on .pl-tag-name")?.textContent === "wiki" && !document.querySelector("#view-planning .pl-card")`,
		`(() => { const m = document.querySelector(".pop-menu"); const heads = [...m.querySelectorAll(".pop-menu-head")].map((x) => x.textContent);
			return heads.join() === "Filter,Project,Tags,Targets" && [...m.querySelectorAll("[role=menuitemcheckbox]")].filter((x) => x.querySelector(".side-project-icon")).length >= 2; })()`)
	b.until("all projects again", `document.querySelector("#view-planning .pl-card") && document.querySelector("#pl-tag-filter").hidden`,
		chromedp.Click(chromedp.CSS(`#pl-tag-filter .pl-project-on .icon-btn`)))

	// Switching project in the sidebar shows that project's cards here too;
	// loading the page (above) left the filter on all projects.
	b.clickTextUntil(`#side-project-list .side-project`, "shop", `document.querySelector("#pl-tag-filter .pl-project-on .pl-tag-name")?.textContent === "shop" && !document.querySelector("#view-planning .pl-card")`)
	b.menuPick("#pl-filter", "All projects", `document.querySelector("#view-planning .pl-card") && document.querySelector("#pl-tag-filter").hidden`, "")

	// Search: the cards whose title or description has each word; Esc clears it.
	b.until("open search", `document.activeElement === document.querySelector("#pl-search-input")`,
		chromedp.Click(chromedp.CSS(`#pl-search-open`)))
	b.do("search finance", chromedp.SendKeys(`#pl-search-input`, "finance"))
	b.waitJS("only the card that says finance", fmt.Sprintf(`document.querySelectorAll("#view-planning .pl-card").length === 1 && !!document.querySelector('#view-planning .pl-card[data-id=%q]')`, c.ID))
	b.do("Esc clears it", chromedp.KeyEvent(kb.Escape))
	b.waitJS("every card again", `document.querySelectorAll("#view-planning .pl-card").length === 2 && document.querySelector("#pl-search-input").hidden`)

	// Pointer ways for what keys do: Join on the card, Move to in its ⋯.
	b.run("join the card", fmt.Sprintf(`(() => { document.querySelector('#view-planning .pl-card[data-id=%q] .pl-join').click(); return true; })()`, c.ID))
	b.waitJS("on the card", fmt.Sprintf(`(() => { const card = document.querySelector('#view-planning .pl-card[data-id=%q]'); return !!card.querySelector(".pl-members .avatar") && !card.querySelector(".pl-join"); })()`, c.ID))
	b.until("open the card", `!document.querySelector("#card-detail").hidden && !!document.querySelector("#pd-more")`,
		chromedp.Click(chromedp.CSS(fmt.Sprintf(`#view-planning .pl-card[data-id=%q] .pl-open`, c.ID))))
	b.menuPick("#pd-more", "Move to Done", fmt.Sprintf(`!!document.querySelector('#view-planning .col[data-col=done] .pl-card[data-id=%q]')`, c.ID), "")
	b.do("close the card", chromedp.KeyEvent(kb.Escape))

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("one column at a time", `[...document.querySelectorAll("#view-planning .board .col")].filter((c) => c.offsetWidth).length === 1`)
	b.waitJS("Ideas first", `document.querySelector("#view-planning .col.active")?.dataset.col === "ideas"`)
	// The head is one row, and the four lanes fit the strip below it.
	b.waitJS("one row of head", `document.querySelector("#pl-head").offsetHeight <= 56 && document.querySelector("#pl-head-more").offsetWidth > 0 && !document.querySelector(".pl-new").offsetWidth`)
	b.waitJS("all four lanes in sight", `(() => { const s = document.querySelector("#view-planning .pl-lanes").getBoundingClientRect();
		const tabs = [...document.querySelectorAll("#view-planning .pl-lanes .lane-tab")];
		return tabs.length === 4 && tabs.every((t) => { const r = t.getBoundingClientRect(); return r.width > 30 && r.left >= s.left && r.right <= s.right + 1; }) &&
			[...document.querySelectorAll("#view-planning .pl-lanes .n")].every((n) => n.textContent !== "0"); })()`)
	b.until("the Done lane", `document.querySelector("#view-planning .col.active")?.dataset.col === "done"`,
		chromedp.Click(chromedp.CSS(`#view-planning .lane-tab[data-col=done]`)))
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
	b.until("the Done lane", `document.querySelector("#view-planning .col.active")?.dataset.col === "done"`,
		chromedp.Click(chromedp.CSS(`#view-planning .lane-tab[data-col=done]`)))
	// On a phone New card is under ⋯.
	b.menuPick("#pl-head-more", "New card", fmt.Sprintf(`document.querySelector("#view-planning .col.active")?.dataset.col === "ideas" && document.querySelector(%q)?.offsetWidth > 0`, ideasBox), "")
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
	b.waitJS("ana on the card, her initials in a circle, and in the pane", fmt.Sprintf(`document.querySelector(".pl-member .pl-member-name")?.textContent === "ana" &&
		document.querySelector(%q)?.textContent === "AN" && document.querySelector(%q)?.title === "ana" &&
		getComputedStyle(document.querySelector(%q)).borderRadius === "50%%" &&
		document.querySelector(%q)?.textContent === "Members: ana"`, card+" .pl-members .avatar", card+" .pl-members .avatar", card+" .pl-members .avatar", card+" .pl-members .sr-only"))
	b.waitJS("no one owns it: no Owner badge, and ana's star is off", `!document.querySelector(".pl-owner-badge") &&
		document.querySelector(".pl-member .pl-owner-btn")?.getAttribute("aria-pressed") === "false"`)
	b.until("make ana the owner", fmt.Sprintf(`document.querySelector(".pl-member.pl-owner .pl-owner-badge")?.textContent === "Owner" &&
		document.querySelector(".pl-member .pl-owner-btn")?.getAttribute("aria-pressed") === "true" &&
		!!document.querySelector(%q)`, card+" .pl-members .avatar.pl-owner-face"),
		chromedp.Click(chromedp.CSS(`.pl-member .pl-owner-btn`)))
	b.run("write a description", `const d = document.querySelector("#pd-desc");
		d.focus();
		d.value = "## Why\nSo **finance** can file them.";
		d.blur();
		d.dispatchEvent(new Event("change", { bubbles: true }));
		true`)
	b.waitJS("saved: the card shows it", fmt.Sprintf(`document.querySelector(%q)?.textContent === "So finance can file them."`, card+" .todo-sub"))
	b.until("take ana off", `!document.querySelector(".pl-member") && !document.querySelector(".pl-members")`,
		chromedp.Click(chromedp.CSS(`.pl-member [aria-label="Take ana off the card"]`)))
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
	b.until("the card's menu", `!!document.querySelector(".pop-menu")`, chromedp.Click(chromedp.CSS(`#pd-more`)))
	b.clickTextUntil(".pop-menu [role=menuitem]", "Ask in chat", fmt.Sprintf(`!document.querySelector("#chat-pane").hidden && !document.querySelector("#chat-general").hidden &&
		document.querySelector("#chat-general textarea")?.value === %q`, want))
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
	b.until("the card's menu", `!!document.querySelector(".pop-menu")`, chromedp.Click(chromedp.CSS(`#pd-more`)))
	b.clickTextUntil(".pop-menu [role=menuitem]", "Ask in chat", fmt.Sprintf(`(() => {
		const pane = document.querySelector("#chat-pane");
		const panel = [...pane.querySelectorAll(".chat-panel[data-chat]")].find((p) => !p.hidden);
		return !pane.hidden && pane.offsetWidth >= innerWidth - 1 && panel?.querySelector("textarea")?.value === %q;
	})()`, want))
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
	b.until("open Billing", `!document.querySelector("#card-detail").hidden && !!document.querySelector(".pl-add-row .pl-dep-add") && !document.querySelector("#pd-deps")`,
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

	// Filter picks tags, more than one: the cards with all of them show.
	// Filter counts them.
	b.menuPick("#pl-filter", "Q3", `document.querySelectorAll("#view-planning .pl-card").length === 1 && document.querySelectorAll("#pl-tag-filter .pl-tag").length === 1`, "")
	b.menuPick("#pl-filter", "ux", `document.querySelectorAll("#view-planning .pl-card").length === 1 && [...document.querySelectorAll("#pl-tag-filter .pl-tag-name")].map((e) => e.textContent).join() === "Q3,ux" && location.search.includes("tag=Q3&tag=ux") && document.querySelector("#pl-filter-n").textContent === "2"`,
		`document.querySelector('.pop-menu [aria-checked=true]')?.textContent.includes("Q3")`)
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

// An org's boards are tabs at the head of the page, with All boards first
// when there is more than one; a click shows one, and the board shown is in
// the address. A new card goes on it; an open card moves to another board; a
// card's link opens its board. + after the tabs adds a board; a double click
// renames one; a tab's menu moves, makes default and deletes its board (a
// deleted board's cards move to the default one, after a confirm). On a phone
// the boards are a strip across the top.
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
	b.waitJS("a tab for each board, the default one shown", `(() => { const t = [...document.querySelectorAll("#pl-boards .pl-board-tab")];
		return t.map((x) => x.textContent).join() === "All boards,Main,Mobile" && t[1].getAttribute("aria-selected") === "true" && !document.querySelector("#pl-boards .n") && !document.querySelector("#pl-boards .pl-board-more") && !document.querySelector("#pl-board-more").hidden && [...document.querySelectorAll("#pl-boards > *")].every((x) => x.getAttribute("role") === "tab"); })()`)
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
	b.until("open Push alerts", `!document.querySelector("#card-detail").hidden && !!document.querySelector("#pd-board")`,
		chromedp.Click(chromedp.CSS(cardOf(onMobile.ID)+" .pl-open")))
	b.run("move it to Main", `(() => { const s = document.querySelector("#pd-board"); s.value = s.options[0].value; s.dispatchEvent(new Event("change", { bubbles: true })); return true; })()`)
	b.waitJS("gone from Mobile", fmt.Sprintf(`!document.querySelector(%q)`, cardOf(onMobile.ID)))
	if got := titles(""); len(got) != 2 || got[1] != "Push alerts" {
		t.Fatalf("Main's cards after the move: %v", got)
	}

	// A card's link opens its board.
	b.do("open a card's link", chromedp.Navigate(h.base+"/planning/cards/"+onMain.ID+"?board="+mobile.ID))
	b.waitJS("the card open, on its board", fmt.Sprintf(`!document.querySelector("#card-detail").hidden && document.querySelector("#pl-boards .pl-board-tab[aria-selected=true]")?.dataset.board !== %q && !!document.querySelector(%q)`, mobile.ID, cardOf(onMain.ID)))
	b.waitJS("its board in the address", `!new URLSearchParams(location.search).get("board")`)
	b.do("close the card", chromedp.KeyEvent(kb.Escape))

	// All boards: every board's cards, each naming its board.
	b.until("All boards", fmt.Sprintf(`!!document.querySelector(%q) && !!document.querySelector(%q) && new URLSearchParams(location.search).get("board") === "all"`, cardOf(onMain.ID), cardOf(onMobile.ID)),
		chromedp.Click(chromedp.CSS(`#pl-boards .pl-board-tab[data-board=all]`)))
	b.waitJS("each card names its board", fmt.Sprintf(`document.querySelector('%s .pl-board-chip')?.textContent === "Main"`, cardOf(onMain.ID)))

	tabs := `[...document.querySelectorAll("#pl-boards .pl-board-tab:not([data-board=all])")].map((x) => x.textContent).join()`
	// + adds a board, which is then shown.
	b.until("+", `document.activeElement?.matches(".pl-board-input")`, chromedp.Click(chromedp.CSS(`#pl-board-add`)))
	// The box has the focus, so the keys go to it as they are. (SendKeys looks
	// the box up first: a reload of the boards in between moves it, with its
	// focus and text, but gives it another node id, and the keys fail.)
	b.do("add Web", chromedp.KeyEvent("Web"), chromedp.KeyEvent(kb.Enter))
	b.waitJS("Web added, last, and shown", tabs+` === "Main,Mobile,Web" && document.querySelector("#pl-boards [aria-selected=true]")?.textContent === "Web"`)
	// A double click renames it.
	b.run("double-click Web", `(() => { document.querySelector("#pl-boards .pl-board-tab[aria-selected=true]").dispatchEvent(new MouseEvent("dblclick", { bubbles: true })); return true; })()`)
	b.waitJS("its name in a box, outside the tabs", `document.activeElement?.matches(".pl-board-input") && document.activeElement.value === "Web" && !document.activeElement.closest("#pl-boards")`)
	b.do("rename it", chromedp.KeyEvent(kb.End), chromedp.KeyEvent("site"), chromedp.KeyEvent(kb.Enter))
	b.waitJS("renamed", tabs+` === "Main,Mobile,Website"`)
	// Its menu (⋯ on the tab shown, or a right click) moves it.
	b.menuPick("#pl-board-more", "Move left", tabs+` === "Main,Website,Mobile"`, "")
	b.waitJS("the default board has no Delete", `(() => { document.querySelector("#pl-boards .pl-board-tab[data-board]:not([data-board=all])").dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
		const m = document.querySelector(".pop-menu"); return !!m && m.textContent.includes("Rename") && !m.textContent.includes("Delete"); })()`)
	b.do("close the menu", chromedp.KeyEvent(kb.Escape))
	b.run("Mobile's menu", fmt.Sprintf(`(() => { document.querySelector('#pl-boards .pl-board-tab[data-board=%q]').dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true })); return true; })()`, mobile.ID))
	b.clickText(`.pop-menu [role^=menuitem]`, "Delete")
	b.waitJS("delete Mobile asks first", `document.querySelector("#ask-sheet")?.open && document.querySelector("#ask-body").textContent.includes("to Main")`)
	b.until("deleted", tabs+` === "Main,Website"`, chromedp.Click(chromedp.CSS(`#ask-ok`)))
	if got := titles(""); len(got) != 3 || got[2] != "Offline mode" {
		t.Fatalf("Main's cards after deleting Mobile: %v", got)
	}
	// Default makes another board the default one.
	b.menuPick("#pl-board-more", "Default", `!document.querySelector("#pl-board-more").hidden`, "")
	var boards []struct {
		Name    string
		Default bool
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		h.api.must("GET", "/api/planning/boards", nil, http.StatusOK, &boards)
		if len(boards) == 2 && boards[1].Name == "Website" && boards[1].Default && !boards[0].Default {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("boards after Default: %+v", boards)
		}
	}

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the boards across the top, above the lanes", `(() => { const s = document.querySelector("#pl-boards"), l = document.querySelector("#view-planning .pl-lanes"); return s.offsetWidth > 0 && s.getBoundingClientRect().bottom <= l.getBoundingClientRect().top + 1 && s.scrollWidth >= s.clientWidth; })()`)
}

// Start shows the text the work starts with, editable, and says what each
// button does: a member makes a draft (the card stays where it is) or
// queues the task.
func TestBrowserPlanningStart(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	var p struct{ ID string }
	h.api.must("POST", "/api/projects", map[string]any{"name": "shop", "remote_url": "https://example.com/shop.git", "runs_on": []string{"any"}}, http.StatusCreated, &p)
	var c struct{ ID string }
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Export reports as PDF", "column": "ideas", "project": p.ID, "description": "So finance can file them."}, http.StatusCreated, &c)

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open Planning", `document.querySelector("#view-planning.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=planning]`)))
	b.until("open the card", `!document.querySelector("#card-detail").hidden && !!document.querySelector("#pd-desc")`,
		chromedp.Click(chromedp.CSS(fmt.Sprintf(`#view-planning .pl-card[data-id=%q] .pl-open`, c.ID))))
	b.clickTextUntil(`#pd-actions button`, "Start…", `document.querySelector("dialog.pl-start-sheet")?.open`)
	b.waitJS("the prompt, from the card, and no explanations", `(() => { const d = document.querySelector("dialog.pl-start-sheet");
		return d.querySelector(".pl-start-text").value === "Export reports as PDF\n\nSo finance can file them." &&
			!d.querySelector("p.hint:not(.warn), em, small") &&
			[...d.querySelectorAll(".radio span")].map((x) => x.textContent).join() === "Task,Goal" &&
			[...d.querySelectorAll(".sheet-actions button")].filter((x) => !x.hidden).map((x) => x.textContent).join() === "Queue task,Create draft"; })()`)
	b.waitJS("a goal's button", `(() => { const d = document.querySelector("dialog.pl-start-sheet");
		d.querySelector("input[value=goal]").click();
		const ok = [...d.querySelectorAll(".sheet-actions button")].filter((x) => !x.hidden).map((x) => x.textContent).join() === "Start goal" &&
			d.querySelector(".pl-start-text").value === "So finance can file them.";
		d.querySelector("input[value=task]").click();
		return ok; })()`)
	b.run("edit the prompt", `document.querySelector(".pl-start-text").value = "Export the monthly reports as PDF."; true`)
	b.until("Create draft", `!document.querySelector("dialog.pl-start-sheet") && document.querySelector("#pd-body .pl-link")`,
		chromedp.Click(chromedp.CSS(`dialog.pl-start-sheet button[value=draft]`)))

	var v struct {
		Column string
		Links  []struct{ ID string }
	}
	h.api.must("GET", "/api/planning/cards/"+c.ID, nil, http.StatusOK, &v)
	if v.Column != "ideas" || len(v.Links) != 1 {
		t.Fatalf("the card: %+v", v)
	}
	var td struct{ Status, Prompt string }
	h.api.must("GET", "/api/todos/"+v.Links[0].ID, nil, http.StatusOK, &td)
	if td.Status != "draft" || td.Prompt != "Export the monthly reports as PDF." {
		t.Fatalf("the task: %+v", td)
	}
}

// The open card: Start… the one main action, Write a feature brief beside
// it and the rest in ⋯; Copy link in the head; the title edited in place;
// the column a menu in the head; the description first, as Markdown, a box
// on a click; sections only when they have something, else a small Add; no
// help texts. Comments are Markdown, with how long ago, and their author's
// delete. The pane lies beside the board, which keeps every column and the
// card in sight; on a phone it is full screen, the description in sight.
func TestBrowserPlanningOpenCard(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	var c struct{ ID string }
	h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Export reports", "column": "planned", "description": "## Why\nSo **finance** can file them."}, http.StatusCreated, &c)
	for _, col := range []string{"ideas", "in_progress", "done"} {
		h.api.must("POST", "/api/planning/cards", map[string]any{"title": "Card in " + col, "column": col}, http.StatusCreated, nil)
	}
	h.api.must("POST", "/api/planning/cards/"+c.ID+"/comments", map[string]any{"text": "Mine, with **bold**."}, http.StatusCreated, nil)
	var me struct{ Org struct{ ID int64 } }
	h.api.must("GET", "/api/me", nil, http.StatusOK, &me)
	var inv struct{ Token string }
	h.api.must("POST", fmt.Sprintf("/api/orgs/%d/invitations", me.Org.ID), map[string]any{"email": "ana@example.com", "password": h.password}, http.StatusCreated, &inv)
	ana := newClient(t, h.base)
	ana.must("POST", "/api/invitations/register", map[string]any{"token": inv.Token, "username": "ana", "password": "another-long-password"}, http.StatusCreated, nil)
	ana.must("POST", "/api/planning/cards/"+c.ID+"/comments", map[string]any{"text": "Ana's."}, http.StatusCreated, nil)

	b := newBrowser(t, chrome)
	b.signIn(h)
	b.until("open Planning", `document.querySelector("#view-planning.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=planning]`)))
	card := fmt.Sprintf(`#view-planning .pl-card[data-id=%q]`, c.ID)
	b.until("open the card", `!document.querySelector("#card-detail").hidden && !!document.querySelector("#pd-desc")`,
		chromedp.Click(chromedp.CSS(card+" .pl-open")))
	b.waitJS("Start… the main action, the brief, ⋯; Copy link in the head", `(() => {
		const a = [...document.querySelectorAll("#pd-actions > button")];
		return a.map((x) => x.textContent || x.getAttribute("aria-label")).join() === "Start…,Write a feature brief,More actions" &&
			document.querySelectorAll("#pd-actions .btn.primary").length === 1 && document.querySelector("#pd-start").matches(".primary") &&
			!!document.querySelector(".detail-head > #pd-copy"); })()`)
	b.waitJS("the description first, as Markdown; the properties after it, no Column select", `(() => {
		const d = document.querySelector("#pd-desc");
		return d.tagName === "DIV" && d.querySelector("h2")?.textContent === "Why" && d.querySelector("strong")?.textContent === "finance" &&
			!!(d.compareDocumentPosition(document.querySelector(".pl-props")) & Node.DOCUMENT_POSITION_FOLLOWING) &&
			!document.querySelector("#pd-column-select, #pd-body select[aria-label=Column]") && !document.querySelector("#pd-body #pd-column"); })()`)
	b.waitJS("no empty sections, a small Add instead, and no help texts", `!document.querySelector("#pd-files, #pd-deps, #pd-work") &&
		[...document.querySelectorAll(".pl-add-row button")].map((x) => x.textContent).join() === "File,Dependency" &&
		!document.querySelector("#card-detail .hint, #card-detail [placeholder*='.']") && !document.querySelector("#pd-actions [title]:not(#pd-more)")`)
	b.waitJS("comments: Markdown, how long ago, the date on hover, delete only on your own", `(() => {
		const cs = [...document.querySelectorAll(".pl-comment")];
		return cs.length === 2 && cs[0].querySelector(".pl-comment-text strong")?.textContent === "bold" &&
			cs[0].querySelector("time").textContent === "just now" && cs[0].querySelector("time").title.length > 8 &&
			!!cs[0].querySelector(".pl-comment-del") && !cs[1].querySelector(".pl-comment-del") &&
			getComputedStyle(cs[0].querySelector(".pl-comment-del")).opacity === "0"; })()`)
	b.waitJS("the pane beside the board: every column, the card in sight and selected", fmt.Sprintf(`(() => {
		const pane = document.querySelector("#card-detail").getBoundingClientRect();
		const cols = [...document.querySelectorAll("#view-planning .pl-col")].map((x) => x.getBoundingClientRect());
		const r = document.querySelector(%q).getBoundingClientRect();
		return cols.length === 4 && cols.every((x) => x.right <= pane.left + 1 && x.width > 100) && r.right <= pane.left && r.left >= 0 &&
			document.querySelector(%q).matches(".selected"); })()`, card, card+" .todo"))
	shot(b, "planning-card-wide")

	// The title, in place: Esc cancels, Enter saves.
	b.until("edit the title", `document.activeElement?.id === "pd-edit-title"`, chromedp.Click(chromedp.CSS(`#pd-title .title-edit`)))
	b.do("type, then Esc", chromedp.KeyEvent(kb.End), chromedp.SendKeys(`#pd-edit-title`, " as PDF"), chromedp.KeyEvent(kb.Escape))
	b.waitJS("cancelled, the pane still open", `document.querySelector("#pd-title").textContent === "Export reports" && !document.querySelector("#card-detail").hidden`)
	b.until("edit it again", `document.activeElement?.id === "pd-edit-title"`, chromedp.Click(chromedp.CSS(`#pd-title .title-edit`)))
	b.do("type, then Enter", chromedp.KeyEvent(kb.End), chromedp.SendKeys(`#pd-edit-title`, " as PDF"), chromedp.KeyEvent(kb.Enter))
	b.waitJS("saved, on the board too", fmt.Sprintf(`document.querySelector("#pd-title").textContent === "Export reports as PDF" && document.querySelector(%q)?.textContent === "Export reports as PDF"`, card+" .todo-title"))

	// The column: the head's chip is a menu.
	b.until("the column menu", `!!document.querySelector(".pop-menu")`, chromedp.Click(chromedp.CSS(`#pd-column`)))
	b.clickTextUntil(".pop-menu [role=menuitem]", "In progress", fmt.Sprintf(`document.querySelector("#pd-column").textContent === "In progress" && !!document.querySelector(%q)`,
		`#view-planning .pl-col[data-col=in_progress] .pl-card[data-id="`+c.ID+`"]`))

	// The description: a click makes it a box; Esc cancels, Ctrl+Enter saves.
	b.until("edit the description", `document.activeElement === document.querySelector("textarea#pd-desc")`, chromedp.Click(chromedp.CSS(`#pd-desc`)))
	b.do("type, then Esc", chromedp.SendKeys(`textarea#pd-desc`, "\nDropped."), chromedp.KeyEvent(kb.Escape))
	b.waitJS("as it was, the pane still open", `document.querySelector("#pd-desc").tagName === "DIV" && !document.querySelector("#pd-desc").textContent.includes("Dropped") && !document.querySelector("#card-detail").hidden`)
	b.until("edit it with the pencil", `document.activeElement === document.querySelector("textarea#pd-desc")`, chromedp.Click(chromedp.CSS(`.pl-desc-edit-btn`)))
	b.do("type, then Ctrl+Enter", chromedp.SendKeys(`textarea#pd-desc`, "\n\n- *Monthly*"), chromedp.KeyEvent(kb.Enter, chromedp.KeyModifiers(chromedp.ModifierCtrl)))
	b.waitJS("saved, as Markdown", `document.querySelector("#pd-desc").tagName === "DIV" && document.querySelector("#pd-desc li em")?.textContent === "Monthly"`)
	var v struct{ Description string }
	h.api.must("GET", "/api/planning/cards/"+c.ID, nil, http.StatusOK, &v)
	if v.Description != "## Why\nSo **finance** can file them.\n\n- *Monthly*" {
		t.Fatalf("description: %q", v.Description)
	}

	// On a phone: full screen, the description in sight without scrolling.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("full screen, the actions on one row, the description in sight", `(() => {
		const pane = document.querySelector("#card-detail"), d = document.querySelector("#pd-desc").getBoundingClientRect();
		const a = [...document.querySelectorAll("#pd-actions > *")].map((x) => x.getBoundingClientRect().top);
		return pane.offsetWidth >= innerWidth - 1 && d.top > 0 && d.top < innerHeight * 0.6 && a.every((t) => Math.abs(t - a[0]) < 2); })()`)
	shot(b, "planning-card-phone")
}

// menuPick opens the menu under anchor (until ready, an expression, is true
// of it, when given), picks the item labelled label, and waits for expr.
func (b *browser) menuPick(anchor, label, expr, ready string) {
	b.t.Helper()
	open := `!!document.querySelector(".pop-menu")`
	if ready != "" {
		open += " && " + ready
	}
	b.until("open "+anchor, open, chromedp.Click(chromedp.CSS(anchor)))
	b.clickText(`.pop-menu [role^=menuitem]`, label)
	b.waitJS("picked "+label, expr)
}
