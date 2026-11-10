package agent

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
)

// With local Claude off (as on every hub), no way into Claude
// in this package runs it: a fake `claude` that leaves a mark is never
// started, and the API is never called.
func TestNoLocalClaudeRunsNothing(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "ran")
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" >> "+mark+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var apiCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer api.Close()

	localrun.Set(func() bool { return false })
	defer localrun.Set(nil)

	ctx := context.Background()
	history := []ChatMessage{{Role: "user", Content: "hi"}}
	titler := Titler{Bin: bin}
	calls := map[string]func() error{
		"task run": func() error {
			_, err := ClaudeCode{Bin: bin}.Run(ctx, ExecRequest{Dir: dir, Prompt: "x"}, func(model.RunEvent) {})
			return err
		},
		"chat": func() error {
			return ClaudeCodeChat{Bin: bin, Dir: dir}.Stream(ctx, dir, history, func(ChatDelta) {})
		},
		"api chat": func() error {
			return NewChat(option.WithBaseURL(api.URL), option.WithAPIKey("x")).Stream(ctx, "", history, func(ChatDelta) {})
		},
		"title":   func() error { _, err := titler.Title(ctx, "x"); return err },
		"summary": func() error { _, err := titler.Summary(ctx, "x"); return err },
		"refine":  func() error { _, err := titler.Refine(ctx, "x"); return err },
		"split":   func() error { _, err := titler.Split(ctx, "x"); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrNoLocalClaude) {
			t.Errorf("%s: %v, want ErrNoLocalClaude", name, err)
		}
	}
	if b, err := os.ReadFile(mark); err == nil {
		t.Errorf("claude ran with local Claude off: %s", b)
	}
	if n := apiCalls.Load(); n > 0 {
		t.Errorf("the API was called %d times with local Claude off", n)
	}

	// Turned back on, the same calls do run it.
	localrun.Set(nil)
	_, _ = titler.Title(ctx, "x")
	if _, err := os.Stat(mark); err != nil {
		t.Error("the fake claude didn't run with local Claude on: the test checks nothing")
	}
}

// Every function in this package that starts a process or makes an API
// client checks the policy first, so a new way into Claude can't forget
// it. One that runs something else (the chat's "!" commands) checks
// localrun's.
func TestEveryClaudeCallChecksThePolicy(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var reaches, checks bool
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch sel := call.Fun.(type) {
				case *ast.SelectorExpr:
					if x, ok := sel.X.(*ast.Ident); ok {
						switch x.Name + "." + sel.Sel.Name {
						case "exec.Command", "exec.CommandContext", "anthropic.NewClient":
							reaches = true
						case "localrun.Check":
							checks = true
						}
					}
				case *ast.Ident:
					if sel.Name == "checkLocal" {
						checks = true
					}
				}
				return true
			})
			if reaches && !checks && fn.Name.Name != "NewChat" {
				t.Errorf("%s: %s reaches Claude without checkLocal() (or runs a process without localrun.Check)", fset.Position(fn.Pos()), fn.Name.Name)
			}
		}
	}
}
