package notify

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/klahr/dash/internal/metrics"
	"github.com/klahr/dash/internal/model"
)

// Web Push (RFC 8030) to the browsers that turned notifications on: the
// installed app hears about reviews, failures, approvals and failing builds
// while it is closed. Each push is encrypted for its browser (RFC 8291,
// aes128gcm) and signed with the hub's VAPID key (RFC 8292), so the push
// service (Google's, Mozilla's, Apple's) sees neither what it carries nor
// can it send in Dash's name.

// PushEvents are the events pushed to browsers.
var PushEvents = []model.NotifyEvent{model.NotifyReview, model.NotifyFailed, model.NotifyPermission, model.NotifyBuild}

// PushStore is what Web Push reads and writes: the browsers subscribed to
// a project's org.
type PushStore interface {
	ProjectOrg(projectID string) (int64, bool)
	// PushSubscriptionsForOrg lists the subscriptions of org's members made in org.
	PushSubscriptionsForOrg(org int64) ([]model.PushSubscription, error)
	DeletePushSubscription(id string) error
	RecordPushDelivery(id string, at time.Time, errMsg string) error
}

// ErrPushGone is what a push to a subscription the browser dropped (404,
// 410) gives: it is deleted.
var ErrPushGone = errors.New("the browser's subscription has ended")

var b64 = base64.RawURLEncoding

// decodeB64 accepts base64url with or without padding (browsers send
// either), and standard base64.
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	s = strings.NewReplacer("+", "-", "/", "_").Replace(s)
	return b64.DecodeString(s)
}

// VAPIDKey is the hub's Web Push signing key (P-256).
type VAPIDKey struct {
	priv *ecdsa.PrivateKey
	// Public is the key browsers subscribe with: the uncompressed point,
	// base64url (the applicationServerKey).
	Public string
}

// NewVAPIDPrivateKey makes a new key, as ParseVAPIDKey reads it.
func NewVAPIDPrivateKey() (string, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	b, err := k.Bytes()
	if err != nil {
		return "", err
	}
	return b64.EncodeToString(b), nil
}

// ParseVAPIDKey reads a key made by NewVAPIDPrivateKey: the private
// scalar, base64url.
func ParseVAPIDKey(private string) (*VAPIDKey, error) {
	raw, err := decodeB64(private)
	if err != nil {
		return nil, fmt.Errorf("VAPID key: %w", err)
	}
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("VAPID key: %w", err)
	}
	pub, err := k.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	return &VAPIDKey{priv: k, Public: b64.EncodeToString(pub)}, nil
}

// jwt is the VAPID token for a push service (aud: its origin), valid 12
// hours, with sub as the contact the push service may reach the sender at.
func (k *VAPIDKey) jwt(aud, sub string, now time.Time) (string, error) {
	head := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": aud, "exp": now.Add(12 * time.Hour).Unix(), "sub": sub})
	in := head + "." + b64.EncodeToString(claims)
	h := sha256.Sum256([]byte(in))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return in + "." + b64.EncodeToString(sig), nil
}

// CheckPushKeys validates a browser subscription's keys: p256dh, its P-256
// public key (uncompressed), and auth, its 16-byte secret.
func CheckPushKeys(p256dh, auth string) error {
	pub, err := decodeB64(p256dh)
	if err != nil {
		return errors.New("the subscription's p256dh key isn't base64url")
	}
	if _, err := ecdh.P256().NewPublicKey(pub); err != nil {
		return errors.New("the subscription's p256dh key isn't a P-256 public key")
	}
	sec, err := decodeB64(auth)
	if err != nil || len(sec) != 16 {
		return errors.New("the subscription's auth secret must be 16 bytes, base64url")
	}
	return nil
}

// pushRecordSize is the one record's size (RFC 8188 rs); payloads must
// fit in it with the padding delimiter and the tag.
const pushRecordSize = 4096

// encryptPush encrypts plaintext for a browser (RFC 8291) with a fresh
// ephemeral key and salt.
func encryptPush(plaintext []byte, p256dh, auth string) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	ua, err := decodeB64(p256dh)
	if err != nil {
		return nil, err
	}
	secret, err := decodeB64(auth)
	if err != nil {
		return nil, err
	}
	return encryptPushWith(plaintext, ua, secret, salt, eph)
}

// encryptPushWith is encryptPush with the salt and the application
// server's ephemeral key given (tests use RFC 8291's example).
func encryptPushWith(plaintext, uaPublic, authSecret, salt []byte, as *ecdh.PrivateKey) ([]byte, error) {
	if len(plaintext)+17 > pushRecordSize {
		return nil, errors.New("push payload too large")
	}
	uaKey, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, err
	}
	shared, err := as.ECDH(uaKey)
	if err != nil {
		return nil, err
	}
	asPublic := as.PublicKey().Bytes()
	info := "WebPush: info\x00" + string(uaPublic) + string(asPublic)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, info, 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(salt)
	_ = binary.Write(&out, binary.BigEndian, uint32(pushRecordSize))
	out.WriteByte(byte(len(asPublic)))
	out.Write(asPublic)
	record := append(append([]byte{}, plaintext...), 0x02) // the last (only) record's delimiter
	out.Write(gcm.Seal(nil, nonce, record, nil))
	return out.Bytes(), nil
}

// PushAction is a button on a pushed notification: the service worker
// posts to URL (a path on Dash) when it's pressed.
type PushAction struct {
	Action string `json:"action"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

// PushPayload is what the service worker (web/static/sw.js) gets: what to
// show, and where a click goes (a path on Dash).
type PushPayload struct {
	Type    model.NotifyEvent `json:"type"`
	Title   string            `json:"title"`
	Body    string            `json:"body,omitempty"`
	Tag     string            `json:"tag"`
	URL     string            `json:"url"`
	Actions []PushAction      `json:"actions,omitempty"`
}

// pushPayload is how n shows on a phone or desktop. tag matches the one the
// open app uses for the same task, so one replaces the other.
func pushPayload(n Notification, actions []PushAction) PushPayload {
	p := PushPayload{Type: n.Type, Title: n.Head() + ": " + oneLine(n.subject(), 120), URL: "/", Actions: actions}
	var body []string
	if n.Task != nil && n.Project.Name != "" {
		body = append(body, n.Project.Name)
	}
	if n.Message != "" {
		body = append(body, oneLine(n.Message, 300))
	}
	p.Body = strings.Join(body, "\n")
	q := url.Values{}
	if n.Project.ID != "" {
		q.Set("project", n.Project.ID)
	}
	switch {
	case n.Task != nil && n.Task.ID != "" && n.Type != model.NotifyTest:
		q.Set("task", n.Task.ID)
		p.Tag = "dash-" + n.Task.ID
	case n.Type == model.NotifyBuild:
		p.Tag = "dash-build-" + n.Project.ID
	default:
		p.Tag = "dash-" + n.ID
	}
	if n.Type == model.NotifyPermission && n.Permission != nil {
		p.Tag = "dash-perm-" + n.Permission.ID
	}
	if len(q) > 0 {
		p.URL = "/?" + q.Encode()
	}
	return p
}

// pushRequest builds the request delivering payload to sub, signed with
// key; sub is the VAPID contact.
func pushRequest(ctx context.Context, key *VAPIDKey, sub model.PushSubscription, contact string, payload []byte, ttl time.Duration, urgent bool, now time.Time) (*http.Request, error) {
	u, err := url.Parse(sub.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("the subscription's endpoint isn't an https URL")
	}
	body, err := encryptPush(payload, sub.P256dh, sub.Auth)
	if err != nil {
		return nil, err
	}
	tok, err := key.jwt(u.Scheme+"://"+u.Host, contact, now)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "vapid t="+tok+", k="+key.Public)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(ttl/time.Second)))
	if urgent {
		req.Header.Set("Urgency", "high")
	} else {
		req.Header.Set("Urgency", "normal")
	}
	req.Header.Set("User-Agent", "Dash-Push/1")
	return req, nil
}

// pushContact is the VAPID "sub" claim: Dash's own address when it has an
// https one, which tells the push service whose pushes these are.
func pushContact(origin string) string {
	if strings.HasPrefix(origin, "https://") {
		return origin
	}
	return "mailto:webpush@dash.invalid"
}

// sendPush makes one attempt to push payload to sub.
func (n *Notifier) sendPush(ctx context.Context, sub model.PushSubscription, payload []byte, projectID string, ttl time.Duration, urgent bool) error {
	req, err := pushRequest(ctx, n.VAPID, sub, pushContact(n.origin(projectID)), payload, ttl, urgent, n.now())
	if err != nil {
		return errPermanent{err}
	}
	resp, err := n.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the URL: endpoints are capabilities
		}
		if errors.Is(err, ErrPrivateAddress) {
			return errPermanent{err}
		}
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return errPermanent{ErrPushGone}
	case resp.StatusCode >= 500 || resp.StatusCode == 429:
		return fmt.Errorf("the push service answered %s", resp.Status)
	default:
		return errPermanent{fmt.Errorf("the push service answered %s", resp.Status)}
	}
}

// PushEnabled reports whether this notifier pushes to browsers.
func (n *Notifier) PushEnabled() bool { return n != nil && n.Push != nil && n.VAPID != nil }

// push sends note to the browsers subscribed to its project's org, if it
// is one of PushEvents.
func (n *Notifier) push(ctx context.Context, note Notification) {
	if !n.PushEnabled() || !slices.Contains(PushEvents, note.Type) {
		return
	}
	org, ok := n.Push.ProjectOrg(note.Project.ID)
	if !ok {
		return
	}
	subs, err := n.Push.PushSubscriptionsForOrg(org)
	if err != nil {
		slog.Error("notifications: list push subscriptions", "org", org, "err", err)
		return
	}
	if len(subs) == 0 {
		return
	}
	var actions []PushAction
	ttl := 24 * time.Hour
	if note.Type == model.NotifyPermission && note.Permission != nil {
		ttl = DefaultActionTTL // an approval that waits longer is likely answered or over
		if n.Actions != nil {
			if n.Actions.TTL > 0 {
				ttl = n.Actions.TTL
			}
			exp := n.now().Add(ttl)
			actions = []PushAction{
				{Action: "allow", Title: "Approve", URL: ActionPath + n.Actions.Token(note.Permission.ID, "allow", exp)},
				{Action: "deny", Title: "Deny", URL: ActionPath + n.Actions.Token(note.Permission.ID, "deny", exp)},
			}
		}
	}
	payload, err := json.Marshal(pushPayload(note, actions))
	if err != nil {
		return
	}
	urgent := looks[note.Type].high
	for _, s := range subs {
		n.wg.Add(1)
		go func(s model.PushSubscription) {
			defer n.wg.Done()
			_ = n.deliverPush(ctx, s, payload, note.Project.ID, ttl, urgent, n.attempts())
		}(s)
	}
}

// deliverPush pushes payload to sub, with retries, and records how it
// went; a subscription the browser dropped is deleted.
func (n *Notifier) deliverPush(ctx context.Context, sub model.PushSubscription, payload []byte, projectID string, ttl time.Duration, urgent bool, attempts int) error {
	err := n.retry(ctx, attempts, func() error { return n.sendPush(ctx, sub, payload, projectID, ttl, urgent) })
	switch {
	case err == nil:
		metrics.PushDeliveries.Inc("ok")
	case errors.Is(err, ErrPushGone):
		metrics.PushDeliveries.Inc("gone")
		if derr := n.Push.DeletePushSubscription(sub.ID); derr != nil {
			slog.Error("notifications: delete push subscription", "subscription", sub.ID, "err", derr)
		}
		return err
	default:
		metrics.PushDeliveries.Inc("failed")
		host := ""
		if u, perr := url.Parse(sub.Endpoint); perr == nil {
			host = u.Hostname()
		}
		slog.Warn("push delivery failed", "subscription", sub.ID, "user", sub.UserID, "host", host, "err", err)
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if rerr := n.Push.RecordPushDelivery(sub.ID, n.now(), msg); rerr != nil {
		slog.Error("notifications: record push delivery", "subscription", sub.ID, "err", rerr)
	}
	return err
}

// TestPush pushes a test notification to sub once, now, and reports how
// it went.
func (n *Notifier) TestPush(ctx context.Context, sub model.PushSubscription) error {
	if !n.PushEnabled() {
		return errors.New("this Dash doesn't push notifications")
	}
	n.init()
	note := Notification{ID: newEventID(), Type: model.NotifyTest, Time: n.now().UTC(), Project: ProjectRef{Name: "Dash"},
		Message: "Notifications reach this device, even when Dash is closed."}
	payload, err := json.Marshal(pushPayload(note, nil))
	if err != nil {
		return err
	}
	return n.deliverPush(ctx, sub, payload, "", time.Hour, false, 1)
}
