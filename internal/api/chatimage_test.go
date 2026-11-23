package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/mcptools"
)

// imageChat shows img (show_image, as the chat's tool would), then says "Here."
type imageChat struct{ img mcptools.Image }

func (c imageChat) Stream(_ context.Context, _ string, _ []agent.ChatMessage, emit func(agent.ChatDelta)) error {
	emit(agent.ChatDelta{Kind: "tool_use", ID: "u1", Tool: "mcp__dash_board__show_image"})
	img := c.img
	emit(agent.ChatDelta{Kind: "tool_result", ID: "u1", Text: "shown", Image: &img})
	emit(agent.ChatDelta{Kind: "text", Text: "Here."})
	emit(agent.ChatDelta{Kind: "done"})
	return nil
}

// An image the chat shows is stored as an upload of the chat's org: the
// client gets the upload, not the image, and the answer is saved with it,
// which the next turn tells Claude about.
func TestProjectChatSavesShownImage(t *testing.T) {
	s, _, p := boardServer(t)
	s.UploadRoot = t.TempDir()
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	s.Chat = imageChat{mcptools.Image{Name: "my chart.png", Type: "image/png", Data: []byte(png)}}
	h := asOwnerHandler(t, s)
	owner := testOwner(t, s)

	conn := chatSocket(t, s)
	if err := conn.WriteJSON(map[string]any{"type": "send", "project": p.ID, "base": 0, "messages": []agent.ChatMessage{cmdMsg("draw a chart")}}); err != nil {
		t.Fatal(err)
	}
	var got *agent.ChatDelta
	for _, d := range readTurn(t, conn, 0) {
		if d.Kind == "tool_result" {
			got = &d
		}
	}
	if got == nil || got.Image != nil || got.Attachment == nil || got.Attachment.Name != "my-chart.png" || got.Attachment.Type != "image/png" {
		t.Fatalf("tool_result the client got: %+v", got)
	}
	a := got.Attachment
	msgs := raw(s).ProjectChat(p.ID, owner.User)
	if len(msgs) != 2 || msgs[1].Content != "Here." || len(msgs[1].Attachments) != 1 || msgs[1].Attachments[0].ID != a.ID {
		t.Fatalf("saved: %+v", msgs)
	}
	w := send(h, "GET", "/api/uploads/"+a.ID+"/"+a.Name, "")
	if w.Code != http.StatusOK || w.Body.String() != png || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("GET the upload: %d %q %s", w.Code, w.Body, w.Header().Get("Content-Type"))
	}
	if hist := withAttachments(msgs); hist[1].Content != "Here.\n\n[Shown to the user: my-chart.png]" {
		t.Errorf("Claude's copy of its answer: %q", hist[1].Content)
	}
}

// A file that isn't an image Dash shows is never stored.
func TestSaveImageRefusesOtherFiles(t *testing.T) {
	s, _, _ := boardServer(t)
	s.UploadRoot = t.TempDir()
	sv := s.as(Caller{User: testOwner(t, s).User, Org: testOwner(t, s).Org})
	for _, img := range []mcptools.Image{
		{Name: "notes.txt", Type: "text/plain", Data: []byte("hi")},
		{Name: "page.html", Type: "image/png", Data: []byte("<script>")},
		{Name: "doc.pdf", Type: "application/pdf", Data: []byte("%PDF")},
	} {
		if a, err := sv.saveImage(img); err == nil {
			t.Errorf("saved %s as %+v", img.Name, a)
		}
	}
}
