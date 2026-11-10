package notify

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := decodeB64(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// RFC 8291, Appendix A.
func TestEncryptPushRFC8291(t *testing.T) {
	as, err := ecdh.P256().NewPrivateKey(mustB64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	ua := mustB64(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	got, err := encryptPushWith([]byte("When I grow up, I want to be a watermelon"), ua,
		mustB64(t, "BTBZMqHH6r4Tts7J_aSIgg"), mustB64(t, "DGv6ra1nlYgDCS1FRnbzlw"), as)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if b64.EncodeToString(got) != want {
		t.Fatalf("got  %s\nwant %s", b64.EncodeToString(got), want)
	}
}

// browser is a push subscriber: its keys, and decrypting what it gets.
type browser struct {
	key  *ecdh.PrivateKey
	auth []byte
}

func newBrowser(t *testing.T) browser {
	k, err := ecdh.P256().GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return browser{key: k, auth: []byte("0123456789abcdef")}
}

func (b browser) sub(endpoint string) model.PushSubscription {
	return model.PushSubscription{ID: "ps1", UserID: 7, OrgID: 1, Endpoint: endpoint,
		P256dh: b64.EncodeToString(b.key.PublicKey().Bytes()), Auth: b64.EncodeToString(b.auth)}
}

func (b browser) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs != pushRecordSize || idlen != 65 {
		t.Fatalf("header rs=%d idlen=%d", rs, idlen)
	}
	asPub, err := ecdh.P256().NewPublicKey(body[21 : 21+idlen])
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := b.key.ECDH(asPub)
	ikm, _ := hkdf.Key(sha256.New, shared, b.auth, "WebPush: info\x00"+string(b.key.PublicKey().Bytes())+string(asPub.Bytes()), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	blk, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(blk)
	pt, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if pt[len(pt)-1] != 2 {
		t.Fatalf("delimiter %x", pt[len(pt)-1])
	}
	return pt[:len(pt)-1]
}

// checkVAPID verifies the request's VAPID authorization with key's public
// key, and returns its claims.
func checkVAPID(t *testing.T, hdr http.Header, key *VAPIDKey) map[string]any {
	t.Helper()
	tok, k, ok := strings.Cut(strings.TrimPrefix(hdr.Get("Authorization"), "vapid t="), ", k=")
	if !ok || k != key.Public {
		t.Fatalf("authorization %q", hdr.Get("Authorization"))
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt %q", tok)
	}
	pub := mustB64(t, k)
	x, y := elliptic.Unmarshal(elliptic.P256(), pub) //nolint:staticcheck // test only
	sig := mustB64(t, parts[2])
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("VAPID signature doesn't verify")
	}
	var claims map[string]any
	if err := json.Unmarshal(mustB64(t, parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestVAPIDKeyRoundTrip(t *testing.T) {
	priv, err := NewVAPIDPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	k1, err := ParseVAPIDKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := ParseVAPIDKey(priv)
	if k1.Public != k2.Public || len(mustB64(t, k1.Public)) != 65 {
		t.Fatalf("public key %q", k1.Public)
	}
	if _, err := ParseVAPIDKey("nope"); err == nil {
		t.Fatal("parsed a bad key")
	}
}

func TestCheckPushKeys(t *testing.T) {
	b := newBrowser(t)
	s := b.sub("https://push.example.com/x")
	if err := CheckPushKeys(s.P256dh, s.Auth); err != nil {
		t.Fatal(err)
	}
	if err := CheckPushKeys(s.P256dh+"=", strings.TrimRight(s.Auth, "=")+"=="); err != nil {
		t.Fatalf("padded: %v", err)
	}
	if CheckPushKeys("AAAA", s.Auth) == nil || CheckPushKeys(s.P256dh, "AAAA") == nil {
		t.Fatal("accepted bad keys")
	}
}

// pushStore is a PushStore with fixed subscriptions.
type pushStore struct {
	mu      sync.Mutex
	subs    []model.PushSubscription
	deleted []string
	errs    map[string]string
}

func (p *pushStore) ProjectOrg(string) (int64, bool) { return 1, true }
func (p *pushStore) PushSubscriptionsForOrg(org int64) ([]model.PushSubscription, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]model.PushSubscription(nil), p.subs...), nil
}
func (p *pushStore) DeletePushSubscription(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deleted = append(p.deleted, id)
	return nil
}
func (p *pushStore) RecordPushDelivery(id string, _ time.Time, msg string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.errs == nil {
		p.errs = map[string]string{}
	}
	p.errs[id] = msg
	return nil
}

func TestPushDelivery(t *testing.T) {
	b := newBrowser(t)
	type got struct {
		hdr     http.Header
		payload PushPayload
	}
	ch := make(chan got, 8)
	var mu sync.Mutex
	code := http.StatusCreated
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p PushPayload
		if err := json.Unmarshal(b.decrypt(t, body), &p); err != nil {
			t.Error(err)
		}
		ch <- got{r.Header.Clone(), p}
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(code)
	}))
	defer srv.Close()

	key, _ := NewVAPIDPrivateKey()
	vapid, _ := ParseVAPIDKey(key)
	ps := &pushStore{subs: []model.PushSubscription{b.sub(srv.URL + "/push/abc")}}
	n, fs, _ := setup(t)
	n.Push, n.VAPID, n.Actions, n.client = ps, vapid, NewActions(nil), srv.Client()
	n.Actions.Now = func() time.Time { return t0 }

	status(fs, n, model.StatusReview, "")
	g := <-ch
	if g.payload.Type != model.NotifyReview || g.payload.Title != "Ready for review: Checkout" || g.payload.Body != "Shop" ||
		g.payload.URL != "/?project=p1&task=t1" || g.payload.Tag != "dash-t1" || len(g.payload.Actions) != 0 {
		t.Fatalf("payload %+v", g.payload)
	}
	if g.hdr.Get("Content-Encoding") != "aes128gcm" || g.hdr.Get("TTL") != "86400" || g.hdr.Get("Urgency") != "normal" {
		t.Fatalf("headers %v", g.hdr)
	}
	claims := checkVAPID(t, g.hdr, vapid)
	if claims["aud"] != srv.URL || claims["sub"] != "https://dash.example.com" || claims["exp"] != float64(t0.Add(12*time.Hour).Unix()) {
		t.Fatalf("claims %v", claims)
	}

	// An approval: urgent, short-lived, with Approve/Deny buttons.
	n.Observe(model.Event{Type: model.EvPermission, ProjectID: "p1", Data: perm.Request{ID: "r1", TodoID: "t1", Tool: "Bash"}})
	g = <-ch
	if g.payload.Type != model.NotifyPermission || g.payload.Tag != "dash-perm-r1" || len(g.payload.Actions) != 2 ||
		g.payload.Actions[0].Action != "allow" || !strings.HasPrefix(g.payload.Actions[0].URL, ActionPath) {
		t.Fatalf("payload %+v", g.payload)
	}
	if id, d, err := n.Actions.Parse(strings.TrimPrefix(g.payload.Actions[1].URL, ActionPath)); err != nil || id != "r1" || d != "deny" {
		t.Fatalf("deny token: %s %s %v", id, d, err)
	}
	if g.hdr.Get("Urgency") != "high" || g.hdr.Get("TTL") != "900" {
		t.Fatalf("headers %v", g.hdr)
	}

	// Merged isn't pushed: the next push is the failure.
	status(fs, n, model.StatusMerged, "")
	status(fs, n, model.StatusFailed, "tests failed")
	if g = <-ch; g.payload.Type != model.NotifyFailed || g.payload.Body != "Shop\ntests failed" {
		t.Fatalf("payload %+v", g.payload)
	}
	settle(n)
	if len(ch) != 0 || ps.errs["ps1"] != "" {
		t.Fatalf("%d more pushes, last error %q", len(ch), ps.errs["ps1"])
	}

	// A subscription the browser dropped is deleted.
	mu.Lock()
	code = http.StatusGone
	mu.Unlock()
	if err := n.TestPush(context.Background(), ps.subs[0]); err == nil {
		t.Fatal("no error for a gone subscription")
	}
	if g = <-ch; g.payload.Type != model.NotifyTest || g.payload.URL != "/" {
		t.Fatalf("test payload %+v", g.payload)
	}
	if len(ps.deleted) != 1 || ps.deleted[0] != "ps1" {
		t.Fatalf("deleted %v", ps.deleted)
	}
}

func TestPushRefusesPlainHTTP(t *testing.T) {
	key, _ := NewVAPIDPrivateKey()
	vapid, _ := ParseVAPIDKey(key)
	b := newBrowser(t)
	if _, err := pushRequest(context.Background(), vapid, b.sub("http://push.example.com/x"), "mailto:x@example.com", []byte("{}"), time.Minute, false, t0); err == nil {
		t.Fatal("pushed over http")
	}
}
