package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/mail"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// ---- summaries, against a real store ----

type pubFunc func(model.Event)

func (f pubFunc) Publish(ev model.Event) { f(ev) }

type fakeMail struct {
	mu   sync.Mutex
	sent []mail.Message
	err  error
}

func (m *fakeMail) Send(_ context.Context, msg mail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, msg)
	return nil
}

func (m *fakeMail) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

// clock is a settable time for the notifier and the summaries.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

type sumEnv struct {
	st         *store.Store
	n          *Notifier
	sum        *Summaries
	mail       *fakeMail
	clock      *clock
	user, org  int64
	admin      int64
	shop, docs model.Project
	other      model.Project // another org's
}

// summaryEnv is a hub with two projects in the default org, one in
// another org, and a member (ana@example.com) whose summaries are tested.
func summaryEnv(t *testing.T) *sumEnv {
	t.Helper()
	e := &sumEnv{mail: &fakeMail{}, clock: &clock{now: time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC)}}
	e.n = &Notifier{AllowPrivate: true, Backoff: time.Millisecond, Timeout: 2 * time.Second, Attempts: 2,
		Origin: func(string) string { return "https://dash.example.com" }, Now: e.clock.Now}
	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), pubFunc(func(ev model.Event) { e.n.Observe(ev) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.st, e.n.Store = st, st
	acc := st.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-password"); err != nil {
		t.Fatal(err)
	}
	admin, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	e.admin, e.org = admin.ID, def.ID
	tok, _, err := acc.Invite(admin.ID, def.ID, "ana@example.com", store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	ana, _, err := acc.RegisterWithInvitation(tok, "ana", "long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	e.user = ana.ID
	other, _ := acc.CreateOrg(admin.ID, "Other")
	e.shop, _ = st.CreateProject(model.Project{Name: "Shop", Path: t.TempDir(), OrgID: def.ID})
	e.docs, _ = st.CreateProject(model.Project{Name: "Docs", Path: t.TempDir(), OrgID: def.ID})
	e.other, _ = st.CreateProject(model.Project{Name: "Secret", Path: t.TempDir(), OrgID: other.ID})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.n.Start(ctx)
	key, _ := NewSummaryKey()
	k, _ := ParseSummaryKey(key)
	e.sum = &Summaries{Store: st, Notifier: e.n, Mail: e.mail, Key: k}
	return e
}

// hook makes one of ana's personal webhooks to r.
func (e *sumEnv) hook(t *testing.T, r *receiver, format string, events ...model.NotifyEvent) model.Webhook {
	t.Helper()
	w, err := e.st.ForOrg(e.org).AsUser(e.user).CreateWebhook(model.Webhook{URL: r.URL, Format: format, Enabled: true, Events: events})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// daily turns ana's daily summary on at 08:00 UTC, by email and to hooks.
func (e *sumEnv) daily(t *testing.T, email bool, hooks ...model.Webhook) model.NotifySettings {
	t.Helper()
	set := model.DefaultNotifySettings()
	set.Summary, set.Time, set.TimeZone, set.Email = model.SummaryDaily, "08:00", "UTC", email
	for _, w := range hooks {
		set.Webhooks = append(set.Webhooks, w.ID)
	}
	set, _, err := e.st.SaveNotifySettings(e.user, e.org, set, e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func (e *sumEnv) activity(t *testing.T, a model.Activity) {
	t.Helper()
	if a.At.IsZero() {
		a.At = e.clock.Now()
	}
	if a.URL == "" {
		a.URL = "https://dash.example.com/?project=" + a.ProjectID + "&task=" + a.Ref
	}
	if err := e.st.RecordActivity(a); err != nil {
		t.Fatal(err)
	}
}

func (e *sumEnv) runAt(t time.Time) {
	e.clock.set(t)
	e.sum.RunDue(context.Background())
}

func summaryOf(t *testing.T, body string) Summary {
	t.Helper()
	var p struct {
		Type    string  `json:"type"`
		Summary Summary `json:"summary"`
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil || p.Type != "summary" {
		t.Fatalf("not a summary: %s (%v)", body, err)
	}
	return p.Summary
}

// The summary goes out at its time, once, even across a restart, and not
// at all when there is nothing to report.
func TestSummaryScheduleOnceAndNothingToReport(t *testing.T) {
	e := summaryEnv(t)
	r := newReceiver(t)
	w := e.hook(t, r, model.WebhookJSON) // no events ticked: a destination gets the summary anyway
	e.daily(t, true, w)
	e.activity(t, model.Activity{ProjectID: e.shop.ID, Kind: model.SummaryReview, Ref: "t1", Title: "Fix checkout"})

	day1 := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	e.runAt(day1.Add(-time.Second))
	if len(r.bodies) != 0 || e.mail.count() != 0 {
		t.Fatal("sent before its time")
	}
	e.runAt(day1.Add(20 * time.Second))
	if len(r.bodies) != 1 || e.mail.count() != 1 {
		t.Fatalf("at its time: %d webhook, %d email", len(r.bodies), e.mail.count())
	}
	sum := summaryOf(t, r.bodies[0])
	if len(sum.Projects) != 1 || sum.Projects[0].Name != "Shop" || sum.Projects[0].Items[0].Kind != "review" ||
		sum.Projects[0].Items[0].Title != "Fix checkout" || !sum.PeriodEnd.Equal(day1.Add(20*time.Second)) {
		t.Fatalf("summary %s", r.bodies[0])
	}
	if r.got[0].Header.Get("Dash-Event") != "summary" || r.got[0].Header.Get(SignatureHeaderName) == "" {
		t.Fatalf("headers %v", r.got[0].Header)
	}
	// Again, and after a restart (a new scheduler on the same database): nothing more.
	e.runAt(day1.Add(time.Minute))
	restarted := &Summaries{Store: e.st, Notifier: e.n, Mail: e.mail, Key: e.sum.Key}
	restarted.RunDue(context.Background())
	if len(r.bodies) != 1 || e.mail.count() != 1 {
		t.Fatalf("sent twice: %d webhook, %d email", len(r.bodies), e.mail.count())
	}
	_, st, _ := e.st.NotifySettings(e.user, e.org)
	if !st.LastSent.Equal(day1.Add(20*time.Second)) || !st.NextAt.Equal(day1.Add(24*time.Hour)) {
		t.Fatalf("state %+v", st)
	}

	// Day 2: nothing happened, nothing is sent; it still moves on.
	day2 := day1.Add(24 * time.Hour)
	e.runAt(day2)
	if len(r.bodies) != 1 || e.mail.count() != 1 {
		t.Fatal("sent with nothing to report")
	}
	// Day 3, after the hub was down over the time: what happened since
	// day 2's run, once, and nothing from before.
	e.clock.set(day2.Add(5 * time.Hour))
	e.activity(t, model.Activity{ProjectID: e.docs.ID, Kind: model.SummaryFailed, Ref: "t2", Title: "Docs build"})
	e.runAt(day2.Add(24*time.Hour + 3*time.Hour))
	restarted.RunDue(context.Background())
	if len(r.bodies) != 2 {
		t.Fatalf("day 3: %d deliveries", len(r.bodies))
	}
	sum = summaryOf(t, r.bodies[1])
	if len(sum.Projects) != 1 || sum.Projects[0].Name != "Docs" || len(sum.Projects[0].Items) != 1 || !sum.PeriodStart.Equal(day2) {
		t.Fatalf("day 3 summary %s", r.bodies[1])
	}
}

// A summary covers only what its user may see: projects of orgs they are
// still in, the ones they picked, their own goals, issues severe enough.
func TestSummaryPermissionsAndFilters(t *testing.T) {
	e := summaryEnv(t)
	set := e.daily(t, true)
	at := e.clock.Now().Add(-time.Minute)
	for _, a := range []model.Activity{
		{ProjectID: e.shop.ID, Kind: model.SummaryIssue, Ref: "i1", Title: "SQL injection", Severity: model.SevCritical},
		{ProjectID: e.shop.ID, Kind: model.SummaryIssue, Ref: "i2", Title: "Typo", Severity: model.SevLow},
		{ProjectID: e.shop.ID, Kind: model.SummaryGoal, Ref: "g1", Title: "Admin's goal", User: e.admin},
		{ProjectID: e.shop.ID, Kind: model.SummaryGoal, Ref: "g2", Title: "Ana's goal", User: e.user},
		{ProjectID: e.docs.ID, Kind: model.SummaryMerged, Ref: "t1", Title: "Docs merged"},
		{ProjectID: e.other.ID, Kind: model.SummaryFailed, Ref: "t9", Title: "Another org's task"},
		{ProjectID: "gone", Kind: model.SummaryFailed, Ref: "t8", Title: "A deleted project's task"},
	} {
		a.At = at
		e.activity(t, a)
	}
	from, to := at.Add(-time.Hour), e.clock.Now()
	sum, _, member, err := e.sum.build(e.user, e.org, set, from, to)
	if err != nil || !member {
		t.Fatal(member, err)
	}
	var titles []string
	for _, p := range sum.Projects {
		for _, it := range p.Items {
			titles = append(titles, p.Name+": "+it.Title)
		}
	}
	if got, want := strings.Join(titles, " | "), "Docs: Docs merged | Shop: SQL injection | Shop: Ana's goal"; got != want {
		t.Fatalf("items\n got %s\nwant %s", got, want)
	}
	// Picked projects only.
	set.AllProjects, set.Projects = false, []string{e.docs.ID, e.other.ID}
	sum, _, _, _ = e.sum.build(e.user, e.org, set, from, to)
	if len(sum.Projects) != 1 || sum.Projects[0].ID != e.docs.ID {
		t.Fatalf("picked %+v", sum.Projects)
	}
	// No longer a member: nothing.
	if err := e.st.Accounts().RemoveMember(e.admin, e.org, e.user); err != nil {
		t.Fatal(err)
	}
	set.AllProjects = true
	sum, _, member, _ = e.sum.build(e.user, e.org, set, from, to)
	if member || sum.Count() != 0 {
		t.Fatalf("after leaving: member %v, %d items", member, sum.Count())
	}
	e.runAt(time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC))
	if e.mail.count() != 0 {
		t.Fatal("mailed someone who left the org")
	}
}

// One destination failing doesn't keep the summary from the others; the
// failure shows in that webhook's history.
func TestSummaryOneDestinationFails(t *testing.T) {
	e := summaryEnv(t)
	bad := newReceiver(t, 400)
	good := newReceiver(t)
	slow := newReceiver(t, 503, 200) // a retry, then through
	wb := e.hook(t, bad, model.WebhookJSON)
	wg := e.hook(t, good, model.WebhookSlack)
	ws := e.hook(t, slow, model.WebhookNtfy)
	e.mail.err = fmt.Errorf("smtp: 554 rejected")
	e.daily(t, true, wb, wg, ws)
	e.activity(t, model.Activity{ProjectID: e.shop.ID, Kind: model.SummaryBuild, Ref: "abc", Title: "Build failing on main"})
	e.runAt(time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC))
	if len(good.bodies) != 1 || len(slow.bodies) != 2 || len(bad.bodies) != 1 {
		t.Fatalf("deliveries: good %d, slow %d, bad %d", len(good.bodies), len(slow.bodies), len(bad.bodies))
	}
	hist, _ := e.st.ListWebhookDeliveries(wb.ID)
	if len(hist) != 1 || hist[0].Event != model.NotifySummary || hist[0].Error == "" || hist[0].Status != 400 {
		t.Fatalf("failing webhook's history %+v", hist)
	}
	hist, _ = e.st.ListWebhookDeliveries(ws.ID)
	if len(hist) != 1 || hist[0].Error != "" || hist[0].Attempts != 2 || !strings.Contains(hist[0].Subject, "1 build failing") {
		t.Fatalf("retried webhook's history %+v", hist)
	}
	if _, st, _ := e.st.NotifySettings(e.user, e.org); st.LastSent.IsZero() {
		t.Fatal("not marked sent though some got it")
	}

	// The test button: the same, at once, and it says how each went.
	e.mail.err = nil
	res, err := e.sum.Test(context.Background(), e.user, e.org)
	if err != nil || len(res) != 4 || res[0].Destination != "email" || !res[0].OK {
		t.Fatalf("test %+v %v", res, err)
	}
	failed := 0
	for _, r := range res {
		if !r.OK {
			failed++
		}
	}
	if failed != 0 { // bad answers 200 now
		t.Fatalf("test results %+v", res)
	}
}

func TestSummaryEmailAndUnsubscribe(t *testing.T) {
	e := summaryEnv(t)
	e.clock.set(time.Date(2026, 11, 17, 6, 0, 0, 0, time.UTC))
	set := e.daily(t, true)
	set.TimeZone = "Europe/Stockholm"
	if _, _, err := e.st.SaveNotifySettings(e.user, e.org, set, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.activity(t, model.Activity{ProjectID: e.shop.ID, Kind: model.SummaryIssue, Ref: "i1", Title: "Leaked key", Severity: model.SevHigh,
		URL: "https://dash.example.com/?issue=i1&project=" + e.shop.ID})
	e.runAt(time.Date(2026, 11, 17, 7, 0, 30, 0, time.UTC)) // 08:00 in Stockholm
	if e.mail.count() != 1 {
		t.Fatal("no email")
	}
	m := e.mail.sent[0]
	if m.To != "ana@example.com" || !strings.Contains(m.Subject, "1 new issue") {
		t.Fatalf("email %q to %q", m.Subject, m.To)
	}
	for _, want := range []string{"Shop\n====", "- New issue (high): Leaked key\n  https://dash.example.com/?issue=i1", "(Europe/Stockholm)", "Stop these summaries: https://dash.example.com/api/notify/unsubscribe/"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, m.Body)
		}
	}
	lu := m.Headers["List-Unsubscribe"]
	if !strings.HasPrefix(lu, "<https://dash.example.com"+UnsubscribePath) || m.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Fatalf("headers %v", m.Headers)
	}
	raw, err := mail.Compose("Dash <dash@example.com>", m, e.clock.Now())
	if err != nil || !strings.Contains(string(raw), "\r\nList-Unsubscribe: <https://") {
		t.Fatalf("composed %v:\n%s", err, raw)
	}

	// The link works without signing in: GET asks, POST turns it off.
	path := strings.TrimPrefix(strings.Trim(lu, "<>"), "https://dash.example.com")
	do := func(method, p string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.sum.ServeHTTP(rec, httptest.NewRequest(method, p, nil))
		return rec
	}
	if rec := do("GET", path); rec.Code != 200 || !strings.Contains(rec.Body.String(), `<form method="post">`) {
		t.Fatalf("GET %d %s", rec.Code, rec.Body)
	}
	if s, _, _ := e.st.NotifySettings(e.user, e.org); !s.On() {
		t.Fatal("GET turned it off")
	}
	if rec := do("POST", path+"x"); rec.Code != http.StatusForbidden {
		t.Fatalf("tampered: %d", rec.Code)
	}
	other := &Summaries{Key: []byte("another key, another hub........")}
	if _, _, err := other.ParseUnsubscribe(strings.TrimPrefix(path, UnsubscribePath)); err == nil {
		t.Fatal("another key's token accepted")
	}
	if rec := do("POST", path); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Summary off") {
		t.Fatalf("POST %d %s", rec.Code, rec.Body)
	}
	if s, st, _ := e.st.NotifySettings(e.user, e.org); s.On() || !st.NextAt.IsZero() {
		t.Fatal("still on")
	}
}

// bigSummary has n items over two projects.
func bigSummary(n int) Summary {
	s := Summary{PeriodStart: t0.Add(-24 * time.Hour), PeriodEnd: t0, URL: "https://dash.example.com/"}
	for p := range 2 {
		sp := SummaryProject{ID: fmt.Sprint("p", p), Name: fmt.Sprint("Project ", p), URL: "https://dash.example.com/?project=p"}
		for i := range n / 2 {
			sp.Items = append(sp.Items, SummaryItem{Kind: model.SummaryIssue, Severity: model.SevHigh, At: t0,
				Title: fmt.Sprintf("Issue %d with a rather long title that goes on <and> on for a while", i),
				URL:   fmt.Sprintf("https://dash.example.com/?project=p%d&issue=i%d", p, i)})
		}
		s.Projects = append(s.Projects, sp)
	}
	return s
}

func TestSummaryFormats(t *testing.T) {
	note := func(s Summary) Notification {
		return Notification{ID: "evt_s", Type: model.NotifySummary, Time: t0, Summary: &s, Message: "Summary: " + s.CountText()}
	}
	small := bigSummary(2)
	small.Projects[1].Items[0].Kind, small.Projects[1].Items[0].Severity = model.SummaryReview, ""

	// JSON: the summary, signed, without a project.
	req, err := request(context.Background(), model.Webhook{URL: "https://example.com/h", Format: model.WebhookJSON, Secret: "whsec_x"}, note(small), t0, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	sum, _ := got["summary"].(map[string]any)
	if got["type"] != "summary" || got["project"] != nil || sum == nil || sum["period_start"] != "2026-11-07T12:00:00Z" || sum["period_end"] != "2026-11-08T12:00:00Z" {
		t.Fatalf("json %s", body)
	}
	p0 := sum["projects"].([]any)[0].(map[string]any)
	it := p0["items"].([]any)[0].(map[string]any)
	if p0["id"] != "p0" || p0["name"] != "Project 0" || it["kind"] != "issue" || it["severity"] != "high" || it["url"] == "" || it["title"] == "" {
		t.Fatalf("json project %v", p0)
	}
	if err := Verify(body, req.Header.Get(SignatureHeaderName), "whsec_x", t0, time.Minute); err != nil {
		t.Fatal(err)
	}

	// Slack: one message, a line per item, grouped by project.
	text := slackSummary(small)
	for _, want := range []string{":newspaper: *Dash summary* · 1 new issue, 1 ready for review", "*<https://dash.example.com/?project=p|Project 0>*",
		":beetle: New issue (high): <https://dash.example.com/?project=p0&amp;issue=i0|Issue 0 with a rather long title that goes on &lt;and&gt; on for a while>",
		":eyes: Ready for review: "} {
		if !strings.Contains(text, want) {
			t.Errorf("slack lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "more") {
		t.Errorf("small summary truncated:\n%s", text)
	}
	// Truncated within Slack's limits, with a link to the rest.
	big := bigSummary(200)
	text = slackSummary(big)
	if len(text) > slackSummaryMax || !strings.Contains(text, "more:_ <https://dash.example.com/|open Dash>") {
		t.Fatalf("slack big: %d bytes\n%s", len(text), text[max(0, len(text)-200):])
	}
	shown := strings.Count(text, ":beetle:")
	if !strings.Contains(text, fmt.Sprintf("_and %d more:_", 200-shown)) {
		t.Fatalf("slack: %d shown, but %s", shown, text[len(text)-80:])
	}

	// ntfy: counts in the title, items in the body, truncated the same way.
	req, _ = request(context.Background(), model.Webhook{URL: "https://ntfy.sh/x", Format: model.WebhookNtfy}, note(big), t0, nil)
	body, _ = io.ReadAll(req.Body)
	title, _ := new(mime.WordDecoder).DecodeHeader(req.Header.Get("Title"))
	if title != "Dash summary: 200 new issues" || len(body) > 4096 || !strings.HasPrefix(string(body), "Project 0\n• New issue (high): Issue 0") ||
		!strings.Contains(string(body), "more: https://dash.example.com/") || req.Header.Get("Click") != "https://dash.example.com/" {
		t.Fatalf("ntfy %q (%d bytes) %q", title, len(body), body[max(0, len(body)-100):])
	}
	req, _ = request(context.Background(), model.Webhook{URL: "https://ntfy.sh/x", Format: model.WebhookNtfy}, note(small), t0, nil)
	body, _ = io.ReadAll(req.Body)
	if want := "Project 0\n• New issue (high): Issue 0 with a rather long title that goes on <and> on for a while\nProject 1\n• Ready for review: Issue 0 with a rather long title that goes on <and> on for a while"; string(body) != want {
		t.Fatalf("ntfy small\n got %q\nwant %q", body, want)
	}
}

// A new issue from a scan or report_issue notifies once per fingerprint;
// existing webhooks don't get it until they tick it.
func TestIssueNewOncePerFingerprint(t *testing.T) {
	e := summaryEnv(t)
	wants := newReceiver(t)
	old := newReceiver(t)
	e.hook(t, wants, model.WebhookJSON, model.NotifyIssue)
	e.hook(t, old, model.WebhookJSON, model.DefaultNotifyEvents...)
	in := model.Issue{ProjectID: e.shop.ID, Title: "Hard-coded key", Type: model.IssueVuln, Severity: model.SevHigh, File: "a.go", Source: "scan:security"}
	first, r, err := e.st.UpsertIssue(in)
	if err != nil || r != model.UpsertNew {
		t.Fatal(r, err)
	}
	in.Details, in.Line = "seen again, with details", 12
	if _, r, _ := e.st.UpsertIssue(in); r != model.UpsertUpdated {
		t.Fatal(r)
	}
	if _, _, err := e.st.UpsertIssueBatch(e.shop.ID, "agent:t1", []model.Issue{in, {Title: "Another", File: "b.go"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.st.UpsertIssue(model.Issue{ProjectID: e.shop.ID, Title: "Added by hand"}); err != nil {
		t.Fatal(err)
	}
	settle(e.n)
	var issues []map[string]any
	for _, b := range wants.bodies {
		var p struct {
			Type  string         `json:"type"`
			Issue map[string]any `json:"issue"`
		}
		_ = json.Unmarshal([]byte(b), &p)
		if p.Type != "issue.new" {
			t.Fatalf("event %s", b)
		}
		issues = append(issues, p.Issue)
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i]["title"].(string) > issues[j]["title"].(string) }) // deliveries race
	if len(issues) != 2 || issues[0]["id"] != first.ID || issues[0]["title"] != "Hard-coded key" || issues[0]["severity"] != "high" ||
		issues[0]["url"] != "https://dash.example.com/?issue="+first.ID+"&project="+e.shop.ID || issues[1]["title"] != "Another" {
		t.Fatalf("issue.new events %v", issues)
	}
	if len(old.bodies) != 0 {
		t.Fatalf("a webhook that didn't tick issue.new got %v", old.bodies)
	}
	// And the summary has them (medium and up, by default).
	acts, _ := e.st.ListActivity([]string{e.shop.ID}, t0, e.clock.Now().Add(time.Second))
	if len(acts) != 2 || acts[0].Kind != model.SummaryIssue || acts[0].Ref != first.ID {
		t.Fatalf("activity %+v", acts)
	}
}
