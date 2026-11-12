package localrun

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCommandRefusesOnHub(t *testing.T) {
	t.Cleanup(func() { Set(nil) })
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// A binary that would leave a mark if it ran.
	dir := t.TempDir()
	mark := filepath.Join(dir, "ran")
	bin := filepath.Join(dir, "tool")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntouch "+mark+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	Set(func() bool { return false })
	for _, tool := range []string{Git, Gh} {
		cmd, err := Command(context.Background(), tool, bin, "status")
		if cmd != nil || !errors.Is(err, ErrHub) {
			t.Fatalf("%s on the hub: cmd=%v err=%v", tool, cmd, err)
		}
		if want := "this hub doesn't run " + tool + " "; !strings.HasPrefix(err.Error(), want) {
			t.Fatalf("%s: error %q, want it to start with %q", tool, err, want)
		}
	}
	if !strings.Contains(logs.String(), Refused) {
		t.Fatalf("refusal not logged: %q", logs.String())
	}
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("the hub started the process")
	}

	// Off the hub (a runner), the same call runs it.
	Set(nil)
	cmd, err := Command(context.Background(), Git, bin, "status")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Fatal("runner didn't run the process:", err)
	}
}

// execAllowed lists the files (outside this package) that may call
// exec.Command/CommandContext, and what they run. None of them runs git or
// gh: those go through Command, so the hub's policy covers them. Each of
// these checks localrun (or agent's guard, for Claude) itself, or only runs
// in `dash runner` / the TUI client, never on the hub.
var execAllowed = map[string]string{
	"internal/runner/runner.go":       "bash for project commands and terminals (localrun.Check)",
	"internal/queue/shell.go":         "bash for project commands (localrun.Check)",
	"internal/api/handlers.go":        "the -editor command (localrun.Check)",
	"internal/agent/executor.go":      "claude (agent's guard)",
	"internal/agent/title.go":         "claude (agent's guard)",
	"internal/agent/chat_cli.go":      "claude (agent's guard)",
	"internal/agent/shell.go":         "the user's shell for a chat's ! commands (localrun.Check)",
	"internal/claudecli/claudecli.go": "claude --version probe (runners only)",
	"internal/runnerctl/preflight.go": "claude auth status (`dash runner` setup)",
	"internal/runnerctl/service.go":   "systemctl/launchctl (`dash runner` setup)",
	"internal/runnerctl/update.go":    "the downloaded dash's version check (runners updating themselves)",
	"internal/tui/app.go":             "xdg-open/open (the TUI client)",
	"internal/testbin/testbin.go":     "sh writing tests' stand-in executables (tests only)",
	"cmd/dash/exec_windows.go":        "the updated dash, restarting `dash runner` in place (Windows; runners only)",
}

// TestNoDirectGitOrGh fails when code outside this package starts git or gh
// itself, or a new file starts processes, so nothing bypasses Command (and
// with it the hub's guard). Tests may run git to set up repositories.
func TestNoDirectGitOrGh(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal("module root not found:", err)
	}
	self, _ := filepath.Abs(".")
	seen := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "vendor", "node_modules", ".git":
				return filepath.SkipDir
			}
			if path == self {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, c := range execCalls(t, path) {
			if c.tool != "" {
				t.Errorf("%s:%d: runs %s with os/exec directly; use localrun.Command(ctx, localrun.%s, ...) so the hub refuses it",
					rel, c.line, c.tool, map[string]string{"git": "Git", "gh": "Gh"}[c.tool])
				continue
			}
			if _, ok := execAllowed[rel]; !ok {
				t.Errorf("%s:%d: starts a process with os/exec: run git/gh through localrun.Command, "+
					"else check localrun.Check first and add the file to execAllowed", rel, c.line)
			}
			seen[rel] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for f := range execAllowed {
		if !seen[f] {
			t.Errorf("%s no longer starts processes: remove it from execAllowed", f)
		}
	}
}

type execCall struct {
	line int
	tool string // "git" or "gh" when the binary is that literal
}

// execCalls finds the exec.Command and exec.CommandContext calls in file
// (os/exec under any import name).
func execCalls(t *testing.T, file string) []execCall {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	name := ""
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "os/exec" {
			name = "exec"
			if imp.Name != nil {
				name = imp.Name.Name
			}
		}
	}
	if name == "" || name == "_" {
		return nil
	}
	var calls []execCall
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != name {
			return true
		}
		bin := -1
		switch sel.Sel.Name {
		case "Command":
			bin = 0
		case "CommandContext":
			bin = 1
		default:
			return true
		}
		c := execCall{line: fset.Position(call.Pos()).Line}
		if len(call.Args) > bin {
			if lit, ok := call.Args[bin].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, _ := strconv.Unquote(lit.Value); s == "git" || s == "gh" {
					c.tool = s
				}
			}
		}
		calls = append(calls, c)
		return true
	})
	return calls
}
