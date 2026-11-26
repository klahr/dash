package web

import (
	"html"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// Every normal-mode key the Keyboard sheet lists gets past BOUND in
// app/vim.js: the keydown handler drops any key BOUND doesn't match, so a
// key missing there never reaches its handler.
func TestKeysSheetBound(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		b, err := fs.ReadFile(Static, name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	m := regexp.MustCompile(`(?m)^export const BOUND = /(.*)/;$`).FindStringSubmatch(read("app/vim.js"))
	if m == nil {
		t.Fatal("no BOUND in app/vim.js")
	}
	bound := regexp.MustCompile(m[1])

	page := read("index.html")
	start := strings.Index(page, `id="keys-sheet"`)
	if start < 0 {
		t.Fatal("no #keys-sheet in index.html")
	}
	page = page[start:]
	page = page[:strings.Index(page, "</dialog>")]

	// The sections whose keys go through the normal-mode handler (the others:
	// drawing, the chat, forms and Planning handle their own keys).
	normal := map[string]bool{"everywhere": true, "sections": true, "board": true, "task": true, "open": true, "perms": true, "shell": true, "history": true, "goals": true, "deploys": true}
	// How the sheet names a key, against e.key.
	names := map[string]string{"Esc": "Escape", "Space": " ", "Ctrl-d": "ctrl+d", "Ctrl-u": "ctrl+u"}
	// Handled before BOUND: the palette (Ctrl-k), the terminal (Ctrl-]), counts (5j).
	skip := map[string]bool{"Ctrl-k": true, "Ctrl-]": true, "5j": true}

	sections := regexp.MustCompile(`(?s)<section[^>]*data-section="([a-z]+)"(.*?)</section>`).FindAllStringSubmatch(page, -1)
	kbd := regexp.MustCompile(`<th scope="row">(.*?)</th>`)
	key := regexp.MustCompile(`<kbd>([^<]*)</kbd>`)
	checked := 0
	for _, s := range sections {
		if !normal[s[1]] {
			continue
		}
		for _, row := range kbd.FindAllStringSubmatch(s[2], -1) {
			for _, k := range key.FindAllStringSubmatch(row[1], -1) {
				name := html.UnescapeString(k[1])
				if skip[name] {
					continue
				}
				if n, ok := names[name]; ok {
					name = n
				}
				checked++
				if !bound.MatchString(name) {
					t.Errorf("Keyboard sheet, %s: %q isn't in BOUND (app/vim.js), so it never fires", s[1], k[1])
				}
			}
		}
	}
	if checked < 50 {
		t.Fatalf("checked only %d keys: has the Keyboard sheet's markup changed?", checked)
	}
}
