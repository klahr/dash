package api

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func writeFile(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// ico makes an .ico file holding img.
func ico(img []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, []uint16{0, 1, 1})
	b.Write([]byte{0, 0, 0, 0, 1, 0, 32, 0})
	_ = binary.Write(&b, binary.LittleEndian, []uint32{uint32(len(img)), 22})
	b.Write(img)
	return b.Bytes()
}

func TestFindFavicon(t *testing.T) {
	parallel(t)

	png := append(append([]byte{}, pngMagic...), "image"...)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)

	root := t.TempDir()
	if _, _, _, ok := findFavicon(root); ok {
		t.Fatal("found a favicon in an empty repo")
	}
	writeFile(t, filepath.Join(root, "node_modules", "pkg", "favicon.svg"), svg)
	writeFile(t, filepath.Join(root, ".cache", "favicon.svg"), svg)
	writeFile(t, filepath.Join(root, "a", "b", "c", "d", "e", "favicon.svg"), svg) // too deep
	writeFile(t, filepath.Join(root, "public", "favicon.ico"), ico(png))
	name, typ, data, ok := findFavicon(root)
	if !ok || name != "favicon.png" || typ != "image/png" || !bytes.Equal(data, png) {
		t.Fatalf("from the .ico: %v %q %q %q", ok, name, typ, data)
	}
	writeFile(t, filepath.Join(root, "public", "favicon-16x16.png"), png)
	writeFile(t, filepath.Join(root, "public", "favicon-32x32.png"), append(png, "bigger"...))
	if _, _, data, _ := findFavicon(root); !bytes.HasSuffix(data, []byte("bigger")) {
		t.Fatalf("want the bigger png, got %q", data)
	}
	writeFile(t, filepath.Join(root, "web", "static", "favicon.svg"), svg)
	writeFile(t, filepath.Join(root, "docs", "favicon.svg"), []byte("not an svg"))
	if name, typ, data, _ := findFavicon(root); name != "favicon.svg" || typ != "image/svg+xml" || !bytes.Equal(data, svg) {
		t.Fatalf("want the svg, got %q %q %q", name, typ, data)
	}
}

// An existing clone with a favicon gets it as its icon; one without has none.
func TestAddExistingCloneTakesFavicon(t *testing.T) {
	parallel(t)

	s := newProjectServer(t)
	s.UploadRoot = t.TempDir()
	org := filepath.Join(s.ProjectRoot, "default")
	gitClone(t, filepath.Join(org, "site"), "main")
	gitClone(t, filepath.Join(org, "plain"), "main")
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	writeFile(t, filepath.Join(org, "site", "web", "static", "favicon.svg"), svg)

	add := func(dir string) model.Project {
		w := createProjectReq(t, s, `{"existing":true,"path":`+jsonString(filepath.Join(org, dir))+`}`)
		var p model.Project
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &p) != nil {
			t.Fatalf("add %s: %d %s", dir, w.Code, w.Body)
		}
		return p
	}
	p := add("site")
	if !strings.HasSuffix(p.Icon, "/favicon.svg") {
		t.Fatalf("want the favicon as icon, got %q", p.Icon)
	}
	got, err := raw(s).GetProject(p.ID)
	if err != nil || got.Icon != p.Icon {
		t.Fatalf("stored icon: %q %v", got.Icon, err)
	}
	id, _, _ := strings.Cut(p.Icon, "/")
	if b, err := os.ReadFile(filepath.Join(s.UploadRoot, id, "favicon.svg")); err != nil || !bytes.Equal(b, svg) {
		t.Fatalf("uploaded icon: %q %v", b, err)
	}
	if p := add("plain"); p.Icon != "" {
		t.Fatalf("a repo without a favicon got icon %q", p.Icon)
	}
}
