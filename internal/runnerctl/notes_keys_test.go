package runnerctl

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/klahr/dash/internal/notes"
	"github.com/klahr/dash/internal/tunnel"
)

// keyCLI is a CLI whose terminal answers with tty's lines, and a runner
// registered in data.
type keyCLI struct {
	*CLI
	out, errOut bytes.Buffer
	data        string
	tty         string // what the user types at the passphrase prompts
	asked       int    // terminals opened
}

func newKeyCLI(t *testing.T) *keyCLI {
	t.Helper()
	t.Setenv("SSH_AUTH_SOCK", "")
	k := &keyCLI{data: t.TempDir()}
	k.CLI = &CLI{Home: t.TempDir(), Hostname: "box", Out: &k.out, Err: &k.errOut}
	k.Terminal = func() (io.Reader, io.Writer, func(), error) {
		k.asked++
		return strings.NewReader(k.tty), io.Discard, nil, nil
	}
	if err := tunnel.SaveCredentials(k.CLI.credsPath(k.data), tunnel.Credentials{Server: "https://hub.example", ID: "r1", Name: "box", Token: "dr_x"}); err != nil {
		t.Fatal(err)
	}
	return k
}

func (k *keyCLI) run(args ...string) error {
	k.out.Reset()
	k.errOut.Reset()
	if code := k.Main(context.Background(), "notes", append(args, "-data", k.data)); code != 0 {
		return errors.New(strings.TrimSpace(k.errOut.String()))
	}
	return nil
}

func (k *keyCLI) shared() string { return filepath.Join(k.data, "shared") }

// keygen makes a root key with passphrase pass at path.
func (k *keyCLI) keygen(t *testing.T, path, pass string) {
	t.Helper()
	k.tty = pass + "\n" + pass + "\n"
	if err := k.run("keygen", "--out", path); err != nil {
		t.Fatal(err)
	}
}

func TestNotesKeygen(t *testing.T) {
	k := newKeyCLI(t)
	path := filepath.Join(k.Home, "keys", "root")
	k.keygen(t, path, "correct horse")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ssh.ParseRawPrivateKeyWithPassphrase(b, []byte("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	priv, ok := raw.(*ed25519.PrivateKey)
	if !ok {
		t.Fatalf("not ed25519: %T", raw)
	}
	if _, err := ssh.ParseRawPrivateKey(b); err == nil {
		t.Fatal("the key isn't encrypted")
	}
	pb, _ := os.ReadFile(path + ".pub")
	pub, comment, _, _, err := ssh.ParseAuthorizedKey(pb)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ssh.NewPublicKey(priv.Public())
	if !bytes.Equal(pub.Marshal(), want.Marshal()) || comment != "dash-notes@box" {
		t.Fatalf(".pub doesn't match: %s", pb)
	}
	if !strings.Contains(k.out.String(), ssh.FingerprintSHA256(pub)) || !strings.Contains(k.out.String(), "enroll --key "+path) {
		t.Fatalf("output:\n%s", k.out.String())
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", fi.Mode())
		}
		if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v", fi.Mode())
		}
	}
	if _, err := exec.LookPath("ssh-keygen"); err == nil {
		out, err := exec.Command("ssh-keygen", "-y", "-P", "correct horse", "-f", path).Output()
		if err != nil {
			t.Fatalf("ssh-keygen -y: %v", err)
		}
		if got, _, _, _, err := ssh.ParseAuthorizedKey(out); err != nil || !bytes.Equal(got.Marshal(), pub.Marshal()) {
			t.Fatalf("ssh-keygen sees %s", out)
		}
	}

	// The key works for enroll.
	k.tty = "correct horse\n"
	if err := k.run("enroll", "--key", path); err != nil {
		t.Fatal(err)
	}
	e, err := notes.ReadEnrollment(k.shared())
	if err != nil || e == nil || !strings.HasPrefix(string(pb), e.Root) {
		t.Fatalf("enrollment %+v %v", e, err)
	}

	// Never over an existing file, the key or its .pub.
	k.tty = "x\nx\n"
	if err := k.run("keygen", "--out", path); err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Fatalf("overwrote: %v", err)
	}
	if again, _ := os.ReadFile(path); !bytes.Equal(again, b) {
		t.Fatal("the key changed")
	}
	other := filepath.Join(k.Home, "keys", "other")
	os.WriteFile(other+".pub", []byte("keep me"), 0o644)
	if err := k.run("keygen", "--out", other); err == nil {
		t.Fatal("wrote next to an existing .pub")
	}
	if _, err := os.Stat(other); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("wrote the key anyway")
	}

	// The passphrase: asked twice, and needed.
	p2 := filepath.Join(k.Home, "keys", "two")
	k.tty = "one\ntwo\n"
	if err := k.run("keygen", "--out", p2); err == nil || !strings.Contains(err.Error(), "don't match") {
		t.Fatalf("mismatch: %v", err)
	}
	k.tty = "\n\n"
	if err := k.run("keygen", "--out", p2); err == nil || !strings.Contains(err.Error(), "needs a passphrase") {
		t.Fatalf("empty: %v", err)
	}
	if _, err := os.Stat(p2); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a refused key was written")
	}
	asked := k.asked
	if err := k.run("keygen", "--out", p2, "--no-passphrase"); err != nil {
		t.Fatal(err)
	}
	if k.asked != asked {
		t.Fatal("--no-passphrase asked for one")
	}
	if b, _ := os.ReadFile(p2); func() error { _, err := ssh.ParseRawPrivateKey(b); return err }() != nil {
		t.Fatal("--no-passphrase key doesn't parse")
	}
}

// writeKey writes key (a crypto private key) in OpenSSH form at path.
func writeKey(t *testing.T, path string, key any, pass string) {
	t.Helper()
	var block *pem.Block
	var err error
	if pass == "" {
		block, err = ssh.MarshalPrivateKey(key, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte(pass))
	}
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
}

func TestNotesEnrollKeys(t *testing.T) {
	k := newKeyCLI(t)
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	k.keygen(t, root, "pw")

	// A wrong passphrase, three times.
	k.tty = "no\nnope\nstill no\n"
	if err := k.run("enroll", "--key", root); err == nil || !strings.Contains(err.Error(), "wrong passphrase") {
		t.Fatalf("wrong passphrase: %v", err)
	}
	// Then right on the second try.
	k.tty = "no\npw\n"
	if err := k.run("enroll", "--key", root); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(k.out.String(), "delete it from this computer") {
		t.Fatalf("no advice to delete the key:\n%s", k.out.String())
	}
	if err := k.run(); err != nil || !strings.Contains(k.out.String(), "Enrolled under root key SHA256:") {
		t.Fatalf("show: %v\n%s", err, k.out.String())
	}

	// Other key types are refused, by name, without asking a passphrase.
	_, rsaKey := mustRSA(t)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cases := map[string]func(p string){
		"RSA":   func(p string) { writeKey(t, p, rsaKey, "") },
		"RSA ":  func(p string) { writeKey(t, p, rsaKey, "secret") }, // encrypted
		"ECDSA": func(p string) { writeKey(t, p, ecKey, "") },
		"OpenPGP": func(p string) {
			os.WriteFile(p, []byte("-----BEGIN PGP PRIVATE KEY BLOCK-----\nxx\n-----END PGP PRIVATE KEY BLOCK-----\n"), 0o600)
		},
		"isn't an": func(p string) { os.WriteFile(p, []byte("garbage"), 0o600) },
		"RSA  .pub": func(p string) {
			s, _ := ssh.NewSignerFromKey(rsaKey)
			os.WriteFile(p, ssh.MarshalAuthorizedKey(s.PublicKey()), 0o644)
		},
	}
	for want, write := range cases {
		p := filepath.Join(dir, strings.ReplaceAll(want, " ", "_"))
		write(p)
		asked := k.asked
		err := k.run("enroll", "--force", "--key", p)
		if err == nil || !strings.Contains(err.Error(), strings.Fields(want)[0]) {
			t.Errorf("%s: %v", want, err)
		}
		if k.asked != asked {
			t.Errorf("%s: asked for a passphrase", want)
		}
	}

	// Under another root: only with --force.
	other := filepath.Join(dir, "other")
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	writeKey(t, other, otherKey, "")
	if err := k.run("enroll", "--key", other); err == nil || !strings.Contains(err.Error(), "different root") {
		t.Fatalf("another root without --force: %v", err)
	}
	if err := k.run("enroll", "--key", other, "--force"); err != nil {
		t.Fatal(err)
	}
	e, _ := notes.ReadEnrollment(k.shared())
	s, _ := ssh.NewSignerFromKey(otherKey)
	if e.Root != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey()))) {
		t.Fatal("not enrolled under the other root")
	}

	// From ssh-agent: the .pub is enough, and no passphrase is asked.
	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: otherKey}); err != nil {
		t.Fatal(err)
	}
	k.Agent = func() (agent.Agent, error) { return ring, nil }
	os.WriteFile(other+".pub", ssh.MarshalAuthorizedKey(s.PublicKey()), 0o644)
	os.Remove(other)
	asked := k.asked
	if err := k.run("enroll", "--key", other+".pub"); err != nil {
		t.Fatal(err)
	}
	if k.asked != asked || !strings.Contains(k.out.String(), "ssh-agent") {
		t.Fatalf("agent: asked %d\n%s", k.asked-asked, k.out.String())
	}
}

func mustRSA(t *testing.T) (ssh.PublicKey, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := ssh.NewPublicKey(&key.PublicKey)
	return pub, key
}

// revoke --key signs with the root; approve refuses a key so revoked
// unless --force; rotate --key gives a new key and enrolls it.
func TestNotesRevokeAndRotate(t *testing.T) {
	k := newKeyCLI(t)
	root := filepath.Join(t.TempDir(), "root")
	k.keygen(t, root, "pw")
	k.tty = "pw\n"
	if err := k.run("enroll", "--key", root); err != nil {
		t.Fatal(err)
	}
	peer, _ := notes.NewIdentity()
	fp := peer.Fingerprint()
	k.tty = "pw\n"
	if err := k.run("revoke", "--key", root, fp); err != nil {
		t.Fatal(err)
	}
	ts, _, err := notes.ReadTrustState(k.shared())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ts.RootRevoked(fp); !ok {
		t.Fatal("not revoked with the root key")
	}
	if err := k.run(); err != nil || !strings.Contains(k.out.String(), "Revoked with the root key") || !strings.Contains(k.out.String(), fp) {
		t.Fatalf("show: %v\n%s", err, k.out.String())
	}
	if err := k.run("approve", fp); err == nil || !strings.Contains(err.Error(), "revoked with the root key") {
		t.Fatalf("approve of a root-revoked key: %v", err)
	}
	if err := k.run("approve", "--force", fp); err != nil {
		t.Fatal(err)
	}
	ts, _, _ = notes.ReadTrustState(k.shared())
	if st := ts.Of("rp", peer.Public, nil); st != notes.Approved {
		t.Fatalf("approve --force: %q", st)
	}

	// Rotate with the key: new identity, enrolled.
	old, _ := notes.ReadIdentity(notes.IdentityPath(k.data))
	k.tty = "pw\n"
	if err := k.run("rotate", "--key", root); err != nil {
		t.Fatal(err)
	}
	id, _ := notes.ReadIdentity(notes.IdentityPath(k.data))
	e, _ := notes.ReadEnrollment(k.shared())
	if id.Public == old.Public || e.Cert.Key != id.Public || e.Cert.Runner != "r1" {
		t.Fatalf("rotate --key: %+v", e.Cert)
	}
	// Without a key (and no agent): rotated, but the certificate is stale,
	// and it says so.
	if err := k.run("rotate"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(k.out.String(), "enroll") {
		t.Fatalf("rotate without key:\n%s", k.out.String())
	}
	if err := k.run(); err != nil || !strings.Contains(k.out.String(), "certificate is for another key") {
		t.Fatalf("show after rotate: %v\n%s", err, k.out.String())
	}
	// A wrong passphrase leaves the key as it was.
	before, _ := os.ReadFile(notes.IdentityPath(k.data))
	k.tty = "x\ny\nz\n"
	if err := k.run("rotate", "--key", root); err == nil {
		t.Fatal("rotated with a wrong passphrase")
	}
	if after, _ := os.ReadFile(notes.IdentityPath(k.data)); !bytes.Equal(before, after) {
		t.Fatal("the key changed though signing failed")
	}
}

// runner setup --notes-key enrolls the runner it registers.
func TestSetupEnrollsWithNotesKey(t *testing.T) {
	tc := newTestCLI(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"version":"v1.2.3"}`)
	}))
	defer hub.Close()
	root := filepath.Join(t.TempDir(), "root")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	writeKey(t, root, priv, "pw")
	tc.Terminal = func() (io.Reader, io.Writer, func(), error) { return strings.NewReader("pw\n"), io.Discard, nil, nil }
	if code := tc.Main(context.Background(), "setup", []string{"--server", hub.URL, "--code", "ABCD-EFGH", "--notes-key", root, "--service=false"}); code != 0 {
		t.Fatalf("setup exited %d", code)
	}
	data := filepath.Join(tc.Home, ".local", "share", "dash-runner")
	e, err := notes.ReadEnrollment(filepath.Join(data, "shared"))
	id, _ := notes.ReadIdentity(notes.IdentityPath(data))
	if err != nil || e == nil || e.Cert.Runner != "id1" || e.Cert.Key != id.Public {
		t.Fatalf("enrollment %+v %v", e, err)
	}
}
