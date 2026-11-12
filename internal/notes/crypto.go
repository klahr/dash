package notes

// End-to-end encryption of shared notes. The hub relays them but never holds
// a key: only runners do.
//
//   - Each runner has an identity key pair (X25519, NaCl box): the private
//     key stays on the runner (Identity, next to its other secrets), the
//     public key is registered with the hub and sent to peers.
//   - Each project has a random symmetric key (NaCl secretbox) that seals
//     every notes message between its runners. A runner wraps it for a peer
//     with box (its private key, the peer's public key), only once the user
//     has approved the peer's key on it (Trust).
//   - When a runner leaves a project, the remaining runners rotate to a new
//     key of a higher epoch and wrap it for each other; messages under
//     older keys are refused from then on.
//
// No crypto of our own: golang.org/x/crypto/nacl only.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/nacl/secretbox"

	"github.com/klahr/dash/internal/fsutil"
)

// Errors of the encryption layer.
var (
	ErrKeyChanged   = errors.New("a runner's shared notes key changed")
	ErrNoProjectKey = errors.New("no project key to open this shared notes message")
	ErrBadFP        = errors.New("a key fingerprint is 20 letters and digits, e.g. ABCD-EFGH-JKLM-NPQR-STUV")
)

// PublicKey is a runner's identity public key (X25519).
type PublicKey [32]byte

// String is k in base64, as sent and kept.
func (k PublicKey) String() string { return base64.StdEncoding.EncodeToString(k[:]) }

// MarshalText implements encoding.TextMarshaler.
func (k PublicKey) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (k *PublicKey) UnmarshalText(b []byte) error {
	p, err := ParsePublicKey(string(b))
	if err != nil {
		return err
	}
	*k = p
	return nil
}

// IsZero reports whether k is unset.
func (k PublicKey) IsZero() bool { return k == PublicKey{} }

// ParsePublicKey reads a key written by String.
func ParsePublicKey(s string) (PublicKey, error) {
	var k PublicKey
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != len(k) {
		return k, errors.New("not a shared notes public key")
	}
	copy(k[:], b)
	if k.IsZero() {
		return k, errors.New("not a shared notes public key")
	}
	return k, nil
}

// fpLen is the length of a fingerprint in base32 letters: 100 bits, too
// many for a hub to grind out a key of its own with the same fingerprint.
const fpLen = 20

// Fingerprint is the short form of k a user compares between runners:
// 20 letters and digits of its SHA-256, in groups of four.
func (k PublicKey) Fingerprint() string {
	sum := sha256.Sum256(k[:])
	s := base32.StdEncoding.EncodeToString(sum[:])[:fpLen]
	var b strings.Builder
	for i := 0; i < len(s); i += 4 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(s[i : i+4])
	}
	return b.String()
}

// NormalizeFingerprint returns fp as Fingerprint writes it (any case,
// with or without dashes and spaces), or ErrBadFP.
func NormalizeFingerprint(fp string) (string, error) {
	s := strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == ' ':
			return -1
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		}
		return r
	}, fp)
	if len(s) != fpLen {
		return "", ErrBadFP
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= '2' && r <= '7') {
			return "", ErrBadFP
		}
	}
	var b strings.Builder
	for i := 0; i < len(s); i += 4 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(s[i : i+4])
	}
	return b.String(), nil
}

// Identity is this runner's key pair.
type Identity struct {
	Public  PublicKey
	private [32]byte
}

// NewIdentity makes a fresh key pair.
func NewIdentity() (*Identity, error) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{Public: *pub, private: *priv}, nil
}

// Fingerprint is the fingerprint of the identity's public key.
func (id *Identity) Fingerprint() string { return id.Public.Fingerprint() }

// IdentityPath is the identity file in a runner's data directory.
func IdentityPath(dataDir string) string { return filepath.Join(dataDir, "notes-identity.json") }

type identityFile struct {
	Public  PublicKey `json:"public"`
	Private []byte    `json:"private"`
	Created time.Time `json:"created"`
}

// ReadIdentity reads the identity saved at path.
func ReadIdentity(path string) (*Identity, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f identityFile
	if err := json.Unmarshal(b, &f); err != nil || len(f.Private) != 32 {
		return nil, fmt.Errorf("%s: not a shared notes identity", path)
	}
	id := &Identity{Public: f.Public}
	copy(id.private[:], f.Private)
	// The public key must be the private key's.
	derived, err := curve25519.X25519(id.private[:], curve25519.Basepoint)
	if err != nil || PublicKey(derived) != id.Public {
		return nil, fmt.Errorf("%s: its public key doesn't match its private key", path)
	}
	return id, nil
}

// LoadIdentity reads the identity at path, making and saving one (readable
// by this user only) the first time.
func LoadIdentity(path string) (*Identity, error) {
	id, err := ReadIdentity(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return id, err
	}
	return newIdentityAt(path)
}

// RotateIdentity replaces the identity at path with a new one, and returns
// it. The file is replaced whole (a reader sees the old or the new); the
// running runner switches to it (Service.IdentityFile).
func RotateIdentity(path string) (*Identity, error) {
	return newIdentityAt(path)
}

func newIdentityAt(path string) (*Identity, error) {
	id, err := NewIdentity()
	if err != nil {
		return nil, err
	}
	b, _ := json.MarshalIndent(identityFile{Public: id.Public, Private: id.private[:], Created: time.Now().UTC()}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := fsutil.WriteFileAtomic(path, b, 0o600); err != nil {
		return nil, err
	}
	return id, nil
}

// projectKey is a project's symmetric key. Every runner of the project
// keeps the keys of the latest epoch: one, or several when runners made
// keys of the same epoch at once (all of them open messages; the highest
// ID seals).
type projectKey struct {
	ID      string    `json:"id"`
	Epoch   uint64    `json:"epoch"`
	Secret  []byte    `json:"secret"`
	Created time.Time `json:"created"`
}

func (k projectKey) valid() bool {
	return len(k.Secret) == 32 && k.Epoch > 0 && len(k.ID) == 16 && isHex(k.ID)
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

// newProjectKey makes a random key of epoch.
func newProjectKey(epoch uint64, now time.Time) (projectKey, error) {
	k := projectKey{Epoch: epoch, Secret: make([]byte, 32), Created: now.UTC()}
	id := make([]byte, 8)
	if _, err := rand.Read(k.Secret); err != nil {
		return k, err
	}
	if _, err := rand.Read(id); err != nil {
		return k, err
	}
	k.ID = hex.EncodeToString(id)
	return k, nil
}

// sealed is a notes message as it travels: sealed with a project key.
type sealed struct {
	Key   string `json:"key"` // the projectKey's ID
	Nonce []byte `json:"nonce"`
	Box   []byte `json:"box"`
}

// seal seals plain with k.
func seal(k projectKey, plain []byte) (sealed, error) {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return sealed{}, err
	}
	var secret [32]byte
	copy(secret[:], k.Secret)
	return sealed{Key: k.ID, Nonce: nonce[:], Box: secretbox.Seal(nil, plain, &nonce, &secret)}, nil
}

// open opens s with the one of keys it was sealed with.
func open(keys []projectKey, s sealed) ([]byte, error) {
	if len(s.Nonce) != 24 {
		return nil, ErrBadEntry
	}
	for _, k := range keys {
		if k.ID != s.Key {
			continue
		}
		var nonce [24]byte
		var secret [32]byte
		copy(nonce[:], s.Nonce)
		copy(secret[:], k.Secret)
		plain, ok := secretbox.Open(nil, s.Box, &nonce, &secret)
		if !ok {
			return nil, ErrBadEntry
		}
		return plain, nil
	}
	return nil, ErrNoProjectKey
}

// wrapped is a project's keys wrapped for one runner: boxed from the
// sender's identity to the recipient's. Only the recipient opens it, and
// it knows who sent it.
type wrapped struct {
	From    string    `json:"from"`
	FromKey PublicKey `json:"from_key"`
	To      string    `json:"to"`
	ToKey   PublicKey `json:"to_key"`
	Nonce   []byte    `json:"nonce"`
	Box     []byte    `json:"box"`
}

// keyset is what a wrapped carries: the project (so a hub can't replay it
// into another project), the keys, and the runners known to hold them.
type keyset struct {
	Project string       `json:"project"`
	To      string       `json:"to"`
	Keys    []projectKey `json:"keys"`
	Holders []string     `json:"holders,omitempty"`
}

// wrap boxes ks from id (runner self) for runner to, whose key is toKey.
func (id *Identity) wrap(self, to string, toKey PublicKey, ks keyset) (wrapped, error) {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return wrapped{}, err
	}
	ks.To = to
	plain, err := json.Marshal(ks)
	if err != nil {
		return wrapped{}, err
	}
	pub := [32]byte(toKey)
	return wrapped{From: self, FromKey: id.Public, To: to, ToKey: toKey, Nonce: nonce[:],
		Box: box.Seal(nil, plain, &nonce, &pub, &id.private)}, nil
}

// unwrap opens w, addressed to id as runner self, for project.
func (id *Identity) unwrap(self, project string, w wrapped) (keyset, error) {
	var ks keyset
	if w.To != self || w.ToKey != id.Public || len(w.Nonce) != 24 {
		return ks, ErrBadEntry
	}
	var nonce [24]byte
	copy(nonce[:], w.Nonce)
	pub := [32]byte(w.FromKey)
	plain, ok := box.Open(nil, w.Box, &nonce, &pub, &id.private)
	if !ok {
		return ks, ErrBadEntry
	}
	if err := json.Unmarshal(plain, &ks); err != nil {
		return ks, ErrBadEntry
	}
	if ks.Project != project || ks.To != self || len(ks.Keys) == 0 || len(ks.Keys) > 16 {
		return ks, ErrBadEntry
	}
	for _, k := range ks.Keys {
		if !k.valid() {
			return ks, ErrBadEntry
		}
	}
	return ks, nil
}
