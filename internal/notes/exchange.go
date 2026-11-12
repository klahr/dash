package notes

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
)

// How runners trade a project's keys, over the same transport as the notes
// (so through the hub, which sees only public keys and wrapped keys):
//
//   - A runner says hello: its id, identity key and the ids of the project
//     keys it holds. Each peer pins that key (trust.see) and, if the user
//     approved it there, wraps the keys the runner lacks for it. The peer
//     answers with its own hello, so the runner wraps for it in turn.
//   - A runner takes wrapped keys only from peers approved on it. Keys of
//     a later epoch replace its own; keys of the same epoch join them (two
//     runners that each made a first key end up with both, and seal with
//     the same one).
//   - A runner that sees a holder of the key leave the project (the hub's
//     member list) or that the user revoked here rotates: a new key of the
//     next epoch, which reaches the approved peers that remain by the next
//     hello round, and from them their own approved peers.

// post sends msg to project's peers and remembers who the hub says is on
// the project.
func (s *Service) post(ctx context.Context, project string, msg []byte) (Sent, error) {
	sent, err := s.transport.SendToPeers(ctx, project, msg)
	if err == nil && sent.Members != nil {
		s.mu.Lock()
		s.members[project] = slices.Clone(sent.Members)
		s.mu.Unlock()
	}
	return sent, err
}

// membersOf is project's runners as the hub last said (nil: not known).
func (s *Service) membersOf(project string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.members[project]
}

// sealMessage seals m with the project's key.
func (s *Service) sealMessage(st *Store, m any) ([]byte, error) {
	keys, err := st.Keys(s.now())
	if err != nil {
		return nil, err
	}
	k, _ := sealing(keys)
	sl, err := seal(k, encode(m))
	if err != nil {
		return nil, err
	}
	return encode(envelope{V: envelopeV, Sealed: &sl}), nil
}

// openSealed opens sl with the project's keys (ErrNoProjectKey: sealed
// with one this runner doesn't hold, or no longer: an older epoch's).
func (s *Service) openSealed(st *Store, sl sealed) ([]byte, error) {
	keys, err := st.Keys(s.now())
	if err != nil {
		return nil, err
	}
	return open(keys, sl)
}

// openEnvelope opens a sealed reply (nil, nil: it held nothing sealed).
func (s *Service) openEnvelope(st *Store, b []byte) ([]byte, error) {
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil || env.V == 0 {
		return nil, ErrBadEntry
	}
	if env.Sealed == nil {
		return nil, nil
	}
	return s.openSealed(st, *env.Sealed)
}

// hello is this runner's hello for st's project.
func (s *Service) hello(st *Store) (hello, error) {
	keys, err := st.Keys(s.now())
	if err != nil {
		return hello{}, err
	}
	h := hello{Runner: s.self, Name: s.Name, Key: s.id.Public}
	for _, k := range keys {
		h.Have = append(h.Have, k.ID)
	}
	return h, nil
}

// meet handles a peer's hello for project: pin its key, note which keys it
// holds, and, if the user approved it here, wrap the keys it lacks for it.
func (s *Service) meet(project string, st *Store, h hello) (wrapped, bool) {
	if h.Runner == s.self || h.Runner == "" || len(h.Have) > 16 {
		return wrapped{}, false
	}
	if err := s.trust.see(h.Runner, h.Name, h.Key); err != nil {
		return wrapped{}, false // logged loudly by see
	}
	if !s.trust.trusted(h.Key) {
		if s.trust.ask(h.Runner, h.Name, h.Key, project) {
			slog.Warn("shared notes: a runner wants to share this project's notes, but its key isn't approved here. "+
				"Run `dash runner notes` on it: if it shows the same fingerprint, approve it here with: dash runner notes approve "+h.Key.Fingerprint(),
				"project", project, "runner", h.Runner, "name", h.Name, "fingerprint", h.Key.Fingerprint(), "this runner", s.Fingerprint())
		}
		return wrapped{}, false
	}
	if err := st.AddHolder(h.Runner, h.Have); err != nil {
		slog.Warn("shared notes: holders", "err", err)
	}
	keys, err := st.Keys(s.now())
	if err != nil {
		return wrapped{}, false
	}
	if !slices.ContainsFunc(keys, func(k projectKey) bool { return !slices.Contains(h.Have, k.ID) }) {
		return wrapped{}, false // it has them all
	}
	return s.wrapFor(project, st, h.Runner, keys)
}

// wrapFor wraps keys for runner (pinned and approved here), and counts it
// as a holder.
func (s *Service) wrapFor(project string, st *Store, runner string, keys []projectKey) (wrapped, bool) {
	key, ok := s.trust.pinned(runner)
	if !ok || !s.trust.trusted(key) {
		return wrapped{}, false
	}
	holders, err := st.Holders()
	if err != nil {
		return wrapped{}, false
	}
	w, err := s.id.wrap(s.self, runner, key, keyset{Project: project, Keys: keys, Holders: holders})
	if err != nil {
		return wrapped{}, false
	}
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	if err := st.AddHolder(runner, ids); err != nil {
		slog.Warn("shared notes: holders", "err", err)
	}
	return w, true
}

// takeWraps takes the keys wrapped for this runner in ws, from peers
// approved here, and reports whether one is new.
func (s *Service) takeWraps(project string, st *Store, ws []wrapped) (added bool) {
	for _, w := range ws {
		if w.To != s.self || w.ToKey != s.id.Public || w.From == s.self {
			continue
		}
		if err := s.trust.see(w.From, "", w.FromKey); err != nil {
			continue // logged loudly by see
		}
		if m := s.membersOf(project); m != nil && !slices.Contains(m, w.From) {
			slog.Warn("shared notes: refused a project key from a runner no longer on the project", "project", project, "runner", w.From)
			continue
		}
		if !s.trust.trusted(w.FromKey) {
			if s.trust.ask(w.From, "", w.FromKey, project) {
				slog.Warn("shared notes: refused a project key from a runner whose key isn't approved here. "+
					"Run `dash runner notes` on it: if it shows the same fingerprint, approve it here with: dash runner notes approve "+w.FromKey.Fingerprint(),
					"project", project, "runner", w.From, "fingerprint", w.FromKey.Fingerprint(), "this runner", s.Fingerprint())
			}
			continue
		}
		ks, err := s.id.unwrap(s.self, project, w)
		if err != nil {
			slog.Warn("shared notes: a wrapped key doesn't open", "project", project, "from", w.From, "err", err)
			continue
		}
		n, err := st.AddKeys(ks.Keys, append(ks.Holders, w.From))
		if err != nil {
			slog.Warn("shared notes: keep keys", "project", project, "err", err)
			continue
		}
		if n {
			slog.Info("shared notes: got the project key", "project", project, "from", w.From, "fingerprint", w.FromKey.Fingerprint())
			added = true
		}
	}
	return added
}

// tradeKeys says hello to project's reachable peers, takes the keys they
// wrap for this runner, rotates if a holder left, and wraps this runner's
// keys for the approved peers that lack them.
func (s *Service) tradeKeys(ctx context.Context, project string, st *Store) error {
	h, err := s.hello(st)
	if err != nil {
		return err
	}
	sent, err := s.post(ctx, project, encode(envelope{V: envelopeV, Hello: &h}))
	if err != nil {
		return err
	}
	var hellos []hello
	for _, r := range sent.Replies {
		var env envelope
		if json.Unmarshal(r, &env) != nil || env.V == 0 {
			continue
		}
		s.takeWraps(project, st, env.Wraps)
		if env.Hello != nil {
			hellos = append(hellos, *env.Hello)
		}
	}
	if _, err := s.rotateIfLeft(project, st, nil); err != nil {
		return err
	}
	// Wrap after taking and rotating, so peers get the keys as they are now.
	var push []wrapped
	for _, ph := range hellos {
		if w, ok := s.meet(project, st, ph); ok {
			push = append(push, w)
		}
	}
	if len(push) > 0 {
		if _, err := s.post(ctx, project, encode(envelope{V: envelopeV, Wraps: push})); err != nil {
			return err
		}
	}
	return nil
}

// rotateIfLeft rotates project's key if a runner that holds it is no
// longer on the project (as the hub last said) or is among gone.
func (s *Service) rotateIfLeft(project string, st *Store, gone []string) (bool, error) {
	holders, err := st.Holders()
	if err != nil {
		return false, err
	}
	members := s.membersOf(project)
	var left []string
	for _, h := range holders {
		if h != s.self && (slices.Contains(gone, h) || members != nil && !slices.Contains(members, h)) {
			left = append(left, h)
		}
	}
	if len(left) == 0 {
		return false, nil
	}
	k, err := st.Rotate(s.now())
	if err != nil {
		return false, err
	}
	slog.Warn("shared notes: rotated the project key, as runners that held it left the project or were revoked; new notes use the new key",
		"project", project, "left", left, "epoch", k.Epoch)
	return true, nil
}

// Reconcile acts on what the user approved or revoked here (dash runner
// notes) since it last looked, for projects: it trades keys with peers
// just approved, and rotates the key of every project a revoked runner
// held, giving the new one to the approved peers that remain. The runner
// calls it every few seconds; it does nothing when nothing changed.
func (s *Service) Reconcile(ctx context.Context, projects []string) {
	gen := s.trust.reload()
	s.mu.Lock()
	seen := s.reconciled
	s.reconciled = gen
	s.mu.Unlock()
	if gen == seen {
		return // the first run always looks: reconciled starts at 0, gen at 1
	}
	// All revoked runners, not only the newly revoked: one revoked while
	// this runner was down may hold a key still. Rotating is done once:
	// after it, they hold none.
	gone := s.trust.revokedRunners()
	approved := s.trust.approvedPending()
	for _, p := range projects {
		if s.allowed != nil && !s.allowed(p) {
			continue
		}
		st, err := s.store(p)
		if err != nil {
			continue
		}
		rotated, err := s.rotateIfLeft(p, st, gone)
		if err != nil {
			slog.Warn("shared notes: rotate", "project", p, "err", err)
			continue
		}
		if rotated || slices.ContainsFunc(approved, func(x Pending) bool { return x.Project == p }) {
			if err := s.CatchUp(ctx, p); err != nil {
				slog.Info("shared notes: catch-up failed", "project", p, "err", err)
			}
		}
	}
}
