package tunnel

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/fsutil"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/notes"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/usage"
)

// EvRunners tells dashboards that a runner registered, connected,
// disconnected or was revoked.
const EvRunners model.EventType = "runners"

const (
	codeTTL         = 10 * time.Minute
	codeAlphabet    = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ" // no 0/O, 1/I
	codeLen         = 8                                  // 40 bits
	maxBadCodes     = 20                                 // wrong codes per badCodeWindow before registration pauses
	badCodeWindow   = 10 * time.Minute
	runnerHostLabel = ".runner.invalid" // made-up host the proxy addresses a runner by
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Errors from register.
var (
	ErrBadCode     = errors.New("unknown or expired registration code")
	ErrTooManyBad  = errors.New("too many wrong registration codes, try again later")
	ErrNameTaken   = errors.New("that name is already used by another computer; revoke it first or pick another --name")
	ErrNameHub     = errors.New("that name is the hub's own computer; pick another --name")
	ErrBadName     = errors.New("runner name: use lowercase letters, digits and dashes (at most 32)")
	ErrUnknownName = errors.New("no such runner")
	ErrNoOwner     = errors.New("a registration code is made by a signed-in user, for their organization")
)

// Runner is a registered runner as the dashboard shows it.
type Runner struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Online   bool      `json:"online"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"last_seen,omitzero"` // when it last connected or disconnected
	// Owner registered the runner: its token acts as them, in their org.
	Owner   *store.RunnerOwner `json:"owner,omitempty"`
	Version string             `json:"version,omitempty"` // the Dash it last connected with, e.g. 1.2.0
	// NotesKey is the public key the runner seals shared notes keys with,
	// as it told the hub, and NotesFingerprint its short form. Shown so a
	// user knows which fingerprint to look for; runners never take it from
	// the hub (they pin what peers send, and the user approves it on them).
	NotesKey         string `json:"notes_key,omitempty"`
	NotesFingerprint string `json:"notes_fingerprint,omitempty"`
	// Parallel is how many tasks the hub sends it at once (0: as many as
	// the runner's own -parallel, which it reports). Paused: the hub sends
	// it none.
	Parallel int  `json:"parallel"`
	Paused   bool `json:"paused"`
	// AutoUpdate: the hub updates it when it connects running another
	// version than the hub's release. Update is its update in progress
	// (nil: none): it is in maintenance mode, taking no new tasks, apart
	// from Paused. UpdateError is why its last update failed (update.go).
	AutoUpdate  bool    `json:"auto_update"`
	Update      *Update `json:"update,omitempty"`
	UpdateError string  `json:"update_error,omitempty"`
	// Reported is what the runner last told the hub about itself: its own
	// limit and its owner's Claude subscription usage (nil until it has).
	Reported *Reported `json:"reported,omitempty"`
	// Running is how many of the hub's tasks it is working on (filled in by
	// the API).
	Running int `json:"running"`
}

// OrgID is the org the runner works for (0: nobody's; it gets no work).
func (r Runner) OrgID() int64 {
	if r.Owner == nil {
		return 0
	}
	return r.Owner.Org.ID
}

// Limit is how many of the hub's tasks the runner takes at once.
func (r Runner) Limit() int {
	switch {
	case r.Parallel > 0:
		return r.Parallel
	case r.Reported != nil && r.Reported.Parallel > 0:
		return r.Reported.Parallel
	}
	return 1
}

// Reported is a runner's report on itself.
type Reported struct {
	Parallel int             `json:"parallel"`
	Usage    *usage.Snapshot `json:"usage,omitempty"`
	// Terminals: it opens terminals for its owner from the hub (false from
	// an older runner started without -hub-commands).
	Terminals bool `json:"terminals"`
	// ChatCommands: its chats run "!" commands for its owner (its chat
	// isn't read-only).
	ChatCommands bool `json:"chat_commands,omitempty"`
	// Claude is its Claude Code CLI: version and warnings (nil until a
	// runner that knows to report it has).
	Claude *claudecli.Status `json:"claude,omitempty"`
	At     time.Time         `json:"at"`
}

// record is a runner in runners.json. Its token is in the token table
// (store.TokenRunner), as its owner's, pinned to their org.
type record struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// TokenHash is set in files from before the token table: Keep moves it there.
	TokenHash string    `json:"token_hash,omitempty"`
	Created   time.Time `json:"created"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
	Parallel  int       `json:"parallel,omitempty"`
	Paused    bool      `json:"paused,omitempty"`
	Version   string    `json:"version,omitempty"`
	NotesKey  string    `json:"notes_key,omitempty"` // public: see Runner.NotesKey
	// Its self-update (update.go).
	AutoUpdate bool          `json:"auto_update,omitempty"`
	Update     *Update       `json:"update,omitempty"`
	LastUpdate *UpdateResult `json:"last_update,omitempty"`
}

// regCode is a registration code, made by Owner: the runner registered with
// it will be theirs.
type regCode struct {
	Hash    string      `json:"hash"` // sha256 of the normalised code, hex
	Expires time.Time   `json:"expires"`
	Owner   model.Owner `json:"owner"`
}

// Hub is the dashboard's side: it hands out registration codes, swaps them
// for runner tokens, accepts runners' tunnels and forwards /peer/<name>/…
// requests through them.
type Hub struct {
	// Pub is told when the runner list changes (optional).
	Pub interface{ Publish(model.Event) }
	// Taken reports names already used by this computer (optional).
	Taken func(name string) bool
	// PreviewURL is where the hub serves runners' previews, with "*" for the
	// host's first label, as the runner connecting with r should be told
	// (optional; "" without a preview domain). See PreviewHeader.
	PreviewURL func(r *http.Request) string
	// Accounts keep the runners' tokens.
	Accounts *store.Accounts
	// OnState is told when a runner's tunnel comes up or goes down, and
	// OnRevoke when a runner is revoked (both optional, called without
	// locks held).
	OnState  func(runnerID string, online bool)
	OnRevoke func(runnerID string)
	// OnReport handles a runner's POST /runner/report body (optional).
	OnReport func(runnerID string, body []byte) error
	// ReportReply is the hub's answer to a runner's report (optional; nil:
	// none, 204). The hub tells a runner how it paces it (dispatch.Reply).
	ReportReply func(runnerID string) any
	// Release is the release runners update to: the hub's own version (""
	// for a build from source: runners aren't updated). See update.go.
	Release string

	now func() time.Time

	mu       sync.Mutex
	path     string
	runners  []*record
	codes    []regCode
	live     map[string]*Session     // runner id → its tunnel
	previews map[string]*previewLink // runner id → its preview connection (preview.go)
	badAt    []time.Time             // recent wrong registration codes
	reported map[string]*Reported    // runner id → its last report on itself

	transportOnce sync.Once
	transport     *http.Transport
}

func (h *Hub) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// Keep loads runners and codes from path and saves every change there.
func (h *Hub) Keep(path string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f struct {
		Runners []*record `json:"runners"`
		Codes   []regCode `json:"codes"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	h.runners = f.Runners
	// Codes from before codes had owners can't make a runner anyone's.
	h.codes = slices.DeleteFunc(f.Codes, func(c regCode) bool { return !c.Owner.Valid() })
	// Runners from before the token table: their tokens move there, as the
	// env account's (the only account then).
	moved := false
	for _, r := range h.runners {
		if r.TokenHash == "" {
			continue
		}
		err := h.Accounts.ImportRunnerToken(r.ID, r.Name, r.TokenHash, r.Created)
		if err != nil && !errors.Is(err, store.ErrNoSoleOrg) && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err != nil {
			slog.Warn("runner must register again: no account to own its token", "name", r.Name, "err", err)
		}
		r.TokenHash, moved = "", true
	}
	// An update the hub was telling a runner about when it stopped: the
	// runner either restarts on the new version or is asked again.
	for _, r := range h.runners {
		if r.Update != nil && r.Update.Phase == UpdateUpdating {
			r.Update.Phase = UpdateWaiting
		}
	}
	if moved {
		h.saveLocked()
	}
	return nil
}

func (h *Hub) saveLocked() {
	if h.path == "" {
		return
	}
	b, _ := json.MarshalIndent(map[string]any{"runners": h.runners, "codes": h.codes}, "", "  ")
	if err := fsutil.WriteFileAtomic(h.path, b, 0o600); err != nil {
		slog.Warn("save runners", "err", err)
	}
}

func (h *Hub) changed() {
	if h.Pub != nil {
		h.Pub.Publish(model.Event{Type: EvRunners})
	}
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func normCode(s string) string {
	s = strings.ToUpper(s)
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, s)
}

// NewCode makes a single-use registration code, valid for ten minutes, for
// owner: the runner registered with it acts as them, in their org.
func (h *Hub) NewCode(owner model.Owner) (code string, expires time.Time, err error) {
	if !owner.Valid() {
		return "", time.Time{}, ErrNoOwner
	}
	b := make([]byte, codeLen)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)] // 256 is a multiple of 32: unbiased
	}
	code = string(b[:4]) + "-" + string(b[4:])
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock()
	h.pruneCodesLocked(now)
	expires = now.Add(codeTTL)
	h.codes = append(h.codes, regCode{Hash: hash(normCode(code)), Expires: expires, Owner: owner})
	h.saveLocked()
	return code, expires, nil
}

func (h *Hub) pruneCodesLocked(now time.Time) {
	h.codes = slices.DeleteFunc(h.codes, func(c regCode) bool { return !now.Before(c.Expires) })
}

// register swaps a registration code for a new runner's id and token (the
// token is shown only now; the hub keeps its hash). prev is the runner token
// the computer was given if it registered before (optional). If it is still
// valid, the new registration replaces that runner, and may take its name.
func (h *Hub) register(code, name, prev string) (id, token string, owner model.Owner, err error) {
	name = strings.ToLower(strings.TrimSpace(name))
	var prevID string
	if prev != "" {
		if old, ok := h.authenticate(prev); ok {
			prevID = old.ID
		}
	}
	defer func() { // runs after the deferred Unlock below
		if err == nil && prevID != "" {
			if rerr := h.Revoke(prevID); rerr != nil && !errors.Is(rerr, ErrUnknownName) {
				slog.Error("retire re-registered runner", "id", prevID, "err", rerr)
			}
		}
	}()
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock()
	h.badAt = slices.DeleteFunc(h.badAt, func(t time.Time) bool { return now.Sub(t) > badCodeWindow })
	if len(h.badAt) >= maxBadCodes {
		return "", "", model.Owner{}, ErrTooManyBad
	}
	h.pruneCodesLocked(now)
	want := hash(normCode(code))
	i := slices.IndexFunc(h.codes, func(c regCode) bool {
		return subtle.ConstantTimeCompare([]byte(c.Hash), []byte(want)) == 1
	})
	if i < 0 {
		h.badAt = append(h.badAt, now)
		return "", "", model.Owner{}, ErrBadCode
	}
	if !nameRe.MatchString(name) {
		return "", "", model.Owner{}, ErrBadName // the code stays usable with a better name
	}
	if h.Taken != nil && h.Taken(name) {
		return "", "", model.Owner{}, ErrNameHub
	}
	if slices.ContainsFunc(h.runners, func(r *record) bool { return r.Name == name && r.ID != prevID }) {
		return "", "", model.Owner{}, ErrNameTaken
	}
	owner = h.codes[i].Owner
	h.codes = slices.Delete(h.codes, i, i+1) // single use
	id = randomString(9)
	token, _, err = h.Accounts.CreateToken(store.NewToken{
		Kind: store.TokenRunner, Name: "Runner " + name, UserID: owner.User, OrgID: owner.Org, RunnerID: id,
	}, now)
	if err != nil {
		h.saveLocked()
		if errors.Is(err, store.ErrNotMember) {
			return "", "", model.Owner{}, fmt.Errorf("%w: whoever made the code has left that organization", ErrBadCode)
		}
		return "", "", model.Owner{}, err
	}
	h.runners = append(h.runners, &record{ID: id, Name: name, Created: now})
	h.saveLocked()
	go h.changed()
	return id, token, owner, nil
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// authenticate finds the runner a token belongs to. The token stops
// working once revoked, or once its owner leaves the org it is pinned to.
func (h *Hub) authenticate(token string) (record, bool) {
	t, _, err := h.Accounts.Authenticate(token, h.clock(), store.TokenRunner)
	if err != nil {
		if !errors.Is(err, store.ErrTokenInvalid) {
			slog.Error("look up runner token", "err", err)
		}
		return record{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.runners {
		if r.ID == t.RunnerID {
			return *r, true
		}
	}
	return record{}, false
}

// Revoke deletes a runner: its token stops working and its tunnel is closed.
func (h *Hub) Revoke(id string) error {
	h.mu.Lock()
	i := slices.IndexFunc(h.runners, func(r *record) bool { return r.ID == id })
	if i < 0 {
		h.mu.Unlock()
		return ErrUnknownName
	}
	if err := h.Accounts.DeleteRunnerToken(id); err != nil {
		h.mu.Unlock()
		return err
	}
	h.runners = slices.Delete(h.runners, i, i+1)
	delete(h.reported, id)
	sess, pv := h.live[id], h.previews[id]
	delete(h.live, id)
	delete(h.previews, id)
	h.saveLocked()
	h.mu.Unlock()
	if sess != nil {
		sess.Close()
	}
	if pv != nil {
		pv.s.Close()
	}
	if h.OnRevoke != nil {
		h.OnRevoke(id)
	}
	h.changed()
	return nil
}

// Counts is how many runners are registered, and how many of them are
// connected now (/metrics).
func (h *Hub) Counts() (registered, connected int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.runners {
		if _, ok := h.live[r.ID]; ok {
			connected++
		}
	}
	return len(h.runners), connected
}

// List returns the registered runners, by name.
func (h *Hub) List() []Runner {
	owners, err := h.Accounts.RunnerOwners()
	if err != nil {
		slog.Error("runner owners", "err", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Runner, 0, len(h.runners))
	for _, r := range h.runners {
		_, online := h.live[r.ID]
		var owner *store.RunnerOwner
		if o, ok := owners[r.ID]; ok {
			owner = &o
		}
		out = append(out, h.viewLocked(r, online, owner))
	}
	slices.SortFunc(out, func(a, b Runner) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// ListBare is List without the runners' owners: cheap enough to call for
// every task the hub dispatches (dispatch.Pacer).
func (h *Hub) ListBare() []Runner {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Runner, 0, len(h.runners))
	for _, r := range h.runners {
		_, online := h.live[r.ID]
		out = append(out, h.viewLocked(r, online, nil))
	}
	return out
}

func (h *Hub) viewLocked(r *record, online bool, owner *store.RunnerOwner) Runner {
	v := Runner{ID: r.ID, Name: r.Name, Online: online, Created: r.Created, LastSeen: r.LastSeen, Owner: owner,
		Version: r.Version, Parallel: r.Parallel, Paused: r.Paused, AutoUpdate: r.AutoUpdate, NotesKey: r.NotesKey}
	if k, err := notes.ParsePublicKey(r.NotesKey); err == nil {
		v.NotesFingerprint = k.Fingerprint()
	}
	if r.Update != nil {
		cp := *r.Update
		v.Update = &cp
	}
	if r.LastUpdate != nil {
		v.UpdateError = r.LastUpdate.Error
	}
	if rep := h.reported[r.ID]; rep != nil {
		cp := *rep
		v.Reported = &cp
	}
	return v
}

// Get returns the runner with id.
func (h *Hub) Get(id string) (Runner, bool) {
	owners, err := h.Accounts.RunnerOwners()
	if err != nil {
		slog.Error("runner owners", "err", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	i := slices.IndexFunc(h.runners, func(r *record) bool { return r.ID == id })
	if i < 0 {
		return Runner{}, false
	}
	var owner *store.RunnerOwner
	if o, ok := owners[id]; ok {
		owner = &o
	}
	_, online := h.live[id]
	return h.viewLocked(h.runners[i], online, owner), true
}

// Configure changes how many tasks the hub sends runner id at once
// (parallel, 0 = the runner's own limit) and whether it sends any (paused).
// Nil leaves a setting as it is.
func (h *Hub) Configure(id string, parallel *int, paused *bool) error {
	h.mu.Lock()
	i := slices.IndexFunc(h.runners, func(r *record) bool { return r.ID == id })
	if i < 0 {
		h.mu.Unlock()
		return ErrUnknownName
	}
	if parallel != nil {
		h.runners[i].Parallel = max(0, min(*parallel, 64))
	}
	if paused != nil {
		h.runners[i].Paused = *paused
	}
	h.saveLocked()
	h.mu.Unlock()
	h.changed()
	return nil
}

// Has reports whether name is a registered runner.
func (h *Hub) Has(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.ContainsFunc(h.runners, func(r *record) bool { return r.Name == name })
}

func (h *Hub) session(name string) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.runners {
		if r.Name == name {
			return h.live[r.ID]
		}
	}
	return nil
}

func (h *Hub) registeredLocked(id string) bool {
	return slices.ContainsFunc(h.runners, func(x *record) bool { return x.ID == id })
}

// touch records that runner id (dis)connected, running version and with
// shared notes key notesKey when those aren't "" (it connected: its update
// is settled, see connectedLocked).
func (h *Hub) touch(id, version, notesKey string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.runners {
		if r.ID == id {
			r.LastSeen = h.clock()
			if version != "" {
				r.Version = version
				h.connectedLocked(r, version)
			}
			if notesKey != "" {
				r.NotesKey = notesKey
			}
		}
	}
	h.saveLocked()
}

// VersionHeader carries the runner's Dash version when it connects.
const VersionHeader = "X-Dash-Version"

// NotesKeyHeader carries the runner's shared notes public key when it
// connects (notes.PublicKey, base64).
const NotesKeyHeader = "X-Dash-Notes-Key"

var versionRe = regexp.MustCompile(`^[0-9A-Za-z.+_-]{1,64}$`)

// cleanVersion is the version a runner sent, or "" if it sent none or junk.
func cleanVersion(v string) string {
	if versionRe.MatchString(v) {
		return v
	}
	return ""
}

// ---- HTTP: registration and the runners' tunnels ----

// ServeRegister handles POST /runner/register {"code","name"} from
// `dash runner --code`, answering {"id","name","token"}. It needs no session:
// the code is the credential.
func (h *Hub) ServeRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
		Name string `json:"name"`
		// Previous is the token this computer registered with before, if any.
		Previous string `json:"previous,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	id, token, owner, err := h.register(req.Code, req.Name, req.Previous)
	switch {
	case errors.Is(err, ErrTooManyBad):
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrBadCode):
		time.Sleep(300 * time.Millisecond)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrNameTaken), errors.Is(err, ErrNameHub):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		slog.Info("runner registered", "name", strings.ToLower(strings.TrimSpace(req.Name)), "id", id)
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": strings.ToLower(strings.TrimSpace(req.Name)), "token": token, "org_id": owner.Org, "user_id": owner.User})
	}
}

// upgrader accepts runners only: they send no Origin, and a browser can't
// set the Authorization header a runner authenticates with.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	CheckOrigin:     func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
}

// ServeConnect handles GET /runner/connect: a runner's tunnel, authenticated
// with "Authorization: Bearer <runner token>", or with ?channel=preview its
// preview connection (preview.go). It returns when the connection closes.
func (h *Hub) ServeConnect(w http.ResponseWriter, r *http.Request) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	rec, ok := h.authenticate(tok)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown or revoked runner token"})
		return
	}
	switch r.URL.Query().Get("channel") {
	case "":
	case previewChannel:
		h.servePreviewConnect(w, r, rec)
		return
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown channel"})
		return
	}
	var hdr http.Header
	if h.PreviewURL != nil {
		if u := h.PreviewURL(r); u != "" {
			hdr = http.Header{PreviewHeader: {u}}
		}
	}
	ws, err := upgrader.Upgrade(w, r, hdr)
	if err != nil {
		return
	}
	sess := NewSession(ws, true) // the hub opens the streams
	h.mu.Lock()
	if !h.registeredLocked(rec.ID) {
		h.mu.Unlock() // revoked while upgrading
		sess.Close()
		return
	}
	if h.live == nil {
		h.live = map[string]*Session{}
	}
	old := h.live[rec.ID]
	h.live[rec.ID] = sess
	h.mu.Unlock()
	if old != nil {
		old.Close() // the runner reconnected before we noticed the old link die
	}
	version := cleanVersion(r.Header.Get(VersionHeader))
	var notesKey string
	if k, err := notes.ParsePublicKey(r.Header.Get(NotesKeyHeader)); err == nil {
		notesKey = k.String()
	}
	h.touch(rec.ID, version, notesKey)
	slog.Info("runner connected", "name", rec.Name, "version", version)
	h.changed()
	if h.OnState != nil {
		h.OnState(rec.ID, true)
	}

	<-sess.Done()

	h.mu.Lock()
	current := h.live[rec.ID] == sess
	if current {
		delete(h.live, rec.ID)
	}
	h.mu.Unlock()
	h.idle().CloseIdleConnections() // pooled streams of the dead tunnel
	if current {
		h.touch(rec.ID, "", "")
		slog.Info("runner disconnected", "name", rec.Name)
		h.changed()
		if h.OnState != nil {
			h.OnState(rec.ID, false)
		}
	}
}

// ServeReport handles POST /runner/report from a runner, authenticated with
// its token like its tunnel: what it is working on for the hub and its
// usage. The body goes to OnReport, which accepts news only of the hub's
// tasks sent to that runner.
func (h *Hub) ServeReport(w http.ResponseWriter, r *http.Request) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	rec, ok := h.authenticate(tok)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown or revoked runner token"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	var self struct {
		Parallel     int               `json:"parallel"`
		Usage        *usage.Snapshot   `json:"usage"`
		Terminals    bool              `json:"terminals"`
		ChatCommands bool              `json:"chat_commands"`
		Claude       *claudecli.Status `json:"claude"`
	}
	if err := json.Unmarshal(body, &self); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	h.mu.Lock()
	if h.reported == nil {
		h.reported = map[string]*Reported{}
	}
	prev := h.reported[rec.ID]
	if self.Claude != nil {
		self.Claude = cleanClaude(*self.Claude)
	}
	h.reported[rec.ID] = &Reported{Parallel: self.Parallel, Usage: self.Usage, Terminals: self.Terminals, ChatCommands: self.ChatCommands, Claude: self.Claude, At: h.clock()}
	h.mu.Unlock()
	if prev == nil || prev.Parallel != self.Parallel || prev.Terminals != self.Terminals || prev.ChatCommands != self.ChatCommands || paused(prev.Usage) != paused(self.Usage) ||
		claudeKey(prev.Claude) != claudeKey(self.Claude) {
		h.changed()
	}
	if h.OnReport != nil {
		if err := h.OnReport(rec.ID, body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if h.ReportReply != nil {
		if v := h.ReportReply(rec.ID); v != nil {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func paused(u *usage.Snapshot) bool { return u != nil && u.Paused }

// cleanClaude bounds what a runner reports about its Claude Code CLI.
func cleanClaude(c claudecli.Status) *claudecli.Status {
	c.Version = cleanVersion(c.Version)
	if len(c.Warnings) > 10 {
		c.Warnings = c.Warnings[:10]
	}
	ws := make([]string, 0, len(c.Warnings))
	for _, w := range c.Warnings {
		if len(w) > 500 {
			w = w[:500] + "…"
		}
		ws = append(ws, w)
	}
	if len(ws) == 0 {
		ws = nil
	}
	c.Warnings = ws
	if a := c.Account; a != nil {
		cp := claudecli.Account{Email: clip(strings.TrimSpace(a.Email), 254), OrgID: clip(a.OrgID, 100),
			OrgName: clip(a.OrgName, 200), Subscription: clip(a.Subscription, 40)}
		c.Account = &cp
		if cp.Email == "" {
			c.Account = nil
		}
	}
	return &c
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Account is the Claude account the runner last reported its CLI logged in
// to: its email, lowercased ("" when it hasn't said). Runners on one
// account share its usage limits.
func (r Runner) Account() string {
	if r.Reported == nil || r.Reported.Claude == nil || r.Reported.Claude.Account == nil {
		return ""
	}
	return strings.ToLower(r.Reported.Claude.Account.Email)
}

// claudeKey is what of a Claude status the runners list shows.
func claudeKey(c *claudecli.Status) string {
	if c == nil {
		return ""
	}
	k := c.Version + "\x00" + strings.Join(c.Warnings, "\x00")
	if a := c.Account; a != nil {
		k += "\x01" + a.Email + "\x00" + a.OrgID + "\x00" + a.OrgName + "\x00" + a.Subscription
	}
	return k
}

// ErrOffline is returned by Call for a runner without a tunnel.
var ErrOffline = errors.New("runner is offline")

// CallError is a runner's error answer to Call.
type CallError struct {
	Status int
	Msg    string
}

func (e *CallError) Error() string { return e.Msg }

// Call makes a request to runner id over its tunnel: in is sent as JSON
// (unless nil) and the JSON answer decoded into out (unless nil; a
// *[]byte gets the raw body).
func (h *Hub) Call(ctx context.Context, id, method, path string, in, out any) error {
	h.mu.Lock()
	var name string
	for _, r := range h.runners {
		if r.ID == id {
			name = r.Name
		}
	}
	_, online := h.live[id]
	h.mu.Unlock()
	if name == "" {
		return ErrUnknownName
	}
	if !online {
		return fmt.Errorf("%s: %w", name, ErrOffline)
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+name+runnerHostLabel+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if uid, ok := userFrom(ctx); ok {
		req.Header.Set(UserHeader, strconv.FormatInt(uid, 10))
	}
	res, err := (&http.Client{Transport: h.idle()}).Do(req)
	if err != nil {
		if h.session(name) == nil {
			return fmt.Errorf("%s: %w", name, ErrOffline)
		}
		return fmt.Errorf("%s is unreachable: %w", name, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return &CallError{Status: res.StatusCode, Msg: name + ": " + e.Error}
	}
	switch o := out.(type) {
	case nil:
	case *[]byte:
		*o = b
	default:
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("%s answered: %w", name, err)
		}
	}
	return nil
}

// CallBytes posts body, as it is, to path on runner id over its tunnel, with
// hdr, and returns the answer's body (at most limit bytes). Nothing of the
// body or the answer is read, kept or logged: it is the shared notes relay
// (package notes).
func (h *Hub) CallBytes(ctx context.Context, id, path string, hdr http.Header, body []byte, limit int64) ([]byte, error) {
	h.mu.Lock()
	var name string
	for _, r := range h.runners {
		if r.ID == id {
			name = r.Name
		}
	}
	_, online := h.live[id]
	h.mu.Unlock()
	if name == "" {
		return nil, ErrUnknownName
	}
	if !online {
		return nil, fmt.Errorf("%s: %w", name, ErrOffline)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://"+name+runnerHostLabel+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, vs := range hdr {
		req.Header[k] = vs
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := (&http.Client{Transport: h.idle()}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s is unreachable", name)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, &CallError{Status: res.StatusCode, Msg: name + ": " + res.Status}
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: answer too large", name)
	}
	return b, nil
}

// Online reports whether runner id has a tunnel now.
func (h *Hub) Online(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.live[id]
	return ok
}

// UserHeader names the hub's user a request to a runner is made for (see
// WithUser). The runner runs Claude only for its owner: it compares this
// with the user it was registered by. Proxy strips it from browsers'
// requests; only the hub's own calls set it.
const UserHeader = "X-Dash-User"

type userKey struct{}

// WithUser makes Call and DialWS with ctx tell the runner they are made for
// the hub's user uid.
func WithUser(ctx context.Context, uid int64) context.Context {
	return context.WithValue(ctx, userKey{}, uid)
}

func userFrom(ctx context.Context) (int64, bool) {
	uid, ok := ctx.Value(userKey{}).(int64)
	return uid, ok && uid != 0
}

// DialWS opens a websocket to path on runner id over its tunnel, e.g. the
// general chat a user runs on their own runner.
func (h *Hub) DialWS(ctx context.Context, id, path string) (*websocket.Conn, error) {
	h.mu.Lock()
	var name string
	for _, r := range h.runners {
		if r.ID == id {
			name = r.Name
		}
	}
	_, online := h.live[id]
	h.mu.Unlock()
	if name == "" {
		return nil, ErrUnknownName
	}
	if !online {
		return nil, fmt.Errorf("%s: %w", name, ErrOffline)
	}
	hdr := http.Header{}
	if uid, ok := userFrom(ctx); ok {
		hdr.Set(UserHeader, strconv.FormatInt(uid, 10))
	}
	d := websocket.Dialer{
		NetDialContext:   h.idle().DialContext,
		HandshakeTimeout: 30 * time.Second,
		ReadBufferSize:   32 << 10,
		WriteBufferSize:  32 << 10,
	}
	conn, res, err := d.DialContext(ctx, "ws://"+name+runnerHostLabel+path, hdr)
	if err != nil {
		if res != nil {
			defer res.Body.Close()
			var e struct{ Error string }
			_ = json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&e)
			if e.Error != "" {
				return nil, &CallError{Status: res.StatusCode, Msg: name + ": " + e.Error}
			}
		}
		if h.session(name) == nil {
			return nil, fmt.Errorf("%s: %w", name, ErrOffline)
		}
		return nil, fmt.Errorf("%s is unreachable: %w", name, err)
	}
	return conn, nil
}

// ---- proxying the dashboard's requests to a runner ----

func (h *Hub) idle() *http.Transport {
	h.transportOnce.Do(func() {
		h.transport = &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(addr)
				name := strings.TrimSuffix(host, runnerHostLabel)
				sess := h.session(name)
				if sess == nil {
					return nil, fmt.Errorf("%s is offline", name)
				}
				return sess.Open()
			},
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       60 * time.Second,
			ResponseHeaderTimeout: 0, // long polls and streams are fine
		}
	})
	return h.transport
}

// Proxy returns the handler that forwards /peer/<name>/… to the runner name,
// or false if there is no such runner.
func (h *Hub) Proxy(name string) (http.Handler, bool) {
	if !h.Has(name) {
		return nil, false
	}
	prefix := "/peer/" + name
	target := &url.URL{Scheme: "http", Host: name + runnerHostLabel}
	return &httputil.ReverseProxy{
		Transport: h.idle(),
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path = strings.TrimPrefix(r.In.URL.Path, prefix)
			r.Out.URL.RawPath = ""
			// The tunnel is the runner's credential for the hub; nothing of
			// the browser's (session cookie, origin) goes along.
			r.Out.Header.Del("Cookie")
			r.Out.Header.Del("Origin")
			r.Out.Header.Del("Authorization")
			r.Out.Header.Del("Proxy-Authorization")
			r.Out.Header.Del(UserHeader) // a person doesn't say whom they are
			// Only the hub's relay delivers shared notes: nobody reads or
			// writes a runner's notes through the hub's API.
			r.Out.Header.Del(notes.RelayHeader)
			// The hub says it, for requests it checked (WithUser).
			if uid, ok := userFrom(r.In.Context()); ok {
				r.Out.Header.Set(UserHeader, strconv.FormatInt(uid, 10))
			}
		},
		ModifyResponse: func(res *http.Response) error {
			// The answer is served on the hub's origin: the runner doesn't
			// get to choose how it renders (see SafeRunnerHeader).
			SafeRunnerHeader(res.Header, res.StatusCode)
			if res.StatusCode == http.StatusUnauthorized {
				res.StatusCode, res.Status = http.StatusBadGateway, "502 Bad Gateway"
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			msg := name + " is unreachable: " + err.Error()
			if h.session(name) == nil {
				msg = name + " is offline"
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": msg})
		},
		FlushInterval: -1,
	}, true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Authenticate is the runner whose token r carries (Authorization: Bearer).
func (h *Hub) Authenticate(r *http.Request) (Runner, bool) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		return Runner{}, false
	}
	rec, ok := h.authenticate(tok)
	if !ok {
		return Runner{}, false
	}
	return h.Get(rec.ID)
}
