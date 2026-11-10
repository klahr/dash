package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/klahr/dash/internal/model"
)

// ---------------------------------------------------------------- Claude files
// The project settings' Claude section lists the files in the repository
// that configure Claude Code: CLAUDE.md, skills, subagents, MCP servers,
// hooks and permissions. They are shortcuts to the file editor, not settings
// Dash stores; like the file browser, tracked files are read from the
// default branch and files git doesn't track from the checkout.

// claudeFile is one row of the list.
type claudeFile struct {
	// Kind is "claude_md", "skill", "agent", "mcp", "settings" or "local_md".
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	// Local is set for a file git doesn't track: it is saved straight to the
	// checkout, not through a task.
	Local bool  `json:"local,omitempty"`
	Size  int64 `json:"size,omitempty"`
	// Name and Description come from a skill's or subagent's frontmatter.
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// Summary is a short preview: the first lines of a markdown file, or
	// what a JSON file configures.
	Summary string `json:"summary,omitempty"`
}

const (
	claudePreviewBytes = maxEditBytes // files larger than the editor opens get no preview
	claudeMaxListed    = 200          // skills or subagents listed at most, each
)

func (s *Server) listClaudeFiles(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.project(w, r)
	if !ok {
		return
	}
	files, err := s.claudeFiles(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"branch": p.Base(), "files": files})
}

// claudeFiles finds the project's Claude files, in the order they are shown.
func (s *Server) claudeFiles(ctx context.Context, p model.Project) ([]claudeFile, error) {
	tracked, err := s.Git.Files(ctx, p.ID, p.Path, p.Base())
	if err != nil {
		return nil, err
	}
	local, err := s.Git.LocalFiles(ctx, p.ID, p.Path)
	if err != nil {
		return nil, err
	}
	isTracked := make(map[string]bool, len(tracked))
	for _, f := range tracked {
		isTracked[f] = true
	}
	isLocal := map[string]bool{}
	for _, f := range local {
		if !isTracked[f] {
			isLocal[f] = true
		}
	}
	all := append(slices.Clone(tracked), local...)

	file := func(kind, name string) claudeFile {
		f := claudeFile{Kind: kind, Path: name, Exists: isTracked[name] || isLocal[name], Local: isLocal[name]}
		if !f.Exists {
			return f
		}
		content, size, ok := s.claudeContent(ctx, p, name, f.Local)
		f.Size = size
		if !ok {
			return f
		}
		switch kind {
		case "skill", "agent":
			meta, body := frontmatter(content)
			f.Name, f.Description = meta["name"], meta["description"]
			f.Summary = markdownPreview(body)
		case "mcp":
			f.Summary = mcpSummary(content)
		case "settings":
			f.Summary = settingsSummary(content)
		default:
			_, body := frontmatter(content)
			f.Summary = markdownPreview(body)
		}
		return f
	}
	// matching lists the files of a pattern (one folder level), sorted.
	matching := func(pattern string) []string {
		var out []string
		for _, f := range all {
			if ok, _ := path.Match(pattern, f); ok && !slices.Contains(out, f) {
				out = append(out, f)
			}
		}
		sort.Strings(out)
		return out[:min(len(out), claudeMaxListed)]
	}

	res := []claudeFile{file("claude_md", "CLAUDE.md")}
	for _, f := range matching(".claude/skills/*/SKILL.md") {
		res = append(res, file("skill", f))
	}
	for _, f := range matching(".claude/agents/*.md") {
		res = append(res, file("agent", f))
	}
	res = append(res, file("mcp", ".mcp.json"), file("settings", ".claude/settings.json"))
	if lm := file("local_md", "CLAUDE.local.md"); lm.Exists {
		res = append(res, lm)
	}
	return res, nil
}

// claudeContent reads the start of a file for its preview: from the
// default branch, or from the checkout when git doesn't track it. ok is false
// for a file that can't be read as text.
func (s *Server) claudeContent(ctx context.Context, p model.Project, name string, local bool) (content string, size int64, ok bool) {
	var b []byte
	if local {
		full, err := s.localPath(ctx, p, name)
		if err != nil {
			return "", 0, false
		}
		f, err := os.Open(full)
		if err != nil {
			return "", 0, false
		}
		defer f.Close()
		if st, err := f.Stat(); err == nil {
			size = st.Size()
		}
		if b, err = io.ReadAll(io.LimitReader(f, claudePreviewBytes)); err != nil {
			return "", size, false
		}
	} else {
		blob, err := s.Git.Blob(ctx, p.ID, p.Path, p.Base(), name)
		if err != nil || blob == "" {
			return "", 0, false
		}
		if b, size, err = s.Git.ReadBlob(ctx, p.ID, p.Path, blob, claudePreviewBytes); err != nil || b == nil {
			return "", size, false
		}
	}
	if !utf8.Valid(b) && size <= claudePreviewBytes {
		return "", size, false
	}
	return strings.ToValidUTF8(string(b), ""), size, true
}

// frontmatter splits a markdown file's YAML frontmatter from its body and
// reads its top-level scalar keys: enough for a skill's or subagent's name
// and description, including folded (">") and literal ("|") blocks.
func frontmatter(s string) (map[string]string, string) {
	meta := map[string]string{}
	s = strings.TrimPrefix(s, "\ufeff")
	rest, ok := strings.CutPrefix(strings.ReplaceAll(s, "\r\n", "\n"), "---\n")
	if !ok {
		return meta, s
	}
	head, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		return meta, s
	}
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	lines := strings.Split(head, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if val == "" || strings.HasPrefix(val, "|") || strings.HasPrefix(val, ">") {
			var parts []string
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t") || lines[i+1] == "") {
				i++
				if t := strings.TrimSpace(lines[i]); t != "" {
					parts = append(parts, t)
				}
			}
			val = strings.Join(parts, " ")
		} else if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			if val[0] == '"' {
				if u, err := strconv.Unquote(val); err == nil {
					val = u
				} else {
					val = val[1 : len(val)-1]
				}
			} else {
				val = strings.ReplaceAll(val[1:len(val)-1], "''", "'")
			}
		}
		meta[key] = val
	}
	return meta, body
}

// markdownPreview is the first few non-empty lines of markdown, shortened.
func markdownPreview(s string) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
			if len(lines) == 3 {
				break
			}
		}
	}
	return clip(strings.Join(lines, "\n"), 240)
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}

// mcpSummary names the servers a .mcp.json configures.
func mcpSummary(s string) string {
	var cfg struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(s), &cfg); err != nil {
		return "Not valid JSON"
	}
	if len(cfg.Servers) == 0 {
		return "No servers"
	}
	names := make([]string, 0, len(cfg.Servers))
	for n := range cfg.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	return clip(plural(len(names), "server")+": "+strings.Join(names, ", "), 240)
}

// settingsSummary says what a .claude/settings.json sets up: hooks by event
// and permission rules by kind.
func settingsSummary(s string) string {
	var cfg struct {
		Hooks       map[string]json.RawMessage `json:"hooks"`
		Permissions struct {
			Allow []string `json:"allow"`
			Ask   []string `json:"ask"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(s), &cfg); err != nil {
		return "Not valid JSON"
	}
	var parts []string
	if len(cfg.Hooks) > 0 {
		events := make([]string, 0, len(cfg.Hooks))
		for e := range cfg.Hooks {
			events = append(events, e)
		}
		sort.Strings(events)
		parts = append(parts, "Hooks: "+strings.Join(events, ", "))
	}
	var perms []string
	for _, k := range []struct {
		n    int
		name string
	}{{len(cfg.Permissions.Allow), "allow"}, {len(cfg.Permissions.Ask), "ask"}, {len(cfg.Permissions.Deny), "deny"}} {
		if k.n > 0 {
			perms = append(perms, strconv.Itoa(k.n)+" "+k.name)
		}
	}
	if len(perms) > 0 {
		parts = append(parts, "Permissions: "+strings.Join(perms, ", "))
	}
	if len(parts) == 0 {
		return "No hooks or permission rules"
	}
	return clip(strings.Join(parts, " · "), 240)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
