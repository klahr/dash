package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

func TestRunnerDiskNotifiesOnce(t *testing.T) {
	r := newReceiver(t)
	other := newReceiver(t)
	n, fs, _ := setup(t,
		model.Webhook{ID: "a", ProjectID: "p1", URL: r.URL, Format: model.WebhookJSON, Enabled: true, Events: model.DefaultNotifyEvents, Secret: "s"},
		// The same URL on another of the runner's projects: told once.
		model.Webhook{ID: "b", ProjectID: "p2", URL: r.URL, Format: model.WebhookJSON, Enabled: true, Events: model.DefaultNotifyEvents, Secret: "s"},
		model.Webhook{ID: "c", ProjectID: "p3", URL: other.URL, Format: model.WebhookJSON, Enabled: true, Events: model.DefaultNotifyEvents, Secret: "s"},
	)
	fs.projects["p2"] = model.Project{ID: "p2", Name: "Docs"}
	now := t0
	n.Now = func() time.Time { return now }
	disk := func(low bool) {
		n.Observe(model.Event{Type: model.EvRunnerDisk, OrgID: 1, Data: model.RunnerDisk{RunnerID: "r1", RunnerName: "box", OrgID: 1, Low: low,
			Message: "box has only 4.0 GB free", Projects: []string{"p1", "p2"}}})
	}
	disk(true)
	disk(true) // the dispatcher tells only crossings; a repeat isn't news either
	settle(n)
	if got := r.events(); len(got) != 1 || got[0] != string(model.NotifyDisk) {
		t.Fatalf("delivered %v, want one runner.disk", got)
	}
	if len(other.events()) != 0 {
		t.Error("a webhook of a project not linked to the runner was told")
	}
	var p struct {
		Runner  RunnerRef `json:"runner"`
		Message string    `json:"message"`
	}
	_ = json.Unmarshal([]byte(r.bodies[0]), &p)
	if p.Runner.Name != "box" || !strings.Contains(p.Message, "4.0 GB free") {
		t.Errorf("payload = %s", r.bodies[0])
	}

	// Cleared, then low again soon after: not told again within DiskEvery…
	disk(false)
	now = now.Add(time.Hour)
	disk(true)
	settle(n)
	if got := r.events(); len(got) != 1 {
		t.Fatalf("flapping around the threshold notified again: %v", got)
	}
	// …but after it.
	disk(false)
	now = now.Add(DefaultDiskEvery)
	disk(true)
	settle(n)
	if got := r.events(); len(got) != 2 {
		t.Fatalf("low again hours later: %v", got)
	}
}

func TestRunnerDiskFormats(t *testing.T) {
	note := Notification{Type: model.NotifyDisk, Runner: &RunnerRef{ID: "r1", Name: "box"}, Message: "box has only 4.0 GB free"}
	if got := slackText(note); !strings.Contains(got, "Disk nearly full") || !strings.Contains(got, "runner box") {
		t.Errorf("slack: %q", got)
	}
	p := pushPayload(note, nil)
	if p.Title != "Disk nearly full: runner box" || p.Tag != "dash-disk-r1" || p.Body != "box has only 4.0 GB free" {
		t.Errorf("push: %+v", p)
	}
}
