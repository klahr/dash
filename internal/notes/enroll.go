package notes

// Enrollment: trust between runners by a key the user holds, instead of
// approving every pair of runners on both (approve, trust.go).
//
// The user has one ed25519 SSH key, the root (`dash runner notes keygen`,
// or ssh-keygen -t ed25519). Enrolling a runner with it (`dash runner notes
// enroll --key`) keeps the root's public key on the runner, and a
// certificate the root signed: "runner R's notes key is K". Runners send
// their certificate with their hello; a runner trusts a peer whose
// certificate verifies against its own root and names the peer's id and
// current key. The private root key is only read by the CLI while it
// signs, and is never kept or sent anywhere.
//
// The root also signs revocations ("notes key with fingerprint F is never
// to be trusted again"). Runners pass the ones that verify against their
// root on in every hello, of every project, so they reach runners that
// share no project with the one where the user revoked. A revocation signed
// by anything but the root is ignored, so a compromised runner can't revoke
// honest ones.
//
// The hub relays certificates and revocations but can't make one: it can
// withhold them (a runner then isn't trusted, or a revocation doesn't
// arrive: `dash runner notes revoke` on each runner is the fallback).
//
// Signatures are SSHSIG, as `ssh-keygen -Y sign` makes them, so they can be
// checked with ssh-keygen -Y check-novalidate too.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/klahr/dash/internal/fsutil"
)

// SSHSIG namespaces of certificates and revocations.
const (
	EnrollNamespace = "dash-notes-enroll-v1"
	RevokeNamespace = "dash-notes-revoke-v1"
)

// Errors of enrollment.
var (
	ErrNotEnrolled  = errors.New("this runner isn't enrolled with a root key: dash runner notes enroll --key <path>")
	ErrOtherRoot    = errors.New("this runner is enrolled under a different root key")
	ErrRootRevoked  = errors.New("that key was revoked with the root key")
	ErrNotEd25519   = errors.New("the root key must be an ed25519 key (ssh-keygen -t ed25519)")
	errBadSignature = errors.New("bad signature")
)

// Limits on revocations: how many a runner keeps, and sends in a hello.
const (
	maxRevocations      = 1024
	maxHelloRevocations = 256
)

// ParseRoot reads an ed25519 SSH public key in authorized_keys form.
func ParseRoot(line string) (ssh.PublicKey, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, fmt.Errorf("not an SSH public key: %w", err)
	}
	if pub.Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("%w, not %s", ErrNotEd25519, pub.Type())
	}
	return pub, nil
}

// RootFingerprint is root's fingerprint as ssh-keygen -l prints it
// (SHA256:…).
func RootFingerprint(root ssh.PublicKey) string { return ssh.FingerprintSHA256(root) }

func rootLine(pub ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
}

// --- SSHSIG ---

const sshsigMagic = "SSHSIG"

func putString(b *bytes.Buffer, s []byte) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(s)))
	b.Write(n[:])
	b.Write(s)
}

func getString(b []byte) (s, rest []byte, ok bool) {
	if len(b) < 4 {
		return nil, nil, false
	}
	n := binary.BigEndian.Uint32(b)
	if uint64(len(b)-4) < uint64(n) {
		return nil, nil, false
	}
	return b[4 : 4+n], b[4+n:], true
}

// sshsigSigned is what an SSHSIG signature signs, for msg in namespace.
func sshsigSigned(namespace string, msg []byte) []byte {
	h := sha512.Sum512(msg)
	var b bytes.Buffer
	b.WriteString(sshsigMagic)
	putString(&b, []byte(namespace))
	putString(&b, nil) // reserved
	putString(&b, []byte("sha512"))
	putString(&b, h[:])
	return b.Bytes()
}

// sshsign signs msg in namespace with signer (an ed25519 key or an agent's)
// and returns the signature armored as ssh-keygen -Y sign writes it.
func sshsign(signer ssh.Signer, namespace string, msg []byte) (string, error) {
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return "", fmt.Errorf("%w, not %s", ErrNotEd25519, signer.PublicKey().Type())
	}
	sig, err := signer.Sign(rand.Reader, sshsigSigned(namespace, msg))
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	b.WriteString(sshsigMagic)
	binary.Write(&b, binary.BigEndian, uint32(1))
	putString(&b, signer.PublicKey().Marshal())
	putString(&b, []byte(namespace))
	putString(&b, nil)
	putString(&b, []byte("sha512"))
	putString(&b, ssh.Marshal(sig))
	enc := base64.StdEncoding.EncodeToString(b.Bytes())
	var out strings.Builder
	out.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(enc) > 70 {
		out.WriteString(enc[:70] + "\n")
		enc = enc[70:]
	}
	out.WriteString(enc + "\n-----END SSH SIGNATURE-----\n")
	return out.String(), nil
}

// sshverify checks that armored is a valid SSHSIG signature of msg in
// namespace, and returns the key that made it. Only ed25519 signers pass.
func sshverify(armored, namespace string, msg []byte) (ssh.PublicKey, error) {
	s := strings.TrimSpace(armored)
	s, ok1 := strings.CutPrefix(s, "-----BEGIN SSH SIGNATURE-----")
	s, ok2 := strings.CutSuffix(s, "-----END SSH SIGNATURE-----")
	if !ok1 || !ok2 || len(s) > 4096 {
		return nil, errBadSignature
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil || !bytes.HasPrefix(raw, []byte(sshsigMagic)) || len(raw) < 10 {
		return nil, errBadSignature
	}
	if binary.BigEndian.Uint32(raw[6:10]) != 1 {
		return nil, errBadSignature
	}
	rest := raw[10:]
	var pubB, ns, reserved, hashAlg, sigB []byte
	for _, p := range []*[]byte{&pubB, &ns, &reserved, &hashAlg, &sigB} {
		var ok bool
		if *p, rest, ok = getString(rest); !ok {
			return nil, errBadSignature
		}
	}
	if len(rest) != 0 || string(ns) != namespace || string(hashAlg) != "sha512" || len(reserved) != 0 {
		return nil, errBadSignature
	}
	pub, err := ssh.ParsePublicKey(pubB)
	if err != nil || pub.Type() != ssh.KeyAlgoED25519 {
		return nil, errBadSignature
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(sigB, &sig); err != nil || sig.Format != ssh.KeyAlgoED25519 {
		return nil, errBadSignature
	}
	if err := pub.Verify(sshsigSigned(namespace, msg), &sig); err != nil {
		return nil, errBadSignature
	}
	return pub, nil
}

func sameKey(a, b ssh.PublicKey) bool {
	return a != nil && b != nil && bytes.Equal(a.Marshal(), b.Marshal())
}

// --- certificates ---

// Cert is a root's certificate for a runner's notes key: the root vouches
// that runner Runner's key is Key.
type Cert struct {
	Runner string    `json:"runner"`
	Key    PublicKey `json:"key"`
	Issued time.Time `json:"issued"`
	Sig    string    `json:"sig"` // SSHSIG, namespace EnrollNamespace, over certMessage
}

func certMessage(runner string, key PublicKey, issued time.Time) []byte {
	return fmt.Appendf(nil, "%s\nrunner %s\nkey %s\nissued %s\n", EnrollNamespace, runner, key, issued.UTC().Format(time.RFC3339))
}

// IssueCert has signer (the root) certify that runner's notes key is key.
func IssueCert(signer ssh.Signer, runner string, key PublicKey, now time.Time) (Cert, error) {
	if runner == "" || strings.ContainsAny(runner, "\n\r") || key.IsZero() {
		return Cert{}, errors.New("a certificate needs a runner id and a key")
	}
	c := Cert{Runner: runner, Key: key, Issued: now.UTC().Truncate(time.Second)}
	var err error
	c.Sig, err = sshsign(signer, EnrollNamespace, certMessage(c.Runner, c.Key, c.Issued))
	return c, err
}

// signer is the key that signed c, if its signature holds (nil if not).
func (c *Cert) signer() ssh.PublicKey {
	if c == nil || c.Runner == "" || strings.ContainsAny(c.Runner, "\n\r") || c.Key.IsZero() {
		return nil
	}
	pub, err := sshverify(c.Sig, EnrollNamespace, certMessage(c.Runner, c.Key, c.Issued))
	if err != nil {
		return nil
	}
	return pub
}

// Root is the fingerprint of the key that signed c ("" if its signature
// doesn't hold).
func (c *Cert) Root() string {
	if pub := c.signer(); pub != nil {
		return RootFingerprint(pub)
	}
	return ""
}

// vouches reports whether c, signed by root, certifies key for runner.
func (c *Cert) vouches(root ssh.PublicKey, runner string, key PublicKey) bool {
	return c != nil && root != nil && c.Runner == runner && c.Key == key && sameKey(c.signer(), root)
}

// --- revocations ---

// Revocation is a root's word that the notes key with fingerprint
// Fingerprint is never to be trusted again.
type Revocation struct {
	Fingerprint string    `json:"fingerprint"`
	At          time.Time `json:"at"`
	Sig         string    `json:"sig"` // SSHSIG, namespace RevokeNamespace, over revocationMessage
}

func revocationMessage(fp string, at time.Time) []byte {
	return fmt.Appendf(nil, "%s\nfingerprint %s\nat %s\n", RevokeNamespace, fp, at.UTC().Format(time.RFC3339))
}

// IssueRevocation has signer (the root) revoke the notes key with
// fingerprint fp.
func IssueRevocation(signer ssh.Signer, fp string, now time.Time) (Revocation, error) {
	fp, err := NormalizeFingerprint(fp)
	if err != nil {
		return Revocation{}, err
	}
	r := Revocation{Fingerprint: fp, At: now.UTC().Truncate(time.Second)}
	r.Sig, err = sshsign(signer, RevokeNamespace, revocationMessage(r.Fingerprint, r.At))
	return r, err
}

// by reports whether r is signed by root.
func (r Revocation) by(root ssh.PublicKey) bool {
	if root == nil {
		return false
	}
	if n, err := NormalizeFingerprint(r.Fingerprint); err != nil || n != r.Fingerprint {
		return false
	}
	pub, err := sshverify(r.Sig, RevokeNamespace, revocationMessage(r.Fingerprint, r.At))
	return err == nil && sameKey(pub, root)
}

// --- the enrollment file ---

// EnrollmentPath is the enrollment file in a runner's shared notes folder.
// Like the approvals file, the CLI writes it and the runner reads it.
func EnrollmentPath(sharedDir string) string { return filepath.Join(sharedDir, "enrollment.json") }

// Enrollment is this runner's root and the certificate it signed for it.
type Enrollment struct {
	Root string    `json:"root"` // the root's public key, authorized_keys form
	Cert Cert      `json:"cert"`
	At   time.Time `json:"at"`
	// Dropped are the runners trusted by the previous root's certificates
	// when the runner was enrolled under this one (--force): they hold the
	// project keys, so the runner rotates them unless trusted again.
	Dropped []string `json:"dropped,omitempty"`
}

// RootKey is e's root (nil if unreadable).
func (e *Enrollment) RootKey() ssh.PublicKey {
	if e == nil {
		return nil
	}
	pub, err := ParseRoot(e.Root)
	if err != nil {
		return nil
	}
	return pub
}

// ReadEnrollment reads the enrollment file (none: nil, nil).
func ReadEnrollment(sharedDir string) (*Enrollment, error) {
	b, err := os.ReadFile(EnrollmentPath(sharedDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var e Enrollment
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, fmt.Errorf("%s: %w", EnrollmentPath(sharedDir), err)
	}
	if e.RootKey() == nil {
		return nil, fmt.Errorf("%s: no ed25519 root key in it", EnrollmentPath(sharedDir))
	}
	return &e, nil
}

// Enroll enrolls runner, whose notes key is key, under signer's key: it
// keeps that public key as the runner's root, and a certificate it signs.
// A runner enrolled under another root stays so (ErrOtherRoot) unless
// force: then the trust the old root's certificates gave is dropped
// (manual approvals stay).
func Enroll(sharedDir string, signer ssh.Signer, runner string, key PublicKey, force bool, now time.Time) (*Enrollment, error) {
	root := signer.PublicKey()
	if root.Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("%w, not %s", ErrNotEd25519, root.Type())
	}
	old, err := ReadEnrollment(sharedDir)
	if err != nil && !force {
		return nil, err
	}
	var dropped []string
	if old != nil {
		oldRoot := old.RootKey()
		if !sameKey(oldRoot, root) {
			if !force {
				return nil, fmt.Errorf("%w (%s); enrolling under %s would drop the trust it gives: pass --force to do so",
					ErrOtherRoot, RootFingerprint(oldRoot), RootFingerprint(root))
			}
			peers, _ := ReadPeers(sharedDir)
			for id, p := range peers.Pins {
				if p.Cert.vouches(oldRoot, id, p.Key) {
					dropped = append(dropped, id)
				}
			}
			slices.Sort(dropped)
		} else {
			dropped = old.Dropped
		}
	}
	cert, err := IssueCert(signer, runner, key, now)
	if err != nil {
		return nil, err
	}
	e := &Enrollment{Root: rootLine(root), Cert: cert, At: now.UTC(), Dropped: dropped}
	if err := os.MkdirAll(sharedDir, 0o700); err != nil {
		return nil, err
	}
	b, _ := json.MarshalIndent(e, "", "  ")
	return e, fsutil.WriteFileAtomic(EnrollmentPath(sharedDir), b, 0o600)
}

// RevokeWithRoot revokes the notes key with fingerprint fp with the root
// key signer: on this runner now, and on every runner enrolled under the
// same root as the revocation reaches them. This runner must be enrolled
// under signer's key.
func RevokeWithRoot(sharedDir string, signer ssh.Signer, fp string, now time.Time) (Revocation, error) {
	e, err := ReadEnrollment(sharedDir)
	if err != nil {
		return Revocation{}, err
	}
	if e == nil {
		return Revocation{}, ErrNotEnrolled
	}
	if root := e.RootKey(); !sameKey(root, signer.PublicKey()) {
		return Revocation{}, fmt.Errorf("%w (%s), not %s: revoke with that key, on a runner enrolled under it",
			ErrOtherRoot, RootFingerprint(root), RootFingerprint(signer.PublicKey()))
	}
	r, err := IssueRevocation(signer, fp, now)
	if err != nil {
		return r, err
	}
	a, err := ReadApprovals(sharedDir)
	if err != nil {
		return r, err
	}
	a.RootRevocations = append(a.RootRevocations, r)
	return r, writeApprovals(sharedDir, a)
}

// --- what a runner trusts ---

// Standing is how this runner stands to a peer's key.
type Standing string

// Standings, strongest first.
const (
	RevokedByRoot Standing = "revoked by the root key"
	RevokedHere   Standing = "revoked here"
	Approved      Standing = "approved here"
	Certified     Standing = "trusted by certificate"
	OtherRoot     Standing = "enrolled under a different root key"
	Unknown       Standing = "not approved"
)

// Trusted reports whether a peer of this standing gets and gives keys.
func (s Standing) Trusted() bool { return s == Approved || s == Certified }

// TrustState is what a runner's trust files say: approvals, its
// enrollment and the root revocations it knows.
type TrustState struct {
	Enrollment *Enrollment
	root       ssh.PublicKey
	appr       Approvals
	revoked    map[string]Revocation // by fingerprint, valid under root
}

func newTrustState(a Approvals, e *Enrollment, learned []Revocation) *TrustState {
	s := &TrustState{Enrollment: e, root: e.RootKey(), appr: a, revoked: map[string]Revocation{}}
	for _, r := range slices.Concat(a.RootRevocations, learned) {
		if _, ok := s.revoked[r.Fingerprint]; !ok && r.by(s.root) {
			s.revoked[r.Fingerprint] = r
		}
	}
	return s
}

// ReadTrustState reads a runner's trust files (for the CLI; the runner
// keeps its own).
func ReadTrustState(sharedDir string) (*TrustState, Peers, error) {
	a, err := ReadApprovals(sharedDir)
	if err != nil {
		return nil, Peers{}, err
	}
	e, err := ReadEnrollment(sharedDir)
	if err != nil {
		return nil, Peers{}, err
	}
	p, err := ReadPeers(sharedDir)
	if err != nil {
		return nil, Peers{}, err
	}
	return newTrustState(a, e, p.Revocations), p, nil
}

// Root is the root's fingerprint ("": not enrolled).
func (s *TrustState) Root() string {
	if s.root == nil {
		return ""
	}
	return RootFingerprint(s.root)
}

// RootRevoked is the root's revocation of fingerprint fp, if any.
func (s *TrustState) RootRevoked(fp string) (Revocation, bool) {
	r, ok := s.revoked[fp]
	return r, ok
}

// RootRevocations are the valid root revocations known, newest first.
func (s *TrustState) RootRevocations() []Revocation {
	out := make([]Revocation, 0, len(s.revoked))
	for _, r := range s.revoked {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Revocation) int {
		if c := b.At.Compare(a.At); c != 0 {
			return c
		}
		return strings.Compare(a.Fingerprint, b.Fingerprint)
	})
	return out
}

// Of is how this runner stands to runner, seen with key and cert (nil:
// none). A revocation beats everything; a root revocation even a manual
// approval, unless the user approved over it (approve --force).
func (s *TrustState) Of(runner string, key PublicKey, cert *Cert) Standing {
	fp := key.Fingerprint()
	if _, ok := s.appr.Revoked[fp]; ok {
		return RevokedHere
	}
	_, approved := s.appr.Approved[fp]
	if _, ok := s.revoked[fp]; ok {
		if _, over := s.appr.Override[fp]; !(approved && over) {
			return RevokedByRoot
		}
	}
	if approved {
		return Approved
	}
	if cert.vouches(s.root, runner, key) {
		return Certified
	}
	if cert != nil && cert.Runner == runner && cert.Key == key {
		if pub := cert.signer(); pub != nil && !sameKey(pub, s.root) {
			return OtherRoot
		}
	}
	return Unknown
}

// otherRoot is the fingerprint of the root cert is signed by, when that
// isn't this runner's ("" otherwise).
func (s *TrustState) otherRoot(cert *Cert) string {
	if pub := cert.signer(); pub != nil && !sameKey(pub, s.root) {
		return RootFingerprint(pub)
	}
	return ""
}

// selfCert is this runner's certificate, if it still certifies key as
// runner's.
func (s *TrustState) selfCert(runner string, key PublicKey) *Cert {
	if s.Enrollment == nil || !s.Enrollment.Cert.vouches(s.root, runner, key) {
		return nil
	}
	c := s.Enrollment.Cert
	return &c
}
