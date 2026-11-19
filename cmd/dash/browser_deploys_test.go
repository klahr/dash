package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// The Deploys page against a real hub and runner: a card per environment,
// Deploy on one runs its script on the runner and goes live; the deploy
// opens in a pane beside the list with its log, and Esc closes it. Once a
// newer commit is live, the card offers Roll back to the one before, which
// deploys it again after asking; its pane shows how its checks went. On a
// phone's screen the pane covers the page and its close button reads
// Back. Project settings → Deploy lists the environments to edit, with
// their checks.
//
// $DASH_TEST_SHOTS, if set, is a folder the page's screenshots are saved
// in (wide and narrow), for a person to look at.
func TestBrowserDeploys(t *testing.T) {
	endToEnd(t)
	chrome := needChrome(t)
	h := startRunnerHub(t)
	api, pid := h.api, h.project.ID
	api.must("PATCH", "/api/projects/"+pid, map[string]any{"environments": []map[string]any{
		{"name": "production", "deploy_command": "echo deploying $DASH_ENV at $DASH_COMMIT", "url": "https://example.com"},
		{"name": "staging", "deploy_command": "echo staging rollback=$DASH_ROLLBACK", "verify_command": `test -n "$DASH_COMMIT"`, "soak": 1},
	}}, http.StatusOK, nil)

	b := newBrowser(t, chrome)
	b.signIn(h)
	shot := func(name string) {
		dir := os.Getenv("DASH_TEST_SHOTS")
		if dir == "" {
			return
		}
		time.Sleep(time.Second) // panes and drawers done sliding
		ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
		defer cancel()
		if png, err := chromedp.Run(ctx, chromedp.FullScreenshot(90)); err == nil {
			_ = os.WriteFile(filepath.Join(dir, name+".png"), png, 0o644)
		}
	}

	b.until("open Deploys", `document.querySelector("#view-deploys.active")`,
		chromedp.Click(chromedp.CSS(`.nav > button[data-view=deploys]`)))
	b.waitJS("a card per environment", `[...document.querySelectorAll("#env-cards .env-card .env-name")].map((e) => e.textContent).join() === "production,staging"`)
	b.waitJS("nothing deployed yet", `document.querySelector('#env-cards [data-env="production"]').textContent.includes("Nothing deployed here yet")`)

	// Deploy production: it needs an admin's approval (the owner is the
	// org's only admin, so their own will do); approved, it runs on the
	// runner and goes live.
	b.waitJS("production needs an approval", `document.querySelector('#env-cards [data-env="production"]').textContent.includes("Deploys need approval from an admin")`)
	b.until("deploy production", `document.querySelector('#env-cards [data-env="production"] .env-awaiting button.primary')`,
		chromedp.Click(chromedp.CSS(`#env-cards [data-env="production"] .env-deploy`)))
	b.waitJS("it awaits approval in the list", `document.querySelector("#deploy-list li .status.d-awaiting_approval")`)
	shot("deploys-approval-wide")
	b.do("approve it", chromedp.Click(chromedp.CSS(`#env-cards [data-env="production"] .env-awaiting button.primary`)))
	b.waitJS("the deploy live in the list", `document.querySelector("#deploy-list li .status.d-live")`)
	b.waitJS("the card shows it live", `document.querySelector('#env-cards [data-env="production"] .env-live .status.d-live')`)
	shot("deploys-wide")

	// Its pane: beside the list, with the log.
	b.until("open the deploy", `!document.querySelector("#deploy-detail").hidden`,
		chromedp.Click(chromedp.CSS(`#deploy-list li .deploy-open`)))
	b.waitJS("its title and log", `document.querySelector("#dd-title").textContent === "Deploy to production" && document.querySelector("#dd-log").textContent.includes("deploying production at ")`)
	b.waitJS("its phases", `[...document.querySelectorAll("#dd-phases .deploy-phase-name")].map((e) => e.textContent).join() === "Awaiting approval,Queued,Deploying,Verifying,Live" || [...document.querySelectorAll("#dd-phases .deploy-phase-name")].map((e) => e.textContent).join() === "Awaiting approval,Queued,Deploying,Live"`)
	b.waitJS("the list still beside it", `document.querySelector("#deploy-list").getBoundingClientRect().right <= document.querySelector("#deploy-detail").getBoundingClientRect().left + 1`)
	b.waitJS("the address names it", `location.pathname.includes("/deploys/")`)
	b.waitJS("who approved it", `document.querySelector("#dd-facts").textContent.includes("Approved by")`)
	shot("deploy-pane-wide")
	if _, err := chromedp.Run(b.ctx, chromedp.Evaluate[bool](`document.activeElement?.blur(), true`)); err != nil {
		t.Fatal(err)
	}
	b.do("press Esc", chromedp.KeyEvent(kb.Escape))
	b.waitJS("the pane closed", `document.querySelector("#deploy-detail").hidden && !document.querySelector("#deploy-list li.selected")`)

	// Staging, checked by its verify command and a soak; then a newer
	// commit, and Roll back to the first.
	// A click that starts a deploy is retried only until the deploy is in
	// the list (each click queues one), never until it is live: checks and
	// a soak take longer than until waits between clicks.
	stagingDeploys := `[...document.querySelectorAll("#deploy-list li .deploy-env")].filter((e) => e.textContent === "staging").length`
	stagingLiveAt := func(commit string) string {
		return `document.querySelector('#env-cards [data-env="staging"] .env-live .status.d-live') && document.querySelector('#env-cards [data-env="staging"] .env-live code')?.textContent === "` + commit[:7] + `"`
	}
	stagingIdle := `!document.querySelector('#env-cards [data-env="staging"].busy')`
	first := strings.TrimSpace(run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "rev-parse", "main"))
	b.until("deploy staging", stagingDeploys+` === 1`, chromedp.Click(chromedp.CSS(`#env-cards [data-env="staging"] .env-deploy`)))
	b.waitJS("staging live at the first", stagingLiveAt(first)+` && `+stagingIdle)
	next := strings.TrimSpace(run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "commit-tree", "main^{tree}", "-p", "main", "-m", "second"))
	run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "update-ref", "refs/heads/main", next)
	b.until("deploy staging's next commit", stagingDeploys+` === 2`, chromedp.Click(chromedp.CSS(`#env-cards [data-env="staging"] .env-deploy`)))
	b.waitJS("staging live at the next", stagingLiveAt(next)+` && `+stagingIdle)
	b.waitJS("Roll back to the first", `document.querySelector('#env-cards [data-env="staging"] .env-rollback button')?.textContent.includes("Roll back to `+first[:7]+`")`)
	shot("deploys-rollback-wide")
	b.until("ask to roll back", `document.querySelector("#ask-sheet").open`, chromedp.Click(chromedp.CSS(`#env-cards [data-env="staging"] .env-rollback button`)))
	b.until("roll back", `!document.querySelector("#ask-sheet").open`, chromedp.Click(chromedp.ID("ask-ok")))
	b.waitJS("rolled back", stagingDeploys+` === 3 && document.querySelector("#deploy-list .status.d-rolled_back") && `+stagingLiveAt(first))
	b.until("open the rollback", `document.querySelector("#dd-title").textContent === "Rollback to staging" && !document.querySelector("#dd-checks-wrap").hidden`,
		chromedp.Click(chromedp.CSS(`#env-cards [data-env="staging"] .env-live`)))
	b.waitJS("its checks", `[...document.querySelectorAll("#dd-checks .deploy-check")].map((e) => e.textContent).join() === "PassedVerify command,PassedSoak"`)
	b.waitJS("its log says it rolled back", `document.querySelector("#dd-log").textContent.includes("staging rollback=1")`)
	b.waitJS("no rollback to the commit just rolled back", `!document.querySelector("#dd-actions").textContent.includes("Roll back") && !document.querySelector('#env-cards [data-env="staging"] .env-rollback')`)
	shot("deploy-rollback-pane-wide")
	b.until("close the pane", `document.querySelector("#deploy-detail").hidden`, chromedp.Click(chromedp.ID("dd-close")))

	// On a phone: the pane covers the page, with Back.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the cards one above the other", `(() => { const c = document.querySelectorAll("#env-cards .env-card"); return c.length === 2 && c[1].getBoundingClientRect().top >= c[0].getBoundingClientRect().bottom; })()`)
	shot("deploys-narrow")
	b.until("open the deploy on the phone", `!document.querySelector("#deploy-detail").hidden`,
		chromedp.Click(chromedp.CSS(`#deploy-list li .deploy-open`)))
	b.waitJS("full screen with Back", `document.querySelector("#deploy-detail").getBoundingClientRect().width >= 389 && document.querySelector("#dd-close").getAttribute("aria-label") === "Back"`)
	shot("deploy-pane-narrow")
	b.until("go back", `document.querySelector("#deploy-detail").hidden`, chromedp.Click(chromedp.ID("dd-close")))

	// Project settings → Deploy lists the environments.
	b.do("a desktop's screen", chromedp.EmulateViewport(1600, 1000))
	b.until("open the environments' settings", `document.querySelector("#settings-sheet").open && document.querySelectorAll("#settings-envs .env-edit").length === 2`,
		chromedp.Click(chromedp.ID("deploy-settings")))
	b.waitJS("the Deploy section shown", `!document.querySelector('#settings-sheet .rail-section[data-section="deploy"]').hidden`)
	b.waitJS("where each deploys", `document.querySelector("#settings-envs .env-hint").textContent.includes("Deploys on")`)
	b.waitJS("their checks", `[...document.querySelectorAll("#settings-envs .env-checks textarea")].map((e) => e.value).join() === ',test -n "$DASH_COMMIT"'`)
	b.waitJS("who may deploy", `[...document.querySelectorAll("#settings-envs .env-role-row select")].map((e) => e.value).join() === "admin,admin,admin,member,none,admin"`)
	b.waitJS("the settings history", `[...document.querySelectorAll("#settings-env-history .env-change")].length === 2 && document.querySelector("#settings-env-history").textContent.includes("added")`)
	shot("settings-deploy-wide")
	b.do("a phone's screen again", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the checks fit the phone", `[...document.querySelectorAll("#settings-envs .env-checks")].every((e) => e.scrollWidth <= e.clientWidth + 1 && e.getBoundingClientRect().right <= innerWidth)`)
	shot("settings-deploy-narrow")
}
