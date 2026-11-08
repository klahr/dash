package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var prURLRe = regexp.MustCompile(`https://\S+/pull/(\d+)`)

// FindPR returns head's open pull request into base, if there is one.
func (g *Git) FindPR(ctx context.Context, projectID, dir, base, head string) (number int, url string, ok bool, err error) {
	out, err := g.ghQuiet(ctx, projectID, dir, "pr", "list", "--head", head, "--base", base, "--state", "open", "--json", "number,url")
	if err != nil {
		return 0, "", false, err
	}
	var found []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	if json.Unmarshal([]byte(jsonPart(out)), &found) != nil || len(found) == 0 {
		return 0, "", false, nil
	}
	return found[0].Number, found[0].URL, true, nil
}

// OpenPR returns the open pull request for head, creating one if there is none.
func (g *Git) OpenPR(ctx context.Context, projectID, dir, base, head, title, body string) (number int, url string, err error) {
	out, err := g.gh(ctx, projectID, dir, "pr", "list", "--head", head, "--state", "open", "--json", "number,url")
	if err != nil {
		return 0, "", err
	}
	var existing []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	if json.Unmarshal([]byte(jsonPart(out)), &existing) == nil && len(existing) > 0 {
		return existing[0].Number, existing[0].URL, nil
	}
	out, err = g.gh(ctx, projectID, dir, "pr", "create", "--base", base, "--head", head, "--title", title, "--body", body)
	if err != nil {
		return 0, "", err
	}
	m := prURLRe.FindStringSubmatch(out)
	if m == nil {
		return 0, "", fmt.Errorf("gh pr create: no pull request URL in output: %s", strings.TrimSpace(out))
	}
	n, _ := strconv.Atoi(m[1])
	return n, m[0], nil
}

// MergePR squash-merges pull request number on GitHub, provided its head is
// still head (so commits pushed meanwhile aren't merged unseen).
func (g *Git) MergePR(ctx context.Context, projectID, dir string, number int, head string) error {
	_, err := g.gh(ctx, projectID, dir, "pr", "merge", strconv.Itoa(number), "--squash", "--match-head-commit", head)
	return err
}

// PRStatus is what Dash needs to know about a pull request.
type PRStatus struct {
	State       string `json:"state"`     // OPEN | MERGED | CLOSED
	Mergeable   string `json:"mergeable"` // MERGEABLE | CONFLICTING | UNKNOWN
	HeadRefOid  string `json:"headRefOid"`
	MergeCommit *struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
	Checks []PRCheck `json:"statusCheckRollup"`
}

// PRCheck is a check run (GitHub Actions etc.) or a commit status.
type PRCheck struct {
	Name       string `json:"name"`       // check run
	Context    string `json:"context"`    // commit status
	Status     string `json:"status"`     // check run: QUEUED | IN_PROGRESS | COMPLETED
	Conclusion string `json:"conclusion"` // check run: SUCCESS | FAILURE | …
	State      string `json:"state"`      // commit status: SUCCESS | FAILURE | ERROR | PENDING
	DetailsURL string `json:"detailsUrl"`
	TargetURL  string `json:"targetUrl"`
}

func (c PRCheck) Label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Context
}

func (c PRCheck) URL() string {
	if c.DetailsURL != "" {
		return c.DetailsURL
	}
	return c.TargetURL
}

// Failed reports a finished check that didn't pass.
func (c PRCheck) Failed() bool {
	switch c.Conclusion {
	case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED":
		return true
	}
	return c.State == "FAILURE" || c.State == "ERROR"
}

// Pending reports a check that hasn't finished.
func (c PRCheck) Pending() bool {
	if c.Name != "" {
		return c.Status != "COMPLETED"
	}
	return c.State == "PENDING" || c.State == "EXPECTED"
}

func (g *Git) PRStatus(ctx context.Context, projectID, dir string, number int) (PRStatus, error) {
	var st PRStatus
	out, err := g.ghQuiet(ctx, projectID, dir, "pr", "view", strconv.Itoa(number),
		"--json", "state,mergeable,headRefOid,mergeCommit,statusCheckRollup")
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal([]byte(jsonPart(out)), &st); err != nil {
		return st, fmt.Errorf("gh pr view: %w", err)
	}
	return st, nil
}

// RequiredChecks returns the names of the checks the base branch requires
// before pull request number can merge. It is empty when none are required
// or gh can't tell (no branch protection, no permission, an old gh).
func (g *Git) RequiredChecks(ctx context.Context, projectID, dir string, number int) map[string]bool {
	out, err := g.ghQuiet(ctx, projectID, dir, "pr", "checks", strconv.Itoa(number), "--required", "--json", "name")
	if err != nil {
		return nil // also "no required checks reported", which exits non-zero
	}
	var checks []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal([]byte(jsonPart(out)), &checks) != nil {
		return nil
	}
	req := make(map[string]bool, len(checks))
	for _, c := range checks {
		req[c.Name] = true
	}
	return req
}

// notCodeRe matches checks that are about the contributor, not the code:
// a contributor licence agreement or a Developer Certificate of Origin.
var notCodeRe = regexp.MustCompile(`(?i)(^|[^a-z])(cla|dco)([^a-z]|$)|license/cla|contributor license|signed[- ]off`)

// NotAboutCode reports a check no change to the code can fix, such as an
// unsigned CLA: sending it to Claude as "fix the code" would only waste a
// fix pass.
func NotAboutCode(c PRCheck) bool {
	return notCodeRe.MatchString(c.Label())
}

var runIDRe = regexp.MustCompile(`/actions/runs/(\d+)`)

// FailedLog returns the tail of a failed GitHub Actions run's log, or ""
// when the check isn't an Actions run.
func (g *Git) FailedLog(ctx context.Context, projectID, dir string, c PRCheck, lines int) string {
	m := runIDRe.FindStringSubmatch(c.URL())
	if m == nil {
		return ""
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	return g.RunFailedLog(ctx, projectID, dir, id, lines)
}

// jsonPart skips anything gh printed before its JSON (e.g. update notices).
func jsonPart(s string) string {
	if i := strings.IndexAny(s, "[{"); i > 0 {
		return s[i:]
	}
	return s
}

// Run is a GitHub Actions workflow run.
type Run struct {
	ID         int64     `json:"id"`
	Workflow   string    `json:"workflow"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`     // queued | in_progress | completed | …
	Conclusion string    `json:"conclusion"` // success | failure | cancelled | skipped | …
	SHA        string    `json:"sha"`
	Branch     string    `json:"branch,omitempty"` // the branch or tag it ran for
	URL        string    `json:"url"`
	Event      string    `json:"event"`
	CreatedAt  time.Time `json:"created_at"`
}

func (r Run) Done() bool { return r.Status == "completed" }

// Failed reports a finished run that didn't pass (cancelled runs don't count).
func (r Run) Failed() bool {
	switch r.Conclusion {
	case "failure", "timed_out", "startup_failure":
		return true
	}
	return false
}

// Runs lists the newest workflow runs on branch (or of a tag), newest first.
func (g *Git) Runs(ctx context.Context, projectID, dir, branch string, limit int) ([]Run, error) {
	return g.runList(ctx, projectID, dir, []string{"--branch", branch}, limit)
}

// RerunRun starts a finished workflow run again: only its failed jobs when
// failedOnly, else all of them.
func (g *Git) RerunRun(ctx context.Context, projectID, dir string, runID int64, failedOnly bool) error {
	args := []string{"run", "rerun", strconv.FormatInt(runID, 10)}
	if failedOnly {
		args = append(args, "--failed")
	}
	_, err := g.gh(ctx, projectID, dir, args...)
	return err
}

// RunFailedLog returns the tail of the failed steps' log of a run.
func (g *Git) RunFailedLog(ctx context.Context, projectID, dir string, runID int64, lines int) string {
	out, err := g.gh(ctx, projectID, dir, "run", "view", strconv.FormatInt(runID, 10), "--log-failed")
	if err != nil {
		return ""
	}
	all := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}
