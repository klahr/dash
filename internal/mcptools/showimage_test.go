package mcptools

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pngHead is the start of a PNG file: enough for its type to be sniffed.
var pngHead = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00")

func TestReadImage(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	png := write("chart.png", pngHead)
	svg := write("Drawing.SVG", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"/>`))
	gif := write("anim.gif", []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"))
	webp := write("photo.webp", []byte("RIFF\x24\x00\x00\x00WEBPVP8 \x18\x00\x00\x00"))
	jpeg := write("photo.jpeg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"))

	for _, tc := range []struct{ path, typ string }{
		{png, "image/png"}, {svg, "image/svg+xml"}, {gif, "image/gif"}, {webp, "image/webp"}, {jpeg, "image/jpeg"},
	} {
		img, err := ReadImage(tc.path)
		if err != nil || img.Type != tc.typ || img.Name != filepath.Base(tc.path) || len(img.Data) == 0 {
			t.Errorf("ReadImage(%s) = %s %s %d bytes, %v", filepath.Base(tc.path), img.Name, img.Type, len(img.Data), err)
		}
	}

	big := filepath.Join(dir, "big.png")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(pngHead)
	if err := f.Truncate(MaxImage + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Mkdir(filepath.Join(dir, "dir.png"), 0o700); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct{ path, want string }{
		"no path":           {"", "path is required"},
		"relative":          {"chart.png", "isn't an absolute path"},
		"not an image name": {write("notes.txt", []byte("hello")), "isn't an image Dash shows"},
		"a pdf":             {write("doc.pdf", []byte("%PDF-1.4")), "isn't an image Dash shows"},
		"missing":           {filepath.Join(dir, "gone.png"), "there is no file"},
		"a directory":       {filepath.Join(dir, "dir.png"), "isn't a file"},
		"too big":           {big, "at most 10 MB"},
		"empty":             {write("empty.png", nil), "is empty"},
		"text named png":    {write("secret.png", []byte("password=hunter2\n")), "doesn't hold the PNG image"},
		"jpeg named png":    {write("wrong.png", []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00")), "doesn't hold the PNG image"},
		"html named svg":    {write("page.svg", []byte("<html><body>hi</body></html>")), "doesn't hold the SVG image"},
		"binary named svg":  {write("bin.svg", []byte("<svg \xff\xfe")), "doesn't hold the SVG image"},
	} {
		if _, err := ReadImage(tc.path); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", name, err, tc.want)
		}
	}
}

// show_image answers with the image's name, or the reason it can't show it.
func TestShowImageTool(t *testing.T) {
	p := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(p, pngHead, 0o600); err != nil {
		t.Fatal(err)
	}
	tool := ShowImage()
	if res, err := tool.Run(Args{"path": p}); err != nil || !strings.Contains(res.(string), "chart.png") {
		t.Errorf("show_image: %v, %v", res, err)
	}
	if _, err := tool.Run(Args{"path": "/etc/hostname"}); err == nil {
		t.Error("show_image showed a file that isn't an image")
	}
	if s := tool.schema(); s["required"].([]string)[0] != "path" {
		t.Errorf("schema %v", s)
	}
}

// A chat's show_image is served here, where the chat runs, also for a
// run of a hub's chat, whose other calls go to the hub; a task's run
// doesn't get it.
func TestHandlerServesShowImageHere(t *testing.T) {
	p := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(p, pngHead, 0o600); err != nil {
		t.Fatal(err)
	}
	toks := &Tokens{}
	hubCalls := 0
	h := &Handler{Tokens: toks,
		Board: func(Scope) (Board, error) { t.Fatal("show_image needs no board"); return nil, nil },
		Hub: func(w http.ResponseWriter, r *http.Request, sc Scope) bool {
			hubCalls++
			w.WriteHeader(http.StatusTeapot)
			return true
		}}
	call := func(token, tool string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": tool, "arguments": map[string]any{"path": p}}})
		r := httptest.NewRequest("POST", "/mcp/dash", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	hubChat, release := toks.RegisterHub(Caller{Org: 1, User: 1}, "hp")
	defer release()
	w := call(hubChat, ShowImageTool)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "chart.png") || hubCalls != 0 {
		t.Errorf("show_image of a hub's chat: %d %s (hub called %d times)", w.Code, w.Body, hubCalls)
	}
	if w := call(hubChat, "list_todos"); w.Code != http.StatusTeapot || hubCalls != 1 {
		t.Errorf("another tool of a hub's chat: %d, hub called %d times", w.Code, hubCalls)
	}
	task, release := toks.RegisterTask(Caller{Org: 1, User: 1}, "p", "t")
	defer release()
	if w := call(task, ShowImageTool); w.Code != http.StatusTeapot || hubCalls != 2 {
		t.Errorf("show_image of a task's run: %d, hub called %d times", w.Code, hubCalls)
	}
}
