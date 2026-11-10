// Command fakeclaude stands in for Claude Code in tests that run dash's
// binaries: it answers the way dash calls `claude -p` for a title, a split, a chat
// turn, a scan and a task, and records each call in $FAKECLAUDE_LOG.
//
// A task reports an issue through the board tools dash gives it (the
// "dash_board" server of --mcp-config) and writes a file, so the test can see
// both on the hub. A task whose prompt says "ask permission" first asks to
// run a command through dash's --permission-prompt-tool and writes the
// answer to permission.txt; a resumed run (rework) writes the notes it got
// to rework-by-runner.txt.
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
	// Dash's check of the CLI (internal/claudecli): its version, and -p
	// without a prompt, which Claude Code refuses.
	if has("--version") {
		fmt.Println("2.1.0 (Claude Code)")
		return
	}
	if len(bytes.TrimSpace(stdin)) == 0 {
		fmt.Fprintln(os.Stderr, "Error: Input must be provided either through stdin or as a prompt argument when using --print")
		os.Exit(1)
	}
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
		// A split's system prompt asks for JSON: two tasks, the second
		// building on the first.
		if i := slices.Index(args, "--system-prompt"); i >= 0 && i+1 < len(args) && strings.Contains(args[i+1], "JSON") {
			fmt.Println(`[{"title": "Add the users table", "prompt": "Create the users table."},` +
				` {"title": "Add the signup form", "prompt": "Build the signup form on the users table.", "after": [1]}]`)
			return
		}
		fmt.Println("Title from the runner")
	case "chat":
		line(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_start"}})
		line(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta",
			"delta": map[string]any{"type": "text_delta", "text": "Hello from the runner"}}})
		// A reply stays streaming while $FAKECLAUDE_LOG.chat-hold exists.
		for f := os.Getenv("FAKECLAUDE_LOG"); f != ""; time.Sleep(50 * time.Millisecond) {
			if _, err := os.Stat(f + ".chat-hold"); err != nil {
				break
			}
		}
		result("Hello from the runner")
	case "scan":
		result("Looked around.\n\n```json\n" + `{"findings": [{"title": "Scan finding from the runner", "type": "bug", "severity": "high", ` +
			`"file": "README.md", "line": 1, "details": "found", "fingerprint": "fake:readme"}]}` + "\n```")
	default:
		if _, err := callTool(args, "dash_board", "report_issue", map[string]any{
			"title": "Issue reported by the task on the runner", "type": "bug", "severity": "low"}); err != nil {
			fmt.Fprintln(os.Stderr, "report_issue:", err)
			os.Exit(1)
		}
		hold()
		files := map[string]string{"done-by-runner.txt": "done\n"}
		if strings.Contains(string(stdin), "ask permission") {
			out, err := callTool(args, "dash", "permission_prompt", map[string]any{
				"tool_name": "Bash", "input": map[string]any{"command": "echo fake-permission-check"}})
			if err != nil {
				fmt.Fprintln(os.Stderr, "permission_prompt:", err)
				os.Exit(1)
			}
			files["permission.txt"] = behavior(out) + "\n"
		}
		if has("--resume") {
			files["rework-by-runner.txt"] = string(stdin)
		}
		for name, content := range files {
			if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
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
	line(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": text, "num_turns": 1, "total_cost_usd": 0.01, "session_id": "fake-session"})
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// callTool calls a tool on one of the MCP servers of the --mcp-config dash
// passed and returns the response.
func callTool(args []string, server, tool string, arguments map[string]any) ([]byte, error) {
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
		if s, ok := cfg.MCPServers[server]; ok {
			url, auth = s.URL, s.Headers["Authorization"]
		}
	}
	if url == "" {
		return nil, fmt.Errorf("no %s MCP server in %q", server, args)
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": tool, "arguments": arguments,
	}})
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", auth)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || bytes.Contains(out, []byte(`"isError":true`)) || bytes.Contains(out, []byte(`"error"`)) {
		return nil, fmt.Errorf("%s: %s", res.Status, out)
	}
	return out, nil
}

// behavior is "allow" or "deny" from a permission_prompt response.
func behavior(out []byte) string {
	var res struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
	}
	_ = json.Unmarshal(out, &res)
	for _, c := range res.Result.Content {
		var answer struct{ Behavior string }
		if json.Unmarshal([]byte(c.Text), &answer) == nil && answer.Behavior != "" {
			return answer.Behavior
		}
	}
	return "unknown: " + string(out)
}
