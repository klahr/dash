package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/textutil"
)

// Notification is one event to tell webhooks about, before it is shaped
// for a format.
type Notification struct {
	ID   string            `json:"id"`
	Type model.NotifyEvent `json:"type"`
	Time time.Time         `json:"timestamp"`
	// Project is what the event is about; none for a summary.
	Project    ProjectRef     `json:"project,omitzero"`
	Task       *TaskRef       `json:"task,omitempty"`
	Permission *PermissionRef `json:"permission,omitempty"`
	Build      *BuildRef      `json:"build,omitempty"`
	// Runner is the runner a runner.disk event is about.
	Runner *RunnerRef `json:"runner,omitempty"`
	// Goal is the goal a goal.attention event is about.
	Goal *GoalRef `json:"goal,omitempty"`
	// Issue is the issue an issue.new event is about.
	Issue *IssueRef `json:"issue,omitempty"`
	// Deploy is the deploy a deploy.failed event is about.
	Deploy *DeployRef `json:"deploy,omitempty"`
	// Card is the Planning card a card.activity event is about.
	Card *CardRef `json:"card,omitempty"`
	// Summary is a summary event's content.
	Summary *Summary `json:"summary,omitempty"`
	// Message is a one-line human summary (the error of a failed task, …).
	Message string `json:"message,omitempty"`

	// creator is who created Task (its CreatedBy: 0 for Dash, or
	// model.DeletedUser). Task events reach them, and the users who chose
	// to hear of every task (model.NotifySettings.Tasks); it isn't sent.
	creator int64
	// byPlanner: creator is a planner, who can't open tasks. The task's
	// news goes to owners instead, the people on the Planning cards that
	// link to it who can (none: to everyone, as a task nobody is behind).
	byPlanner bool
	owners    []int64
	// head, if set, is the heading (a card.activity's: what happened).
	head string
}

// CardRef is a Planning card.
type CardRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url,omitempty"` // the card in Dash
}

type ProjectRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Icon and Color are how Dash shows the project (model.Project's):
	// pushed notifications show them; webhooks don't get them.
	Icon  string `json:"-"`
	Color string `json:"-"`
}

// projectRef is p as a notification names it.
func projectRef(p model.Project) ProjectRef {
	return ProjectRef{ID: p.ID, Name: p.Name, Icon: p.Icon, Color: p.Color}
}

type TaskRef struct {
	ID     string           `json:"id"`
	Title  string           `json:"title"`
	Status model.TodoStatus `json:"status"`
	URL    string           `json:"url,omitempty"` // the task in Dash
}

type PermissionRef struct {
	ID   string `json:"id"`
	Tool string `json:"tool"`
}

// Question reports whether the request is Claude asking the user a
// question (perm.AskTool), which is answered in Dash, not approved.
func (p *PermissionRef) Question() bool { return p != nil && p.Tool == perm.AskTool }

type GoalRef struct {
	ID     string           `json:"id"`
	Title  string           `json:"title"`
	Status model.GoalStatus `json:"status"`
	// Blocked says why the goal's coordinator can't run (model.Goal.Blocked).
	Blocked string `json:"blocked,omitempty"`
	URL     string `json:"url,omitempty"` // the goal in Dash
}

type IssueRef struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Severity model.Severity `json:"severity"`
	URL      string         `json:"url,omitempty"` // the draft task made for the issue, in Dash
}

type DeployRef struct {
	ID          string `json:"id"`
	Environment string `json:"environment"`
	Commit      string `json:"commit,omitempty"`
	Rollback    bool   `json:"rollback,omitempty"`
	URL         string `json:"url,omitempty"` // the deploy in Dash
}

type RunnerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type BuildRef struct {
	Branch string `json:"branch"`
	SHA    string `json:"sha,omitempty"`
	URL    string `json:"url,omitempty"` // the failed run on GitHub
}

// jsonPayload is the generic JSON body: the notification with its creation
// time in Unix seconds too, like Stripe's events.
type jsonPayload struct {
	Notification
	Created int64 `json:"created"`
}

// What each event looks like in chat apps and on phones.
var looks = map[model.NotifyEvent]struct {
	head, slack, ntfyTag string
	high                 bool
}{
	model.NotifyReview:     {"Ready for review", ":eyes:", "eyes", false},
	model.NotifyFailed:     {"Failed", ":x:", "x", true},
	model.NotifyMerged:     {"Merged", ":white_check_mark:", "white_check_mark", false},
	model.NotifyPermission: {"Claude needs your approval", ":raising_hand:", "raising_hand", true},
	model.NotifyGoal:       {"Goal", ":dart:", "dart", false},
	model.NotifyBuild:      {"Build failing", ":rotating_light:", "rotating_light", false},
	model.NotifyBehind:     {"Checkout behind origin", ":arrow_down:", "arrow_down", false},
	model.NotifyDisk:       {"Disk nearly full", ":floppy_disk:", "floppy_disk", true},
	model.NotifySpend:      {"Spending at 80% of its cap", ":money_with_wings:", "money_with_wings", false},
	model.NotifyIssue:      {"New issue", ":beetle:", "beetle", false},
	model.NotifyDeploy:     {"Deploy failed", ":boom:", "boom", true},
	model.NotifyCard:       {"Planning", ":memo:", "memo", false},
	model.NotifySummary:    {"Summary", ":newspaper:", "newspaper", false},
	model.NotifyTest:       {"Test notification", ":wave:", "wave", false},
}

// ClosedNoChanges is the message of a task.merged notification for a task
// closed without changes: nothing was merged.
const ClosedNoChanges = "No changes: nothing was merged"

// AutoApproved is the message of a task.merged notification for a task
// the project's auto-approve rules approved, with no review by a person.
const AutoApproved = "Approved automatically: it met the project's auto-approve rules"

// Head is the event's short heading, e.g. "Ready for review".
func (n Notification) Head() string {
	if n.head != "" {
		return n.head
	}
	h := looks[n.Type].head
	if n.Type == model.NotifyPermission && n.Permission.Question() {
		h = "Claude has a question"
	}
	if n.Type == model.NotifyMerged && n.Message == ClosedNoChanges {
		h = "Closed"
	}
	if n.Type == model.NotifyGoal && n.Goal != nil {
		h = goalHeads[n.Goal.Status]
		if n.Goal.Blocked != "" {
			h = "Goal can't run"
		}
	}
	if n.Type == model.NotifyIssue && n.Issue != nil && n.Issue.Severity != "" {
		h += " (" + string(n.Issue.Severity) + ")"
	}
	if n.Type == model.NotifyDeploy && n.Deploy != nil && n.Deploy.Rollback {
		h = "Rollback failed"
	}
	if n.Type == model.NotifyBuild && n.Build != nil && n.Build.Branch != "" {
		h += " on " + n.Build.Branch
	}
	return h
}

// goalHeads head a goal.attention notification by the goal's status.
var goalHeads = map[model.GoalStatus]string{
	model.GoalWaiting:     "Goal waits for you",
	model.GoalDone:        "Goal done",
	model.GoalOutOfBudget: "Goal out of budget",
}

// URL is where the notification links to: the task, else the goal, else the build.
func (n Notification) URL() string {
	if n.Task != nil && n.Task.URL != "" {
		return n.Task.URL
	}
	if n.Goal != nil && n.Goal.URL != "" {
		return n.Goal.URL
	}
	if n.Issue != nil && n.Issue.URL != "" {
		return n.Issue.URL
	}
	if n.Deploy != nil && n.Deploy.URL != "" {
		return n.Deploy.URL
	}
	if n.Card != nil && n.Card.URL != "" {
		return n.Card.URL
	}
	if n.Summary != nil && n.Summary.URL != "" {
		return n.Summary.URL
	}
	if n.Build != nil {
		return n.Build.URL
	}
	return ""
}

// subject is what the notification is about: the task's title, else the
// project's name.
func (n Notification) subject() string {
	if n.Card != nil && n.Card.Title != "" {
		return n.Card.Title
	}
	if n.Task != nil && n.Task.Title != "" {
		return n.Task.Title
	}
	if n.Runner != nil {
		return "runner " + n.Runner.Name
	}
	if n.Goal != nil && n.Goal.Title != "" {
		return n.Goal.Title
	}
	if n.Issue != nil && n.Issue.Title != "" {
		return n.Issue.Title
	}
	if n.Deploy != nil {
		s := n.Project.Name + " to " + n.Deploy.Environment
		if c := n.Deploy.Commit; c != "" {
			s += " (" + c[:min(len(c), 8)] + ")"
		}
		return s
	}
	return n.Project.Name
}

// request builds the HTTP request delivering n to w, signed (for generic
// JSON) as of now. actions, if set, adds ntfy buttons.
func request(ctx context.Context, w model.Webhook, n Notification, now time.Time, actions []ntfyAction) (*http.Request, error) {
	var body []byte
	hdr := http.Header{}
	switch w.Format {
	case model.WebhookJSON:
		var err error
		if body, err = json.Marshal(jsonPayload{Notification: n, Created: n.Time.Unix()}); err != nil {
			return nil, err
		}
		hdr.Set("Content-Type", "application/json")
		hdr.Set("Dash-Event", string(n.Type))
		hdr.Set("Dash-Delivery", n.ID)
		hdr.Set(SignatureHeaderName, Sign(body, w.Secret, now))
	case model.WebhookSlack:
		text := slackText(n)
		if n.Summary != nil {
			text = slackSummary(*n.Summary)
		}
		body, _ = json.Marshal(map[string]string{"text": text})
		hdr.Set("Content-Type", "application/json")
	case model.WebhookNtfy:
		body = []byte(ntfyMessage(n))
		hdr.Set("Content-Type", "text/plain; charset=utf-8")
		title := n.Head() + " · " + n.Project.Name
		if n.Summary != nil {
			body = []byte(ntfySummary(*n.Summary))
			title = "Dash summary: " + n.Summary.CountText()
		}
		hdr.Set("Title", headerText(title))
		if looks[n.Type].high {
			hdr.Set("Priority", "high")
		} else {
			hdr.Set("Priority", "default")
		}
		if tag := looks[n.Type].ntfyTag; tag != "" {
			hdr.Set("Tags", tag)
		}
		if u := n.URL(); u != "" {
			hdr.Set("Click", u)
		}
		if len(actions) > 0 {
			hdr.Set("Actions", ntfyActions(actions))
		}
	default:
		return nil, fmt.Errorf("unknown webhook format %q", w.Format)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", "Dash-Webhook/1")
	if w.Token != "" {
		req.Header.Set("Authorization", "Bearer "+w.Token)
	}
	return req, nil
}

// slackText is a short mrkdwn line: emoji, heading, the linked task and
// its project, then a detail line if there is one.
func slackText(n Notification) string {
	var b strings.Builder
	l := looks[n.Type]
	fmt.Fprintf(&b, "%s *%s* · ", l.slack, slackEscape(n.Head()))
	subj := slackEscape(oneLine(n.subject(), 150))
	if u := n.URL(); u != "" {
		fmt.Fprintf(&b, "<%s|%s>", slackEscape(u), subj)
	} else {
		b.WriteString(subj)
	}
	if (n.Task != nil || n.Goal != nil || n.Issue != nil) && n.Project.Name != "" {
		fmt.Fprintf(&b, " in _%s_", slackEscape(n.Project.Name))
	}
	if n.Message != "" {
		fmt.Fprintf(&b, "\n>%s", slackEscape(oneLine(n.Message, 300)))
	}
	return b.String()
}

// slackEscape escapes the three characters Slack's mrkdwn treats as markup.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func ntfyMessage(n Notification) string {
	msg := oneLine(n.subject(), 200)
	if n.Message != "" {
		msg += "\n" + oneLine(n.Message, 500)
	}
	return msg
}

// headerText makes s safe for a header: one line, RFC 2047-encoded when
// it isn't plain ASCII (ntfy decodes it).
func headerText(s string) string {
	return mime.QEncoding.Encode("utf-8", oneLine(s, 200))
}

// ntfyAction is a button on an ntfy notification that makes an HTTP request.
type ntfyAction struct {
	Label, URL string
}

func ntfyActions(as []ntfyAction) string {
	parts := make([]string, len(as))
	for i, a := range as {
		parts[i] = fmt.Sprintf("http, %s, %s, method=POST, clear=true", a.Label, a.URL)
	}
	return strings.Join(parts, "; ")
}

// oneLine is s's first line, clipped to max characters.
func oneLine(s string, max int) string { return textutil.OneLine(textutil.FirstLine(s), max) }
