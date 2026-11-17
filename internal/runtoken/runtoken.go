// Package runtoken issues the per-run bearer tokens that authenticate a
// Claude run's calls to Dash's MCP endpoints.
package runtoken

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// Map maps random bearer tokens to a value of type V. The zero value is
// ready to use; it is safe for concurrent use.
type Map[V any] struct {
	mu sync.Mutex
	m  map[string]V
}

// New returns a random token: 24 bytes, hex-encoded.
func New() string {
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// Register issues a new token for v. Call release when the run ends; the
// token is unknown after that.
func (t *Map[V]) Register(v V) (token string, release func()) {
	token = New()
	t.mu.Lock()
	if t.m == nil {
		t.m = map[string]V{}
	}
	t.m[token] = v
	t.mu.Unlock()
	return token, func() {
		t.mu.Lock()
		delete(t.m, token)
		t.mu.Unlock()
	}
}

// Lookup returns the value registered for token, if it is still registered.
func (t *Map[V]) Lookup(token string) (v V, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok = t.m[token]
	return v, ok
}
