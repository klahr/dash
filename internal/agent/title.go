package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Titler writes short titles for todos created without one, using a small
// model through the Claude Code CLI (so it runs on the same login as todos).
type Titler struct {
	Bin   string // default "claude"
	Model string // default "haiku"
}

func (t Titler) Title(ctx context.Context, prompt string) (string, error) {
	out, err := t.ask(ctx, "You name software tasks. Reply with only the title.",
		"Write a title of at most 6 words for this coding task. Use sentence case, "+
			"start with a verb, no quotes, no trailing period.\n\nTask:\n"+prompt)
	if err != nil {
		return "", err
	}
	return cleanTitle(out)
}

// Summary condenses Claude's final report on a todo into one short sentence.
func (t Titler) Summary(ctx context.Context, report string) (string, error) {
	out, err := t.ask(ctx, "You summarise finished software tasks. Reply with only the sentence.",
		"In one short sentence (at most 20 words), say what was done, in past tense, "+
			"no preamble, no quotes.\n\nReport:\n"+report)
	if err != nil {
		return "", err
	}
	return cleanSummary(out)
}

func (t Titler) ask(ctx context.Context, system, input string) (string, error) {
	bin, model := t.Bin, t.Model
	if bin == "" {
		bin = "claude"
	}
	if model == "" {
		model = "haiku"
	}
	if len(input) > 4000 {
		input = input[:4000]
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
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
