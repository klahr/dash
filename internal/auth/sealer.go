package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// A Sealer encrypts the secrets Dash keeps in its database but must use in
// the clear (Google client secrets), with AES-256-GCM under the hub's secret
// key. Each value is bound to where it is stored (its context), so a sealed
// value copied to another row or org doesn't open there.
type Sealer struct{ aead cipher.AEAD }

const sealPrefix = "v1:"

// SecretKeySize is the length of the hub's secret key.
const SecretKeySize = 32

// ErrNoSecretKey is what storing a secret without a key gives.
var ErrNoSecretKey = errors.New("this Dash has no secret key to encrypt client secrets with (DASH_SECRET_KEY)")

// NewSealer makes a Sealer with key (SecretKeySize bytes).
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != SecretKeySize {
		return nil, fmt.Errorf("the secret key must be %d bytes, not %d", SecretKeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plain for context. An empty plain stays empty.
func (s *Sealer) Seal(plain, context string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if s == nil {
		return "", ErrNoSecretKey
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, []byte(plain), []byte(context))
	return sealPrefix + base64.RawStdEncoding.EncodeToString(out), nil
}

// Open decrypts what Seal made for context.
func (s *Sealer) Open(sealed, context string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if s == nil {
		return "", ErrNoSecretKey
	}
	b64, ok := strings.CutPrefix(sealed, sealPrefix)
	if !ok {
		return "", errors.New("not a sealed secret")
	}
	b, err := base64.RawStdEncoding.DecodeString(b64)
	if err != nil || len(b) < s.aead.NonceSize() {
		return "", errors.New("a sealed secret is damaged")
	}
	plain, err := s.aead.Open(nil, b[:s.aead.NonceSize()], b[s.aead.NonceSize():], []byte(context))
	if err != nil {
		return "", errors.New("a sealed secret doesn't open with this secret key (was DASH_SECRET_KEY or the key file changed?)")
	}
	return string(plain), nil
}

// SetSecretKey lets Dash keep client secrets in its database, sealed with
// key (see LoadSecretKey).
func (a *Auth) SetSecretKey(key []byte) error {
	s, err := NewSealer(key)
	if err != nil {
		return err
	}
	a.sealer = s
	return nil
}

// LoadSecretKey is the hub's secret key: value (DASH_SECRET_KEY: 64 hex
// digits or 32 bytes in base64) when set, else the key in the file at path,
// made (readable by its owner only) if there is none.
func LoadSecretKey(value, path string) ([]byte, error) {
	if value = strings.TrimSpace(value); value != "" {
		return parseKey(value)
	}
	b, err := os.ReadFile(path)
	if err == nil {
		return parseKey(string(b))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, SecretKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(hex.EncodeToString(key) + "\n"); err != nil {
		f.Close()
		return nil, err
	}
	return key, f.Close()
}

func parseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := hex.DecodeString(s); err == nil && len(b) == SecretKeySize {
		return b, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == SecretKeySize {
			return b, nil
		}
	}
	return nil, fmt.Errorf("the secret key must be %d bytes as 64 hex digits or base64", SecretKeySize)
}
