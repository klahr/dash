package notes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// The relay through the hub. A runner posts a message to the hub
// (RelayPath, with its runner token); the hub posts it, unread, to each
// other runner of the project that is online (PeerPath, over its tunnel)
// and hands their replies back. The hub keeps nothing and logs no message.
const (
	// RelayPath is POST on the hub, ?project=<hub project id>; the body is
	// the message, the answer a relayReply.
	RelayPath = "/runner/notes"
	// PeerPath is POST on a runner, from the hub over its tunnel; the body
	// is the message, the answer the runner's reply (may be empty).
	PeerPath = "/api/hub/projects/{hpid}/notes"
	// RelayHeader marks the hub's own delivery to a runner. The hub's
	// proxy to runners (/peer/…) strips it, so nobody reaches a runner's
	// notes through the hub's API.
	RelayHeader = "X-Dash-Notes-Relay"
	// CommandHeader marks the hub's own status requests and removals
	// (admin.go) to a runner; its proxy strips it too.
	CommandHeader = "X-Dash-Notes-Command"
	// StatusPath and RemovePath are POST on a runner, from the hub over its
	// tunnel: a Status of the project's copy, and a Removal (answered
	// with RemoveResult).
	StatusPath = "/api/hub/projects/{hpid}/notes/status"
	RemovePath = "/api/hub/projects/{hpid}/notes/remove"
)

// RemoveResult is a runner's answer to a Removal: a count, nothing else.
type RemoveResult struct {
	Removed int `json:"removed"`
}

// StatusPathFor and RemovePathFor are StatusPath and RemovePath for project.
func StatusPathFor(project string) string { return PeerPathFor(project) + "/status" }
func RemovePathFor(project string) string { return PeerPathFor(project) + "/remove" }

// Limits of the relay.
const (
	MaxMessage   = 8 << 20 // bytes in one message
	MaxReply     = 8 << 20 // bytes of one runner's reply the hub reads
	peerTimeout  = 30 * time.Second
	maxRelayWait = 45 * time.Second
)

// PeerPathFor is PeerPath for project.
func PeerPathFor(project string) string {
	return "/api/hub/projects/" + url.PathEscape(project) + "/notes"
}

// relayReply is the hub's answer to a runner's message: the replies of the
// runners it reached, still opaque.
type relayReply struct {
	Peers   int      `json:"peers"`   // the project's other runners online
	Reached int      `json:"reached"` // of those, how many took the message
	Replies [][]byte `json:"replies,omitempty"`
	// Members are all the project's runners, online or not (ids only).
	Members []string `json:"members"`
}

// RelayError is a refusal by Relay.Route, with its HTTP status.
type RelayError struct {
	Status int
	Msg    string
}

func (e *RelayError) Error() string { return e.Msg }

// Relay is the hub's side: it passes runners' messages on and keeps no
// state at all (it has no fields but its two functions).
type Relay struct {
	// Route authenticates the runner posting r for project and names the
	// runners its message goes to: the project's other runners online now;
	// and the project's members: all its runners. Return a *RelayError to
	// refuse with a status.
	Route func(r *http.Request, project string) (peers, members []string, err error)
	// Forward posts msg to runner peer for project, and returns its reply.
	Forward func(ctx context.Context, peer, project string, msg []byte) ([]byte, error)
}

// ServeHTTP handles POST RelayPath.
func (rl *Relay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		relayJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	project := r.URL.Query().Get("project")
	if project == "" {
		relayJSON(w, http.StatusBadRequest, map[string]string{"error": "which project?"})
		return
	}
	peers, members, err := rl.Route(r, project)
	if err != nil {
		status := http.StatusForbidden
		if re, ok := errors.AsType[*RelayError](err); ok {
			status = re.Status
		}
		relayJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	msg, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxMessage))
	if err != nil {
		relayJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "message too large"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), maxRelayWait)
	defer cancel()
	out := relayReply{Peers: len(peers), Members: members}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range peers {
		wg.Go(func() {
			pctx, cancel := context.WithTimeout(ctx, peerTimeout)
			defer cancel()
			reply, err := rl.Forward(pctx, p, project, msg)
			if err != nil {
				return // that runner catches up when it can
			}
			mu.Lock()
			out.Reached++
			if len(reply) > 0 {
				out.Replies = append(out.Replies, reply)
			}
			mu.Unlock()
		})
	}
	wg.Wait()
	relayJSON(w, http.StatusOK, out)
}

func relayJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// HubTransport is a runner's Transport through its hub's relay.
type HubTransport struct {
	// Post sends body to the hub's RelayPath for project, as this runner,
	// and returns the hub's answer.
	Post func(ctx context.Context, project string, body []byte) ([]byte, error)

	mu   sync.Mutex
	recv Receiver
}

// SendToPeers implements Transport.
func (t *HubTransport) SendToPeers(ctx context.Context, project string, msg []byte) (Sent, error) {
	if len(msg) > MaxMessage {
		return Sent{}, ErrTooLarge
	}
	b, err := t.Post(ctx, project, msg)
	if err != nil {
		return Sent{}, err
	}
	var r relayReply
	if err := json.Unmarshal(b, &r); err != nil {
		return Sent{}, err
	}
	return Sent{Replies: r.Replies, Members: r.Members}, nil
}

// ReceiveFromPeers implements Transport.
func (t *HubTransport) ReceiveFromPeers(h Receiver) {
	t.mu.Lock()
	t.recv = h
	t.mu.Unlock()
}

// ServeHTTP handles POST PeerPath on the runner: a message from another
// runner, which the hub relayed. Only the hub's relay sends it (RelayHeader;
// the caller checks the request came over the tunnel).
func (t *HubTransport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(RelayHeader) == "" {
		relayJSON(w, http.StatusForbidden, map[string]string{"error": "shared notes come from the hub's relay only"})
		return
	}
	t.mu.Lock()
	h := t.recv
	t.mu.Unlock()
	if h == nil {
		relayJSON(w, http.StatusNotFound, map[string]string{"error": ErrNoProject.Error()})
		return
	}
	msg, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxMessage))
	if err != nil {
		relayJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "message too large"})
		return
	}
	reply, err := h(r.Context(), r.PathValue("hpid"), msg)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrNoProject) {
			status = http.StatusNotFound
		}
		relayJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(reply)
}
