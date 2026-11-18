package notify

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/applink"
	"github.com/klahr/dash/internal/mail"
	"github.com/klahr/dash/internal/model"
)

// A user's daily or weekly summary (Account → Notifications) of what
// happened in their org's projects: new issues, tasks ready for review,
// failed or merged, goals waiting for them, builds starting to fail. The
// hub's scheduler (Summaries.Run) sends it at the user's time, in their
// time zone, by email and to the personal webhooks they chose, through
// the same delivery as every webhook event (signing, retries, private
// addresses, history). Nothing to report sends nothing.

// Summary is what a summary tells: the period it covers and what happened
// in it, by project.
type Summary struct {
	PeriodStart time.Time        `json:"period_start"`
	PeriodEnd   time.Time        `json:"period_end"`
	Projects    []SummaryProject `json:"projects"`
	// URL opens Dash.
	URL string `json:"url,omitempty"`
}

type SummaryProject struct {
	ID    string        `json:"id"`
	Name  string        `json:"name"`
	URL   string        `json:"url,omitempty"`
	Items []SummaryItem `json:"items"`
}

// SummaryItem is one thing that happened: kind is one of model.SummaryKinds.
type SummaryItem struct {
	Kind     string         `json:"kind"`
	Title    string         `json:"title"`
	URL      string         `json:"url,omitempty"`
	Severity model.Severity `json:"severity,omitempty"`
	At       time.Time      `json:"at"`
}

// summaryLooks: how each kind of item reads, alone and counted.
var summaryLooks = map[string]struct{ head, one, many, slack string }{
	model.SummaryIssue:  {"New issue", "new issue", "new issues", ":beetle:"},
	model.SummaryReview: {"Ready for review", "ready for review", "ready for review", ":eyes:"},
	model.SummaryFailed: {"Failed", "failed", "failed", ":x:"},
	model.SummaryMerged: {"Merged", "merged", "merged", ":white_check_mark:"},
	model.SummaryGoal:   {"Goal waits for you", "goal waits for you", "goals wait for you", ":dart:"},
	model.SummaryBuild:  {"Build failing", "build failing", "builds failing", ":rotating_light:"},
}

// Count is how many items the summary has.
func (s Summary) Count() int {
	n := 0
	for _, p := range s.Projects {
		n += len(p.Items)
	}
	return n
}

// CountText says how many of each kind, e.g. "3 new issues, 1 failed".
func (s Summary) CountText() string {
	counts := map[string]int{}
	for _, p := range s.Projects {
		for _, it := range p.Items {
			counts[it.Kind]++
		}
	}
	var parts []string
	for _, k := range model.SummaryKinds {
		c := counts[k]
		if c == 0 {
			continue
		}
		w := summaryLooks[k].one
		if c > 1 {
			w = summaryLooks[k].many
		}
		parts = append(parts, fmt.Sprintf("%d %s", c, w))
	}
	if len(parts) == 0 {
		return "nothing to report"
	}
	return strings.Join(parts, ", ")
}

// itemHead is how an item starts: its kind, and an issue's severity.
func itemHead(it SummaryItem) string {
	h := summaryLooks[it.Kind].head
	if it.Kind == model.SummaryIssue && it.Severity != "" {
		h += " (" + string(it.Severity) + ")"
	}
	return h
}

// Limits of the chat formats: Slack shows about 4,000 characters of a
// message before folding it, ntfy turns a message over 4,096 bytes into
// an attachment.
const (
	slackSummaryMax = 3500
	ntfySummaryMax  = 3500
	summaryMaxItems = 40
)

// summaryLines renders s as lines, a project's head before its items,
// within max bytes and summaryMaxItems items; what doesn't fit is counted
// in a last "and N more" line from more.
func summaryLines(s Summary, head func(SummaryProject) string, item func(SummaryItem) string, more func(int) string, max int) string {
	var b strings.Builder
	shown, total := 0, s.Count()
	full := false
	for _, p := range s.Projects {
		if full {
			break
		}
		ph := head(p)
		for i, it := range p.Items {
			line := item(it)
			add := len(line) + 1
			if i == 0 {
				add += len(ph) + 1
			}
			// Leave room for the "and N more" line.
			if shown >= summaryMaxItems || b.Len()+add+len(more(total))+1 > max {
				full = true
				break
			}
			if i == 0 {
				b.WriteString(ph + "\n")
			}
			b.WriteString(line + "\n")
			shown++
		}
	}
	if shown < total {
		b.WriteString(more(total - shown))
	}
	return strings.TrimRight(b.String(), "\n")
}

// slackSummary is one Slack message: a heading with the counts, then a
// short line per item, grouped by project.
func slackSummary(s Summary) string {
	head := fmt.Sprintf(":newspaper: *Dash summary* · %s", slackEscape(s.CountText()))
	link := func(u, text string) string {
		text = slackEscape(text)
		if u == "" {
			return text
		}
		return "<" + slackEscape(u) + "|" + text + ">"
	}
	body := summaryLines(s,
		func(p SummaryProject) string { return "*" + link(p.URL, p.Name) + "*" },
		func(it SummaryItem) string {
			return fmt.Sprintf("%s %s: %s", summaryLooks[it.Kind].slack, slackEscape(itemHead(it)), link(it.URL, oneLine(it.Title, 150)))
		},
		func(n int) string {
			if s.URL == "" {
				return fmt.Sprintf("_and %d more_", n)
			}
			return fmt.Sprintf("_and %d more:_ <%s|open Dash>", n, slackEscape(s.URL))
		}, slackSummaryMax-len(head)-1)
	return head + "\n" + body
}

// ntfySummary is an ntfy message's body: the items, grouped by project
// (the title has the counts).
func ntfySummary(s Summary) string {
	return summaryLines(s,
		func(p SummaryProject) string { return p.Name },
		func(it SummaryItem) string { return "• " + itemHead(it) + ": " + oneLine(it.Title, 150) },
		func(n int) string {
			if s.URL == "" {
				return fmt.Sprintf("…and %d more", n)
			}
			return fmt.Sprintf("…and %d more: %s", n, s.URL)
		}, ntfySummaryMax)
}

// SummaryStore is what the summaries read and write.
type SummaryStore interface {
	SummariesDue(now time.Time) ([]model.UserNotify, error)
	ClaimSummary(user, org int64, due, through, next time.Time) (bool, error)
	MarkSummarySent(user, org int64, at time.Time) error
	NotifySettings(user, org int64) (model.NotifySettings, model.NotifyState, error)
	TurnOffSummary(user, org int64, now time.Time) (bool, error)
	ListActivity(projects []string, from, to time.Time) ([]model.Activity, error)
	PruneActivity(before time.Time) error
	ListProjects() []model.Project
	GetWebhook(id string) (model.Webhook, error)
	// SummaryRecipient is user's email address and org's name, and
	// whether user is still a member of org.
	SummaryRecipient(user, org int64) (email, orgName string, member bool, err error)
}

// UnsubscribePath is where a summary email's unsubscribe link goes:
// UnsubscribePath+<token>. The signed token is the only credential, so
// the path is public (auth lets it through without a session).
const UnsubscribePath = "/api/notify/unsubscribe/"

// Summaries sends users' summaries. Set its fields, then Run it.
type Summaries struct {
	Store SummaryStore
	// Notifier delivers to webhooks, makes the links and tells the time.
	Notifier *Notifier
	// Mail sends summary emails; nil: email summaries are unavailable.
	Mail mail.Sender
	// Key signs unsubscribe links (NewSummaryKey; keep it: links in old
	// emails must go on working).
	Key []byte
	// Every is how often the scheduler looks for summaries due (a minute).
	Every time.Duration
	// MaxPeriod caps how far back a summary looks (after the hub was down
	// long, say). Default 30 days.
	MaxPeriod time.Duration

	pruned time.Time
}

// NewSummaryKey makes a key for Summaries.Key, base64url.
func NewSummaryKey() (string, error) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(k), nil
}

// ParseSummaryKey reads a key made by NewSummaryKey.
func ParseSummaryKey(s string) ([]byte, error) {
	k, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(k) < 16 {
		return nil, errors.New("summary key: not base64url, or too short")
	}
	return k, nil
}

// EmailOn reports whether summaries can go by email.
func (s *Summaries) EmailOn() bool { return s != nil && s.Mail != nil }

func (s *Summaries) now() time.Time { return s.Notifier.now() }

// Run sends the summaries due, every Every, until ctx ends.
func (s *Summaries) Run(ctx context.Context) {
	every := s.Every
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.RunDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunDue sends each summary whose time has come, and waits for them.
// Each covers from where the last one ended (so a restart neither repeats
// nor skips anything) up to now.
func (s *Summaries) RunDue(ctx context.Context) {
	now := s.now().UTC().Truncate(time.Second)
	if now.Sub(s.pruned) > 24*time.Hour {
		s.pruned = now
		if err := s.Store.PruneActivity(now.Add(-35 * 24 * time.Hour)); err != nil {
			slog.Error("summaries: prune activity", "err", err)
		}
	}
	due, err := s.Store.SummariesDue(now)
	if err != nil {
		slog.Error("summaries: list due", "err", err)
		return
	}
	var wg sync.WaitGroup
	for _, u := range due {
		from := s.periodStart(u, now)
		next := u.Settings.Next(now)
		ok, err := s.Store.ClaimSummary(u.UserID, u.OrgID, u.State.NextAt, now, next)
		if err != nil {
			slog.Error("summaries: claim", "user", u.UserID, "org", u.OrgID, "err", err)
			continue
		}
		if !ok {
			continue // another run took it, or the settings changed
		}
		wg.Add(1)
		go func(u model.UserNotify) {
			defer wg.Done()
			sum, _, member, err := s.build(u.UserID, u.OrgID, u.Settings, from, now)
			if err != nil {
				slog.Error("summaries: build", "user", u.UserID, "org", u.OrgID, "err", err)
				return
			}
			if !member || sum.Count() == 0 {
				return // nothing to report
			}
			s.send(ctx, u.UserID, u.OrgID, u.Settings, sum, false)
		}(u)
	}
	wg.Wait()
}

// periodStart is where u's next summary starts: where the last one ended,
// else one period ago; at most MaxPeriod ago.
func (s *Summaries) periodStart(u model.UserNotify, now time.Time) time.Time {
	from := u.State.Through
	if from.IsZero() {
		from = now.Add(-u.Settings.Period())
	}
	max := s.MaxPeriod
	if max <= 0 {
		max = 30 * 24 * time.Hour
	}
	if from.Before(now.Add(-max)) {
		from = now.Add(-max)
	}
	return from
}

// build collects what happened in (from, to] in the projects of org that
// user's settings cover, as they want it. It answers user's address too,
// and whether they are still a member of org (if not, the summary is
// empty).
func (s *Summaries) build(user, org int64, set model.NotifySettings, from, to time.Time) (Summary, string, bool, error) {
	sum := Summary{PeriodStart: from.UTC(), PeriodEnd: to.UTC(), Projects: []SummaryProject{}}
	email, _, member, err := s.Store.SummaryRecipient(user, org)
	if err != nil || !member {
		return sum, email, member, err
	}
	var projects []model.Project
	for _, p := range s.Store.ListProjects() {
		if p.OrgID != org || org == 0 {
			continue
		}
		if !set.AllProjects && !slices.Contains(set.Projects, p.ID) {
			continue
		}
		projects = append(projects, p)
	}
	if len(projects) > 0 {
		sum.URL = s.Notifier.origin(projects[0].ID)
	} else {
		sum.URL = s.Notifier.origin("")
	}
	if sum.URL != "" {
		sum.URL += "/"
	}
	ids := make([]string, len(projects))
	for i, p := range projects {
		ids[i] = p.ID
	}
	acts, err := s.Store.ListActivity(ids, from, to)
	if err != nil {
		return sum, email, member, err
	}
	// The latest of each thing, once: a task failed then ready for review
	// is two items, the same task ready for review twice is one.
	type key struct{ project, kind, ref string }
	latest := map[key]model.Activity{}
	inOrg := map[int64]bool{user: true} // task creators: still in org?
	for _, a := range acts {
		if !set.Include.Wants(a.Kind, a.Severity) || (a.User != 0 && a.User != user) {
			continue
		}
		if a.Creator > 0 && !set.WantsTask(user, a.Creator) {
			in, known := inOrg[a.Creator]
			if !known {
				_, _, member, err := s.Store.SummaryRecipient(a.Creator, org)
				if err != nil {
					return sum, email, true, err
				}
				in, inOrg[a.Creator] = member, member
			}
			if in {
				continue // someone else's task, and its creator answers for it
			}
		}
		latest[key{a.ProjectID, a.Kind, a.Ref}] = a
	}
	byProject := map[string][]SummaryItem{}
	for k, a := range latest {
		byProject[k.project] = append(byProject[k.project], SummaryItem{Kind: a.Kind, Title: a.Title, URL: a.URL, Severity: a.Severity, At: a.At})
	}
	sort.Slice(projects, func(i, j int) bool { return strings.ToLower(projects[i].Name) < strings.ToLower(projects[j].Name) })
	for _, p := range projects {
		items := byProject[p.ID]
		if len(items) == 0 {
			continue
		}
		sort.Slice(items, func(i, j int) bool {
			a, b := items[i], items[j]
			if ka, kb := slices.Index(model.SummaryKinds, a.Kind), slices.Index(model.SummaryKinds, b.Kind); ka != kb {
				return ka < kb
			}
			if a.Kind == model.SummaryIssue && a.Severity != b.Severity {
				return a.Severity.Rank() < b.Severity.Rank()
			}
			return a.At.Before(b.At)
		})
		pu := s.Notifier.origin(p.ID)
		if pu != "" {
			pu += applink.Project(p.ID)
		}
		sum.Projects = append(sum.Projects, SummaryProject{ID: p.ID, Name: p.Name, URL: pu, Items: items})
	}
	return sum, email, member, nil
}

// DestResult is how sending a summary to one destination went.
type DestResult struct {
	// Destination is "email" or a webhook's id.
	Destination string `json:"destination"`
	Name        string `json:"name"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
}

// send delivers sum to each of the settings' destinations at once: one
// failing doesn't hold up or stop the others. A webhook's delivery shows
// in its history. test sends to paused webhooks too, once each.
func (s *Summaries) send(ctx context.Context, user, org int64, set model.NotifySettings, sum Summary, test bool) []DestResult {
	n := s.Notifier
	n.init()
	var mu sync.Mutex
	var out []DestResult
	add := func(r DestResult) {
		mu.Lock()
		out = append(out, r)
		mu.Unlock()
	}
	attempts := n.attempts()
	if test {
		attempts = 1
	}
	var wg sync.WaitGroup
	if set.Email {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := DestResult{Destination: "email", Name: "Email"}
			if err := s.sendEmail(ctx, user, org, set, sum, attempts); err != nil {
				r.Error = err.Error()
				slog.Warn("summary email failed", "user", user, "org", org, "err", err)
			} else {
				r.OK = true
			}
			add(r)
		}()
	}
	note := Notification{ID: newEventID(), Type: model.NotifySummary, Time: n.now().UTC().Truncate(time.Second), Summary: &sum,
		Message: "Summary: " + sum.CountText()}
	for _, id := range set.Webhooks {
		w, err := s.Store.GetWebhook(id)
		if err != nil || !w.Personal() || w.UserID != user || w.OrgID != org {
			continue // deleted meanwhile
		}
		if !w.Enabled && !test {
			continue
		}
		wg.Add(1)
		go func(w model.Webhook) {
			defer wg.Done()
			r := DestResult{Destination: w.ID, Name: w.Name, OK: true}
			if r.Name == "" {
				if u, err := url.Parse(w.URL); err == nil {
					r.Name = u.Host
				}
			}
			if err := n.deliver(ctx, w, note, attempts); err != nil {
				r.OK, r.Error = false, err.Error()
			}
			add(r)
		}(w)
	}
	wg.Wait()
	for _, r := range out {
		if r.OK {
			if err := s.Store.MarkSummarySent(user, org, n.now()); err != nil {
				slog.Error("summaries: mark sent", "user", user, "org", org, "err", err)
			}
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Destination == "email" && out[j].Destination != "email" })
	return out
}

// ErrNoDestination: a summary with nowhere to go.
var ErrNoDestination = errors.New("choose where the summary goes first: email or one of your webhooks")

// Test sends user's summary in org now, of what happened since the last
// one (or over the last period), to the destinations they chose, even if
// there is nothing to report; the schedule doesn't change.
func (s *Summaries) Test(ctx context.Context, user, org int64) ([]DestResult, error) {
	set, st, err := s.Store.NotifySettings(user, org)
	if err != nil {
		return nil, err
	}
	if !set.Email && len(set.Webhooks) == 0 {
		return nil, ErrNoDestination
	}
	now := s.now().UTC().Truncate(time.Second)
	from := s.periodStart(model.UserNotify{Settings: set, State: st}, now)
	sum, _, member, err := s.build(user, org, set, from, now)
	if err != nil {
		return nil, err
	}
	if !member {
		return nil, errors.New("you aren't a member of this organization")
	}
	return s.send(ctx, user, org, set, sum, true), nil
}

// sendEmail mails sum to user, with an unsubscribe link.
func (s *Summaries) sendEmail(ctx context.Context, user, org int64, set model.NotifySettings, sum Summary, attempts int) error {
	if s.Mail == nil {
		return errors.New("email is off on this Dash (no SMTP)")
	}
	to, orgName, _, err := s.Store.SummaryRecipient(user, org)
	if err != nil {
		return err
	}
	if to == "" {
		return errors.New("your account has no email address")
	}
	unsub := ""
	if base := strings.TrimSuffix(sum.URL, "/"); base != "" {
		unsub = base + UnsubscribePath + s.UnsubscribeToken(user, org)
	}
	m := mail.Message{To: to, Subject: emailSubject(sum, orgName), Body: EmailBody(sum, orgName, set.Location(), unsub)}
	if unsub != "" {
		m.Headers = map[string]string{"List-Unsubscribe": "<" + unsub + ">", "List-Unsubscribe-Post": "List-Unsubscribe=One-Click"}
	}
	return s.Notifier.retry(ctx, attempts, func() error { return s.Mail.Send(ctx, m) })
}

func emailSubject(sum Summary, orgName string) string {
	subj := "Dash summary"
	if orgName != "" {
		subj += " for " + orgName
	}
	return subj + ": " + sum.CountText()
}

// periodText is the summary's period as the user's clock reads it.
func periodText(sum Summary, loc *time.Location) string {
	a, b := sum.PeriodStart.In(loc), sum.PeriodEnd.In(loc)
	return fmt.Sprintf("%s – %s (%s)", a.Format("Mon 2 Jan 15:04"), b.Format("Mon 2 Jan 15:04"), loc.String())
}

// EmailBody is a summary email: plain text, grouped by project, with a
// link to each item and, at the end, to change or stop these emails.
func EmailBody(sum Summary, orgName string, loc *time.Location, unsubscribe string) string {
	var b strings.Builder
	who := "your projects"
	if orgName != "" {
		who = orgName
	}
	fmt.Fprintf(&b, "What happened in %s, %s:\n\n", who, periodText(sum, loc))
	if sum.Count() == 0 {
		b.WriteString("Nothing to report.\n")
	} else {
		fmt.Fprintf(&b, "%s.\n", upperFirst(sum.CountText()))
	}
	for _, p := range sum.Projects {
		fmt.Fprintf(&b, "\n%s\n%s\n", p.Name, strings.Repeat("=", min(len([]rune(p.Name)), 60)))
		for _, it := range p.Items {
			fmt.Fprintf(&b, "\n- %s: %s\n", itemHead(it), oneLine(it.Title, 200))
			if it.URL != "" {
				fmt.Fprintf(&b, "  %s\n", it.URL)
			}
		}
	}
	b.WriteString("\n-- \n")
	if sum.URL != "" {
		fmt.Fprintf(&b, "Open Dash: %s\n", sum.URL)
		b.WriteString("Change what this summary covers, or when it comes: Account → Notifications in Dash.\n")
	}
	if unsubscribe != "" {
		fmt.Fprintf(&b, "Stop these summaries: %s\n", unsubscribe)
	}
	return b.String()
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ---- unsubscribe links ----

// ErrUnsubscribeToken covers unknown and tampered unsubscribe tokens.
var ErrUnsubscribeToken = errors.New("this link isn't valid")

func (s *Summaries) sig(msg string) []byte {
	m := hmac.New(sha256.New, s.Key)
	m.Write([]byte("unsubscribe." + msg))
	return m.Sum(nil)
}

// UnsubscribeToken lets its bearer turn user's summary in org off. It
// doesn't expire: it is in emails people keep.
func (s *Summaries) UnsubscribeToken(user, org int64) string {
	msg := strconv.FormatInt(user, 10) + "." + strconv.FormatInt(org, 10)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(msg)) + "." + enc.EncodeToString(s.sig(msg))
}

// ParseUnsubscribe checks a token and returns whose summary it turns off.
func (s *Summaries) ParseUnsubscribe(tok string) (user, org int64, err error) {
	if len(s.Key) == 0 {
		return 0, 0, ErrUnsubscribeToken
	}
	enc := base64.RawURLEncoding
	m, sg, ok := strings.Cut(tok, ".")
	if !ok {
		return 0, 0, ErrUnsubscribeToken
	}
	msg, err1 := enc.DecodeString(m)
	sig, err2 := enc.DecodeString(sg)
	if err1 != nil || err2 != nil || !hmac.Equal(sig, s.sig(string(msg))) {
		return 0, 0, ErrUnsubscribeToken
	}
	us, os, ok := strings.Cut(string(msg), ".")
	if !ok {
		return 0, 0, ErrUnsubscribeToken
	}
	user, err1 = strconv.ParseInt(us, 10, 64)
	org, err2 = strconv.ParseInt(os, 10, 64)
	if err1 != nil || err2 != nil || user <= 0 || org <= 0 {
		return 0, 0, ErrUnsubscribeToken
	}
	return user, org, nil
}

// ServeHTTP handles UnsubscribePath{token}, without signing in. GET shows
// a page with a button (so a mail scanner opening the link changes
// nothing); POST, from that button or a mail app's one-click unsubscribe
// (RFC 8058), turns the summary off.
func (s *Summaries) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.URL.Path, UnsubscribePath)
	user, org, err := s.ParseUnsubscribe(tok)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		unsubscribePage(w, "Link not valid", "This unsubscribe link isn't valid. Turn the summary off in Dash: Account → Notifications.", "")
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		unsubscribePage(w, "Stop the summary?", "Dash stops sending you its summary of this organization's projects, by email and to your webhooks. You can turn it on again in Account → Notifications.",
			`<form method="post"><button type="submit">Stop the summary</button></form>`)
	case http.MethodPost:
		if _, err := s.Store.TurnOffSummary(user, org, s.now()); err != nil {
			slog.Error("summaries: unsubscribe", "user", user, "org", org, "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			unsubscribePage(w, "Something went wrong", "The summary is still on. Try again, or turn it off in Dash: Account → Notifications.", "")
			return
		}
		unsubscribePage(w, "Summary off", "Dash won't send you its summary any more. You can turn it on again in Account → Notifications.", "")
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func unsubscribePage(w http.ResponseWriter, title, text, form string) {
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex">
<title>%s</title><style>body{font:16px/1.5 system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1.5rem;color:#222}button{font:inherit;padding:.5rem 1rem;border-radius:.4rem;border:1px solid #888;background:#fff;cursor:pointer}@media(prefers-color-scheme:dark){body{background:#151515;color:#ddd}button{background:#2a2a2a;color:#ddd}}</style></head>
<body><h1>%s</h1><p>%s</p>%s</body></html>`, html.EscapeString(title), html.EscapeString(title), html.EscapeString(text), form)
}
