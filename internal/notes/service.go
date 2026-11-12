package notes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/secretscan"
)

// sendTimeout bounds how long a write waits to reach the other runners;
// it is saved here already, and a catch-up delivers it later otherwise.
const sendTimeout = 15 * time.Second

// Service is a runner's shared notes: one Store per hub project, kept in
// Dir, synced with the project's other runners over Transport, sealed end
// to end with the project's key (crypto.go, exchange.go).
//
// Only runners have one. A Dash running tasks itself (no runner) has no
// shared notes, and the hub keeps none: it only relays, and holds no key.
type Service struct {
	dir       string
	self      string
	id        *Identity
	transport Transport
	trust     *trust
	// Allowed reports whether this runner keeps notes for a hub project:
	// one linked to it (nil: any).
	allowed func(project string) bool
	// Now is the clock (tests).
	Now func() time.Time
	// Name is this runner's name, told to peers with its key (display
	// only: the user approves the fingerprint).
	Name string
	// Policies gives a project's tidying policy (nil, or zero fields: the
	// defaults). Its runners should agree on it.
	Policies func(project string) Policy

	mu       sync.Mutex
	stores   map[string]*Store
	catching map[string]bool      // projects with a catch-up running
	members  map[string][]string  // project → its runners, as the hub last said
	pulled   map[string]time.Time // project → last catch-up for a key we lacked
	closed   bool
	// reconciled is the approvals' generation Reconcile last acted on.
	reconciled int
}

// NewService keeps runner self's notes in dir (e.g. <data>/shared), one
// database per project, synced over t and sealed with keys it trades as
// identity id. allowed picks the projects it keeps notes for (nil: any).
func NewService(dir, self string, id *Identity, t Transport, allowed func(project string) bool) (*Service, error) {
	if self == "" {
		return nil, ErrNoRunnerID
	}
	if dir == "" || t == nil || id == nil {
		return nil, errors.New("shared notes need a folder, an identity and a transport")
	}
	s := &Service{dir: dir, self: self, id: id, transport: t, allowed: allowed, stores: map[string]*Store{},
		catching: map[string]bool{}, members: map[string][]string{}, pulled: map[string]time.Time{}}
	tr, err := openTrust(dir, s.now)
	if err != nil {
		return nil, err
	}
	s.trust = tr
	t.ReceiveFromPeers(s.receive)
	return s, nil
}

// Fingerprint is this runner's key fingerprint, for the user to compare.
func (s *Service) Fingerprint() string { return s.id.Fingerprint() }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

var plainName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// file is the database of project in dir: its id when that is a plain
// name, else a hash of it.
func file(dir, project string) string {
	name := project
	if !plainName.MatchString(project) {
		h := sha256.Sum256([]byte(project))
		name = "h-" + hex.EncodeToString(h[:16])
	}
	return filepath.Join(dir, name+".db")
}

// store opens project's store, if this runner keeps its notes.
func (s *Service) store(project string) (*Store, error) {
	if project == "" || (s.allowed != nil && !s.allowed(project)) {
		return nil, ErrNoProject
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("shared notes are closed")
	}
	if st, ok := s.stores[project]; ok {
		return st, nil
	}
	st, err := OpenStore(file(s.dir, project), s.self)
	if err != nil {
		return nil, err
	}
	st.SetPolicy(s.policy(project))
	if err := st.markOpened(s.now()); err != nil {
		st.Close()
		return nil, err
	}
	s.stores[project] = st
	return st, nil
}

func (s *Service) policy(project string) Policy {
	if s.Policies == nil {
		return DefaultPolicy()
	}
	return s.Policies(project).orDefaults()
}

// Close closes every store.
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	var errs []error
	for _, st := range s.stores {
		errs = append(errs, st.Close())
	}
	s.stores = map[string]*Store{}
	return errors.Join(errs...)
}

// Put saves d in project's notes, then sends it to the project's other
// runners. A note that looks like it holds a secret never leaves this
// runner: it isn't saved either.
func (s *Service) Put(ctx context.Context, project string, d Draft) (Entry, error) {
	st, err := s.store(project)
	if err != nil {
		return Entry{}, err
	}
	if kind, found := secretscan.Has(d.Key + "\n" + d.Body + "\n" + strings.Join(d.Tags, "\n")); found {
		return Entry{}, fmt.Errorf("%w (%s): leave it out, and say where to find it instead", ErrSecret, kind)
	}
	e, evicted, err := st.Put(d, s.now())
	if err != nil {
		return Entry{}, err
	}
	s.send(ctx, project, append(evicted, e))
	return e, nil
}

// Confirm says note id of project is still good: it expires ttl from now
// (0: the project's default TTL), on every copy.
func (s *Service) Confirm(ctx context.Context, project, id, task string, ttl time.Duration) (Entry, error) {
	st, err := s.store(project)
	if err != nil {
		return Entry{}, err
	}
	e, err := st.Confirm(id, task, ttl, s.now())
	if err != nil {
		return Entry{}, err
	}
	s.send(ctx, project, []Entry{e})
	return e, nil
}

// Clear deletes every note of project last updated before before (zero:
// now), on every copy.
func (s *Service) Clear(ctx context.Context, project string, before time.Time, task string) error {
	st, err := s.store(project)
	if err != nil {
		return err
	}
	e, err := st.Clear(before, task, s.now())
	if err != nil {
		return err
	}
	s.send(ctx, project, []Entry{e})
	return nil
}

// Tidy tidies project's notes now (tidy.go): purges expired notes and old
// tombstones, merges same-title notes, evicts past the caps, and sends
// what it wrote to the other runners. It is what "remove expired" runs;
// RunTidying runs it on its own.
func (s *Service) Tidy(ctx context.Context, project string) (TidyReport, error) {
	st, err := s.store(project)
	if err != nil {
		return TidyReport{}, err
	}
	st.SetPolicy(s.policy(project))
	r, err := st.Tidy(s.now())
	if err != nil {
		return r, err
	}
	s.send(ctx, project, r.Writes)
	return r, nil
}

// TidyAll tidies every project of projects this runner keeps notes for.
func (s *Service) TidyAll(ctx context.Context, projects []string) {
	for _, p := range projects {
		if s.allowed != nil && !s.allowed(p) {
			continue
		}
		r, err := s.Tidy(ctx, p)
		switch {
		case err != nil:
			slog.Info("shared notes: tidying failed", "project", p, "err", err)
		case r.Expired+r.Purged+r.Evicted+r.Merged > 0 || r.Vacuumed:
			slog.Info("shared notes: tidied", "project", p, "expired", r.Expired, "purged", r.Purged,
				"evicted", r.Evicted, "merged", r.Merged, "vacuumed", r.Vacuumed, "stale", r.Stale)
		}
	}
}

// TidyEvery is how often RunTidying tidies.
const TidyEvery = time.Hour

// RunTidying tidies projects' notes now and then every TidyEvery, until
// ctx ends. Each runner does so on its own: no hub or task is involved.
func (s *Service) RunTidying(ctx context.Context, projects func() []string) {
	s.TidyAll(ctx, projects())
	t := time.NewTicker(TidyEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.TidyAll(ctx, projects())
		}
	}
}

// settle merges same-title notes after taking peers' writes, and sends
// the merge on.
func (s *Service) settle(project string, st *Store) {
	writes, err := st.Dedup(s.now())
	if err != nil || len(writes) == 0 {
		return
	}
	go s.send(context.Background(), project, writes)
}

// Delete deletes entry id of project's notes, everywhere.
func (s *Service) Delete(ctx context.Context, project, id, task string) error {
	st, err := s.store(project)
	if err != nil {
		return err
	}
	e, err := st.Delete(id, task, s.now())
	if err != nil {
		return err
	}
	s.send(ctx, project, []Entry{e})
	return nil
}

// Get returns entry id of project's notes, from this runner's copy.
func (s *Service) Get(project, id string) (Entry, error) {
	st, err := s.store(project)
	if err != nil {
		return Entry{}, err
	}
	return st.Get(id, s.now())
}

// List returns project's notes that match q, from this runner's copy.
func (s *Service) List(project string, q Query) ([]Entry, error) {
	st, err := s.store(project)
	if err != nil {
		return nil, err
	}
	return st.List(q, s.now())
}

// send tells the project's other runners of entries, sealed, best effort:
// those it doesn't reach now get them when they next catch up.
func (s *Service) send(ctx context.Context, project string, entries []Entry) {
	if len(entries) == 0 {
		return
	}
	st, err := s.store(project)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendTimeout)
	defer cancel()
	msg, err := s.sealMessage(st, message{Kind: kindEntries, Entries: entries})
	if err != nil {
		slog.Warn("shared notes: can't seal", "project", project, "err", err)
		return
	}
	if _, err := s.post(ctx, project, msg); err != nil {
		slog.Info("shared notes: not sent now, peers catch up later", "project", project, "entries", len(entries), "err", err)
		return
	}
	// The hub told who is on the project now: if a runner that holds the
	// key left, rotate it.
	if rotated, err := s.rotateIfLeft(project, st, nil); err == nil && rotated {
		s.catchUpSoon(project)
	}
}

// CatchUp brings project's notes up to date with its runners that are
// reachable now, both ways. First it trades keys with them (hello): it
// gets the project keys its approved peers hold, gives them its own, and
// rotates the key if a runner that held it left. Then it asks them for the
// notes this copy lacks, and sends them what they lack (writes made here
// while the hub was away).
func (s *Service) CatchUp(ctx context.Context, project string) error {
	st, err := s.store(project)
	if err != nil {
		return err
	}
	if err := s.tradeKeys(ctx, project, st); err != nil {
		return err
	}
	if stale, err := st.Stale(s.now()); err != nil {
		return err
	} else if stale {
		if done, err := s.resync(ctx, project, st); err != nil || done {
			return err
		}
	}
	v, err := st.Vector()
	if err != nil {
		return err
	}
	want, err := s.sealMessage(st, message{Kind: kindWant, Vector: v})
	if err != nil {
		return err
	}
	sent, err := s.post(ctx, project, want)
	if err != nil {
		return err
	}
	// The hub answered: this copy is in touch with the project's runners.
	// What they write while it is reaches it, or they catch up with it.
	if err := st.MarkSynced(s.now()); err != nil {
		return err
	}
	var theirs []Vector
	for _, r := range sent.Replies {
		plain, err := s.openEnvelope(st, r)
		if err != nil || plain == nil {
			continue // a peer sealing with a key we don't share (yet), or a newer runner
		}
		var a answer
		if err := json.Unmarshal(plain, &a); err != nil || a.Stale {
			continue // a stale peer resyncs from us: don't send it what "it lacks"
		}
		if _, err := st.Merge(a.Entries, a.Vector); err != nil {
			return err
		}
		theirs = append(theirs, a.Vector)
	}
	s.settle(project, st)
	// What each peer lacks of ours, sent once to all of them.
	lack := map[string]Entry{}
	for _, pv := range theirs {
		es, _, err := st.Since(pv, s.now())
		if err != nil {
			return err
		}
		for _, e := range es {
			lack[e.ID] = e
		}
	}
	if len(lack) > 0 {
		out := make([]Entry, 0, len(lack))
		for _, e := range lack {
			out = append(out, e)
		}
		s.send(ctx, project, out)
	}
	return nil
}

// resync replaces a stale copy of project's notes (one that hasn't synced
// within the retention window) with its peers' copies, so that notes they
// purged don't come back from it. Writes made here meanwhile are dropped
// with the rest. done is false if a normal catch-up should follow: every
// peer that answered is stale too (no copy is fresher; the first to catch
// up becomes the one the others resync from), or there are no other
// runners on the project. If no peer is reachable it stays stale and
// returns an error.
func (s *Service) resync(ctx context.Context, project string, st *Store) (done bool, err error) {
	want, err := s.sealMessage(st, message{Kind: kindWant, Vector: Vector{}})
	if err != nil {
		return false, err
	}
	sent, err := s.post(ctx, project, want)
	if err != nil {
		return false, err
	}
	var fresh []answer
	staleOnes := 0
	for _, r := range sent.Replies {
		plain, err := s.openEnvelope(st, r)
		if err != nil || plain == nil {
			continue
		}
		var a answer
		if err := json.Unmarshal(plain, &a); err != nil {
			continue
		}
		if a.Stale {
			staleOnes++
			continue
		}
		fresh = append(fresh, a)
	}
	alone := sent.Members != nil && !slices.ContainsFunc(sent.Members, func(m string) bool { return m != s.self })
	switch {
	case len(fresh) > 0:
		n, err := st.replace(fresh, s.now())
		if err != nil {
			return false, err
		}
		slog.Warn("shared notes: this runner was away longer than the retention window; its copy was replaced by its peers'",
			"project", project, "notes", n)
		return true, nil
	case staleOnes > 0 || alone:
		return false, nil
	}
	return false, fmt.Errorf("%w (no other runner of the project is reachable now)", ErrStale)
}

// CatchUpAll catches up every project of projects this runner keeps notes
// for, e.g. when its connection to the hub comes back.
func (s *Service) CatchUpAll(ctx context.Context, projects []string) {
	for _, p := range projects {
		if s.allowed != nil && !s.allowed(p) {
			continue
		}
		if err := s.CatchUp(ctx, p); err != nil {
			slog.Info("shared notes: catch-up failed", "project", p, "err", err)
		}
	}
}

// catchUpSoon starts a catch-up of project unless one is running.
func (s *Service) catchUpSoon(project string) {
	s.mu.Lock()
	if s.catching[project] || s.closed {
		s.mu.Unlock()
		return
	}
	s.catching[project] = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.catching, project)
			s.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.CatchUp(ctx, project); err != nil {
			slog.Info("shared notes: catch-up failed", "project", project, "err", err)
		}
	}()
}

// pullKeysSoon catches up (trading keys first) because a peer sealed with
// a key this runner lacks; at most once a minute per project, so a peer
// that isn't approved here can't keep it busy.
func (s *Service) pullKeysSoon(project string) {
	s.mu.Lock()
	last := s.pulled[project]
	now := s.now()
	if now.Sub(last) < time.Minute && !last.IsZero() {
		s.mu.Unlock()
		return
	}
	s.pulled[project] = now
	s.mu.Unlock()
	s.catchUpSoon(project)
}

// receive handles a message from another runner of project: key traffic,
// and notes sealed with the project key. Anything unsealed is refused.
func (s *Service) receive(ctx context.Context, project string, msg []byte) ([]byte, error) {
	st, err := s.store(project)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(msg, &env); err != nil || env.V == 0 {
		return nil, fmt.Errorf("%w: shared notes must be sealed (update Dash on every runner of the project)", ErrBadEntry)
	}
	var out envelope
	if len(env.Wraps) > 0 {
		if s.takeWraps(project, st, env.Wraps) {
			s.catchUpSoon(project) // new keys: give them on, and sync the notes they open
		}
	}
	if env.Hello != nil {
		h, err := s.hello(st)
		if err != nil {
			return nil, err
		}
		out.Hello = &h
		if w, ok := s.meet(project, st, *env.Hello); ok {
			out.Wraps = []wrapped{w}
		}
	}
	if env.Sealed != nil {
		plain, err := s.openSealed(st, *env.Sealed)
		if errors.Is(err, ErrNoProjectKey) {
			s.pullKeysSoon(project)
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		reply, err := s.handle(project, st, plain)
		if err != nil || reply == nil {
			return nil, err
		}
		return s.sealMessage(st, reply)
	}
	if out.Hello == nil && out.Wraps == nil {
		return nil, nil
	}
	out.V = envelopeV
	return encode(out), nil
}

// handle handles an opened notes message, and returns what to answer
// (nil: nothing).
func (s *Service) handle(project string, st *Store, plain []byte) (any, error) {
	var m message
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadEntry, err)
	}
	switch m.Kind {
	case kindEntries:
		if len(m.Entries) > 2*MaxEntries {
			return nil, ErrTooLarge
		}
		applied, gap, err := st.Apply(m.Entries)
		if err != nil {
			return nil, err
		}
		if gap {
			s.catchUpSoon(project)
		}
		if applied > 0 {
			s.settle(project, st)
		}
		return nil, nil
	case kindWant:
		// A stale copy may hold notes the others purged: it offers none,
		// and resyncs from them instead.
		if stale, err := st.Stale(s.now()); err != nil {
			return nil, err
		} else if stale {
			s.catchUpSoon(project)
			return answer{Stale: true}, nil
		}
		es, v, err := st.Since(m.Vector, s.now())
		if err != nil {
			return nil, err
		}
		return answer{Entries: es, Vector: v}, nil
	}
	return nil, fmt.Errorf("unknown shared notes message %q", m.Kind)
}
