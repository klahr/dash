package runnerctl

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/notes"
	"github.com/klahr/dash/internal/tunnel"
)

// notes runs `dash runner notes [approve|revoke <fingerprint>]`: shows this
// runner's shared notes key fingerprint and the runners it trades project
// keys with, or approves or revokes one. This is where the user decides who
// gets a project's key, by comparing fingerprints between runners; the hub
// only shows them and could lie.
func (c *CLI) notes(args []string) error {
	// The command and fingerprint come first or after the flags.
	var sub, fp string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			fp, args = args[0], args[1:]
		}
	}
	fs, data := c.flags("notes", `Shows this runner's shared notes key fingerprint, the runners approved here, and those waiting.

  dash runner notes                          what this runner trusts, and who waits
  dash runner notes approve <fingerprint>    share project keys with that runner
  dash runner notes revoke <fingerprint>     stop, and rotate the keys it held

A runner gets a project's notes key only from runners where you approved
its fingerprint, and takes keys only from runners approved on it: approve
each pair of runners on both. Compare the fingerprint with the one
`+"`dash runner notes`"+` shows on that runner itself, not only with the hub's page.`)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: dash runner notes [approve|revoke <fingerprint>] [flags]")
		fs.PrintDefaults()
	}
	if err := c.parse(fs, args, data); err != nil {
		return err
	}
	shared := filepath.Join(*data, "shared")
	now := time.Now()
	switch sub {
	case "":
		return c.notesShow(*data, shared)
	case "approve", "revoke":
		if fp == "" && fs.NArg() == 1 {
			fp = fs.Arg(0)
		} else if fs.NArg() > 0 {
			fp = "" // more than one fingerprint
		}
		if fp == "" {
			return fmt.Errorf("usage: dash runner notes %s <fingerprint>", sub)
		}
		return c.notesSet(*data, shared, sub, fp, now)
	}
	return fmt.Errorf("unknown notes command %q (want approve or revoke)", sub)
}

func (c *CLI) notesSet(data, shared, sub, fp string, now time.Time) error {
	id, err := notes.LoadIdentity(notes.IdentityPath(data))
	if err != nil {
		return err
	}
	if n, err := notes.NormalizeFingerprint(fp); err == nil && n == id.Fingerprint() {
		return errors.New("that is this runner's own fingerprint: approve it on the other runners")
	}
	peers, _ := notes.ReadPeers(shared)
	w := c.Out
	if sub == "revoke" {
		fp, err := notes.Revoke(shared, fp, now)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "Revoked %s%s.\n", fp, namesOf(peers, fp))
		fmt.Fprintln(w, "The running runner stops trading keys with it, and rotates the key of each project it held:")
		fmt.Fprintln(w, "new notes are sealed with a key it doesn't have. Revoke it on the project's other runners too.")
		return nil
	}
	fp, err = notes.Approve(shared, fp, now)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Approved %s%s.\n", fp, namesOf(peers, fp))
	fmt.Fprintln(w, "The running runner trades project keys with it within seconds, once it is approved there too.")
	fmt.Fprintf(w, "If you haven't, approve this runner on it: dash runner notes approve %s\n", id.Fingerprint())
	return nil
}

// namesOf is " (name, id …)" for the runners pinned with fingerprint fp.
func namesOf(p notes.Peers, fp string) string {
	var out []string
	for _, id := range slices.Sorted(maps.Keys(p.Pins)) {
		if pin := p.Pins[id]; pin.Fingerprint == fp {
			out = append(out, runnerLabel(pin.Name, id))
		}
	}
	if len(out) == 0 {
		return ""
	}
	return " (" + strings.Join(out, ", ") + ")"
}

func runnerLabel(name, id string) string {
	if name == "" {
		return "runner " + id
	}
	return name + ", id " + id
}

func (c *CLI) notesShow(data, shared string) error {
	w := c.Out
	id, err := notes.LoadIdentity(notes.IdentityPath(data))
	if err != nil {
		return err
	}
	name := "this runner"
	if creds, err := tunnel.LoadCredentials(c.credsPath(data)); err == nil {
		name = creds.Name
	}
	appr, err := notes.ReadApprovals(shared)
	if err != nil {
		return err
	}
	peers, err := notes.ReadPeers(shared)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Shared notes key of %s: %s\n", name, id.Fingerprint())
	fmt.Fprintln(w, "  Other runners ask you to approve this fingerprint: check theirs shows exactly this.")

	fmt.Fprintln(w, "\nApproved here (they get this runner's project keys, and it takes theirs):")
	if len(appr.Approved) == 0 {
		fmt.Fprintln(w, "  none")
	}
	for _, fp := range slices.Sorted(maps.Keys(appr.Approved)) {
		fmt.Fprintf(w, "  %s%s, approved %s\n", fp, namesOf(peers, fp), appr.Approved[fp].Local().Format("2006-01-02 15:04"))
	}
	if len(peers.Pending) > 0 {
		fmt.Fprintln(w, "\nWaiting for your approval:")
		for _, p := range peers.Pending {
			if _, ok := appr.Approved[p.Fingerprint]; ok {
				continue
			}
			if _, ok := appr.Revoked[p.Fingerprint]; ok {
				continue
			}
			fmt.Fprintf(w, "  %s  %s, project %s, %s ago\n", p.Fingerprint, runnerLabel(p.Name, p.Runner), p.Project, since(p.At))
			fmt.Fprintf(w, "    On %s, run dash runner notes. If it shows %s, approve it here:\n", cmp.Or(p.Name, "it"), p.Fingerprint)
			fmt.Fprintf(w, "      dash runner notes approve %s\n", p.Fingerprint)
		}
	}
	if len(peers.Warnings) > 0 {
		fmt.Fprintln(w, "\n!! Runners seen with a key other than the one pinned for them (refused unless approved):")
		for _, k := range peers.Warnings {
			fmt.Fprintf(w, "  %s: pinned %s, offered %s, %s\n", runnerLabel(k.Name, k.Runner), k.Pinned, k.Offered, k.At.Local().Format("2006-01-02 15:04"))
		}
		fmt.Fprintln(w, "  Someone (the hub?) may be posing as it. If it really has a new key, approve the new fingerprint after checking it there.")
	}
	if len(appr.Revoked) > 0 {
		fmt.Fprintln(w, "\nRevoked here:")
		for _, fp := range slices.Sorted(maps.Keys(appr.Revoked)) {
			fmt.Fprintf(w, "  %s%s, revoked %s\n", fp, namesOf(peers, fp), appr.Revoked[fp].Local().Format("2006-01-02 15:04"))
		}
	}
	return nil
}
