package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Titler writes short titles for todos created without one, using a small
// model through the Claude Code CLI (so it runs on the same login as todos).
type Titler struct {
	Bin   string // default "claude"
	Model string // default "haiku"
	// SplitModel splits large tasks (default "sonnet"): planning the parts
	// takes more judgement than naming them.
	SplitModel string
}

func (t Titler) Title(ctx context.Context, prompt string) (string, error) {
	out, err := t.ask(ctx, "You name software tasks. Reply with only the title.",
		"Write a title of at most 6 words for this coding task. Use sentence case, "+
			"start with a verb, no quotes, no trailing period.\n\nTask:\n"+prompt, 4000)
	if err != nil {
		return "", err
	}
	return cleanTitle(out)
}

// Summary condenses Claude's final report on a todo into one short sentence.
func (t Titler) Summary(ctx context.Context, report string) (string, error) {
	out, err := t.ask(ctx, "You summarise finished software tasks. Reply with only the sentence.",
		"In one short sentence (at most 20 words), say what was done, in past tense, "+
			"no preamble, no quotes.\n\nReport:\n"+report, 4000)
	if err != nil {
		return "", err
	}
	return cleanSummary(out)
}

// Refine rewrites pasted text, often a chat answer, into clean instructions
// for a todo: what to do stays, chatter around it goes.
func (t Titler) Refine(ctx context.Context, prompt string) (string, error) {
	if len(prompt) > maxRefine {
		return "", errors.New("instructions are too long to clean up")
	}
	out, err := t.ask(ctx, "You edit task instructions for a coding agent. Reply with only the rewritten instructions.",
		"Rewrite the text below as clear, self-contained instructions for a coding agent working in the repository. "+
			"It may be pasted from a chat response: drop greetings, commentary, offers to help, rejected alternatives "+
			"and anything else that is not part of the task. Keep every concrete requirement, constraint, file name, "+
			"identifier and code snippet. Do not invent requirements or add steps that are not there. Be concise; "+
			"use Markdown lists or headings only where they help. No preamble.\n\nText:\n"+prompt, 0)
	if err != nil {
		return "", err
	}
	return cleanRefined(out)
}

// Part is one of the todos a large task is split into.
type Part struct {
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
	// After are the parts (indexes into the split, all before this one)
	// that must be merged before this one starts.
	After []int `json:"after,omitempty"`
}

// MaxParts is how many todos a task is split into at most.
const MaxParts = 10

// Split proposes how to break a large task into smaller todos, each one
// that can be done, reviewed and merged on its own. Nothing is stored: the
// user confirms (and edits) the parts first.
func (t Titler) Split(ctx context.Context, prompt string) ([]Part, error) {
	if len(prompt) > maxRefine {
		return nil, errors.New("instructions are too long to split")
	}
	model := t.SplitModel
	if model == "" {
		model = "sonnet"
	}
	out, err := t.run(ctx, model, 3*time.Minute,
		"You plan software work for a coding agent. Reply with only JSON.",
		"Split the task below into smaller tasks for a coding agent working in the repository. "+
			"Each task must be done, reviewed and merged on its own and leave the code working, so a "+
			"reviewer can read its diff in one sitting. Use as few tasks as the work needs, at most "+
			fmt.Sprint(MaxParts)+"; a task that is already small stays one. In order: earlier tasks first.\n\n"+
			"Reply with only a JSON array, one object per task: "+
			`{"title": "...", "prompt": "...", "after": [1]}. `+
			"title: at most 6 words, sentence case, starting with a verb. "+
			"prompt: self-contained instructions; the agent sees only this task, so repeat the context, "+
			"constraints, file names, identifiers and code from the original that it needs. Do not invent "+
			"requirements that are not in the original. "+
			"after: the numbers (1-based) of earlier tasks in the array that must be merged before this one "+
			"can start; leave it empty when it doesn't build on them.\n\nTask:\n"+prompt)
	if err != nil {
		return nil, err
	}
	return parseParts(out)
}

// parseParts reads Split's reply: a JSON array, perhaps in a code fence or
// with words around it. "after" is 1-based there and 0-based in a Part;
// numbers that don't name an earlier part are dropped.
func parseParts(s string) ([]Part, error) {
	i, j := strings.IndexByte(s, '['), strings.LastIndexByte(s, ']')
	if i < 0 || j < i {
		return nil, errors.New("no tasks were proposed")
	}
	var raw []struct {
		Title  string `json:"title"`
		Prompt string `json:"prompt"`
		After  []int  `json:"after"`
	}
	if err := json.Unmarshal([]byte(s[i:j+1]), &raw); err != nil {
		return nil, fmt.Errorf("the proposed tasks weren't readable: %w", err)
	}
	var parts []Part
	kept := make([]int, len(raw)) // raw index → index in parts, or -1
	for i, r := range raw {
		kept[i] = -1
		p := Part{Prompt: strings.TrimSpace(r.Prompt)}
		if p.Prompt == "" || len(parts) == MaxParts {
			continue
		}
		p.Title, _ = cleanTitle(r.Title) // untitled: named when it's created
		for _, n := range r.After {
			if n >= 1 && n-1 < i && kept[n-1] >= 0 && !slices.Contains(p.After, kept[n-1]) {
				p.After = append(p.After, kept[n-1])
			}
		}
		kept[i] = len(parts)
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return nil, errors.New("no tasks were proposed")
	}
	return parts, nil
}

// ask runs the small model on input, cut to limit bytes when limit > 0.
func (t Titler) ask(ctx context.Context, system, input string, limit int) (string, error) {
	model := t.Model
	if model == "" {
		model = "haiku"
	}
	if limit > 0 && len(input) > limit {
		input = input[:limit]
	}
	return t.run(ctx, model, 90*time.Second, system, input)
}

// run runs model on input, without tools, for at most timeout.
func (t Titler) run(ctx context.Context, model string, timeout time.Duration, system, input string) (string, error) {
	if err := checkLocal(); err != nil {
		return "", err
	}
	bin := t.Bin
	if bin == "" {
		bin = "claude"
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-p", "--output-format", "text", "--model", model,
		"--tools", "", "--no-session-persistence", "--strict-mcp-config", "--setting-sources", "",
		"--system-prompt", system)
	cmd.Dir = os.TempDir()
	cmd.Stdin = strings.NewReader(input)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	out, err := cmd.Output()
	return string(out), err
}

const maxRefine = 30000

func cleanRefined(s string) (string, error) {
	s = strings.TrimSpace(s)
	// A reply wrapped whole in a code fence is unwrapped.
	if strings.HasPrefix(s, "```") && strings.HasSuffix(s, "```") && strings.Count(s, "```") == 2 {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = strings.TrimSpace(s[i+1 : len(s)-3])
		}
	}
	if s == "" {
		return "", errors.New("no usable instructions")
	}
	return s, nil
}

func cleanTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, "\"'`*# ")
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 80 {
		return "", errors.New("no usable title")
	}
	return s, nil
}

func cleanSummary(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, "\"'`*# ")
	if s == "" || len(s) > 200 {
		return "", errors.New("no usable summary")
	}
	return s, nil
}
