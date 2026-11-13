package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	return g.openPR(ctx, projectID, dir, base, head, title, body, false)
}

// OpenDraftPR is OpenPR, creating the pull request as a draft. Where GitHub
// has no draft pull requests (a private repository on a free plan), it is
// created ready for review instead.
func (g *Git) OpenDraftPR(ctx context.Context, projectID, dir, base, head, title, body string) (number int, url string, err error) {
	return g.openPR(ctx, projectID, dir, base, head, title, body, true)
}

func (g *Git) openPR(ctx context.Context, projectID, dir, base, head, title, body string, draft bool) (number int, url string, err error) {
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
	args := []string{"pr", "create", "--base", base, "--head", head, "--title", title, "--body", body}
	if draft {
		out, err = g.gh(ctx, projectID, dir, append(args, "--draft")...)
		var ce *CmdError
		if errors.As(err, &ce) && strings.Contains(strings.ToLower(ce.Output), "draft") {
			out, err = g.gh(ctx, projectID, dir, args...)
		}
	} else {
		out, err = g.gh(ctx, projectID, dir, args...)
	}
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

// ReadyPR marks draft pull request number ready for review.
func (g *Git) ReadyPR(ctx context.Context, projectID, dir string, number int) error {
	_, err := g.gh(ctx, projectID, dir, "pr", "ready", strconv.Itoa(number))
	return err
}

// ClosePR closes pull request number without merging it, saying why in a
// comment (optional).
func (g *Git) ClosePR(ctx context.Context, projectID, dir string, number int, comment string) error {
	args := []string{"pr", "close", strconv.Itoa(number)}
	if comment != "" {
		args = append(args, "--comment", comment)
	}
	_, err := g.gh(ctx, projectID, dir, args...)
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
	Checks  []PRCheck `json:"statusCheckRollup"`
	IsDraft bool      `json:"isDraft"`
	// ReviewDecision is GitHub's verdict when the base branch requires
	// reviews: APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED, else "".
	ReviewDecision string `json:"reviewDecision"`
}

// PRReview is a review submitted on a pull request (GitHub's REST shape).
type PRReview struct {
	ID          int64     `json:"id"`
	User        GHUser    `json:"user"`
	Body        string    `json:"body"`
	State       string    `json:"state"` // APPROVED | CHANGES_REQUESTED | COMMENTED | DISMISSED | PENDING
	SubmittedAt time.Time `json:"submitted_at"`
	CommitID    string    `json:"commit_id"`
	HTMLURL     string    `json:"html_url"`
	Association string    `json:"author_association"`
}

// PRComment is a review comment on a line of a pull request's diff.
type PRComment struct {
	ID       int64  `json:"id"`
	ReviewID int64  `json:"pull_request_review_id"`
	User     GHUser `json:"user"`
	Body     string `json:"body"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	// OriginalLine is where it was made, for a comment on code changed since.
	OriginalLine int    `json:"original_line"`
	InReplyTo    int64  `json:"in_reply_to_id"`
	HTMLURL      string `json:"html_url"`
	Association  string `json:"author_association"`
}

// IssueComment is a comment in a pull request's conversation tab (GitHub's
// issue comments), not on a line of its diff.
type IssueComment struct {
	ID          int64  `json:"id"`
	User        GHUser `json:"user"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	Association string `json:"author_association"`
}

// Trusted reports an author_association of someone who can push to the
// repository: its owner, a member of the organization owning it, or a
// collaborator. Anyone else on GitHub can comment on a public repository's
// pull requests, so what they write must never reach Claude, which runs
// with the shell allowed.
func Trusted(association string) bool {
	switch association {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	}
	return false
}

// GHUser is a GitHub account as the REST API gives it.
type GHUser struct {
	Login string `json:"login"`
	Type  string `json:"type"` // User | Bot | Organization
}

// Bot reports an app's account (GitHub Actions, Dependabot, a CI or CLA bot).
func (u GHUser) Bot() bool { return u.Type == "Bot" || strings.HasSuffix(u.Login, "[bot]") }

// PRReviews returns the reviews submitted on pull request number, oldest first.
func (g *Git) PRReviews(ctx context.Context, projectID, dir string, number int) ([]PRReview, error) {
	var out []PRReview
	return out, g.ghPages(ctx, projectID, dir, fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/reviews", number), &out)
}

// PRComments returns the review comments on the lines of pull request
// number, oldest first.
func (g *Git) PRComments(ctx context.Context, projectID, dir string, number int) ([]PRComment, error) {
	var out []PRComment
	return out, g.ghPages(ctx, projectID, dir, fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/comments", number), &out)
}

// PRIssueComments returns the comments in the conversation tab of pull
// request number, oldest first.
func (g *Git) PRIssueComments(ctx context.Context, projectID, dir string, number int) ([]IssueComment, error) {
	var out []IssueComment
	return out, g.ghPages(ctx, projectID, dir, fmt.Sprintf("repos/{owner}/{repo}/issues/%d/comments", number), &out)
}

// CommentPR posts body as a comment in pull request number's conversation.
func (g *Git) CommentPR(ctx context.Context, projectID, dir string, number int, body string) error {
	_, err := g.gh(ctx, projectID, dir, "pr", "comment", strconv.Itoa(number), "--body", body)
	return err
}

// ReplyToReviewComment replies to the thread of line comment commentID (the
// thread's first comment: GitHub doesn't take replies to replies) on pull
// request number.
func (g *Git) ReplyToReviewComment(ctx context.Context, projectID, dir string, number int, commentID int64, body string) error {
	_, err := g.gh(ctx, projectID, dir, "api", "-X", "POST",
		fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/comments/%d/replies", number, commentID), "-f", "body="+body)
	return err
}

// RequestReview asks logins to review pull request number (again).
func (g *Git) RequestReview(ctx context.Context, projectID, dir string, number int, logins []string) error {
	if len(logins) == 0 {
		return nil
	}
	args := []string{"api", "-X", "POST", fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/requested_reviewers", number)}
	for _, l := range logins {
		args = append(args, "-f", "reviewers[]="+l)
	}
	_, err := g.gh(ctx, projectID, dir, args...)
	return err
}

// ReviewThread is a thread of line comments on a pull request.
type ReviewThread struct {
	ID       string // GraphQL node ID, for ResolveReviewThread
	Resolved bool
}

const reviewThreadsQuery = `query($owner:String!,$repo:String!,$n:Int!,$endCursor:String){repository(owner:$owner,name:$repo){pullRequest(number:$n){` +
	`reviewThreads(first:100,after:$endCursor){pageInfo{hasNextPage endCursor}nodes{id isResolved comments(first:1){nodes{databaseId}}}}}}}`

// ReviewThreads returns pull request number's review threads by the REST ID
// of each thread's first comment.
func (g *Git) ReviewThreads(ctx context.Context, projectID, dir string, number int) (map[int64]ReviewThread, error) {
	out, err := g.ghQuiet(ctx, projectID, dir, "api", "graphql", "--paginate",
		"-F", "owner={owner}", "-F", "repo={repo}", "-F", "n="+strconv.Itoa(number), "-f", "query="+reviewThreadsQuery)
	if err != nil {
		return nil, err
	}
	threads := map[int64]ReviewThread{}
	dec := json.NewDecoder(strings.NewReader(jsonPart(out))) // one object per page
	for {
		var page struct {
			Data struct {
				Repository struct {
					PullRequest struct {
						ReviewThreads struct {
							Nodes []struct {
								ID         string `json:"id"`
								IsResolved bool   `json:"isResolved"`
								Comments   struct {
									Nodes []struct {
										DatabaseID int64 `json:"databaseId"`
									} `json:"nodes"`
								} `json:"comments"`
							} `json:"nodes"`
						} `json:"reviewThreads"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := dec.Decode(&page); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("gh api graphql: %w", err)
		}
		for _, n := range page.Data.Repository.PullRequest.ReviewThreads.Nodes {
			if len(n.Comments.Nodes) > 0 {
				threads[n.Comments.Nodes[0].DatabaseID] = ReviewThread{ID: n.ID, Resolved: n.IsResolved}
			}
		}
	}
	return threads, nil
}

// ResolveReviewThread marks a review thread resolved.
func (g *Git) ResolveReviewThread(ctx context.Context, projectID, dir, threadID string) error {
	_, err := g.gh(ctx, projectID, dir, "api", "graphql",
		"-f", "query=mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{isResolved}}}", "-f", "id="+threadID)
	return err
}

// ghPages GETs every page of a REST list into out (a pointer to a slice).
// gh fills in {owner}/{repo} from dir's remote and prints one JSON array per
// page.
func (g *Git) ghPages(ctx context.Context, projectID, dir, path string, out any) error {
	raw, err := g.ghQuiet(ctx, projectID, dir, "api", "--paginate", path+"?per_page=100")
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(jsonPart(raw)))
	var all []json.RawMessage
	for {
		var page []json.RawMessage
		if err := dec.Decode(&page); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("gh api %s: %w", path, err)
		}
		all = append(all, page...)
	}
	b, _ := json.Marshal(all)
	return json.Unmarshal(b, out)
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
		"--json", "state,mergeable,headRefOid,mergeCommit,statusCheckRollup,isDraft,reviewDecision")
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

// CheckEvents returns, by check name, the event that triggered each check of
// pull request number, e.g. pull_request or pull_request_target ("" for
// commit statuses). It is nil when gh can't tell.
func (g *Git) CheckEvents(ctx context.Context, projectID, dir string, number int) map[string]string {
	// With --json gh exits 0 whatever the checks' state.
	out, err := g.ghQuiet(ctx, projectID, dir, "pr", "checks", strconv.Itoa(number), "--json", "name,event")
	if err != nil {
		return nil
	}
	var checks []struct {
		Name  string `json:"name"`
		Event string `json:"event"`
	}
	if json.Unmarshal([]byte(jsonPart(out)), &checks) != nil {
		return nil
	}
	events := make(map[string]string, len(checks))
	for _, c := range checks {
		// Of same-named checks, one the branch triggers wins.
		if e, seen := events[c.Name]; !seen || e == "pull_request_target" {
			events[c.Name] = c.Event
		}
	}
	return events
}

// RunsFromBase reports a check whose workflow runs from the base branch
// (pull_request_target), so no change to the pull request's branch affects it.
func RunsFromBase(event string) bool {
	return event == "pull_request_target"
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
