package tui

import (
	"strings"
	"testing"
)

// Every command Tab completion offers runs something.
func TestCompletedCommandsExist(t *testing.T) {
	for _, c := range commands {
		name, _, _ := strings.Cut(strings.TrimSpace(c), " ")
		if _, ok := exCommands[name]; !ok {
			t.Errorf("completion %q has no command %q", c, name)
		}
	}
}

func TestRunCommand(t *testing.T) {
	tabs := map[string]tab{"board": tabBoard, "b": tabBoard, "chat": tabChat, "c": tabChat, "procs": tabProcs, "ps": tabProcs}
	for line, want := range tabs {
		a := NewApp(nil, "")
		a.tab, a.overlay = -1, ovHelp
		a.runCommand(line)
		if a.tab != want || a.overlay != ovNone {
			t.Errorf(":%s: tab %v, overlay %v; want tab %v and no overlay", line, a.tab, a.overlay, want)
		}
	}

	for _, tc := range []struct {
		line  string
		over  overlay
		flash bool
	}{
		{line: "help", over: ovHelp},
		{line: "h", over: ovHelp},
		{line: "project", over: ovPicker},
		{line: "project nope", flash: true},
		{line: "newproject", flash: true},
		{line: "addclone", flash: true},
		{line: "task", flash: true},
		{line: "bogus", flash: true},
		{line: "w", flash: true}, // :w only means something in the form
	} {
		a := NewApp(nil, "")
		a.runCommand(tc.line)
		if a.overlay != tc.over {
			t.Errorf(":%s: overlay %v, want %v", tc.line, a.overlay, tc.over)
		}
		if a.flashErr != tc.flash {
			t.Errorf(":%s: flash error %v (%q), want %v", tc.line, a.flashErr, a.flash, tc.flash)
		}
	}
}

func TestQuitClosesOverlayFirst(t *testing.T) {
	a := NewApp(nil, "")
	a.overlay = ovHelp
	if cmd := a.runCommand("q"); cmd != nil || a.overlay != ovNone {
		t.Fatalf(":q in an overlay: cmd %v, overlay %v", cmd, a.overlay)
	}
	a.overlay = ovHelp
	if cmd := a.runCommand("qa"); cmd == nil {
		t.Fatal(":qa in an overlay doesn't quit")
	}
	a = NewApp(nil, "")
	if cmd := a.runCommand("quit"); cmd == nil {
		t.Fatal(":quit doesn't quit")
	}
}
