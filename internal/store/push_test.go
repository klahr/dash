package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

func TestPushSubscriptions(t *testing.T) {
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

	// The VAPID key is made once and kept.
	n := 0
	gen := func() (string, error) { n++; return "key-" + string(rune('0'+n)), nil }
	k1, err := s.PushVAPIDKey(gen)
	if err != nil {
		t.Fatal(err)
	}
	if k2, _ := s.PushVAPIDKey(gen); k1 != "key-1" || k2 != k1 || n != 1 {
		t.Fatalf("keys %q %q after %d", k1, k2, n)
	}

	a, err := s.SavePushSubscription(model.PushSubscription{UserID: u.ID, OrgID: def.ID, Endpoint: "https://push.example/a", P256dh: "pa", Auth: "aa"})
	if err != nil || a.ID == "" || a.OrgID != def.ID {
		t.Fatalf("save: %+v %v", a, err)
	}
	if _, err := s.SavePushSubscription(model.PushSubscription{UserID: u.ID, OrgID: other.ID, Endpoint: "https://push.example/b", P256dh: "pb", Auth: "ab"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPushDelivery(a.ID, time.Unix(100, 0), "boom"); err != nil {
		t.Fatal(err)
	}
	// The same browser again (new keys, another org): one subscription, status cleared.
	a2, err := s.SavePushSubscription(model.PushSubscription{UserID: u.ID, OrgID: other.ID, Endpoint: "https://push.example/a", P256dh: "pa2", Auth: "aa2"})
	if err != nil || a2.ID != a.ID || a2.P256dh != "pa2" || a2.OrgID != other.ID || a2.LastError != "" || !a2.LastAt.IsZero() {
		t.Fatalf("resave: %+v %v", a2, err)
	}
	if subs, _ := s.PushSubscriptionsForOrg(def.ID); len(subs) != 0 {
		t.Fatalf("default org: %+v", subs)
	}
	if subs, _ := s.PushSubscriptionsForOrg(other.ID); len(subs) != 2 {
		t.Fatalf("other org: %+v", subs)
	}

	// Someone no longer in the org gets none of its pushes.
	if _, err := s.db.Exec("DELETE FROM org_memberships WHERE user_id = ? AND org_id = ?", u.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if subs, _ := s.PushSubscriptionsForOrg(other.ID); len(subs) != 0 {
		t.Fatalf("after leaving: %+v", subs)
	}

	if err := s.DeleteUserPushSubscription(u.ID+1, "https://push.example/a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else's: %v", err)
	}
	if err := s.DeleteUserPushSubscription(u.ID, "https://push.example/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PushSubscription(u.ID, "https://push.example/a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.DeletePushSubscription("nope"); err != nil {
		t.Fatal(err)
	}
}
