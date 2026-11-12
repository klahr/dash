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
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/secretscan"
)

// sendTimeout bounds how long a write waits to reach the other runners;
// it is saved here already, and a catch-up delivers it later otherwise.
const sendTimeout = 15 * time.Second

// Service is a runner's shared notes: one Store per hub project, kept in
// Dir, synced with the project's other runners over Transport.
//
// Only runners have one. A Dash running tasks itself (no runner) has no
// shared notes, and the hub keeps none: it only relays.
type Service struct {
	dir       string
	self      string
	transport Transport
	// Allowed reports whether this runner keeps notes for a hub project:
	// one linked to it (nil: any).
	allowed func(project string) bool
	// Now is the clock (tests).
	Now func() time.Time

	mu       sync.Mutex
	stores   map[string]*Store
	catching map[string]bool // projects with a catch-up running
	closed   bool
}

// NewService keeps runner self's notes in dir (e.g. ~/.dash/shared), one
// database per project, synced over t. allowed picks the projects it keeps
// notes for (nil: any).
func NewService(dir, self string, t Transport, allowed func(project string) bool) (*Service, error) {
	if self == "" {
		return nil, ErrNoRunnerID
	}
	if dir == "" || t == nil {
		return nil, errors.New("shared notes need a folder and a transport")
	}
	s := &Service{dir: dir, self: self, transport: t, allowed: allowed, stores: map[string]*Store{}, catching: map[string]bool{}}
	t.ReceiveFromPeers(s.receive)
	return s, nil
}

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
	s.stores[project] = st
	return st, nil
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
	e, err := st.Write(d, s.now())
	if err != nil {
		return Entry{}, err
	}
	s.send(ctx, project, []Entry{e})
	return e, nil
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

// send tells the project's other runners of entries, best effort: those it
// doesn't reach now get them when they next catch up.
func (s *Service) send(ctx context.Context, project string, entries []Entry) {
	if len(entries) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendTimeout)
	defer cancel()
	if _, err := s.transport.SendToPeers(ctx, project, encode(message{Kind: kindEntries, Entries: entries})); err != nil {
		slog.Info("shared notes: not sent now, peers catch up later", "project", project, "entries", len(entries), "err", err)
	}
}

// CatchUp brings project's notes up to date with its runners that are
// reachable now, both ways: it asks them for what this copy lacks, then
// sends them what they lack (writes made here while the hub was away).
func (s *Service) CatchUp(ctx context.Context, project string) error {
	st, err := s.store(project)
	if err != nil {
		return err
	}
	v, err := st.Vector()
	if err != nil {
		return err
	}
	replies, err := s.transport.SendToPeers(ctx, project, encode(message{Kind: kindWant, Vector: v}))
	if err != nil {
		return err
	}
	var theirs []Vector
	for _, r := range replies {
		var a answer
		if err := json.Unmarshal(r, &a); err != nil {
			continue // a peer we can't understand (a newer runner?)
		}
		if _, err := st.Merge(a.Entries, a.Vector); err != nil {
			return err
		}
		theirs = append(theirs, a.Vector)
	}
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

// receive handles a message from another runner of project.
func (s *Service) receive(ctx context.Context, project string, msg []byte) ([]byte, error) {
	st, err := s.store(project)
	if err != nil {
		return nil, err
	}
	var m message
	if err := json.Unmarshal(msg, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadEntry, err)
	}
	switch m.Kind {
	case kindEntries:
		if len(m.Entries) > 2*MaxEntries {
			return nil, ErrTooLarge
		}
		_, gap, err := st.Apply(m.Entries)
		if err != nil {
			return nil, err
		}
		if gap {
			s.catchUpSoon(project)
		}
		return nil, nil
	case kindWant:
		es, v, err := st.Since(m.Vector, s.now())
		if err != nil {
			return nil, err
		}
		return encode(answer{Entries: es, Vector: v}), nil
	}
	return nil, fmt.Errorf("unknown shared notes message %q", m.Kind)
}
