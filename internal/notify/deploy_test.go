package notify

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// A deploy that fails once on its way notifies deploy.failed, linking to
// it, with why; one cancelled doesn't, nor does a failed one changed again
// (its draft task noted on it).
func TestDeployFailedNotifies(t *testing.T) {
	e := summaryEnv(t)
	r := newReceiver(t)
	e.hook(t, r, model.WebhookJSON, model.DefaultNotifyEvents...)
	p, err := e.st.UpdateProject(e.shop.ID, func(x *model.Project) error {
		x.Environments = []model.Environment{{Name: "production", DeployCommand: "./ship"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fail := func(cancelled bool) model.Deploy {
		t.Helper()
		d, err := e.st.CreateDeploy(model.Deploy{ProjectID: p.ID, Environment: "production", Trigger: model.DeployByUser})
		if err != nil {
			t.Fatal(err)
		}
		d, _ = e.st.UpdateDeploy(d.ID, func(x *model.Deploy) error {
			x.Commit = "0123456789abcdef0123456789abcdef01234567"
			x.SetStatus(model.DeployDeploying, time.Now())
			return nil
		})
		d, _ = e.st.UpdateDeploy(d.ID, func(x *model.Deploy) error {
			x.Error, x.Cancelled = "health check of https://example.com/healthz: it answers 502 Bad Gateway", cancelled
			x.SetStatus(model.DeployFailed, time.Now())
			return nil
		})
		_, _ = e.st.UpdateDeploy(d.ID, func(x *model.Deploy) error { x.FixTask = "t1"; return nil })
		return d
	}
	d := fail(false)
	fail(true)
	settle(e.n)
	if len(r.bodies) != 1 {
		t.Fatalf("want one notification, got %v", r.bodies)
	}
	var got struct {
		Type    string            `json:"type"`
		Message string            `json:"message"`
		Deploy  map[string]string `json:"deploy"`
	}
	_ = json.Unmarshal([]byte(r.bodies[0]), &got)
	if got.Type != "deploy.failed" || got.Deploy["id"] != d.ID || got.Deploy["environment"] != "production" ||
		got.Deploy["url"] != "https://dash.example.com/p/"+p.ID+"/deploys/"+d.ID || got.Message == "" {
		t.Errorf("notification %s", r.bodies[0])
	}
	n := Notification{Type: model.NotifyDeploy, Project: ProjectRef{Name: "Shop"}, Deploy: &DeployRef{Environment: "production", Commit: d.Commit}}
	if n.Head() != "Deploy failed" || n.subject() != "Shop to production (01234567)" {
		t.Errorf("head %q, subject %q", n.Head(), n.subject())
	}
}
