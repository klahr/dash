// Command fakeclaude stands in for Claude Code in tests that run dash's
// binaries: it answers the way dash calls `claude -p` for a title, a chat
// turn, a scan and a task, and records each call in $FAKECLAUDE_LOG.
//
// A task reports an issue through the board tools dash gives it (the
// "dash_board" server of --mcp-config) and writes a file, so the test can see
// both on the hub.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

func main() {
	args := os.Args[1:]
	stdin, _ := io.ReadAll(os.Stdin)
	has := func(a string) bool { return slices.Contains(args, a) }
	kind := "task"
	switch {
	case has("text"): // --output-format text: titles, summaries, clean-ups
		kind = "small"
	case has("--include-partial-messages"):
		kind = "chat"
	case has("dontAsk"):
		kind = "scan"
	}
	if f := os.Getenv("FAKECLAUDE_LOG"); f != "" {
		wd, _ := os.Getwd()
		if lf, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(lf, "%s %s\n", kind, wd)
			lf.Close()
		}
	}
	switch kind {
	case "small":
		fmt.Println("Title from the runner")
	case "chat":
		line(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_start"}})
		line(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta",
			"delta": map[string]any{"type": "text_delta", "text": "Hello from the runner"}}})
		result("Hello from the runner")
	case "scan":
		result("Looked around.\n\n```json\n" + `{"findings": [{"title": "Scan finding from the runner", "type": "bug", "severity": "high", ` +
			`"file": "README.md", "line": 1, "details": "found", "fingerprint": "fake:readme"}]}` + "\n```")
	default:
		if err := reportIssue(args); err != nil {
			fmt.Fprintln(os.Stderr, "report_issue:", err)
			os.Exit(1)
		}
		hold()
		if err := os.WriteFile("done-by-runner.txt", []byte("done\n"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		line(map[string]any{"type": "system", "subtype": "init", "session_id": "fake-session"})
		line(map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{{"type": "text", "text": "Working on: " + firstLine(string(stdin))}}}})
		result("Wrote done-by-runner.txt.")
	}
}

// hold keeps a task running while the file $FAKECLAUDE_HOLD exists, so a
// test can stop the hub or the runner mid-task. Each held run appends its
// pid to $FAKECLAUDE_HOLD.pids first.
func hold() {
	path := os.Getenv("FAKECLAUDE_HOLD")
	if path == "" {
		return
	}
	if _, err := os.Stat(path); err != nil {
		return
	}
	if f, err := os.OpenFile(path+".pids", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, os.Getpid())
		f.Close()
	}
	for {
		if _, err := os.Stat(path); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func line(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func result(text string) {
	line(map[string]any{"type": "result", "subtype": "success", "result": text, "num_turns": 1, "total_cost_usd": 0.01, "session_id": "fake-session"})
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// reportIssue calls report_issue on the board server of the --mcp-config
// dash passed.
func reportIssue(args []string) error {
	var url, auth string
	for i, a := range args {
		if a != "--mcp-config" || i+1 >= len(args) {
			continue
		}
		var cfg struct {
			MCPServers map[string]struct {
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			} `json:"mcpServers"`
		}
		if json.Unmarshal([]byte(args[i+1]), &cfg) != nil {
			continue
		}
		if s, ok := cfg.MCPServers["dash_board"]; ok {
			url, auth = s.URL, s.Headers["Authorization"]
		}
	}
	if url == "" {
		return fmt.Errorf("no dash_board MCP server in %q", args)
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "report_issue", "arguments": map[string]any{"title": "Issue reported by the task on the runner", "type": "bug", "severity": "low"},
	}})
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", auth)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || bytes.Contains(out, []byte(`"isError":true`)) || bytes.Contains(out, []byte(`"error"`)) {
		return fmt.Errorf("%s: %s", res.Status, out)
	}
	return nil
}
