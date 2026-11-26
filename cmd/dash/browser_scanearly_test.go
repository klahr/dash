package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
)

// A scan's schedule has "Run early if there's spare usage" under it: off
// for a scan without a schedule, and, once on, a When choice. On a wide
// screen and a phone's, nothing scrolls sideways.
func TestBrowserScanEarly(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
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
	early := func() map[string]any {
		var res struct {
			Scans []struct {
				Name     string
				Schedule struct {
					Early map[string]any `json:"early"`
				} `json:"schedule"`
			} `json:"scans"`
		}
		h.api.must("GET", "/api/projects/"+h.project.ID+"/scans", nil, http.StatusOK, &res)
		for _, s := range res.Scans {
			if s.Name == "security" {
				return s.Schedule.Early
			}
		}
		return nil
	}
	card := func(name string) string { return `.scan-card[data-scan="` + name + `"]` }
	// Scrolled to in the page: the cards are drawn again as the scans
	// refresh, so a node found first may be gone by the time it is scrolled.
	scrollTo := func() {
		t.Helper()
		b.run("scroll to it", `const e = document.querySelector('`+card("security")+` .scan-early');
			e?.scrollIntoView({ block: "center" });
			!!e`)
	}

	b.do("a wide screen", chromedp.EmulateViewport(1280, 900))
	b.topMenu("pm-scans", `document.querySelector("#scans-sheet").open`)
	b.until("open the security scan's details", `!!document.querySelector('`+card("security")+` .scan-early')`,
		chromedp.Click(chromedp.CSS(card("security")+` .scan-more`)))
	b.until("open breaking changes' details", `!!document.querySelector('`+card("breaking-changes")+` .scan-early')`,
		chromedp.Click(chromedp.CSS(card("breaking-changes")+` .scan-more`)))
	b.waitJS("off without a schedule", `(() => {
		const e = document.querySelector('`+card("breaking-changes")+` .scan-early');
		return e.querySelector("input").disabled && e.textContent.includes("needs a schedule");
	})()`)
	b.waitJS("the toggle and the hint", `(() => {
		const e = document.querySelector('`+card("security")+` .scan-early');
		return !e.querySelector("input").disabled && !e.querySelector("select") &&
			e.textContent.includes("Run early if there's spare usage") &&
			e.textContent.includes("Runs the scan up to an hour before your Claude usage resets if less than 70% is used. Otherwise it runs at the scheduled time.");
	})()`)
	b.until("turn it on", `!!document.querySelector('`+card("security")+` .scan-early select')`,
		chromedp.Click(chromedp.CSS(card("security")+` .scan-early input`)))
	if e := early(); e["on"] != true {
		t.Fatalf("early after turning it on: %v", e)
	}
	b.waitJS("When: Any time", `[...document.querySelector('`+card("security")+` .scan-early select').options].map((o) => o.textContent).join() === "Any time,At night only" &&
		document.querySelector('`+card("security")+` .scan-early select').value === "any"`)
	b.do("pick At night only", chromedp.SetValue(chromedp.CSS(card("security")+` .scan-early select`), "night"))
	if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`document.querySelector('`+card("security")+` .scan-early select').dispatchEvent(new Event("change"))`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, nil, "night saved", func() bool { return early()["when"] == "night" })
	noSideways := `document.documentElement.scrollWidth <= document.documentElement.clientWidth`
	b.waitJS("no sideways scroll", noSideways)
	scrollTo()
	shot("scan-early-wide")

	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("no sideways scroll on a phone", noSideways+` && (() => {
		const e = document.querySelector('`+card("security")+` .scan-early');
		return e.getBoundingClientRect().right <= document.documentElement.clientWidth;
	})()`)
	scrollTo()
	shot("scan-early-phone")
}
