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
// them (end-to-end encryption can then be layered on in Service).
type Transport interface {
	// SendToPeers delivers msg to the project's other runners that are
	// reachable now, and returns their replies (one per runner that gave a
	// non-empty one).
	SendToPeers(ctx context.Context, project string, msg []byte) (replies [][]byte, err error)
	// ReceiveFromPeers sets the handler for messages from other runners.
	ReceiveFromPeers(h Receiver)
}

// Receiver handles a message from another runner of project; its reply
// goes back to the sender.
type Receiver func(ctx context.Context, project string, msg []byte) (reply []byte, err error)

// Message kinds.
const (
	kindEntries = "entries" // writes, as they happen or to fill a peer's gaps
	kindWant    = "want"    // a catch-up: the sender's vector
)

// message is what runners send each other, inside the transport's opaque
// bytes.
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
}

func encode(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
