// Command dash-mcp is an MCP server (stdio) for a running Dash server, so a
// regular Claude client (Claude Desktop, Claude Code) can list, create and
// review todos on every computer in the dashboard.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/buildinfo"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/tui"
)

func main() {
	server := flag.String("server", envOr("DASH_SERVER", "http://127.0.0.1:7788"), "Dash server URL (or $DASH_SERVER)")
	envFile := flag.String("env", ".env", "file with DASH_USERNAME / DASH_PASSWORD")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("dash-mcp", buildinfo.Version)
		return
	}

	if err := auth.LoadDotEnv(*envFile); err != nil {
		fail(err)
	}
	user, pass := os.Getenv("DASH_USERNAME"), os.Getenv("DASH_PASSWORD")
	if user == "" || pass == "" {
		fail(errors.New("set DASH_USERNAME and DASH_PASSWORD (environment or -env file)"))
	}
	c, err := tui.NewClient(*server)
	if err != nil {
		fail(err)
	}
	login := func() error { return c.Login(user, pass) }
	if err := login(); err != nil {
		fail(fmt.Errorf("login to %s: %w", *server, err))
	}
	s := &mcptools.Server{
		Name:         "dash",
		Instructions: mcptools.FleetInstructions,
		Tools:        tools(c),
		// Log in again once if the session expired.
		Retry: func(err error) bool { return errors.Is(err, tui.ErrUnauthorized) && login() == nil },
	}
	if err := s.ServeStdio(os.Stdin, os.Stdout); err != nil {
		fail(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "dash-mcp:", err)
	os.Exit(1)
}

// api is the Dash server's REST API for mcptools.Fleet.
type api struct{ c *tui.Client }

func (a api) Call(method, path string, body, out any) error {
	err := a.c.Call(method, path, body, out)
	if errors.Is(err, tui.ErrUnauthorized) {
		return fmt.Errorf("%w (%w)", err, mcptools.ErrUnauthorized)
	}
	return err
}

func tools(c *tui.Client) []mcptools.Tool {
	return append(mcptools.FleetTools(mcptools.Fleet{API: api{c}}), []mcptools.Tool{
		tool("list_permissions", "Tool calls Claude is waiting for permission to run, on every computer.", nil,
			func(a mcptools.Args) (any, error) {
				type row struct {
					Machine string `json:"machine"`
					tui.PermRequest
				}
				out := []row{}
				err := eachMachine(c, func(m tui.Machine) error {
					ps, err := c.Permissions("")
					for _, p := range ps {
						out = append(out, row{m.Name, p})
					}
					return err
				})
				return out, err
			}),
		tool("decide_permission", "Answer a permission request from list_permissions.", []mcptools.Param{
			param("id", "string", "Permission request id.", true),
			param("decision", "string", "allow, always or deny.", true)},
			func(a mcptools.Args) (any, error) {
				id, d := a.Str("id"), a.Str("decision")
				if d != "allow" && d != "always" && d != "deny" {
					return nil, errors.New("decision must be allow, always or deny")
				}
				found := false
				err := eachMachine(c, func(m tui.Machine) error {
					ps, err := c.Permissions("")
					for _, p := range ps {
						if p.ID == id && !found {
							found = true
							return c.Decide(id, d)
						}
					}
					return err
				})
				if err == nil && !found {
					err = fmt.Errorf("no pending permission request %q", id)
				}
				return d, err
			}),
		tool("get_usage", "Claude subscription usage and whether the queue is paused.", nil,
			func(a mcptools.Args) (any, error) { return c.Usage() }),
		tool("set_queue_paused", "Pause or resume starting new tasks.", []mcptools.Param{
			param("paused", "boolean", "true to pause, false to resume.", true)},
			func(a mcptools.Args) (any, error) { return c.SetQueuePaused(a.Flag("paused")) }),
	}...)
}

func tool(name, desc string, params []mcptools.Param, run func(mcptools.Args) (any, error)) mcptools.Tool {
	return mcptools.Tool{Name: name, Desc: desc, Params: params, Run: run}
}

func param(name, typ, desc string, required bool) mcptools.Param {
	return mcptools.Param{Name: name, Type: typ, Desc: desc, Required: required}
}

// eachMachine points the client at every reachable computer in turn.
func eachMachine(c *tui.Client, fn func(tui.Machine) error) error {
	ms, err := c.Machines()
	if err != nil {
		return err
	}
	defer c.SetPrefix("")
	for _, m := range ms {
		if !m.OK {
			continue
		}
		c.SetPrefix(m.Prefix)
		if err := fn(m); err != nil {
			return err
		}
	}
	return nil
}
