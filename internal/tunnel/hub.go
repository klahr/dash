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
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/model"
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

// Errors from Register.
var (
	ErrBadCode     = errors.New("unknown or expired registration code")
	ErrTooManyBad  = errors.New("too many wrong registration codes, try again later")
	ErrNameTaken   = errors.New("that name is already used by another computer; revoke it first or pick another -name")
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
	Version string             `json:"version,omitempty"` // the Dash it last connected with, e.g. v1.2.0
	// Parallel is how many tasks the hub sends it at once (0: as many as
	// the runner's own -parallel, which it reports). Paused: the hub sends
	// it none.
	Parallel int  `json:"parallel"`
	Paused   bool `json:"paused"`
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
	At       time.Time       `json:"at"`
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
	// Taken reports names already used by this computer or a -peer (optional).
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
	tmp := h.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err == nil {
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, h.path)
		}
		if err == nil {
			return
		}
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

// Register swaps a registration code for a new runner's id and token. The
// token is shown only now; the hub keeps its hash.
func (h *Hub) Register(code, name string) (id, token string, err error) {
	id, token, _, err = h.register(code, name)
	return id, token, err
}

func (h *Hub) register(code, name string) (id, token string, org int64, err error) {
	name = strings.ToLower(strings.TrimSpace(name))
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock()
	h.badAt = slices.DeleteFunc(h.badAt, func(t time.Time) bool { return now.Sub(t) > badCodeWindow })
	if len(h.badAt) >= maxBadCodes {
		return "", "", 0, ErrTooManyBad
	}
	h.pruneCodesLocked(now)
	want := hash(normCode(code))
	i := slices.IndexFunc(h.codes, func(c regCode) bool {
		return subtle.ConstantTimeCompare([]byte(c.Hash), []byte(want)) == 1
	})
	if i < 0 {
		h.badAt = append(h.badAt, now)
		return "", "", 0, ErrBadCode
	}
	if !nameRe.MatchString(name) {
		return "", "", 0, ErrBadName // the code stays usable with a better name
	}
	if h.nameTakenLocked(name) {
		return "", "", 0, ErrNameTaken
	}
	owner := h.codes[i].Owner
	h.codes = slices.Delete(h.codes, i, i+1) // single use
	id = randomString(9)
	token, _, err = h.Accounts.CreateToken(store.NewToken{
		Kind: store.TokenRunner, Name: "Runner " + name, UserID: owner.User, OrgID: owner.Org, RunnerID: id,
	}, now)
	if err != nil {
		h.saveLocked()
		if errors.Is(err, store.ErrNotMember) {
			return "", "", 0, fmt.Errorf("%w: whoever made the code has left that organization", ErrBadCode)
		}
		return "", "", 0, err
	}
	h.runners = append(h.runners, &record{ID: id, Name: name, Created: now})
	h.saveLocked()
	go h.changed()
	return id, token, owner.Org, nil
}

func (h *Hub) nameTakenLocked(name string) bool {
	if h.Taken != nil && h.Taken(name) {
		return true
	}
	return slices.ContainsFunc(h.runners, func(r *record) bool { return r.Name == name })
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

func (h *Hub) viewLocked(r *record, online bool, owner *store.RunnerOwner) Runner {
	v := Runner{ID: r.ID, Name: r.Name, Online: online, Created: r.Created, LastSeen: r.LastSeen, Owner: owner,
		Version: r.Version, Parallel: r.Parallel, Paused: r.Paused}
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

// touch records that runner id (dis)connected, running version when
// that's not "".
func (h *Hub) touch(id, version string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.runners {
		if r.ID == id {
			r.LastSeen = h.clock()
			if version != "" {
				r.Version = version
			}
		}
	}
	h.saveLocked()
}

// VersionHeader carries the runner's Dash version when it connects.
const VersionHeader = "X-Dash-Version"

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
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	id, token, org, err := h.register(req.Code, req.Name)
	switch {
	case errors.Is(err, ErrTooManyBad):
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrBadCode):
		time.Sleep(300 * time.Millisecond)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrNameTaken):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		slog.Info("runner registered", "name", strings.ToLower(strings.TrimSpace(req.Name)), "id", id)
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": strings.ToLower(strings.TrimSpace(req.Name)), "token": token, "org_id": org})
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
	h.touch(rec.ID, version)
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
		h.touch(rec.ID, "")
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
		Parallel int             `json:"parallel"`
		Usage    *usage.Snapshot `json:"usage"`
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
	h.reported[rec.ID] = &Reported{Parallel: self.Parallel, Usage: self.Usage, At: h.clock()}
	h.mu.Unlock()
	if prev == nil || prev.Parallel != self.Parallel || paused(prev.Usage) != paused(self.Usage) {
		h.changed()
	}
	if h.OnReport != nil {
		if err := h.OnReport(rec.ID, body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func paused(u *usage.Snapshot) bool { return u != nil && u.Paused }

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
		},
		ModifyResponse: func(res *http.Response) error {
			res.Header.Del("Set-Cookie")
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
