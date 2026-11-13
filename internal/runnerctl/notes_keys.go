package runnerctl

// The root key of shared notes (internal/notes/enroll.go): making one
// (keygen), and reading one to sign with (enroll, rotate, revoke --key).
// The private key is read here, while signing, and nowhere else: it is
// never kept by the runner, sent to the hub, or opened by the installer
// scripts.

import (
	"bufio"
	"bytes"
	//lint:ignore SA1019 only to name the key type in an error
	"crypto/dsa"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"
)

// DefaultNotesKey is where keygen writes the root key by default, under
// home: ~/.ssh/dash_notes (%USERPROFILE%\.ssh\dash_notes on Windows).
func DefaultNotesKey(home string) string { return filepath.Join(home, ".ssh", "dash_notes") }

// --- asking for a passphrase ---

// openTerminal opens the controlling terminal: /dev/tty, or the console
// on Windows. Never stdin: `curl … | sh` runs setup with the script on it.
func openTerminal() (io.Reader, io.Writer, func(), error) {
	if runtime.GOOS == "windows" {
		in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
		if err != nil {
			return nil, nil, nil, err
		}
		out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
		if err != nil {
			in.Close()
			return nil, nil, nil, err
		}
		return in, out, func() { in.Close(); out.Close() }, nil
	}
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	return f, f, func() { f.Close() }, nil
}

// prompter asks for passphrases on the terminal, without echo. A reader
// that isn't a terminal (tests) gives one line per question.
type prompter struct {
	in    io.Reader
	out   io.Writer
	br    *bufio.Reader
	close func()
}

func (c *CLI) prompter() (*prompter, error) {
	open := c.Terminal
	if open == nil {
		open = openTerminal
	}
	in, out, closeFn, err := open()
	if err != nil {
		return nil, fmt.Errorf("no terminal to ask for the passphrase on (%v): run this in a terminal, or add the key to ssh-agent", err)
	}
	if closeFn == nil {
		closeFn = func() {}
	}
	return &prompter{in: in, out: out, br: bufio.NewReader(in), close: closeFn}, nil
}

func (p *prompter) ask(prompt string) ([]byte, error) {
	fmt.Fprint(p.out, prompt)
	if f, ok := p.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(p.out)
		return b, err
	}
	line, err := p.br.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return nil, fmt.Errorf("reading the passphrase: %w", err)
	}
	return []byte(strings.TrimRight(line, "\r\n")), nil
}

// --- reading the root key ---

// keyTypeError is the error for a key that isn't ed25519.
func keyTypeError(path, kind string) error {
	return fmt.Errorf("%s is an %s key; the shared notes root key must be an ed25519 SSH key (make one with: dash runner notes keygen, or ssh-keygen -t ed25519 -f ~/.ssh/dash_notes)", path, kind)
}

func sshTypeName(t string) string {
	switch {
	case t == ssh.KeyAlgoRSA:
		return "RSA"
	case strings.HasPrefix(t, "ecdsa-"):
		return "ECDSA"
	//lint:ignore SA1019 only to name the key type in an error
	case t == ssh.KeyAlgoDSA:
		return "DSA"
	case strings.HasPrefix(t, "sk-"):
		return "security key (" + t + ")"
	}
	return t
}

// openSSHPublic is the public key an OpenSSH private key file carries
// unencrypted, even when the private key is (nil if b isn't one).
func openSSHPublic(b []byte) ssh.PublicKey {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "OPENSSH PRIVATE KEY" {
		return nil
	}
	rest, ok := bytes.CutPrefix(block.Bytes, []byte("openssh-key-v1\x00"))
	if !ok {
		return nil
	}
	str := func() []byte {
		if len(rest) < 4 {
			return nil
		}
		n := binary.BigEndian.Uint32(rest)
		if uint64(len(rest)-4) < uint64(n) {
			rest = nil
			return nil
		}
		s := rest[4 : 4+n]
		rest = rest[4+n:]
		return s
	}
	str() // cipher
	str() // kdf
	str() // kdf options
	if len(rest) < 4 || binary.BigEndian.Uint32(rest) != 1 {
		return nil
	}
	rest = rest[4:]
	pub, err := ssh.ParsePublicKey(str())
	if err != nil {
		return nil
	}
	return pub
}

// agentSigner is ssh-agent's signer for pub, if the agent holds it.
func (c *CLI) agentSigner(pub ssh.PublicKey) ssh.Signer {
	if pub == nil {
		return nil
	}
	dial := c.Agent
	if dial == nil {
		dial = func() (agent.Agent, error) {
			sock := os.Getenv("SSH_AUTH_SOCK")
			if sock == "" {
				return nil, errors.New("no SSH_AUTH_SOCK")
			}
			conn, err := net.Dial("unix", sock)
			if err != nil {
				return nil, err
			}
			return agent.NewClient(conn), nil
		}
	}
	ag, err := dial()
	if err != nil || ag == nil {
		return nil
	}
	signers, err := ag.Signers()
	if err != nil {
		return nil
	}
	for _, s := range signers {
		if bytes.Equal(s.PublicKey().Marshal(), pub.Marshal()) {
			return s
		}
	}
	return nil
}

// rootSigner reads the root key at path to sign with: from ssh-agent if
// it holds the key, else from the file, asking for its passphrase on the
// terminal if it has one. Only ed25519 keys are taken.
func (c *CLI) rootSigner(path string) (ssh.Signer, error) {
	path, err := c.absPath(path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the root key: %w", err)
	}
	if bytes.Contains(b, []byte("-----BEGIN PGP")) {
		return nil, keyTypeError(path, "OpenPGP")
	}
	// Its public half: the file itself (a .pub given, for a key in
	// ssh-agent), the OpenSSH file's header, or the .pub next to it.
	pub, _, _, _, perr := ssh.ParseAuthorizedKey(b)
	isPublic := perr == nil
	if !isPublic {
		pub = openSSHPublic(b)
	}
	if pub == nil {
		if pb, err := os.ReadFile(path + ".pub"); err == nil {
			pub, _, _, _, _ = ssh.ParseAuthorizedKey(pb)
		}
	}
	if pub != nil && pub.Type() != ssh.KeyAlgoED25519 {
		return nil, keyTypeError(path, sshTypeName(pub.Type()))
	}
	if s := c.agentSigner(pub); s != nil {
		fmt.Fprintln(c.Out, "Signing with the key in ssh-agent.")
		return s, nil
	}
	if isPublic {
		return nil, fmt.Errorf("%s is a public key, and ssh-agent doesn't hold its private half: give the private key's path (or ssh-add it)", path)
	}
	raw, err := ssh.ParseRawPrivateKey(b)
	if _, ok := errors.AsType[*ssh.PassphraseMissingError](err); ok {
		p, perr := c.prompter()
		if perr != nil {
			return nil, perr
		}
		defer p.close()
		for try := 0; ; try++ {
			pass, perr := p.ask(fmt.Sprintf("Passphrase for %s: ", path))
			if perr != nil {
				return nil, perr
			}
			raw, err = ssh.ParseRawPrivateKeyWithPassphrase(b, pass)
			if !errors.Is(err, x509.IncorrectPasswordError) {
				break
			}
			if try == 2 {
				return nil, fmt.Errorf("wrong passphrase for %s", path)
			}
			fmt.Fprintln(p.out, "Wrong passphrase, try again.")
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%s isn't an OpenSSH private key (%v); the root key must be an ed25519 SSH key, e.g. from: dash runner notes keygen", path, err)
	}
	switch k := raw.(type) {
	case ed25519.PrivateKey:
		return ssh.NewSignerFromKey(k)
	case *ed25519.PrivateKey:
		return ssh.NewSignerFromKey(*k)
	case *rsa.PrivateKey:
		return nil, keyTypeError(path, "RSA")
	case *ecdsa.PrivateKey:
		return nil, keyTypeError(path, "ECDSA")
	case *dsa.PrivateKey:
		return nil, keyTypeError(path, "DSA")
	}
	return nil, keyTypeError(path, fmt.Sprintf("unsupported (%T)", raw))
}

// --- keygen ---

// notesKeygen makes a root key: an ed25519 SSH key in OpenSSH's format,
// passphrase-protected unless noPass, at path and path.pub. It never
// replaces a file.
func (c *CLI) notesKeygen(path string, noPass bool) error {
	path, err := c.absPath(path)
	if err != nil {
		return err
	}
	pubPath := path + ".pub"
	for _, p := range []string{path, pubPath} {
		if _, err := os.Lstat(p); err == nil {
			return fmt.Errorf("%s exists already; not replacing it (a new root key orphans every runner enrolled with the old one). Pick another --out, or delete it yourself first", p)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	var pass []byte
	if !noPass {
		p, err := c.prompter()
		if err != nil {
			return err
		}
		defer p.close()
		if pass, err = p.ask("Passphrase for the new key: "); err != nil {
			return err
		}
		if len(pass) == 0 {
			return errors.New("the root key needs a passphrase (or pass --no-passphrase to store it unencrypted)")
		}
		again, err := p.ask("Same passphrase again: ")
		if err != nil {
			return err
		}
		if !bytes.Equal(pass, again) {
			return errors.New("the passphrases don't match; nothing written")
		}
	}
	pubKey, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	comment := "dash-notes@" + c.Hostname
	var block *pem.Block
	if noPass {
		block, err = ssh.MarshalPrivateKey(priv, comment)
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, comment, pass)
	}
	if err != nil {
		return err
	}
	sshPub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return err
	}
	pubLine := append(bytes.TrimSpace(ssh.MarshalAuthorizedKey(sshPub)), []byte(" "+comment+"\n")...)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := writeNewFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return err
	}
	if err := writeNewFile(pubPath, pubLine, 0o644); err != nil {
		os.Remove(path) // ours, just written
		return err
	}
	w := c.Out
	fmt.Fprintln(w, "Made a root key for shared notes:")
	fmt.Fprintf(w, "  private key  %s", path)
	if noPass {
		fmt.Fprintln(w, " (no passphrase)")
	} else {
		fmt.Fprintln(w, " (passphrase protected)")
	}
	fmt.Fprintf(w, "  public key   %s\n", pubPath)
	fmt.Fprintf(w, "  fingerprint  %s\n", ssh.FingerprintSHA256(sshPub))
	fmt.Fprintln(w, "Back it up: without it you can't enroll more runners under this root, nor revoke one.")
	fmt.Fprintln(w, "Next, enroll each runner with it, on that runner:")
	fmt.Fprintf(w, "  dash runner notes enroll --key %s\n", path)
	fmt.Fprintln(w, "or when installing a runner, add --notes-key <path> to install.sh (-NotesKey <path> to install.ps1).")
	return nil
}

// writeNewFile writes b to path, all at once, and never over an existing
// file: it writes a temporary file next to it and links it into place.
func writeNewFile(path string, b []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".dash-keygen-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	exists := func() error {
		return fmt.Errorf("%s exists already; not replacing it", path)
	}
	if err := os.Link(tmp, path); err == nil {
		return nil
	} else if errors.Is(err, fs.ErrExist) {
		return exists()
	}
	// No hard links here: rename, after checking again.
	if _, err := os.Lstat(path); err == nil {
		return exists()
	}
	return os.Rename(tmp, path)
}
