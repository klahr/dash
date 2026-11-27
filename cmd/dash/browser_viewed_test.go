package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// Files of a task's changes marked viewed: the Viewed checkbox folds the
// file and fills the bar above the files and the thin one on the card in
// Review; v marks the file in view. Marks are stored on the hub, so they
// outlive a reload; a rework that changes a file clears its mark by itself,
// and keeps the marks on the files it didn't touch. On a wide screen and a
// phone.
func TestBrowserViewedFiles(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	b := newBrowser(t, chrome)
	b.signIn(h)
	shot := func(name string) {
		if dir := os.Getenv("DASH_TEST_SHOTS"); dir != "" {
			buf, err := chromedp.Run(b.ctx, chromedp.FullScreenshot(90))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name+".jpg"), buf, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	type viewedTodo struct {
		Status string
		Runs   int
		Viewed []struct{ File string }
	}
	viewedOf := func(id string) map[string]bool {
		var got viewedTodo
		api.must("GET", "/api/todos/"+id, nil, http.StatusOK, &got)
		m := map[string]bool{}
		for _, v := range got.Viewed {
			m[v.File] = true
		}
		return m
	}
	rework := func(id string, runs int) {
		t.Helper()
		api.must("POST", "/api/todos/"+id+"/rework", map[string]any{"feedback": fmt.Sprintf("Rework %d", runs)}, http.StatusOK, nil)
		waitStatus(t, api, "/api/todos/"+id, "review", func(td todo) bool { return td.Status == "review" && td.Runs >= runs })
	}
	paneText := `document.querySelector("#d-diff .review-progress .review-text")?.textContent`
	cardBar := func(id string) string {
		return fmt.Sprintf(`document.querySelector(%q)?.querySelector(".card-review-bar")`, cardSel(id))
	}
	box := func(file string) string {
		return fmt.Sprintf(`#d-diff details.diff-file[data-path=%q] .diff-viewed input`, file)
	}

	for _, size := range []struct {
		name  string
		vp    chromedp.Action[chromedp.Void]
		phone bool
	}{
		{"wide", chromedp.EmulateViewport(1280, 900), false},
		{"phone", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch), true},
	} {
		b.do("a "+size.name+" screen", size.vp)
		var td struct{ ID string }
		api.must("POST", "/api/projects/"+pid+"/todos", map[string]any{"title": "Viewed " + size.name, "prompt": "Add a done file", "enqueue": true}, http.StatusCreated, &td)
		waitStatus(t, api, "/api/todos/"+td.ID, "review", nil)
		// A second file comes with a rework (the fake Claude writes it on --resume).
		rework(td.ID, 2)
		if size.phone {
			b.until("show the review lane", `document.querySelector("#board .col.active")?.dataset.lane === "review" || !document.querySelector(".lane-tab")?.offsetParent`,
				chromedp.Click(chromedp.CSS(`.lane-tab[data-lane="review"]`)))
		}
		b.waitCard(td.ID, "review")
		b.waitJS("no bar on the card before anything is viewed", "!"+cardBar(td.ID))

		b.openTask(td.ID)
		b.until("show the changes", `document.querySelector("#d-diff")?.textContent.includes("rework-by-runner.txt")`,
			chromedp.Click(chromedp.CSS(`.pane-tabs button[data-pane=diff]`)))
		// Back from a rework, Changes shows only what changed since the last
		// review; the bar counts the whole diff all the same.
		b.waitJS("the bar over the whole diff, nothing viewed", paneText+` === "0 of 2 files viewed" && !document.querySelector('#d-diff details.diff-file[data-path="done-by-runner.txt"]')`)
		b.until("show all changes", `!!document.querySelector('#d-diff details.diff-file[data-path="done-by-runner.txt"]')`,
			chromedp.Click(chromedp.CSS(`#d-diff .since-switch`)))
		b.waitJS("the bar, nothing viewed", paneText+` === "0 of 2 files viewed"`)
		// Open a file, then check Viewed: it folds.
		b.until("open the file", `document.querySelector('#d-diff details.diff-file[data-path="done-by-runner.txt"]').open`,
			chromedp.Click(chromedp.CSS(`#d-diff details.diff-file[data-path="done-by-runner.txt"] > summary .path`)))
		b.until("check Viewed", `document.querySelector(`+jsq(box("done-by-runner.txt"))+`)?.checked`, chromedp.Click(chromedp.CSS(box("done-by-runner.txt"))))
		b.waitJS("the file folded and dimmed, and the bar", `(() => {
			const f = document.querySelector('#d-diff details.diff-file[data-path="done-by-runner.txt"]');
			return !f.open && f.classList.contains("viewed") && `+paneText+` === "1 of 2 files viewed";
		})()`)
		// Filled by lines: done-by-runner.txt is 1 line, rework-by-runner.txt more.
		b.waitJS("the fill under half", `(() => {
			const bar = document.querySelector("#d-diff .review-progress .review-bar");
			const n = Number(bar.getAttribute("aria-valuenow"));
			return n > 0 && n < 50;
		})()`)
		waitFor(t, nil, "the mark stored", func() bool { return viewedOf(td.ID)["done-by-runner.txt"] })
		// Still folded once the server's answer and its events have been shown.
		b.waitJS("still folded", `!document.querySelector('#d-diff details.diff-file[data-path="done-by-runner.txt"]').open`)
		shot("viewed-pane-" + size.name)

		// v marks the file in view: focus the other file's header.
		b.do("focus the other file", chromedp.Focus(chromedp.CSS(`#d-diff details.diff-file[data-path="rework-by-runner.txt"] > summary`)))
		b.until("press v", `document.querySelector(`+jsq(box("rework-by-runner.txt"))+`)?.checked`, chromedp.KeyEvent("v"))
		b.waitJS("every file viewed", paneText+` === "2 of 2 files viewed" && document.querySelector("#d-diff .review-progress.done")`)

		// The card: a full bar along its bottom edge, the text its tooltip.
		if !size.phone {
			b.waitJS("a full bar on the card", cardBar(td.ID)+`?.getAttribute("aria-valuenow") === "100" && `+cardBar(td.ID)+`.title === "2 of 2 files viewed"`)
		}

		waitFor(t, nil, "both marks stored", func() bool { v := viewedOf(td.ID); return v["done-by-runner.txt"] && v["rework-by-runner.txt"] })
		// A reload keeps them.
		b.reload()
		b.openTask(td.ID)
		b.until("show the changes after the reload", `!!document.querySelector(`+jsq(box("rework-by-runner.txt"))+`)`,
			chromedp.Click(chromedp.CSS(`.pane-tabs button[data-pane=diff]`)))
		b.waitJS("both still viewed", `document.querySelector(`+jsq(box("done-by-runner.txt"))+`).checked && document.querySelector(`+jsq(box("rework-by-runner.txt"))+`).checked`)
		b.until("close the task", `document.querySelector("#detail").hidden`, chromedp.KeyEvent(kb.Escape))

		// A rework changes rework-by-runner.txt only: its mark clears by itself.
		rework(td.ID, 3)
		waitFor(t, nil, "the changed file's mark cleared", func() bool {
			v := viewedOf(td.ID)
			return v["done-by-runner.txt"] && !v["rework-by-runner.txt"]
		})
		if size.phone {
			b.until("show the review lane again", `document.querySelector("#board .col.active")?.dataset.lane === "review" || !document.querySelector(".lane-tab")?.offsetParent`,
				chromedp.Click(chromedp.CSS(`.lane-tab[data-lane="review"]`)))
		}
		b.waitJS("a part-filled bar on the card", `(() => {
			const bar = `+cardBar(td.ID)+`;
			const n = Number(bar?.getAttribute("aria-valuenow"));
			return n > 0 && n < 100 && bar.title === "1 of 2 files viewed" && bar.getBoundingClientRect().height <= 3;
		})()`)
		shot("viewed-card-" + size.name)
	}
}
