package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/ci"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// GitHub's deliveries reach POST /api/github/hook without signing in, and
// only those the repository's secret signed are taken.
func TestGitHubHookEndpoint(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	var postTo func(path, body, sig string) *httptest.ResponseRecorder
	post := func(body, sig string) *httptest.ResponseRecorder {
		t.Helper()
		return postTo(ci.HookPath+"?org=0", body, sig)
	}
	postTo = func(path, body, sig string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("X-GitHub-Event", "ping")
		r.Header.Set("Content-Type", "application/json")
		if sig != "" {
			r.Header.Set("X-Hub-Signature-256", sig)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	ping := `{"zen":"hi","repository":{"full_name":"o/r"}}`

	// Off (no public URL): not found, rather than the login.
	if w := post(ping, ""); w.Code != http.StatusNotFound {
		t.Fatalf("off: %d %s", w.Code, w.Body)
	}

	st := s.Store.(*store.Store)
	s.GitHubHooks = &ci.Hooks{Store: st, PublicURL: "https://dash.example.com"}
	if err := st.PutGitHubHook(model.GitHubHook{Repo: "o/r", HookID: 1, Secret: "s3cret", URL: "https://dash.example.com/api/github/hook",
		State: ci.HookPending, Registered: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(ping))
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if w := post(ping, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned: %d %s", w.Code, w.Body)
	}
	if w := post(ping, "sha256=00"); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d %s", w.Code, w.Body)
	}
	if w := post(ping, good); w.Code != http.StatusOK {
		t.Fatalf("signed: %d %s", w.Code, w.Body)
	}
	if rec, _ := st.GitHubHook(0, "o/r"); rec.LastDelivery.IsZero() {
		t.Fatal("delivery not noted")
	}
	// Signed with the secret, but to no org's webhook, or another org's.
	for _, path := range []string{ci.HookPath, ci.HookPath + "?org=2", ci.HookPath + "?org=x"} {
		if w := postTo(path, ping, good); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	big := `{"repository":{"full_name":"o/r"},"pad":"` + strings.Repeat("x", maxHookBody) + `"}`
	if w := post(big, good); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large: %d", w.Code)
	}
}
