package scan

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/mcptools"
)

// A scan reads git history through these MCP tools, not Bash: a Bash rule
// like "Bash(git log:*)" allows any arguments, including --output=<file>,
// which writes anywhere. The tools build the git command themselves from
// checked revisions and paths, so a scan (or a prompt injection in the code
// it reads) can't pass git an option.

// GitServerName is the scan's name for the git tools server.
const GitServerName = "dash_git"

// GitToolNames are the git tools a scan gets.
var GitToolNames = []string{"git_diff", "git_log", "git_show", "git_blame"}

// gitAllowed are GitToolNames as Claude Code permission rules.
func gitAllowed() []string {
	out := make([]string, len(GitToolNames))
	for i, n := range GitToolNames {
		out[i] = "mcp__" + GitServerName + "__" + n
	}
	return out
}

// GitMCP serves the git tools to running scans (POST), each authenticated
// by a token that maps to its throwaway checkout.
type GitMCP struct {
	mu sync.Mutex
	m  map[string]string // token → checkout
}

// Register issues a token for a scan of the checkout in dir. Call release
// when the run ends.
func (g *GitMCP) Register(dir string) (token string, release func()) {
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	token = hex.EncodeToString(buf)
	g.mu.Lock()
	if g.m == nil {
		g.m = map[string]string{}
	}
	g.m[token] = dir
	g.mu.Unlock()
	return token, func() {
		g.mu.Lock()
		delete(g.m, token)
		g.mu.Unlock()
	}
}

func (g *GitMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "this MCP server only accepts POST", http.StatusMethodNotAllowed)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	var dir string
	if ok && token != "" {
		g.mu.Lock()
		dir, ok = g.m[token]
		g.mu.Unlock()
	}
	if !ok {
		http.Error(w, "unknown or expired token", http.StatusUnauthorized)
		return
	}
	srv := &mcptools.Server{
		Name:         GitServerName,
		Instructions: "Read-only git for the scan's checkout: diffs, history, commits and blame.",
		Tools:        GitTools(dir),
	}
	srv.ServeHTTP(w, r)
}

// GitServerConfig is the --mcp-config entry for the git tools at url (the
// GitMCP handler's address).
func GitServerConfig(url, token string) map[string]any {
	return map[string]any{
		"type":    "http",
		"url":     url,
		"headers": map[string]string{"Authorization": "Bearer " + token},
	}
}

// GitTools are git_diff, git_log, git_show and git_blame on the repository
// checked out in dir.
func GitTools(dir string) []mcptools.Tool {
	pathsParam := mcptools.Param{Name: "paths", Type: "array", Desc: "Limit to these files or directories (relative to the repository root)."}
	return []mcptools.Tool{
		{
			Name: "git_diff",
			Desc: "Show the changes between two commits (git diff <from> [<to>]); without to, between from and the checkout.",
			Params: []mcptools.Param{
				{Name: "from", Type: "string", Desc: "Commit, branch or tag to diff from, e.g. a commit hash or HEAD~3.", Required: true},
				{Name: "to", Type: "string", Desc: "Commit to diff to (default: the checkout)."},
				pathsParam,
				{Name: "stat", Type: "boolean", Desc: "Only list the changed files and line counts (--stat)."},
			},
			Run: func(a mcptools.Args) (any, error) {
				revs, err := revisions(a.Str("from"), a.Str("to"))
				if err != nil {
					return nil, err
				}
				if len(revs) == 0 {
					return nil, fmt.Errorf("from is required")
				}
				paths, err := pathspecs(a.List("paths"))
				if err != nil {
					return nil, err
				}
				args := []string{"diff", "--no-ext-diff", "--no-textconv"}
				if a.Flag("stat") {
					args = append(args, "--stat")
				}
				return runGit(dir, args, revs, paths)
			},
		},
		{
			Name: "git_log",
			Desc: "List commits (git log), newest first.",
			Params: []mcptools.Param{
				{Name: "revision", Type: "string", Desc: "Commit or range to list, e.g. HEAD or abc123..HEAD (default HEAD)."},
				pathsParam,
				{Name: "max_count", Type: "integer", Desc: "How many commits (default 20, at most 500)."},
				{Name: "oneline", Type: "boolean", Desc: "One line per commit (--oneline)."},
				{Name: "patch", Type: "boolean", Desc: "Include each commit's diff (-p)."},
				{Name: "stat", Type: "boolean", Desc: "Include each commit's changed files (--stat)."},
			},
			Run: func(a mcptools.Args) (any, error) {
				revs, err := revisions(a.Str("revision"))
				if err != nil {
					return nil, err
				}
				paths, err := pathspecs(a.List("paths"))
				if err != nil {
					return nil, err
				}
				n := min(max(a.Num("max_count", 20), 1), 500)
				args := []string{"log", "--no-ext-diff", "--no-textconv", "--max-count=" + strconv.Itoa(n)}
				if a.Flag("oneline") {
					args = append(args, "--oneline")
				}
				if a.Flag("patch") {
					args = append(args, "--patch")
				}
				if a.Flag("stat") {
					args = append(args, "--stat")
				}
				return runGit(dir, args, revs, paths)
			},
		},
		{
			Name: "git_show",
			Desc: "Show a commit with its diff (git show <revision>), or a file at a commit (revision abc123:path/to/file).",
			Params: []mcptools.Param{
				{Name: "revision", Type: "string", Desc: "Commit, or commit:path for a file's content.", Required: true},
				pathsParam,
				{Name: "stat", Type: "boolean", Desc: "Only list the changed files and line counts (--stat)."},
			},
			Run: func(a mcptools.Args) (any, error) {
				revs, err := revisions(a.Str("revision"))
				if err != nil {
					return nil, err
				}
				if len(revs) == 0 {
					return nil, fmt.Errorf("revision is required")
				}
				paths, err := pathspecs(a.List("paths"))
				if err != nil {
					return nil, err
				}
				args := []string{"show", "--no-ext-diff", "--no-textconv"}
				if a.Flag("stat") {
					args = append(args, "--stat")
				}
				return runGit(dir, args, revs, paths)
			},
		},
		{
			Name: "git_blame",
			Desc: "Show which commit last changed each line of a file (git blame).",
			Params: []mcptools.Param{
				{Name: "path", Type: "string", Desc: "The file, relative to the repository root.", Required: true},
				{Name: "revision", Type: "string", Desc: "Blame the file as of this commit (default HEAD)."},
				{Name: "start_line", Type: "integer", Desc: "First line (1-based)."},
				{Name: "end_line", Type: "integer", Desc: "Last line."},
			},
			Run: func(a mcptools.Args) (any, error) {
				rev := a.Str("revision")
				if rev == "" {
					rev = "HEAD"
				}
				revs, err := revisions(rev)
				if err != nil {
					return nil, err
				}
				paths, err := pathspecs([]string{a.Str("path")})
				if err != nil {
					return nil, err
				}
				if len(paths) != 1 {
					return nil, fmt.Errorf("path is required")
				}
				args := []string{"blame"}
				if s, e := a.Num("start_line", 0), a.Num("end_line", 0); s > 0 || e > 0 {
					s = max(s, 1)
					if e < s {
						args = append(args, "-L", strconv.Itoa(s)+",")
					} else {
						args = append(args, "-L", strconv.Itoa(s)+","+strconv.Itoa(e))
					}
				}
				// blame takes no "--" after --end-of-options: rev, then the file.
				args = append(args, "--end-of-options", revs[0], paths[0])
				return gitOutput(dir, args)
			},
		},
	}
}

// revRE is a revision: a ref, hash, range (a..b, a...b), ancestry (~ ^),
// reflog (@{1}) or commit:path. Never an option: it can't start with '-'.
var revRE = regexp.MustCompile(`^[A-Za-z0-9_.@^~{}/:+][A-Za-z0-9_.@^~{}/:+-]*$`)

// revisions checks the revisions given, skipping empty ones.
func revisions(revs ...string) ([]string, error) {
	var out []string
	for _, r := range revs {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if len(r) > 300 || !revRE.MatchString(r) {
			return nil, fmt.Errorf("not a revision: %q", r)
		}
		out = append(out, r)
	}
	return out, nil
}

// pathspecs checks paths are inside the repository and plain (no options).
func pathspecs(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		clean := path.Clean(strings.ReplaceAll(p, "\\", "/"))
		if strings.HasPrefix(p, "-") || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsRune(p, 0) {
			return nil, fmt.Errorf("not a path in the repository: %q", p)
		}
		out = append(out, clean)
	}
	return out, nil
}

// runGit runs git args, then the revisions after --end-of-options (so
// none is read as an option), then the paths after --.
func runGit(dir string, args, revs, paths []string) (string, error) {
	args = append(args, "--end-of-options")
	args = append(args, revs...)
	args = append(args, "--")
	args = append(args, paths...)
	return gitOutput(dir, args)
}

// maxGitOutput bounds what one call returns.
const maxGitOutput = 256 << 10

func gitOutput(dir string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := localrun.Check("git " + strings.Join(args, " ")); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager", "-c", "color.ui=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(gitEnv(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "PAGER=cat", "GIT_OPTIONAL_LOCKS=0")
	cmd.WaitDelay = 5 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limited{b: &out, n: maxGitOutput + 1}, &limited{b: &errb, n: 8 << 10}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	s := out.String()
	if len(s) > maxGitOutput {
		s = s[:maxGitOutput] + "\n… (output cut at 256 KB: narrow it with paths, max_count or stat)"
	}
	if s == "" {
		s = "(no output)"
	}
	return s, nil
}

// gitEnv is the environment without variables that make git run another
// program or read another repository.
func gitEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GIT_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// limited keeps the first n bytes written and drops the rest.
type limited struct {
	b *bytes.Buffer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.n - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
