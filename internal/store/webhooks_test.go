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
	if err := s.RecordWebhookDelivery(w.ID, model.NotifyReview, at, "the server answered 500"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordWebhookDelivery("deleted", model.NotifyReview, at, ""); err != nil {
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

	if err := s.DeleteWebhook(w.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteWebhook(w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if list, _ := s.ListWebhooks(p.ID); len(list) != 0 {
		t.Fatalf("left %v", list)
	}
}

func TestWebhooksBelongToTheProjectsOrg(t *testing.T) {
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
