// Command dash-tui is a terminal client for a running Dash server.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/tui"
)

func main() {
	server := flag.String("server", envOr("DASH_SERVER", "http://127.0.0.1:7777"), "Dash server URL (or $DASH_SERVER)")
	envFile := flag.String("env", ".env", "file with DASH_USERNAME / DASH_PASSWORD")
	project := flag.String("project", "", "project name or id to open")
	flag.Parse()

	if err := auth.LoadDotEnv(*envFile); err != nil {
		fail(err)
	}
	user, pass := os.Getenv("DASH_USERNAME"), os.Getenv("DASH_PASSWORD")
	if user == "" || pass == "" {
		var err error
		if user, pass, err = promptCredentials(); err != nil {
			fail(err)
		}
	}

	c, err := tui.NewClient(*server)
	if err != nil {
		fail(err)
	}
	if err := c.Login(user, pass); err != nil {
		fail(fmt.Errorf("login to %s: %w", *server, err))
	}

	app := tui.NewApp(c, *project)
	p := tea.NewProgram(app)
	app.SetProgram(p)
	if _, err := p.Run(); err != nil {
		fail(err)
	}
}

func promptCredentials() (string, string, error) {
	fmt.Fprint(os.Stderr, "Dash username: ")
	user, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", "", err
	}
	fmt.Fprint(os.Stderr, "Password: ")
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return strings.TrimSpace(user), string(pw), err
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "dash-tui:", err)
	os.Exit(1)
}
