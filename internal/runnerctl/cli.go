package runnerctl

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh/agent"

	"github.com/klahr/dash/internal/notes"
	"github.com/klahr/dash/internal/tunnel"
)

// Commands are the `dash runner <command>`s handled here; plain `dash
// runner` (and `dash runner --server … --code …`) runs the runner itself.
var Commands = []string{"setup", "install", "uninstall", "status", "update", "notes"}

// CLI runs `dash runner setup|install|uninstall|status|notes`. Zero fields
// default to the real environment; tests set them.
type CLI struct {
	Version    string
	Home       string
	Hostname   string
	Out, Err   io.Writer
	Preflight  Preflight
	Register   func(ctx context.Context, server, code, name, prev string) (tunnel.Credentials, error)
	Executable func() (string, error)
	// Service adjusts the service a command manages (tests: a fake Run).
	Service func(*Service)
	// Updater adjusts the update `dash runner update` makes (tests: a fake
	// release server).
	Updater func(*Updater)
	// HubVersion asks the hub at server which version it runs (nil: GET
	// <server>/runner/version).
	HubVersion func(ctx context.Context, server string) (string, error)
	// Terminal opens the terminal passphrases are asked on (nil: the
	// controlling terminal, never stdin).
	Terminal func() (in io.Reader, out io.Writer, close func(), err error)
	// Agent connects to ssh-agent (nil: SSH_AUTH_SOCK's).
	Agent func() (agent.Agent, error)
}

func (c *CLI) defaults() {
	if c.Home == "" {
		c.Home, _ = os.UserHomeDir()
	}
	if c.Hostname == "" {
		h, _ := os.Hostname()
		c.Hostname = strings.ToLower(strings.Split(h, ".")[0])
	}
	if c.Out == nil {
		c.Out = os.Stdout
	}
	if c.Err == nil {
		c.Err = os.Stderr
	}
	if c.Register == nil {
		c.Register = tunnel.Register
	}
	if c.Executable == nil {
		c.Executable = func() (string, error) {
			p, err := os.Executable()
			if err != nil {
				return "", err
			}
			return filepath.EvalSymlinks(p)
		}
	}
}

func (c *CLI) defaultData() string { return RunnerData(c.Home) }

// parse parses a command's flags and readies its data directory: the
// registration moves there from a hub's directory (MigrateRunnerData).
func (c *CLI) parse(fs *flag.FlagSet, args []string, data *string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	return MigrateRunnerData(c.Home, *data)
}

// Main runs command with its args and returns the exit status.
func (c *CLI) Main(ctx context.Context, command string, args []string) int {
	c.defaults()
	var err error
	switch command {
	case "setup":
		err = c.setup(ctx, args)
	case "install":
		err = c.install(args)
	case "uninstall":
		err = c.uninstall(args)
	case "status":
		err = c.status(ctx, args)
	case "update":
		err = c.update(ctx, args)
	case "notes":
		err = c.notes(args)
	default:
		err = fmt.Errorf("unknown command %q (want one of %s)", command, strings.Join(Commands, ", "))
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(c.Err, "dash runner "+command+":", err)
		return 1
	}
	return 0
}

func (c *CLI) flags(command, usage string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("dash runner "+command, flag.ContinueOnError)
	fs.SetOutput(c.Err)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: dash runner %s [flags]\n%s\n", command, usage)
		fs.PrintDefaults()
	}
	data := fs.String("data", c.defaultData(), "Dash's data directory (where runner.json is kept)")
	return fs, data
}

func (c *CLI) credsPath(data string) string { return filepath.Join(data, "runner.json") }

// setup is what the install script runs: check the computer, register with
// the hub (or keep the registration), and install the service.
func (c *CLI) setup(ctx context.Context, args []string) error {
	fs, data := c.flags("setup", "Checks this computer, registers it with the hub (or keeps its registration when\nno --code is given), enrolls it under a shared notes root key with --notes-key,\nand installs the background service.")
	server := fs.String("server", "", "the hub's URL, e.g. https://dash.example.com")
	code := fs.String("code", "", "registration code from the hub's Runners page; omit to keep the registration")
	name := fs.String("name", "", "this computer's name in the dashboard (default: its host name)")
	projects := fs.String("projects", "", "where the runner's projects live (default: as before, else ~/dash-projects)")
	service := fs.Bool("service", true, "install and start the background service")
	skip := fs.Bool("skip-checks", false, "register even if the checks fail")
	claudeBin := fs.String("claude", "claude", "Claude Code binary the runner uses")
	notesKey := fs.String("notes-key", "", "enroll this runner for shared notes with this root key (an ed25519 SSH private key; see dash runner notes enroll)")
	if err := c.parse(fs, args, data); err != nil {
		return err
	}
	if err := ClaimData(*data, ModeRunner); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected %q", fs.Arg(0))
	}
	path := c.credsPath(*data)
	creds, err := tunnel.LoadCredentials(path)
	registered := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if *server == "" {
		if !registered {
			return errors.New("--server is needed, the hub's URL")
		}
		*server = creds.Server
	}
	u, err := tunnel.CheckServer(*server)
	if err != nil {
		return err
	}
	if *code == "" {
		switch {
		case !registered:
			return fmt.Errorf("this computer isn't registered yet: get a code from the Runners page of %s and run this again with --code", u)
		case creds.Server != u.String():
			return fmt.Errorf("this computer is registered with %s, not %s: get a code from %s's Runners page and run this again with --code", creds.Server, u, u)
		case *name != "" && *name != creds.Name:
			return fmt.Errorf("this computer is registered as %s; to rename it, revoke it on the hub and register again with --code and --name", creds.Name)
		}
	}

	fmt.Fprintln(c.Out, "Checking this computer:")
	checks := c.Preflight.Run(ctx, u.String(), *claudeBin)
	fmt.Fprint(c.Out, FormatChecks(checks))
	if Failed(checks) {
		if !*skip {
			return errors.New("fix the above and run this again (or pass --skip-checks)")
		}
		fmt.Fprintln(c.Out, "Going ahead anyway (--skip-checks).")
	}

	if *code != "" {
		n := *name
		if n == "" {
			n = c.Hostname
		}
		keep := creds.Projects
		var prev string // registered with this hub before: replace that runner
		if registered && creds.Server == u.String() {
			prev = creds.Token
		}
		if creds, err = c.Register(ctx, u.String(), *code, n, prev); err != nil {
			return err
		}
		creds.Projects = keep
		fmt.Fprintf(c.Out, "Registered with %s as %s.\n", creds.Server, creds.Name)
	} else {
		fmt.Fprintf(c.Out, "Already registered with %s as %s; keeping that.\n", creds.Server, creds.Name)
	}
	if *projects != "" {
		if creds.Projects, err = c.absPath(*projects); err != nil {
			return err
		}
	}
	if err := tunnel.SaveCredentials(path, creds); err != nil {
		return err
	}
	if creds.Projects != "" {
		fmt.Fprintln(c.Out, "Projects directory:", creds.Projects)
	}
	if *notesKey != "" {
		// Before the service starts, so it starts enrolled (a running one
		// would pick it up too).
		if err := c.notesEnroll(*data, filepath.Join(*data, "shared"), *notesKey, false); err != nil {
			return fmt.Errorf("%w\nThe runner is registered; fix that and run this again, or enroll it later with: dash runner notes enroll --key %s", err, *notesKey)
		}
	}

	svc, err := c.newService(*data)
	if err != nil {
		return err
	}
	if !*service {
		if st := svc.State(); st != "not installed" && !strings.HasPrefix(st, "unsupported") {
			fmt.Fprintln(c.Out, "note: the background service is installed too ("+st+"); don't run two runners. Remove it with: dash runner uninstall")
		}
		return nil
	}
	return c.installService(svc)
}

func (c *CLI) absPath(p string) (string, error) {
	if p == "~" {
		return c.Home, nil
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		p = filepath.Join(c.Home, rest)
	} else if rest, ok := strings.CutPrefix(p, "~"+string(filepath.Separator)); ok {
		p = filepath.Join(c.Home, rest) // ~\x on Windows
	}
	return filepath.Abs(p)
}

// newService describes the service that runs this binary as a runner with
// data as its data directory.
func (c *CLI) newService(data string) (*Service, error) {
	bin, err := c.Executable()
	if err != nil {
		return nil, fmt.Errorf("find the dash binary: %w", err)
	}
	args := []string{"runner"}
	if filepath.Clean(data) != newRunnerData(c.Home) {
		abs, err := filepath.Abs(data)
		if err != nil {
			return nil, err
		}
		args = append(args, "-data", abs)
	}
	// Services start with a bare PATH: give the runner the one it was set
	// up with, plus where dash, git and claude are.
	var extra []string
	extra = append(extra, filepath.Dir(bin))
	for _, b := range []string{"git", "claude"} {
		if p, err := exec.LookPath(b); err == nil {
			extra = append(extra, filepath.Dir(p))
		}
	}
	svc := &Service{Bin: bin, Args: args, Home: c.Home, Path: ServicePath(os.Getenv("PATH"), extra...), Data: data}
	if c.Service != nil {
		c.Service(svc)
	}
	return svc, nil
}

// ServicePath is path with dirs added at the end when missing.
func ServicePath(path string, dirs ...string) string {
	parts := slices.DeleteFunc(strings.Split(path, string(os.PathListSeparator)), func(s string) bool { return s == "" })
	for _, d := range dirs {
		if d != "" && !slices.Contains(parts, d) {
			parts = append(parts, d)
		}
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

func (c *CLI) installService(svc *Service) error {
	res, err := svc.Install()
	if err != nil {
		return err
	}
	verb := "Installed"
	if !res.Changed {
		verb = "Kept"
	}
	fmt.Fprintf(c.Out, "%s the background service (%s) and (re)started it.\n", verb, res.File)
	if res.Hint != "" {
		fmt.Fprintln(c.Out, res.Hint)
	}
	fmt.Fprintln(c.Out, "See how it's doing with: dash runner status")
	return nil
}

func (c *CLI) install(args []string) error {
	fs, data := c.flags("install", "Installs and starts the background service that runs dash runner\n(a systemd user unit on Linux, a LaunchAgent on macOS, a scheduled task on Windows).")
	if err := c.parse(fs, args, data); err != nil {
		return err
	}
	if _, err := tunnel.LoadCredentials(c.credsPath(*data)); err != nil {
		return errors.New("not registered yet: run dash runner setup --server <hub URL> --code <code> first")
	}
	svc, err := c.newService(*data)
	if err != nil {
		return err
	}
	return c.installService(svc)
}

func (c *CLI) uninstall(args []string) error {
	fs, data := c.flags("uninstall", "Stops and removes the background service. The registration is kept.")
	if err := c.parse(fs, args, data); err != nil {
		return err
	}
	svc, err := c.newService(*data)
	if err != nil {
		return err
	}
	removed, err := svc.Uninstall()
	if err != nil {
		return err
	}
	if !removed {
		fmt.Fprintln(c.Out, "No background service was installed.")
		return nil
	}
	fmt.Fprintln(c.Out, "Removed the background service. The registration is kept: run dash runner to start it by hand.")
	return nil
}

func (c *CLI) status(ctx context.Context, args []string) error {
	fs, data := c.flags("status", "Shows this runner's registration, connection and service.")
	if err := c.parse(fs, args, data); err != nil {
		return err
	}
	w := c.Out
	fmt.Fprintf(w, "Dash:         %s\n", c.Version)
	creds, err := tunnel.LoadCredentials(c.credsPath(*data))
	if err != nil {
		fmt.Fprintln(w, "Registration: not registered (get a code from the hub's Runners page)")
	} else {
		fmt.Fprintf(w, "Registration: %s (id %s)\n", creds.Name, creds.ID)
		fmt.Fprintf(w, "Hub:          %s\n", creds.Server)
		if creds.Projects != "" {
			fmt.Fprintf(w, "Projects:     %s\n", creds.Projects)
		}
	}
	if id, err := notes.ReadIdentity(notes.IdentityPath(*data)); err == nil {
		fmt.Fprintf(w, "Notes key:    %s (dash runner notes)\n", id.Fingerprint())
	}
	st, running := ReadState(StatePath(*data))
	switch {
	case !running:
		fmt.Fprintln(w, "Connection:   not running")
	case st.Connected:
		fmt.Fprintf(w, "Connection:   connected for %s (pid %d, dash %s)\n", since(st.Since), st.PID, st.Version)
	default:
		fmt.Fprintf(w, "Connection:   disconnected for %s, retrying (pid %d, dash %s)\n", since(st.Since), st.PID, st.Version)
	}
	if running && st.Version != "" && st.Version != c.Version {
		fmt.Fprintf(w, "              the running runner is %s; restart it to run %s\n", st.Version, c.Version)
	}
	svc, err := c.newService(*data)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Service:      %s\n", svc.State())
	return nil
}

// update replaces this dash binary with the hub's release (or --version)
// and restarts the background service on it.
func (c *CLI) update(ctx context.Context, args []string) error {
	fs, data := c.flags("update", "Downloads the hub's Dash release (or --version), checks it against the release's\nchecksums, keeps this binary as dash.prev and puts the new one in its place.\nThen it restarts the background service on it.")
	version := fs.String("version", "", "the release to install (default: the hub's version)")
	restart := fs.Bool("restart", true, "restart the background service, if installed, on the new binary")
	if err := c.parse(fs, args, data); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected %q", fs.Arg(0))
	}
	if *version == "" {
		creds, err := tunnel.LoadCredentials(c.credsPath(*data))
		if err != nil {
			return errors.New("not registered with a hub: pass --version")
		}
		get := c.HubVersion
		if get == nil {
			get = HubVersion
		}
		v, err := get(ctx, creds.Server)
		if err != nil {
			return fmt.Errorf("ask %s for its version: %w", creds.Server, err)
		}
		if *version = ReleaseVersion(v); *version == "" {
			return fmt.Errorf("the hub runs %s, a build from source with no release to install: pass --version", v)
		}
	}
	if SameVersion(*version, c.Version) {
		fmt.Fprintf(c.Out, "Dash %s is installed already.\n", c.Version)
		return nil
	}
	bin, err := c.Executable()
	if err != nil {
		return fmt.Errorf("find the dash binary: %w", err)
	}
	u := Updater{Version: *version, Bin: bin, Data: *data, Current: c.Version}
	if c.Updater != nil {
		c.Updater(&u)
	}
	fmt.Fprintf(c.Out, "Downloading Dash %s...\n", *version)
	if err := u.Run(ctx); err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "Installed Dash %s in %s (the previous one is %s.prev).\n", *version, bin, bin)
	if !*restart {
		return nil
	}
	svc, err := c.newService(*data)
	if err != nil {
		return err
	}
	if st := svc.State(); st == "not installed" || strings.HasPrefix(st, "unsupported") {
		fmt.Fprintln(c.Out, "Restart the runner to run it (no background service is installed).")
		return nil
	}
	return c.installService(svc)
}

// HubVersion asks the hub at server which Dash version it runs.
func HubVersion(ctx context.Context, server string) (string, error) {
	u, err := tunnel.CheckServer(server)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String()+"/runner/version", nil)
	if err != nil {
		return "", err
	}
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct{ Version string }
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&out) != nil || out.Version == "" {
		return "", fmt.Errorf("unexpected answer: %s", res.Status)
	}
	return out.Version, nil
}

func since(t time.Time) string {
	if t.IsZero() {
		return "a while"
	}
	return time.Since(t).Round(time.Second).String()
}
