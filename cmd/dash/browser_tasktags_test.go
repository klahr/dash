package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/chromedp/chromedp"
)

// Tags when making a task: #tags in quick add become the task's tags (shown
// as chips while typing), the task form has the task pane's tag input, its
// tags are kept in the form's draft, and editing a task shows and saves its
// tags. On a wide screen and a phone's.
func TestBrowserTaskTags(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	b := newBrowser(t, chrome)
	b.signIn(h)
	shot := func(name string) {
		if dir := os.Getenv("DASH_TEST_SHOTS"); dir != "" {
			if buf, err := chromedp.Run(b.ctx, chromedp.CaptureScreenshot()); err == nil {
				_ = os.WriteFile(filepath.Join(dir, name+".png"), buf, 0o644)
			}
		}
	}
	tagsOf := func(id string) []string {
		var td struct{ Tags []string }
		api.must("GET", "/api/todos/"+id, nil, http.StatusOK, &td)
		return td.Tags
	}
	chips := func(sel string) string {
		return fmt.Sprintf(`[...document.querySelectorAll(%q)].map((e) => e.firstChild.textContent).join()`, sel+" .tag-chip")
	}
	quickAdd := func(text string) {
		b.until("open the new task sheet", `document.querySelector("#quick-sheet").open`, chromedp.Click(chromedp.ID("new-todo")))
		b.do("write the task", chromedp.SendKeys(chromedp.CSS(`#quick-sheet[open] #quick-rows .quick-row`), text))
		b.waitJS("the task written", fmt.Sprintf(`document.querySelector("#quick-rows .quick-row").value === %q`, text))
	}

	for _, size := range []struct {
		name string
		vp   chromedp.Action[chromedp.Void]
	}{
		{"wide", chromedp.EmulateViewport(1280, 900)},
		{"phone", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch)},
	} {
		b.do("a "+size.name+" screen", size.vp)

		// Quick add: the #tags show as chips and are taken out of the text; issue #123 stays.
		quickAdd("Fix login redirect " + size.name + " for issue #123 #auth #bug")
		b.waitJS("the tags shown below the field", `!document.querySelector("#quick-tags").hidden && `+chips("#quick-tags")+` === "auth,bug"`)
		shot("quick-tags-" + size.name)
		b.until("save the draft", `!document.querySelector("#quick-sheet").open`, chromedp.Click(chromedp.CSS(`#quick-form button[value=draft]`)))
		quick := waitTodo(t, api, pid, "Fix login redirect "+size.name+" for issue #123")
		if got := tagsOf(quick); !slices.Equal(got, []string{"auth", "bug"}) {
			t.Fatalf("%s: quick add's tags %q", size.name, got)
		}

		// More options… carries the tags to the form; one more is typed there.
		quickAdd("Restyle the header " + size.name + " #ui")
		b.until("open the full form", `document.querySelector("#todo-sheet").open`, chromedp.Click(chromedp.ID("quick-more")))
		b.waitJS("the tag and text carried over", chips("#todo-tags")+` === "ui" && document.querySelector("#todo-form").prompt.value === "Restyle the header `+size.name+`"`)
		b.do("type a tag", chromedp.Click(chromedp.CSS(`#todo-tags .tag-input`)), chromedp.SendKeys(chromedp.CSS(`#todo-tags .tag-input`), "design\r"))
		b.waitJS("the typed tag as a chip, the form still open", chips("#todo-tags")+` === "ui,design" && document.querySelector("#todo-sheet").open`)
		b.waitJS("the tags field fits the sheet", `(() => {
			const r = document.querySelector("#todo-tags").getBoundingClientRect(), s = document.querySelector("#todo-form").getBoundingClientRect();
			return r.width > 0 && r.left >= s.left && r.right <= s.right && document.querySelector("#todo-form").scrollWidth <= document.querySelector("#todo-form").clientWidth;
		})()`)
		b.waitJS("the draft saved", `Object.entries(localStorage).some(([k, v]) => k.startsWith("dash.draft.") && v.includes('"tags":["ui","design"]'))`)
		// Close is above the fold on a phone, in the sheet's own scroller.
		b.run("close the form", `document.querySelector("#todo-form .sheet-head button[value=cancel]").click();
			true`)
		b.waitJS("the form closed", `!document.querySelector("#todo-sheet").open`)

		// Opening the form again restores the draft, tags too.
		b.until("open the new task sheet again", `document.querySelector("#quick-sheet").open`, chromedp.Click(chromedp.ID("new-todo")))
		b.until("open the full form again", `document.querySelector("#todo-sheet").open`, chromedp.Click(chromedp.ID("quick-more")))
		b.waitJS("the draft restored with its tags", chips("#todo-tags")+` === "ui,design" && document.querySelector("#todo-form").prompt.value === "Restyle the header `+size.name+`"`)
		b.do("scroll to the tags", chromedp.ScrollIntoView(chromedp.CSS(`#todo-tags`)))
		shot("form-tags-" + size.name)
		b.until("save the draft", `!document.querySelector("#todo-sheet").open`, chromedp.Click(chromedp.CSS(`#todo-form button[value=draft]`)))
		full := waitTodo(t, api, pid, "Restyle the header "+size.name)
		if got := tagsOf(full); !slices.Equal(got, []string{"ui", "design"}) {
			t.Fatalf("%s: the form's tags %q", size.name, got)
		}
	}

	// Editing a task (the phone's Edit): its tags are in the form; a change is saved.
	id := waitTodo(t, api, pid, "Restyle the header phone")
	b.waitCard(id, "draft")
	b.openTask(id)
	b.clickTextUntil("#detail .btn", "Edit", `document.querySelector("#todo-sheet").open`)
	b.waitJS("the task's tags in the form", chips("#todo-tags")+` === "ui,design"`)
	b.until("remove a tag", chips("#todo-tags")+` === "ui"`, chromedp.Click(chromedp.CSS(`#todo-tags .tag-x[aria-label="Remove tag design"]`)))
	b.do("type a tag, not ended", chromedp.Click(chromedp.CSS(`#todo-tags .tag-input`)), chromedp.SendKeys(chromedp.CSS(`#todo-tags .tag-input`), "later"))
	b.until("save", `!document.querySelector("#todo-sheet").open`, chromedp.Click(chromedp.CSS(`#todo-form button[value=draft]`)))
	waitFor(t, nil, "the edited tags", func() bool { return slices.Equal(tagsOf(id), []string{"ui", "later"}) })
}
