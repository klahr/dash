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
)

// Notification is one event to tell webhooks about, before it is shaped
// for a format.
type Notification struct {
	ID         string            `json:"id"`
	Type       model.NotifyEvent `json:"type"`
	Time       time.Time         `json:"timestamp"`
	Project    ProjectRef        `json:"project"`
	Task       *TaskRef          `json:"task,omitempty"`
	Permission *PermissionRef    `json:"permission,omitempty"`
	Build      *BuildRef         `json:"build,omitempty"`
	// Message is a one-line human summary (the error of a failed task, …).
	Message string `json:"message,omitempty"`
}

type ProjectRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
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
	model.NotifyBuild:      {"Build failing", ":rotating_light:", "rotating_light", false},
	model.NotifyTest:       {"Test notification", ":wave:", "wave", false},
}

// Head is the event's short heading, e.g. "Ready for review".
func (n Notification) Head() string {
	h := looks[n.Type].head
	if n.Type == model.NotifyBuild && n.Build != nil && n.Build.Branch != "" {
		h += " on " + n.Build.Branch
	}
	return h
}

// URL is where the notification links to: the task, else the build.
func (n Notification) URL() string {
	if n.Task != nil && n.Task.URL != "" {
		return n.Task.URL
	}
	if n.Build != nil {
		return n.Build.URL
	}
	return ""
}

// subject is what the notification is about: the task's title, else the
// project's name.
func (n Notification) subject() string {
	if n.Task != nil && n.Task.Title != "" {
		return n.Task.Title
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
		body, _ = json.Marshal(map[string]string{"text": slackText(n)})
		hdr.Set("Content-Type", "application/json")
	case model.WebhookNtfy:
		body = []byte(ntfyMessage(n))
		hdr.Set("Content-Type", "text/plain; charset=utf-8")
		hdr.Set("Title", headerText(n.Head()+" · "+n.Project.Name))
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
	if n.Task != nil && n.Project.Name != "" {
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

// oneLine is s's first line, cut to max runes.
func oneLine(s string, max int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}
