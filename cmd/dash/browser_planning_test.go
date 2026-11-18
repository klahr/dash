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
	b.waitJS("the planner's sections", `[...document.querySelectorAll("#account-sheet .rail-item")].filter((x) => !x.hidden).map((x) => x.dataset.rail).join() === "picture,twofactor,data"`)
	b.until("two-factor sign-in", `document.querySelector("#twofactor-body button")?.textContent.includes("Set up")`,
		chromedp.Click(chromedp.CSS(`#account-sheet .rail-item[data-rail=twofactor]`)))
	b.do("close Account", chromedp.Click(chromedp.CSS(`#account-sheet .sheet-head button`)))

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("no bottom nav, the drawer closed", `!matchMedia("(min-width: 1024px)").matches &&
		!document.querySelector('.nav > button[data-view=planning]').offsetWidth && !document.querySelector(".app").classList.contains("menu-open")`)
	b.until("open the drawer", `document.querySelector(".app").classList.contains("menu-open") && document.querySelector("#planning-main").inert &&
		document.querySelector(".menu-view[data-view=planning]").offsetWidth > 0 && document.querySelector("#foot-menu").offsetWidth > 0`,
		chromedp.Click(chromedp.CSS(`#menu-open`)))
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
