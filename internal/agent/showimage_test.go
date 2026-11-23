package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/testbin"
)

// TestShownImageDelta: a show_image call that worked carries the image,
// read here, where the chat runs; a failed call and other tools' results
// carry none.
func TestShownImageDelta(t *testing.T) {
	testbin.SkipOnWindows(t, "runs a #!/bin/sh stand-in for claude")
	dir := t.TempDir()
	img := filepath.Join(dir, "chart.png")
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	if err := os.WriteFile(img, []byte(png), 0o600); err != nil {
		t.Fatal(err)
	}
	msg := func(role, content string) string {
		return fmt.Sprintf(`{"type":%q,"message":{"content":[%s]}}`, role, content)
	}
	lines := []string{
		msg("assistant", `{"type":"tool_use","id":"u1","name":"mcp__dash_board__show_image","input":{"path":"`+img+`"}}`),
		msg("user", `{"type":"tool_result","tool_use_id":"u1","content":"The user sees chart.png in your reply."}`),
		msg("assistant", `{"type":"tool_use","id":"u2","name":"mcp__dash_board__show_image","input":{"path":"/nope.png"}}`),
		msg("user", `{"type":"tool_result","tool_use_id":"u2","content":"there is no file /nope.png","is_error":true}`),
		msg("assistant", `{"type":"tool_use","id":"u3","name":"Read","input":{"file_path":"`+img+`"}}`),
		msg("user", `{"type":"tool_result","tool_use_id":"u3","content":"…"}`),
		`{"type":"result","is_error":false,"result":""}`,
	}
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncat >/dev/null\ncat <<'EOF'\n" + strings.Join(lines, "\n") + "\nEOF\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	images := map[string]*mcptools.Image{}
	err := ClaudeCodeChat{Bin: bin}.Stream(context.Background(), dir, []ChatMessage{{Role: "user", Content: "draw"}}, func(d ChatDelta) {
		if d.Kind == "tool_result" {
			images[d.ID] = d.Image
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := images["u1"]; got == nil || got.Name != "chart.png" || got.Type != "image/png" || string(got.Data) != png {
		t.Errorf("show_image's image: %+v", got)
	}
	if len(images) != 3 || images["u2"] != nil || images["u3"] != nil {
		t.Errorf("images on other results: %+v", images)
	}
}
