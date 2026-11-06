package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/klahr/dash/internal/model"
)

func typeKeys(a *App, keys ...tea.KeyPressMsg) {
	for _, k := range keys {
		a.Update(k)
	}
}

func text(s string) []tea.KeyPressMsg {
	var ks []tea.KeyPressMsg
	for _, r := range s {
		ks = append(ks, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return ks
}

func TestCommandCompletion(t *testing.T) {
	c, _ := NewClient("http://127.0.0.1:1")
	a := NewApp(c, "")
	a.w, a.h = 120, 30
	a.projects = []model.Project{{ID: "1", Name: "webshop"}, {ID: "2", Name: "website"}}

	typeKeys(a, text(":proj")...)
	if a.mode != modeCommand {
		t.Fatal("want command mode")
	}
	if wm := ansi.Strip(a.wildmenu(120)); !strings.Contains(wm, "project webshop") || !strings.Contains(wm, "project website") {
		t.Fatalf("wildmenu should list the projects, got %q", wm)
	}
	typeKeys(a, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := a.cmdline.Value(); got != "project " {
		t.Fatalf("Tab should complete the command, got %q", got)
	}
	typeKeys(a, text("webs")...)
	if screen := ansi.Strip(a.View().Content); !strings.Contains(screen, "project webshop") || !strings.Contains(screen, ":project webs") {
		t.Fatalf("the wildmenu and command line should be on screen:\n%s", screen)
	}
	typeKeys(a, tea.KeyPressMsg{Code: tea.KeyDown}, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := a.cmdline.Value(); got != "project website" {
		t.Fatalf("↓ then Tab should pick the second match, got %q", got)
	}
}
