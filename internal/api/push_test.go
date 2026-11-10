package api

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/notify"
)

func TestPushAPI(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.mux()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, asOwner(t, s, r))
		return w
	}

	// Off: no notifier (a runner), or no key.
	if w := call("GET", "/api/push", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) || strings.Contains(w.Body.String(), `"key"`) {
		t.Fatalf("info off: %d %s", w.Code, w.Body)
	}
	k, _ := ecdh.P256().GenerateKey(rand.Reader)
	enc := base64.RawURLEncoding
	sub := func(endpoint string) string {
		b, _ := json.Marshal(map[string]any{"endpoint": endpoint, "keys": map[string]string{
			"p256dh": enc.EncodeToString(k.PublicKey().Bytes()), "auth": enc.EncodeToString([]byte("0123456789abcdef"))}})
		return string(b)
	}
	if w := call("PUT", "/api/push/subscription", sub("https://push.example.com/a")); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("subscribe while off: %d %s", w.Code, w.Body)
	}

	key, _ := notify.NewVAPIDPrivateKey()
	vapid, _ := notify.ParseVAPIDKey(key)
	s.Notify = &notify.Notifier{Store: raw(s), Push: raw(s), VAPID: vapid, Attempts: 1}
	w := call("GET", "/api/push", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"key":"`+vapid.Public+`"`) || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("info: %d %s", w.Code, w.Body)
	}

	for _, bad := range []string{
		sub("http://push.example.com/a"),      // not https
		sub("https://169.254.169.254/latest"), // into this Dash's network
		`{"endpoint":"https://push.example.com/a","keys":{"p256dh":"AAAA","auth":"AAAA"}}`,
	} {
		if w := call("PUT", "/api/push/subscription", bad); w.Code != http.StatusBadRequest {
			t.Errorf("subscribe %s: %d %s", bad, w.Code, w.Body)
		}
	}
	w = call("PUT", "/api/push/subscription", sub("https://push.example.com/a"))
	if w.Code != 200 || strings.Contains(w.Body.String(), "push.example.com") {
		t.Fatalf("subscribe: %d %s", w.Code, w.Body)
	}
	// Subscribing again is the same subscription.
	if w := call("PUT", "/api/push/subscription", sub("https://push.example.com/a")); w.Code != 200 {
		t.Fatalf("resubscribe: %d %s", w.Code, w.Body)
	}
	org, _ := s.Store.ProjectOrg(p.ID)
	subs, err := raw(s).PushSubscriptionsForOrg(org)
	if err != nil || len(subs) != 1 || subs[0].Endpoint != "https://push.example.com/a" || subs[0].UserID != ownerCaller(t, s).User {
		t.Fatalf("org's subscriptions %+v %v", subs, err)
	}
	if other, _ := raw(s).PushSubscriptionsForOrg(org + 1000); len(other) != 0 {
		t.Fatalf("another org's subscriptions %+v", other)
	}

	if w := call("DELETE", "/api/push/subscription", `{"endpoint":"https://push.example.com/a"}`); w.Code != http.StatusNoContent {
		t.Fatalf("unsubscribe: %d %s", w.Code, w.Body)
	}
	if w := call("DELETE", "/api/push/subscription", `{"endpoint":"https://push.example.com/a"}`); w.Code != http.StatusNotFound {
		t.Fatalf("unsubscribe twice: %d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/push/test", `{"endpoint":"https://push.example.com/a"}`); w.Code != http.StatusNotFound {
		t.Fatalf("test of no subscription: %d %s", w.Code, w.Body)
	}
}
