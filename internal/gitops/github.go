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

// Runs lists the newest workflow runs on branch, newest first.
func (g *Git) Runs(ctx context.Context, projectID, dir, branch string, limit int) ([]Run, error) {
	out, err := g.ghQuiet(ctx, projectID, dir, "run", "list", "--branch", branch, "--limit", strconv.Itoa(limit),
		"--json", "databaseId,workflowName,displayTitle,status,conclusion,headSha,url,event,createdAt")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID         int64     `json:"databaseId"`
		Workflow   string    `json:"workflowName"`
		Title      string    `json:"displayTitle"`
		Status     string    `json:"status"`
		Conclusion string    `json:"conclusion"`
		SHA        string    `json:"headSha"`
		URL        string    `json:"url"`
		Event      string    `json:"event"`
		CreatedAt  time.Time `json:"createdAt"`
	}
	if err := json.Unmarshal([]byte(jsonPart(out)), &raw); err != nil {
		return nil, fmt.Errorf("gh run list: %w", err)
	}
	runs := make([]Run, len(raw))
	for i, r := range raw {
		runs[i] = Run(r)
	}
	return runs, nil
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
