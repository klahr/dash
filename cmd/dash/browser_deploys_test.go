package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/klahr/dash/internal/model"
)

// putReleaseBuild saves the build of a release tag of project pid in the
// running hub's database, as its CI watcher does when it follows one on
// GitHub (these tests have no GitHub).
func (h *runnerHub) putReleaseBuild(t *testing.T, pid, tag, sha, state string, runs ...model.CIRun) {
	t.Helper()
	if runs == nil {
		runs = []model.CIRun{}
	}
	now := time.Now()
	data, err := json.Marshal(model.ReleaseBuild{Tag: tag, SHA: sha, State: state, Runs: runs, Pushed: now, Updated: now})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(h.hubData, "dash.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO release_builds (project_id, tag, state, pushed, data) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (project_id, tag) DO UPDATE SET state = excluded.state, pushed = excluded.pushed, data = excluded.data`,
		pid, tag, state, now.UnixMilli(), string(data)); err != nil {
		t.Fatal(err)
	}
}

// The Deploys page against a real hub and runner: a card per environment,
// Deploy on one runs its script on the runner and goes live; the deploy
// opens in a pane beside the list with its log, and Esc closes it. Deploy
// picks one of the project's release tags, the newest chosen. Once a
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

	// Deploy ships release tags: tag what origin has.
	first := strings.TrimSpace(run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "rev-parse", "main"))
	run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "tag", "1.0.0", first)

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

	b.until("open History", `document.querySelector("#view-history.active")`,
		chromedp.Click(chromedp.CSS(`.nav [data-view=history]`)))
	b.until("its Deploys tab", `document.querySelector("#view-deploys.active")`,
		chromedp.Click(chromedp.CSS(`#view-history .page-tabs [data-tab=deploys]`)))
	b.waitJS("a card per environment", `[...document.querySelectorAll("#env-cards .env-card .env-name")].map((e) => e.textContent).join() === "production,staging"`)
	b.waitJS("nothing deployed yet", `document.querySelector('#env-cards [data-env="production"]').textContent.includes("Nothing deployed here yet")`)

	// Deploy production: it needs an admin's approval (the owner is the
	// org's only admin, so their own will do); approved, it runs on the
	// runner and goes live.
	b.waitJS("production needs an approval", `document.querySelector('#env-cards [data-env="production"]').textContent.includes("Deploys need approval from an admin")`)
	// pickTag deploys tag to env with Deploy's picker; the newest tag is
	// the one chosen.
	pickTag := func(env, tag string) {
		t.Helper()
		b.until("open "+env+"'s Deploy", `document.querySelector("dialog.sheet[open] .tag-pick input[name=tag]")`,
			chromedp.Click(chromedp.CSS(`#env-cards [data-env="`+env+`"] .env-deploy`)))
		b.waitJS(tag+" the newest, chosen", `document.querySelector('dialog.sheet[open] .tag-pick input[name=tag]:checked')?.value === "`+tag+`"`)
		b.until("deploy "+tag+" to "+env, `!document.querySelector("dialog.sheet[open] .tag-pick")`,
			chromedp.Click(chromedp.CSS(`dialog.sheet[open] footer button[value=ok]`)))
	}
	pickTag("production", "1.0.0")
	b.waitJS("approve on the card", `document.querySelector('#env-cards [data-env="production"] .env-awaiting button.primary')`)
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
	// The picker's Deploy is clicked only until the picker closes (each
	// click queues one), never until it is live: checks and a soak take
	// longer than until waits between clicks.
	stagingDeploys := `[...document.querySelectorAll("#deploy-list li .deploy-env")].filter((e) => e.textContent === "staging").length`
	stagingLiveAt := func(tag, commit string) string {
		return `document.querySelector('#env-cards [data-env="staging"] .env-live .status.d-live') && document.querySelector('#env-cards [data-env="staging"] .env-live code')?.textContent === "` + tag + ` (` + commit[:7] + `)"`
	}
	stagingIdle := `!document.querySelector('#env-cards [data-env="staging"].busy')`
	pickTag("staging", "1.0.0")
	b.waitJS("staging deployed once", stagingDeploys+` === 1`)
	b.waitJS("staging live at the first", stagingLiveAt("1.0.0", first)+` && `+stagingIdle)
	next := strings.TrimSpace(run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "commit-tree", "main^{tree}", "-p", "main", "-m", "second"))
	run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "update-ref", "refs/heads/main", next)
	run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "tag", "1.1.0", next)
	pickTag("staging", "1.1.0")
	b.waitJS("staging deployed twice", stagingDeploys+` === 2`)
	b.waitJS("staging live at the next", stagingLiveAt("1.1.0", next)+` && `+stagingIdle)
	b.waitJS("Roll back to the first", `document.querySelector('#env-cards [data-env="staging"] .env-rollback button')?.textContent.includes("Roll back to `+first[:7]+`")`)
	shot("deploys-rollback-wide")
	b.until("ask to roll back", `document.querySelector("#ask-sheet").open`, chromedp.Click(chromedp.CSS(`#env-cards [data-env="staging"] .env-rollback button`)))
	b.until("roll back", `!document.querySelector("#ask-sheet").open`, chromedp.Click(chromedp.ID("ask-ok")))
	b.waitJS("rolled back", stagingDeploys+` === 3 && document.querySelector("#deploy-list .status.d-rolled_back") && `+stagingLiveAt("1.0.0", first))
	b.until("open the rollback", `document.querySelector("#dd-title").textContent === "Rollback to staging" && !document.querySelector("#dd-checks-wrap").hidden`,
		chromedp.Click(chromedp.CSS(`#env-cards [data-env="staging"] .env-live`)))
	b.waitJS("its checks", `[...document.querySelectorAll("#dd-checks .deploy-check")].map((e) => e.textContent).join() === "PassedVerify command,PassedSoak"`)
	b.waitJS("its log says it rolled back", `document.querySelector("#dd-log").textContent.includes("staging rollback=1")`)
	b.waitJS("no rollback to the commit just rolled back", `!document.querySelector("#dd-actions").textContent.includes("Roll back") && !document.querySelector('#env-cards [data-env="staging"] .env-rollback')`)
	shot("deploy-rollback-pane-wide")
	b.until("close the pane", `document.querySelector("#deploy-detail").hidden`, chromedp.Click(chromedp.ID("dd-close")))
	// History → Commits: the commit is marked live in both, and its pane lists
	// its deploys, each opening on History → Deploys.
	b.until("History → Commits", `document.querySelector("#view-history.active")`,
		chromedp.Click(chromedp.CSS(`#view-deploys .page-tabs [data-tab=history]`)))
	b.waitJS("live in both", `[...document.querySelectorAll('#graph li[data-hash="`+first+`"] .graph-ref.env')].map((e) => e.textContent).join() === "Live in production,Live in staging"`)
	b.until("open the commit", `document.querySelector("#cd-facts .commit-deploys")`,
		chromedp.Click(chromedp.CSS(`#graph li[data-hash="`+first+`"] .graph-commit`)))
	b.waitJS("its deploys", `document.querySelectorAll("#cd-facts .commit-deploys button").length === 3`)
	b.until("open one on History → Deploys", `document.querySelector("#view-deploys.active") && !document.querySelector("#deploy-detail").hidden && location.pathname.includes("/deploys/")`,
		chromedp.Click(chromedp.CSS(`#cd-facts .commit-deploys button`)))
	b.until("close the pane", `document.querySelector("#deploy-detail").hidden`, chromedp.Click(chromedp.ID("dd-close")))

	// Deploy's picker shows each tag's release build, kept up to date while
	// one builds, with a link to its runs; deploying a tag whose build
	// failed asks first, and nothing deploys when you don't confirm.
	h.putReleaseBuild(t, pid, "1.1.0", next, "building")
	build := func(tag string) string {
		return `document.querySelector('dialog.sheet[open] .tag-pick-row[data-tag="` + tag + `"] .tag-build')`
	}
	b.until("open staging's Deploy with the builds", build("1.1.0"),
		chromedp.Click(chromedp.CSS(`#env-cards [data-env="staging"] .env-deploy`)))
	b.waitJS("1.1.0 building, 1.0.0 without a build", build("1.1.0")+`.dataset.build === "building" && `+build("1.0.0")+`.dataset.build === "unknown"`)
	shot("deploy-builds-wide")
	h.putReleaseBuild(t, pid, "1.1.0", next, "failed", model.CIRun{ID: 9, Workflow: "Release", Status: "completed", Conclusion: "failure",
		SHA: next, Branch: "1.1.0", URL: "https://example.com/runs/9"})
	b.waitJS("it turns failed while the picker is open, linked to its run", build("1.1.0")+`?.dataset.build === "failed" && `+build("1.1.0")+`.href === "https://example.com/runs/9"`)
	b.do("pick 1.1.0", chromedp.Click(chromedp.CSS(`dialog.sheet[open] .tag-pick-row[data-tag="1.1.0"] input`)))
	b.do("deploy it", chromedp.Click(chromedp.CSS(`dialog.sheet[open] footer button[value=ok]`)))
	b.waitJS("it asks first", `document.querySelector("#ask-sheet").open && document.querySelector("#ask-title").textContent.includes("release build of 1.1.0 failed")`)
	shot("deploy-build-failed-ask-wide")
	b.until("don't deploy", `!document.querySelector("#ask-sheet").open`, chromedp.Click(chromedp.CSS(`#ask-sheet header button[value=cancel]`)))
	time.Sleep(500 * time.Millisecond)
	b.waitJS("nothing deployed", stagingDeploys+` === 3`)

	// A commit after the newest tag: the picker offers a new release from
	// origin/main first, tags it and deploys it in one go.
	third := strings.TrimSpace(run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "commit-tree", "main^{tree}", "-p", "main", "-m", "third"))
	run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "update-ref", "refs/heads/main", third)
	b.until("open staging's Deploy", `document.querySelector('dialog.sheet[open] .tag-pick input[name=tag][value="@new"]')`,
		chromedp.Click(chromedp.CSS(`#env-cards [data-env="staging"] .env-deploy`)))
	b.waitJS("a new release, first and chosen", `document.querySelector('dialog.sheet[open] .tag-pick input[name=tag]:checked')?.value === "@new" && document.querySelector("dialog.sheet[open] .tag-pick-release").textContent.includes("1 commit since 1.1.0")`)
	b.waitJS("one button: tag and deploy", `document.querySelector("dialog.sheet[open] footer button[value=ok]").textContent === "Tag 1.1.1 and deploy"`)
	b.waitJS("the existing tags below it", `document.querySelectorAll("dialog.sheet[open] .tag-pick-list input[name=tag]").length === 2`)
	shot("deploy-new-release-wide")
	b.until("tag and deploy", `!document.querySelector("dialog.sheet[open] .tag-pick")`,
		chromedp.Click(chromedp.CSS(`dialog.sheet[open] footer button[value=ok]`)))
	b.waitJS("staging deploys the new tag", stagingDeploys+` === 4`)
	if got := strings.TrimSpace(run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "rev-parse", "1.1.1^{commit}")); got != third {
		t.Fatalf("origin's 1.1.1 is %s, want %s", got, third)
	}
	b.waitJS("staging live at the new tag", stagingLiveAt("1.1.1", third)+` && `+stagingIdle)

	// On a phone: the pane covers the page, with Back.
	b.do("a phone's screen", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the cards one above the other", `(() => { const c = document.querySelectorAll("#env-cards .env-card"); return c.length === 2 && c[1].getBoundingClientRect().top >= c[0].getBoundingClientRect().bottom; })()`)
	shot("deploys-narrow")
	b.until("open the deploy on the phone", `!document.querySelector("#deploy-detail").hidden`,
		chromedp.Click(chromedp.CSS(`#deploy-list li .deploy-open`)))
	b.waitJS("full screen with Back", `document.querySelector("#deploy-detail").getBoundingClientRect().width >= 389 && document.querySelector("#dd-close").getAttribute("aria-label") === "Back"`)
	shot("deploy-pane-narrow")
	b.until("go back", `document.querySelector("#deploy-detail").hidden`, chromedp.Click(chromedp.ID("dd-close")))
	// The picker's new release fits the phone.
	fourth := strings.TrimSpace(run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "commit-tree", "main^{tree}", "-p", "main", "-m", "fourth"))
	run(t, "", h.gitEnv, "git", "--git-dir", h.origin, "update-ref", "refs/heads/main", fourth)
	b.until("open staging's Deploy on the phone", `document.querySelector('dialog.sheet[open] .tag-pick input[name=tag][value="@new"]:checked')`,
		chromedp.Click(chromedp.CSS(`#env-cards [data-env="staging"] .env-deploy`)))
	b.waitJS("it fits the phone", `(() => { const d = document.querySelector("dialog.sheet[open]"); return d.getBoundingClientRect().right <= innerWidth + 1 && d.querySelector("form").scrollWidth <= d.clientWidth + 1; })()`)
	shot("deploy-new-release-narrow")
	b.until("Esc closes the picker", `!document.querySelector("dialog.sheet[open] .tag-pick")`, chromedp.KeyEvent(kb.Escape))

	// Project settings → Deploy lists the environments.
	b.do("a desktop's screen", chromedp.EmulateViewport(1600, 1000))
	b.until("open the environments' settings", `document.querySelector("#settings-sheet").open && document.querySelectorAll("#settings-envs .env-edit").length === 2`,
		chromedp.Click(chromedp.ID("deploy-settings")))
	b.waitJS("the Deploy section shown", `document.querySelector('#settings-sheet .rail-section[data-section="deploy"]').classList.contains("current")`)
	b.waitJS("where each deploys", `document.querySelector("#settings-envs .env-hint").textContent.includes("Deploys on")`)
	b.waitJS("their checks", `[...document.querySelectorAll("#settings-envs .env-checks textarea")].map((e) => e.value).join() === ',test -n "$DASH_COMMIT"'`)
	b.waitJS("who may deploy", `[...document.querySelectorAll("#settings-envs .env-role-row select")].map((e) => e.value).join() === "admin,admin,admin,member,none,admin"`)
	b.waitJS("the settings history", `[...document.querySelectorAll("#settings-env-history .env-change")].length === 2 && document.querySelector("#settings-env-history").textContent.includes("added")`)
	shot("settings-deploy-wide")
	b.do("a phone's screen again", chromedp.EmulateViewport(390, 844, chromedp.EmulatePortrait, chromedp.EmulateMobile, chromedp.EmulateTouch))
	b.waitJS("the checks fit the phone", `[...document.querySelectorAll("#settings-envs .env-checks")].every((e) => e.scrollWidth <= e.clientWidth + 1 && e.getBoundingClientRect().right <= innerWidth)`)
	shot("settings-deploy-narrow")
}
