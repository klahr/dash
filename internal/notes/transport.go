package notes

import (
	"context"
	"encoding/json"
)

// Transport carries messages between the runners of a project. The relay
// through the hub (HubTransport) is one; a direct runner-to-runner
// transport can replace it without touching the store or the tools.
//
// Messages are opaque bytes to a transport: it must not read, keep or log
// them. Service seals them end to end (crypto.go): a transport only ever
// carries ciphertext, public keys and keys wrapped for one runner.
type Transport interface {
	// SendToPeers delivers msg to the project's other runners that are
	// reachable now, and returns their replies (one per runner that gave a
	// non-empty one).
	SendToPeers(ctx context.Context, project string, msg []byte) (Sent, error)
	// ReceiveFromPeers sets the handler for messages from other runners.
	ReceiveFromPeers(h Receiver)
}

// Receiver handles a message from another runner of project; its reply
// goes back to the sender.
type Receiver func(ctx context.Context, project string, msg []byte) (reply []byte, err error)

// Sent is what SendToPeers got back.
type Sent struct {
	Replies [][]byte
	// Members are the project's runners, online or not, as the transport
	// knows them (nil: it doesn't say). A runner that held the project's
	// key and is no longer among them makes the others rotate it.
	Members []string
}

// envelopeV is the version of envelope this runner speaks.
const envelopeV = 1

// envelope is what travels between runners: a notes message sealed with
// the project key, or key traffic (public keys, and keys wrapped for one
// runner).
type envelope struct {
	V      int       `json:"v"`
	Sealed *sealed   `json:"sealed,omitempty"`
	Hello  *hello    `json:"hello,omitempty"`
	Wraps  []wrapped `json:"wraps,omitempty"`
}

// hello introduces a runner: its identity key, and the project keys it
// holds. A peer that approved its key wraps the ones it lacks for it.
type hello struct {
	Runner string    `json:"runner"`
	Name   string    `json:"name,omitempty"`
	Key    PublicKey `json:"key"`
	Have   []string  `json:"have,omitempty"` // ids of its project keys
	// Cert is the root's certificate for its key, if it is enrolled
	// (enroll.go), and Revocations the root revocations it knows: a peer
	// keeps those its own root signed, and passes them on.
	Cert        *Cert        `json:"cert,omitempty"`
	Revocations []Revocation `json:"revocations,omitempty"`
}

// Message kinds.
const (
	kindEntries = "entries" // writes, as they happen or to fill a peer's gaps
	kindWant    = "want"    // a catch-up: the sender's vector
)

// message is what runners send each other, sealed in an envelope.
type message struct {
	Kind    string  `json:"kind"`
	Entries []Entry `json:"entries,omitempty"`
	Vector  Vector  `json:"vector,omitempty"`
}

// answer is the reply to a "want": what the asker lacks, and the vector of
// the copy it came from.
type answer struct {
	Entries []Entry `json:"entries,omitempty"`
	Vector  Vector  `json:"vector"`
	// Stale: the answering copy hasn't synced within the retention window
	// and offers nothing (it resyncs in full itself).
	Stale bool `json:"stale,omitempty"`
}

func encode(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
