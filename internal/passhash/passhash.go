// Package passhash hashes passwords with Argon2id, stored as standard PHC
// strings:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<b64 salt>$<b64 hash>
//
// The string carries its own parameters, so raising them later still
// verifies old hashes and Verify reports when one should be rehashed.
package passhash

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/argon2"
)

// params are the cost settings Hash uses.
type params struct {
	memoryKiB uint32
	passes    uint32
	lanes     uint8
}

// production is OWASP's first recommendation (2024): 19 MiB, 2 passes, 1
// lane. About 50 ms a guess, and every guess has to hold 19 MiB.
var production = params{memoryKiB: 19 * 1024, passes: 2, lanes: 1}

// cheap is the least Argon2id allows, for tests only (UseCheapParamsInTests).
var cheap = params{memoryKiB: 8, passes: 1, lanes: 1}

const (
	saltLen        = 16
	keyLen  uint32 = 32
)

// testParams, when set, replaces production in a test binary.
var testParams atomic.Pointer[params]

// current is what Hash uses now: production, except in a test binary that
// called UseCheapParamsInTests.
func current() params {
	if p := testParams.Load(); p != nil {
		return *p
	}
	return production
}

// UseCheapParamsInTests makes Hash use the cheapest Argon2id settings, so
// tests that create and sign in accounts don't spend their time hashing.
// Hashes keep their parameters, so either kind still verifies. It panics
// outside a test binary (built by go test): production always hashes with
// the real settings. Call it from TestMain, before any test runs.
func UseCheapParamsInTests() {
	if !testing.Testing() {
		panic("passhash: cheap parameters are for tests only")
	}
	p := cheap
	testParams.Store(&p)
}

// ErrMalformed is returned for a stored value that isn't an Argon2id PHC
// string this package wrote.
var ErrMalformed = errors.New("passhash: malformed hash")

// Hash returns a new Argon2id PHC string for password.
func Hash(password string) (string, error) { return hashWith(current(), password) }

func hashWith(p params, password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.passes, p.memoryKiB, p.lanes, keyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.memoryKiB, p.passes, p.lanes,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// Verify reports whether password matches stored, in constant time, and
// whether stored uses weaker parameters than Hash does now (so the caller
// should store a fresh Hash).
func Verify(stored, password string) (ok, rehash bool, err error) {
	// ["", "argon2id", "v=19", "m=…,t=…,p=…", salt, hash]
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, false, ErrMalformed
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false, ErrMalformed
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 {
		return false, false, ErrMalformed
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false, false, ErrMalformed
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, false, ErrMalformed
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	cur := current()
	return true, m < cur.memoryKiB || t < cur.passes || p < cur.lanes, nil
}

var (
	decoyMu sync.Mutex
	decoy   string
)

// Prime makes the decoy hash SpendVerifyTime uses. Call it at startup, so
// the first login for an unknown name doesn't take longer than the rest
// (making it costs a hash on top of the verify).
func Prime() {
	decoyMu.Lock()
	defer decoyMu.Unlock()
	if decoy == "" {
		decoy, _ = Hash("not the password of anyone")
	}
}

// SpendVerifyTime does the work of a Verify and throws the result away. A
// login for a name that doesn't exist calls it, so it takes as long as a
// wrong password for one that does: the timing doesn't tell which names
// have accounts.
func SpendVerifyTime(password string) {
	Prime()
	decoyMu.Lock()
	d := decoy
	decoyMu.Unlock()
	if d != "" {
		_, _, _ = Verify(d, password)
	}
}
