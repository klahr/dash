package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/usage"
)

// report posts runner name's report, logged in to Claude account email
// ("" for an older runner that doesn't say), and returns the hub's answer.
func (f *fleetServer) report(t *testing.T, name, email string) *httptest.ResponseRecorder {
	t.Helper()
	claude := `{"version":"2.1.0"}`
	if email != "" {
		claude = fmt.Sprintf(`{"version":"2.1.0","account":{"email":%q,"org_id":"org-1","subscription":"max"}}`, email)
	}
	week := time.Now().Add(usage.WeekSpan / 2).UTC().Format(time.RFC3339)
	body := fmt.Sprintf(`{"parallel":2,"claude":%s,"usage":{"known":true,"updated_at":%q,"windows":[{"name":"seven_day","utilization":0.2,"resets_at":%q}]}}`,
		claude, time.Now().UTC().Format(time.RFC3339Nano), week)
	r := httptest.NewRequest("POST", "/runner/report", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+f.tokens[name])
	w := httptest.NewRecorder()
	f.rh.ServeReport(w, r)
	return w
}

func pacedFleet(t *testing.T) *fleetServer {
	t.Helper()
	f := newFleetServer(t)
	f.s.Dispatch.Pacer = &dispatch.Pacer{Runners: f.rh.ListBare}
	f.rh.ReportReply = f.s.Dispatch.Reply
	return f
}

// The weekly pace on a hub is a Claude account's: the owner of a runner on
// it or an org admin changes it, nobody else; platform admin isn't needed.
func TestAccountPacePermissions(t *testing.T) {
	parallel(t)

	f := pacedFleet(t)
	f.report(t, "u-box", "Shared@Example.com")
	f.report(t, "c-box", "c@example.com")
	f.report(t, "b-box", "shared@example.com") // Beta's, on the same account as u-box

	put := func(h http.Handler, body string) *httptest.ResponseRecorder {
		return send(h, "PUT", "/api/queue/pace", body)
	}
	// c, a member, paces their own account (the one their runner reports).
	if w := put(f.c, `{"follow":true,"max_boost":3}`); w.Code != http.StatusOK {
		t.Fatalf("owner pacing their account: %d %s", w.Code, w.Body)
	}
	if s := f.s.Dispatch.Pacer.Settings("c@example.com"); !s.Follow || s.MaxBoost != 3 {
		t.Fatalf("c's account: %+v", s)
	}
	// …but not another member's account.
	if w := put(f.c, `{"follow":true,"account":"shared@example.com"}`); w.Code != http.StatusForbidden {
		t.Errorf("member pacing someone else's account: %d %s", w.Code, w.Body)
	}
	// d, an org admin without runners, paces any account of the org's runners…
	if w := put(f.d, `{"follow":true,"account":"shared@example.com"}`); w.Code != http.StatusOK {
		t.Errorf("org admin: %d %s", w.Code, w.Body)
	}
	// …but has no account of their own to default to.
	if w := put(f.d, `{"follow":true}`); w.Code != http.StatusBadRequest {
		t.Errorf("admin without runners, no account named: %d %s", w.Code, w.Body)
	}
	// b, in Beta, owns a runner on the shared account: they may; but not c's.
	if w := put(f.b, `{"follow":false}`); w.Code != http.StatusOK {
		t.Errorf("Beta's owner of a runner on the account: %d %s", w.Code, w.Body)
	}
	if w := put(f.b, `{"follow":true,"account":"c@example.com"}`); w.Code != http.StatusForbidden {
		t.Errorf("another org's member pacing c's account: %d %s", w.Code, w.Body)
	}
	if w := put(f.c, `{"follow":true,"max_boost":20}`); w.Code != http.StatusBadRequest {
		t.Errorf("boost out of range: %d %s", w.Code, w.Body)
	}

	// The usage sheet shows the caller's account and its runners in their org.
	var u usage.Snapshot
	if w := send(f.u, "GET", "/api/usage", ""); json.Unmarshal(w.Body.Bytes(), &u) != nil {
		t.Fatalf("usage: %s", w.Body)
	}
	if !u.Pace.Hub || u.Pace.Account != "shared@example.com" || !slices.Equal(u.Pace.Runners, []string{"u-box"}) || u.Pace.Follow {
		t.Errorf("u's pace: %+v", u.Pace)
	}
	// c's account follows the pace; a fifth used half way through is behind
	// the line. (Its runner has no tunnel here, so nothing more runs at
	// once: see the dispatch tests for that.)
	if w := send(f.c, "GET", "/api/usage", ""); json.Unmarshal(w.Body.Bytes(), &u) != nil || !u.Pace.Follow || u.Pace.MaxBoost != 3 ||
		u.Pace.Account != "c@example.com" || u.Pace.Gap() > -time.Hour {
		t.Errorf("c's pace: %s", w.Body)
	}

	// The hub tells each runner how it paces it, in its answer to a report:
	// the runner's own limit while it can't take more.
	var reply dispatch.Reply
	if w := f.report(t, "c-box", "c@example.com"); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &reply) != nil ||
		reply.Pace == nil || !reply.Pace.Hub || !reply.Pace.Follow || reply.Pace.Slots != 2 {
		t.Errorf("report answer: %d %s", w.Code, w.Body)
	}
}

// On a runner's own Dash (not a hub) the setting stays its own, for platform
// admins, and is refused while a hub paces it.
func TestRunnerPaceIsLocal(t *testing.T) {
	parallel(t)

	s, h, _ := runnerSide(t, 0)
	s.Usage = usage.New(nil, 0.9)
	if w := send(h, "PUT", "/api/queue/pace", `{"follow":true}`); w.Code != http.StatusOK {
		t.Fatalf("local pace: %d %s", w.Code, w.Body)
	}
	if !s.Usage.Snapshot().Pace.Follow {
		t.Fatal("local setting not kept")
	}
	s.Usage.SetHubPace(&usage.Pace{Follow: true, State: usage.PaceOnPace, Slots: 2})
	if w := send(h, "PUT", "/api/queue/pace", `{"follow":false}`); w.Code != http.StatusConflict {
		t.Errorf("a hub-paced runner's own setting: %d %s", w.Code, w.Body)
	}
}
