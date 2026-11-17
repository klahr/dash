package dispatch

import (
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"
)

// NotLinked is a runner's error for a request about a project the hub
// hasn't linked to it.
const NotLinked = "that project isn't linked to this runner"

// linkFresh is how long the hub trusts a runner's answer to a link it
// didn't change. A link it sent again only after that picks up what the
// runner does on a link even when nothing changed (moving an old clone into
// its org's folder once the clone is idle).
const linkFresh = 10 * time.Minute

// linkCache keeps the last link the hub sent each runner for each project,
// and the runner's answer, so the hub sends a project's link again only
// when its settings change, the runner reconnects, or the runner says it
// doesn't have the project.
type linkCache struct {
	mu      sync.Mutex
	entries map[[2]string]linkEntry // {runner id, hub project id} → the last link
}

type linkEntry struct {
	sum [sha256.Size]byte // of the Link sent
	out Linked
	at  time.Time
}

func linkSum(l Link) [sha256.Size]byte {
	b, _ := json.Marshal(l)
	return sha256.Sum256(b)
}

// get is runner's answer to link l of project, when the hub sent that
// same link lately.
func (c *linkCache) get(runner, project string, sum [sha256.Size]byte, now time.Time) (Linked, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[[2]string{runner, project}]
	if !ok || e.sum != sum || now.Sub(e.at) >= linkFresh {
		return Linked{}, false
	}
	return e.out, true
}

func (c *linkCache) put(runner, project string, sum [sha256.Size]byte, out Linked, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[[2]string]linkEntry{}
	}
	c.entries[[2]string{runner, project}] = linkEntry{sum: sum, out: out, at: now}
}

// forget drops runner's links: of project, or of every project when
// project is "".
func (c *linkCache) forget(runner, project string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if k[0] == runner && (project == "" || k[1] == project) {
			delete(c.entries, k)
		}
	}
}

// Relink makes the hub send project's link to runner again the next time
// it needs it: the runner said it doesn't have the project.
func (d *Dispatcher) Relink(runner, project string) { d.links.forget(runner, project) }
