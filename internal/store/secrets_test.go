package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// testSealer seals like auth.Sealer (which this package can't import):
// AES-GCM bound to the context, behind the same prefix.
type testSealer struct{ aead cipher.AEAD }

func newTestSealer(t *testing.T) *testSealer {
	t.Helper()
	key := make([]byte, 32)
	rand.Read(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return &testSealer{aead}
}

func (s *testSealer) Seal(plain, context string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	rand.Read(nonce)
	return sealedPrefix + base64.RawStdEncoding.EncodeToString(s.aead.Seal(nonce, nonce, []byte(plain), []byte(context))), nil
}

func (s *testSealer) Open(sealed, context string) (string, error) {
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sealed, sealedPrefix))
	if err != nil || len(b) < s.aead.NonceSize() {
		return "", errors.New("damaged")
	}
	p, err := s.aead.Open(nil, b[:s.aead.NonceSize()], b[s.aead.NonceSize():], []byte(context))
	if err != nil {
		return "", errors.New("a sealed secret doesn't open with this secret key")
	}
	return string(p), nil
}

// The plaintexts the tests store, one per sealed column.
const (
	plainTOTP      = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
	plainToken     = "bearer-token-0123456789"
	plainHook      = "github-hook-secret-0123456789"
	plainCreds     = "tracker-api-key-0123456789"
	plainTrackerWS = "trsec_0123456789abcdef"
	plainVAPID     = "vapid-private-key-0123456789"
	plainNotify    = "notify-key-0123456789"
)

// secretFixture is one of each secret, written through the store.
type secretFixture struct {
	uid      int64
	project  string
	webhook  string
	whSecret string
}

func writeSecrets(t *testing.T, s *Store) secretFixture {
	t.Helper()
	var f secretFixture
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-password"); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT id FROM users ORDER BY id LIMIT 1").Scan(&f.uid); err != nil {
		t.Fatal(err)
	}
	if err := acc.BeginTOTP(f.uid, plainTOTP); err != nil {
		t.Fatal(err)
	}
	if got, err := acc.PendingTOTP(f.uid); err != nil || got != plainTOTP {
		t.Fatalf("pending TOTP = %q, %v", got, err)
	}
	if err := acc.ConfirmTOTP(f.uid, plainTOTP, 1, nil); err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(model.Project{Name: "shop", Path: t.TempDir(), OrgID: 1})
	if err != nil {
		t.Fatal(err)
	}
	f.project = p.ID
	w, err := s.CreateWebhook(model.Webhook{ProjectID: p.ID, URL: "https://hooks.example.com/x", Format: model.WebhookJSON, Token: plainToken})
	if err != nil {
		t.Fatal(err)
	}
	f.webhook, f.whSecret = w.ID, w.Secret
	if err := s.PutGitHubHook(model.GitHubHook{OrgID: 1, Repo: "Owner/Repo", HookID: 7, Secret: plainHook, URL: "https://dash.example.com/hook"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTrackerConfig(model.TrackerConfig{ProjectID: p.ID, Backend: "linear",
		Credentials: map[string]string{"api_key": plainCreds}, WebhookSecret: plainTrackerWS}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PushVAPIDKey(func() (string, error) { return plainVAPID, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NotifyKey(func() (string, error) { return plainNotify, nil }); err != nil {
		t.Fatal(err)
	}
	return f
}

// checkSecrets reads every secret back through the store.
func checkSecrets(t *testing.T, s *Store, f secretFixture) {
	t.Helper()
	if got, err := s.Accounts().TOTPSecret(f.uid); err != nil || got != plainTOTP {
		t.Errorf("TOTP secret = %q, %v", got, err)
	}
	w, err := s.GetWebhook(f.webhook)
	if err != nil || w.Secret != f.whSecret || w.Token != plainToken || !w.HasToken {
		t.Errorf("webhook = secret %q token %q has %v, %v", w.Secret, w.Token, w.HasToken, err)
	}
	if !strings.HasPrefix(w.Secret, WebhookSecretPrefix) {
		t.Errorf("webhook secret %q", w.Secret)
	}
	if ws, err := s.ListWebhooks(f.project); err != nil || len(ws) != 1 || ws[0].Token != plainToken {
		t.Errorf("listed webhooks = %+v, %v", ws, err)
	}
	h, err := s.GitHubHook(1, "owner/repo")
	if err != nil || h.Secret != plainHook {
		t.Errorf("GitHub hook secret = %q, %v", h.Secret, err)
	}
	if hs, err := s.GitHubHooks(); err != nil || len(hs) != 1 || hs[0].Secret != plainHook {
		t.Errorf("GitHub hooks = %+v, %v", hs, err)
	}
	c, err := s.TrackerConfig(f.project)
	if err != nil || c.Credentials["api_key"] != plainCreds || !c.HasCredentials || c.WebhookSecret != plainTrackerWS || !c.HasWebhookSecret {
		t.Errorf("tracker = %+v, %v", c, err)
	}
	if cs, err := s.TrackerConfigs(); err != nil || len(cs) != 1 || cs[0].Credentials["api_key"] != plainCreds {
		t.Errorf("tracker configs = %+v, %v", cs, err)
	}
	gen := func() (string, error) { return "", errors.New("made a new key") }
	if k, err := s.PushVAPIDKey(gen); err != nil || k != plainVAPID {
		t.Errorf("VAPID key = %q, %v", k, err)
	}
	if k, err := s.NotifyKey(gen); err != nil || k != plainNotify {
		t.Errorf("notify key = %q, %v", k, err)
	}
}

// rawSecrets are the sealed columns as stored, by name.
func rawSecrets(t *testing.T, s *Store) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, q := range map[string]string{
		"totp":            "SELECT totp_secret FROM users WHERE totp_secret IS NOT NULL",
		"webhook secret":  "SELECT secret FROM webhooks",
		"webhook token":   "SELECT token FROM webhooks",
		"github hook":     "SELECT secret FROM github_hooks",
		"tracker creds":   "SELECT credentials FROM tracker_projects",
		"tracker secret":  "SELECT webhook_secret FROM tracker_projects",
		"vapid key":       "SELECT private_key FROM push_vapid",
		"notify key":      "SELECT key FROM notify_key",
		"pending totp(s)": "SELECT COUNT(*) FROM users WHERE totp_pending IS NOT NULL",
	} {
		var v string
		if err := s.db.QueryRow(q).Scan(&v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = v
	}
	return out
}

var plaintexts = []string{plainTOTP, plainToken, plainHook, plainCreds, plainTrackerWS, plainVAPID, plainNotify, WebhookSecretPrefix}

func assertSealed(t *testing.T, s *Store) {
	t.Helper()
	for name, v := range rawSecrets(t, s) {
		if name == "pending totp(s)" {
			if v != "0" {
				t.Errorf("%s pending TOTP enrolments left", v)
			}
			continue
		}
		if !IsSealed(v) {
			t.Errorf("%s is stored in plaintext: %q", name, v)
		}
		for _, p := range plaintexts {
			if strings.Contains(v, p) {
				t.Errorf("%s holds %q: %q", name, p, v)
			}
		}
	}
}

func TestSecretsSealedRoundTrip(t *testing.T) {
	t.Parallel()
	s := openTest(t, filepath.Join(t.TempDir(), "dash.db"))
	s.SetSealer(newTestSealer(t))
	f := writeSecrets(t, s)
	assertSealed(t, s)
	checkSecrets(t, s, f)

	// Updating and rotating keep them sealed.
	if _, err := s.UpdateWebhook(f.webhook, func(w *model.Webhook) error { w.Token = plainToken + "-2"; return nil }); err != nil {
		t.Fatal(err)
	}
	secret, err := s.RotateWebhookSecret(f.webhook)
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.GetWebhook(f.webhook)
	if err != nil || w.Token != plainToken+"-2" || w.Secret != secret {
		t.Fatalf("after update = %+v, %v", w, err)
	}
	raw := rawSecrets(t, s)
	if !IsSealed(raw["webhook secret"]) || !IsSealed(raw["webhook token"]) || strings.Contains(raw["webhook secret"], secret) {
		t.Errorf("webhook after update stored as %q, %q", raw["webhook secret"], raw["webhook token"])
	}

	// No sealing twice.
	if n, bad, err := s.SealSecrets(); err != nil || n != 0 || bad != 0 {
		t.Errorf("SealSecrets on sealed rows = %d, %v", n, err)
	}

	// A sealed value is bound to its row: copied to another, it doesn't open.
	w2, err := s.CreateWebhook(model.Webhook{ProjectID: f.project, URL: "https://hooks.example.com/y", Format: model.WebhookJSON})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE webhooks SET secret = ? WHERE id = ?", raw["webhook secret"], w2.ID); err != nil {
		t.Fatal(err)
	}
	if w, err := s.GetWebhook(w2.ID); err != nil || w.Secret != "" || w.Enabled || !w.Unreadable || w.LastError == "" {
		t.Errorf("a sealed secret copied to another webhook: %+v, %v", w, err)
	}
}

func TestSealSecretsMigratesPlaintext(t *testing.T) {
	t.Parallel()
	s := openTest(t, filepath.Join(t.TempDir(), "dash.db"))
	f := writeSecrets(t, s) // no key yet: as before sealing
	raw := rawSecrets(t, s)
	if raw["totp"] != plainTOTP || raw["webhook token"] != plainToken || raw["github hook"] != plainHook ||
		raw["vapid key"] != plainVAPID || raw["notify key"] != plainNotify || raw["tracker secret"] != plainTrackerWS {
		t.Fatalf("without a key the secrets should be stored as they are: %v", raw)
	}
	checkSecrets(t, s, f)
	s.SetSealer(newTestSealer(t))
	n, bad, err := s.SealSecrets()
	if err != nil || bad != 0 {
		t.Fatal(bad, err)
	}
	if n != 8 { // TOTP, webhook secret and token, GitHub hook, tracker creds and secret, VAPID, notify
		t.Errorf("sealed %d secrets, want 8", n)
	}
	assertSealed(t, s)
	checkSecrets(t, s, f)
	if n, bad, err := s.SealSecrets(); err != nil || n != 0 || bad != 0 {
		t.Errorf("sealing again = %d, %v; want nothing sealed twice", n, err)
	}
}

func TestSealSecretsPendingTOTP(t *testing.T) {
	t.Parallel()
	s := openTest(t, filepath.Join(t.TempDir(), "dash.db"))
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-password"); err != nil {
		t.Fatal(err)
	}
	var uid int64
	if err := s.db.QueryRow("SELECT id FROM users ORDER BY id LIMIT 1").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if err := acc.BeginTOTP(uid, plainTOTP); err != nil { // before the key
		t.Fatal(err)
	}
	s.SetSealer(newTestSealer(t))
	if _, _, err := s.SealSecrets(); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.db.QueryRow("SELECT totp_pending FROM users WHERE id = ?", uid).Scan(&raw); err != nil || !IsSealed(raw) {
		t.Fatalf("pending TOTP stored as %q, %v", raw, err)
	}
	// Confirming the migrated enrolment works, and a wrong secret doesn't.
	if err := acc.ConfirmTOTP(uid, "WRONG", 1, nil); !errors.Is(err, ErrNoPendingTOTP) {
		t.Errorf("confirm with another secret: %v", err)
	}
	if err := acc.ConfirmTOTP(uid, plainTOTP, 1, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := acc.TOTPSecret(uid); err != nil || got != plainTOTP {
		t.Errorf("TOTP secret = %q, %v", got, err)
	}
}

func TestSecretsWithoutKey(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "dash.db")
	s := openTest(t, path)
	if n, bad, err := s.SealSecrets(); err != nil || n != 0 || bad != 0 {
		t.Errorf("SealSecrets without a key = %d, %v", n, err)
	}
	s.SetSealer(newTestSealer(t))
	f := writeSecrets(t, s)

	// The same database without the key: a clear error, not garbage.
	s.SetSealer(nil)
	if _, err := s.GitHubHook(1, "owner/repo"); !errors.Is(err, ErrSealedNoKey) {
		t.Errorf("GitHub hook without the key: %v", err)
	}
	if _, err := s.PushVAPIDKey(noNewKey); !errors.Is(err, ErrSealedNoKey) {
		t.Errorf("VAPID key without the key: %v", err)
	}
	if _, err := s.NotifyKey(noNewKey); !errors.Is(err, ErrSealedNoKey) {
		t.Errorf("notify key without the key: %v", err)
	}
	// Webhooks and boards still list, without their secrets, off, saying
	// why: one doesn't take the others down, and each can be fixed.
	ws, err := s.ListWebhooks(f.project)
	if err != nil || len(ws) != 1 {
		t.Fatalf("webhooks without the key = %+v, %v", ws, err)
	}
	if w := ws[0]; w.Secret != "" || w.Token != "" || w.HasToken || w.Enabled || !w.Unreadable || !strings.Contains(w.LastError, "secret key") {
		t.Errorf("webhook without the key = %+v", w)
	}
	if _, err := s.RotateWebhookSecret(f.webhook); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateWebhook(f.webhook, func(w *model.Webhook) error { w.Token, w.Enabled = "new-token", true; return nil }); err != nil {
		t.Fatal(err)
	}
	if w, err := s.GetWebhook(f.webhook); err != nil || w.Unreadable || w.Secret == "" || w.Token != "new-token" || !w.Enabled {
		t.Errorf("after rotating and a new token = %+v, %v", w, err)
	}
	c, err := s.TrackerConfig(f.project)
	if err != nil || c.HasCredentials || c.WebhookSecret != "" || !strings.Contains(c.LastError, "secret key") {
		t.Errorf("tracker without the key = %+v, %v", c, err)
	}
	if cs, err := s.TrackerConfigs(); err != nil || len(cs) != 1 {
		t.Errorf("tracker configs without the key = %+v, %v", cs, err)
	}
	_, err = s.Accounts().TOTPSecret(f.uid)
	if !errors.Is(err, ErrSealedNoKey) || !strings.Contains(err.Error(), "recovery code") {
		t.Errorf("TOTP without the key: %v", err)
	}

	// Another key: the sealer's own error, which names the key, and
	// SealSecrets counts what doesn't open (the webhook's new secret and
	// token were written in plaintext without a key: they're sealed now).
	s.SetSealer(newTestSealer(t))
	if n, bad, err := s.SealSecrets(); err != nil || n != 2 || bad != 6 {
		t.Errorf("SealSecrets with another key = %d sealed, %d unreadable, %v; want 2, 6", n, bad, err)
	}
	if _, err := s.Accounts().TOTPSecret(f.uid); err == nil || !strings.Contains(err.Error(), "secret key") {
		t.Errorf("TOTP with another key: %v", err)
	}
	// Turning 2FA off (dash reset-2fa) still works: it doesn't need the secret.
	if _, err := s.Accounts().ResetTOTP("admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accounts().TOTPSecret(f.uid); !errors.Is(err, ErrTOTPNotSetUp) {
		t.Errorf("after reset: %v", err)
	}
}

// noNewKey is a key generator that must not be called.
func noNewKey() (string, error) { return "", errors.New("made a new key") }
