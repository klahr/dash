package auth

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// The store seals its secrets with a Sealer, and tells sealed values from
// plaintext by the Sealer's prefix.
func TestSealerSealsStoreSecrets(t *testing.T) {
	t.Parallel()
	sl, err := NewSealer(make([]byte, SecretKeySize))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sl.Seal("x", "ctx")
	if err != nil || !store.IsSealed(sealed) {
		t.Fatalf("Seal = %q, %v: the store doesn't see it as sealed", sealed, err)
	}
	if store.IsSealed("x") {
		t.Fatal("plaintext seen as sealed")
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.CreateProject(model.Project{Name: "shop", Path: t.TempDir(), OrgID: 1})
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateWebhook(model.Webhook{ProjectID: p.ID, URL: "https://hooks.example.com/x", Format: model.WebhookJSON, Token: "tok-123"})
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sl)
	if n, _, err := st.SealSecrets(); err != nil || n != 2 {
		t.Fatalf("SealSecrets = %d, %v; want the secret and token", n, err)
	}
	got, err := st.GetWebhook(w.ID)
	if err != nil || got.Secret != w.Secret || got.Token != "tok-123" {
		t.Fatalf("webhook = %+v, %v", got, err)
	}
	if !strings.HasPrefix(got.Secret, store.WebhookSecretPrefix) {
		t.Fatalf("secret %q", got.Secret)
	}
}
