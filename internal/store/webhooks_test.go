package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

func TestWebhookCRUD(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(model.Project{Name: "shop", Path: t.TempDir(), OrgID: 1})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.CreateWebhook(model.Webhook{ProjectID: "nope", URL: "https://x.example", Format: model.WebhookJSON}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("webhook of an unknown project: %v", err)
	}
	for _, bad := range []model.Webhook{
		{ProjectID: p.ID, URL: "ftp://x.example", Format: model.WebhookJSON},
		{ProjectID: p.ID, URL: "https://x.example", Format: "teams"},
		{ProjectID: p.ID, URL: "https://x.example", Format: model.WebhookJSON, Events: []model.NotifyEvent{"task.exploded"}},
		{ProjectID: p.ID, URL: "https://user:pw@x.example", Format: model.WebhookJSON},
	} {
		if _, err := s.CreateWebhook(bad); err == nil {
			t.Errorf("created %+v", bad)
		}
	}

	w, err := s.CreateWebhook(model.Webhook{ProjectID: p.ID, Name: " Alerts ", URL: " https://ntfy.sh/topic ", Format: model.WebhookNtfy,
		Events: []model.NotifyEvent{model.NotifyFailed, model.NotifyFailed, model.NotifyPermission}, Enabled: true, Token: "tk_1"})
	if err != nil {
		t.Fatal(err)
	}
	if w.ID == "" || !strings.HasPrefix(w.Secret, WebhookSecretPrefix) || w.Name != "Alerts" || w.URL != "https://ntfy.sh/topic" || !w.HasToken || len(w.Events) != 2 {
		t.Fatalf("created %+v", w)
	}

	got, err := s.GetWebhook(w.ID)
	if err != nil || got.Secret != w.Secret || got.Token != "tk_1" || !got.Enabled || !got.Wants(model.NotifyFailed) || got.Wants(model.NotifyReview) {
		t.Fatalf("get %+v %v", got, err)
	}

	up, err := s.UpdateWebhook(w.ID, func(h *model.Webhook) error {
		h.Enabled = false
		h.Events = []model.NotifyEvent{model.NotifyReview}
		h.Token = ""
		h.Secret = "hijacked" // not saved
		h.ProjectID = "other" // not saved
		return nil
	})
	if err != nil || up.Enabled || up.HasToken || up.Secret != w.Secret || up.ProjectID != p.ID {
		t.Fatalf("update %+v %v", up, err)
	}
	if _, err := s.UpdateWebhook(w.ID, func(h *model.Webhook) error { h.URL = "nope"; return nil }); err == nil {
		t.Fatal("saved a bad URL")
	}
	if _, err := s.UpdateWebhook("missing", func(*model.Webhook) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}

	secret, err := s.RotateWebhookSecret(w.ID)
	if err != nil || secret == w.Secret || !strings.HasPrefix(secret, WebhookSecretPrefix) {
		t.Fatalf("rotate %q %v", secret, err)
	}

	at := time.Unix(1_800_000_000, 0)
	if err := s.RecordWebhookDelivery(model.WebhookDelivery{WebhookID: w.ID, Event: model.NotifyReview, At: at, Error: "the server answered 500",
		Status: 500, Attempts: 4, ProjectID: p.ID, Project: "shop", Subject: "Fix it"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordWebhookDelivery(model.WebhookDelivery{WebhookID: "deleted", Event: model.NotifyReview, At: at}); err != nil {
		t.Fatalf("recording for a deleted webhook: %v", err)
	}

	// It all survives a restart.
	s.Close()
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, err := s.ListWebhooks(p.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	got = list[0]
	if got.Secret != secret || got.Enabled || got.LastError != "the server answered 500" || got.LastEvent != model.NotifyReview || !got.LastAt.Equal(at) {
		t.Fatalf("after restart %+v", got)
	}
	if ds, err := s.ListWebhookDeliveries(w.ID); err != nil || len(ds) != 1 || ds[0].Status != 500 || ds[0].Attempts != 4 ||
		ds[0].Subject != "Fix it" || ds[0].Project != "shop" || !ds[0].At.Equal(at) || ds[0].Error != "the server answered 500" {
		t.Fatalf("history %+v %v", ds, err)
	}

	if err := s.DeleteWebhook(w.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteWebhook(w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if list, _ := s.ListWebhooks(p.ID); len(list) != 0 {
		t.Fatalf("left %v", list)
	}
	// Its history goes with it.
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM webhook_deliveries").Scan(&n); err != nil || n != 0 {
		t.Fatalf("history left: %d %v", n, err)
	}
}

func TestWebhooksBelongToTheProjectsOrg(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, _ := s.CreateProject(model.Project{Name: "a", Path: t.TempDir(), OrgID: 1})
	mine, other := s.ForOrg(1), s.ForOrg(2)

	w, err := mine.CreateWebhook(model.Webhook{ProjectID: p.ID, URL: "https://x.example", Format: model.WebhookSlack, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.CreateWebhook(model.Webhook{ProjectID: p.ID, URL: "https://x.example", Format: model.WebhookSlack}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org created one: %v", err)
	}
	if _, err := other.ListWebhooks(p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org listed: %v", err)
	}
	if _, err := other.GetWebhook(w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org read: %v", err)
	}
	if _, err := other.UpdateWebhook(w.ID, func(h *model.Webhook) error { h.Enabled = false; return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org updated: %v", err)
	}
	if _, err := other.RotateWebhookSecret(w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org rotated: %v", err)
	}
	if err := other.DeleteWebhook(w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org deleted: %v", err)
	}
	if list, err := mine.ListWebhooks(p.ID); err != nil || len(list) != 1 {
		t.Fatalf("mine: %v %v", list, err)
	}

	// Deleting the project deletes its webhooks.
	if _, err := s.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListWebhooks(""); len(list) != 0 {
		t.Fatalf("left after the project: %v", list)
	}
}

func TestPersonalWebhooks(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret"); err != nil {
		t.Fatal(err)
	}
	def, _ := acc.DefaultOrg()
	u, _ := acc.EnvUser()
	other, err := acc.CreateOrg(u.ID, "Other")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.CreateProject(model.Project{Name: "shop", Path: t.TempDir(), OrgID: def.ID})
	q, _ := s.CreateProject(model.Project{Name: "elsewhere", Path: t.TempDir(), OrgID: other.ID})

	proj, err := s.CreateWebhook(model.Webhook{ProjectID: p.ID, URL: "https://proj.example", Format: model.WebhookSlack, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	me := s.ForOrg(def.ID).AsUser(u.ID)
	if _, err := s.ForOrg(def.ID).CreateWebhook(model.Webhook{URL: "https://me.example", Format: model.WebhookSlack}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a personal webhook without a user: %v", err)
	}
	if _, err := s.CreateWebhook(model.Webhook{URL: "https://me.example", Format: model.WebhookSlack}); err == nil {
		t.Fatal("a personal webhook without an owner")
	}
	mine, err := me.CreateWebhook(model.Webhook{URL: "https://me.example", Format: model.WebhookNtfy, Enabled: true, UserID: 999, OrgID: 999})
	if err != nil || !mine.Personal() || mine.UserID != u.ID || mine.OrgID != def.ID {
		t.Fatalf("personal %+v %v", mine, err)
	}
	up, err := me.UpdateWebhook(mine.ID, func(h *model.Webhook) error { h.Name = "Phone"; h.UserID = 5; h.ProjectID = p.ID; return nil })
	if err != nil || up.Name != "Phone" || up.UserID != u.ID || !up.Personal() {
		t.Fatalf("update %+v %v", up, err)
	}
	if list, err := me.ListPersonalWebhooks(); err != nil || len(list) != 1 || list[0].ID != mine.ID {
		t.Fatalf("mine %+v %v", list, err)
	}
	if list, _ := s.ListWebhooks(""); len(list) != 1 || list[0].ID != proj.ID {
		t.Fatalf("projects' webhooks %+v", list)
	}

	// Only its owner, in its org, reaches it: not the org, not them in another org.
	for _, o := range []*OrgStore{s.ForOrg(def.ID), s.ForOrg(def.ID).AsUser(u.ID + 1), s.ForOrg(other.ID).AsUser(u.ID)} {
		if _, err := o.GetWebhook(mine.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("read by %+v: %v", o, err)
		}
		if err := o.DeleteWebhook(mine.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted by %+v: %v", o, err)
		}
		if _, err := o.ListWebhookDeliveries(mine.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("history read by %+v: %v", o, err)
		}
	}
	if list, _ := s.ForOrg(other.ID).AsUser(u.ID).ListPersonalWebhooks(); len(list) != 0 {
		t.Fatalf("in another org: %+v", list)
	}

	// Events of the org's projects go to the project's webhooks, then the personal ones.
	if hooks, err := s.WebhooksFor(p.ID); err != nil || len(hooks) != 2 || hooks[0].ID != proj.ID || hooks[1].ID != mine.ID {
		t.Fatalf("for the project: %+v %v", hooks, err)
	}
	if hooks, _ := s.WebhooksFor(q.ID); len(hooks) != 0 {
		t.Fatalf("another org's project: %+v", hooks)
	}
	// Not while its owner is out of the org.
	if _, err := s.db.Exec("DELETE FROM org_memberships WHERE user_id = ? AND org_id = ?", u.ID, def.ID); err != nil {
		t.Fatal(err)
	}
	if hooks, _ := s.WebhooksFor(p.ID); len(hooks) != 1 {
		t.Fatalf("after leaving: %+v", hooks)
	}

	// The history keeps the newest WebhookHistory deliveries.
	for i := range WebhookHistory + 5 {
		if err := s.RecordWebhookDelivery(model.WebhookDelivery{WebhookID: mine.ID, Event: model.NotifyFailed, At: time.Unix(int64(1000+i), 0), Subject: strings.Repeat("x", 300)}); err != nil {
			t.Fatal(err)
		}
	}
	ds, err := s.ListWebhookDeliveries(mine.ID)
	if err != nil || len(ds) != WebhookHistory || ds[0].At.Unix() != int64(1000+WebhookHistory+4) || ds[len(ds)-1].At.Unix() != 1005 || len(ds[0].Subject) != 200 || ds[0].Attempts != 1 {
		t.Fatalf("history: %d %v", len(ds), err)
	}
	if got, _ := s.GetWebhook(mine.ID); got.LastEvent != model.NotifyFailed || got.LastAt.Unix() != int64(1000+WebhookHistory+4) {
		t.Fatalf("last delivery %+v", got)
	}
}
