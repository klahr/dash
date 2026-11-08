package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

func TestFrontmatter(t *testing.T) {
	meta, body := frontmatter("---\r\nname: pdf-tools\r\ndescription: \"Fill \\\"PDF\\\" forms\"\r\nallowed-tools: Read\r\n---\r\n# PDF\r\nUse it.\r\n")
	if meta["name"] != "pdf-tools" || meta["description"] != `Fill "PDF" forms` || !strings.HasPrefix(body, "# PDF") {
		t.Fatalf("meta %q body %q", meta, body)
	}
	meta, _ = frontmatter("---\nname: 'it''s'\ndescription: >\n  Reviews code\n  for bugs.\ntools:\n  - Read\n---\n")
	if meta["name"] != "it's" || meta["description"] != "Reviews code for bugs." {
		t.Fatalf("folded: %q", meta)
	}
	if meta, body := frontmatter("# No frontmatter\n"); len(meta) != 0 || body != "# No frontmatter\n" {
		t.Fatalf("plain: %q %q", meta, body)
	}
}

// The Claude section lists the repo's Claude files: tracked ones from the
// default branch, local ones from the checkout, with what's missing.
func TestClaudeFilesEndpoint(t *testing.T) {
	ctx, g := context.Background(), gitops.New(nil)
	dir := filepath.Join(t.TempDir(), "repo")
	if err := g.InitRepo(ctx, "p", dir, "main"); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "CLAUDE.local.md\n")
	write(".claude/skills/release/SKILL.md", "---\nname: release\ndescription: Cut a release\n---\n# Release\nTag it.\n")
	write(".claude/skills/nested/deeper/SKILL.md", "---\nname: no\n---\n") // not a skill
	write(".claude/agents/reviewer.md", "---\nname: reviewer\ndescription: Reviews diffs\n---\nYou review.\n")
	write(".mcp.json", `{"mcpServers":{"github":{},"db":{}}}`)
	if _, err := g.CommitAll(ctx, "p", dir, "claude files"); err != nil {
		t.Fatal(err)
	}
	// Not on main: an uncommitted skill, and the ignored CLAUDE.local.md.
	write(".claude/skills/draft/SKILL.md", "---\nname: draft\ndescription: Work in progress\n---\n")
	write("CLAUDE.local.md", "My sandbox URLs\n")
	write("CLAUDE.md", "# Uncommitted\n") // untracked, so local too

	s, _, p := boardServer(t)
	s.Git = g
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.Path, x.DefaultBranch = dir, "main"; return nil }); err != nil {
		t.Fatal(err)
	}
	h := loggedIn(t, s.Routes())
	w := do(t, h, "GET", "/api/projects/"+p.ID+"/claude-files", "")
	var res struct {
		Branch string
		Files  []claudeFile
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil {
		t.Fatalf("claude-files: %d %s", w.Code, w.Body)
	}
	var got []string
	byPath := map[string]claudeFile{}
	for _, f := range res.Files {
		got = append(got, f.Kind+":"+f.Path)
		byPath[f.Path] = f
	}
	want := []string{"claude_md:CLAUDE.md", "skill:.claude/skills/draft/SKILL.md", "skill:.claude/skills/release/SKILL.md",
		"agent:.claude/agents/reviewer.md", "mcp:.mcp.json", "settings:.claude/settings.json", "local_md:CLAUDE.local.md"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("files\n got %v\nwant %v", got, want)
	}
	if f := byPath[".claude/skills/release/SKILL.md"]; !f.Exists || f.Local || f.Name != "release" || f.Description != "Cut a release" || f.Summary != "# Release\nTag it." {
		t.Errorf("release skill %+v", f)
	}
	if f := byPath[".claude/skills/draft/SKILL.md"]; !f.Exists || !f.Local || f.Description != "Work in progress" {
		t.Errorf("draft skill %+v", f)
	}
	if f := byPath[".claude/agents/reviewer.md"]; f.Name != "reviewer" || f.Description != "Reviews diffs" {
		t.Errorf("agent %+v", f)
	}
	if f := byPath[".mcp.json"]; f.Summary != "2 servers: db, github" {
		t.Errorf("mcp %+v", f)
	}
	if f := byPath[".claude/settings.json"]; f.Exists {
		t.Errorf("settings %+v", f)
	}
	if f := byPath["CLAUDE.md"]; !f.Exists || !f.Local || f.Summary != "# Uncommitted" {
		t.Errorf("CLAUDE.md %+v", f)
	}
	if f := byPath["CLAUDE.local.md"]; !f.Exists || !f.Local || f.Summary != "My sandbox URLs" {
		t.Errorf("CLAUDE.local.md %+v", f)
	}

	// Once committed, CLAUDE.md comes from main; without CLAUDE.local.md its row goes.
	write(".claude/settings.json", `{"hooks":{"Stop":[],"PreToolUse":[]},"permissions":{"allow":["Bash(go test *)"],"deny":["Read(.env)"]}}`)
	if _, err := g.CommitAll(ctx, "p", dir, "commit"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "CLAUDE.local.md")); err != nil {
		t.Fatal(err)
	}
	files, err := s.claudeFiles(ctx, model.Project{ID: p.ID, Path: dir, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		switch f.Path {
		case "CLAUDE.md":
			if f.Local || !f.Exists {
				t.Errorf("committed CLAUDE.md %+v", f)
			}
		case ".claude/settings.json":
			if f.Summary != "Hooks: PreToolUse, Stop · Permissions: 1 allow, 1 deny" {
				t.Errorf("settings summary %q", f.Summary)
			}
		case "CLAUDE.local.md":
			t.Errorf("CLAUDE.local.md listed after it was removed")
		}
	}
}

// The task instructions are a project setting, trimmed and capped.
func TestTaskInstructionsSetting(t *testing.T) {
	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	h := loggedIn(t, s.Routes())
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"task_instructions":"  Use pnpm.  "}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"task_instructions":"Use pnpm."`) {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	big, _ := json.Marshal(map[string]string{"task_instructions": strings.Repeat("x", maxTaskInstructions+1)})
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID, string(big)); w.Code != http.StatusBadRequest {
		t.Fatalf("too long: %d %s", w.Code, w.Body)
	}
	if got, _ := raw(s).GetProject(p.ID); got.TaskInstructions != "Use pnpm." {
		t.Fatalf("stored %q", got.TaskInstructions)
	}
}

// A runner's copy of a hub project follows the hub's task instructions,
// which the hub sends with the link before every task.
func TestRunnerFollowsTaskInstructions(t *testing.T) {
	s, h, remote := runnerSide(t, 7)
	link := func(instr string) string {
		t.Helper()
		b, _ := json.Marshal(dispatch.Link{Name: "App", RemoteURL: remote, DefaultBranch: "main", OrgID: 7, TaskInstructions: instr})
		w := send(h, "PUT", "/api/hub/projects/hub-p1", string(b))
		var linked dispatch.Linked
		if w.Code/100 != 2 || json.Unmarshal(w.Body.Bytes(), &linked) != nil {
			t.Fatalf("link: %d %s", w.Code, w.Body)
		}
		p, _ := raw(s).GetProject(linked.ProjectID)
		return p.TaskInstructions
	}
	if got := link("Use pnpm."); got != "Use pnpm." {
		t.Fatalf("new link: %q", got)
	}
	if got := link("Use yarn."); got != "Use yarn." {
		t.Fatalf("changed: %q", got)
	}
	if got := link(""); got != "" {
		t.Fatalf("cleared: %q", got)
	}
}

// "Generate with /init" queues a task whose prompt is the slash command; it
// must reach Claude first to run as one.
func TestSlashCommandPrompt(t *testing.T) {
	got := agent.BuildPrompt(model.Todo{Title: "Generate CLAUDE.md", Prompt: "/init"})
	if !strings.HasPrefix(got, "/init\n") {
		t.Fatalf("slash command prompt %q", got)
	}
	if got := agent.BuildPrompt(model.Todo{Title: "Fix", Prompt: "/api/x returns 500"}); !strings.HasPrefix(got, "# Task: Fix") {
		t.Fatalf("path prompt %q", got)
	}
}
