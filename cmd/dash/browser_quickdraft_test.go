package main

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// The quick new task sheet: Shift-Enter types a new line (the field grows,
// and the text stays one task, its #tags taken from any line), Alt-Enter
// starts another task's field, and closing the sheet with Esc keeps the
// text, which is back the next time it opens and gone once saved. On a wide
// screen and a phone's.
func TestBrowserQuickDraft(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	b := newBrowser(t, chrome)
	b.signIn(h)

	const row = `#quick-sheet[open] #quick-rows .quick-row`
	rows := `document.querySelectorAll("#quick-rows .quick-row")`
	stored := `Object.entries(localStorage).filter(([k]) => k.startsWith("dash.draft.quick/")).map(([, v]) => v).join()`
	shift := chromedp.KeyModifiers(chromedp.ModifierShift)
	alt := chromedp.KeyModifiers(chromedp.ModifierAlt)

	for _, size := range []struct {
		name string
		vp   chromedp.Action[chromedp.Void]
	}{
		{"wide", chromedp.EmulateViewport(1280, 900)},
		{"phone", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch)},
	} {
		b.do("a "+size.name+" screen", size.vp)
		first := "Line one " + size.name

		b.until("open the new task sheet", `document.querySelector("#quick-sheet").open`, chromedp.Click(chromedp.ID("new-todo")))
		b.waitJS("an empty field", rows+`.length === 1 && `+rows+`[0].value === ""`)
		b.run("note the field's height", `window.quickH = document.querySelector("#quick-rows .quick-row").offsetHeight; true`)
		b.do("write two lines", chromedp.SendKeys(chromedp.CSS(row), first),
			chromedp.KeyEvent(kb.Enter, shift),
			chromedp.SendKeys(chromedp.CSS(row), "line two #ui"))
		b.waitJS("one field with both lines, grown to fit", fmt.Sprintf(`(() => {
			const r = %s;
			return r.length === 1 && r[0].value === %q && r[0].offsetHeight > window.quickH && r[0].scrollHeight <= r[0].clientHeight + 1;
		})()`, rows, first+"\nline two #ui"))
		b.waitJS("the tag from the second line", `!document.querySelector("#quick-tags").hidden && document.querySelector("#quick-tags .tag-chip")?.firstChild.textContent === "ui"`)
		b.waitJS("the text autosaved", stored+fmt.Sprintf(`.includes(%q)`, first))

		b.until("Esc closes the sheet", `!document.querySelector("#quick-sheet").open`, chromedp.KeyEvent(kb.Escape))
		b.waitJS("the text still kept", stored+fmt.Sprintf(`.includes(%q)`, first))
		b.until("open the sheet again", `document.querySelector("#quick-sheet").open`, chromedp.Click(chromedp.ID("new-todo")))
		b.waitJS("the text back", fmt.Sprintf(`%s.length === 1 && %s[0].value === %q`, rows, rows, first+"\nline two #ui"))

		b.do("Alt-Enter for another task", chromedp.KeyEvent(kb.Enter, alt))
		b.waitJS("a second field, with focus", rows+`.length === 2 && document.activeElement === `+rows+`[1]`)
		b.do("write the second task", chromedp.SendKeys(chromedp.CSS(`#quick-rows .quick-row:last-child`), "Second "+size.name))
		b.until("Enter saves both", `!document.querySelector("#quick-sheet").open`, chromedp.KeyEvent(kb.Enter))

		one := waitTodo(t, api, pid, first+"\nline two")
		waitTodo(t, api, pid, "Second "+size.name)
		var td struct{ Tags []string }
		api.must("GET", "/api/todos/"+one, nil, http.StatusOK, &td)
		if !slices.Equal(td.Tags, []string{"ui"}) {
			t.Fatalf("%s: the multi-line task's tags %q", size.name, td.Tags)
		}
		b.waitJS("the draft forgotten", stored+` === ""`)
		b.until("open the sheet once more", `document.querySelector("#quick-sheet").open`, chromedp.Click(chromedp.ID("new-todo")))
		b.waitJS("an empty field again", rows+`.length === 1 && `+rows+`[0].value === ""`)
		b.until("close it", `!document.querySelector("#quick-sheet").open`, chromedp.KeyEvent(kb.Escape))
	}
}
