package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// The hub's PUT /api/hub/uploads/{id} on a runner takes only an id of its
// own shape (never a path out of the upload root) and a file of a type Dash
// takes, at most maxUpload big. The same id sent again with the same name
// changes nothing; with another name, it is refused.
func TestHubUploadRefusesBadIDsTypesConflictsAndBigFiles(t *testing.T) {
	parallel(t)
	s, h, _ := runnerSide(t, 7)
	parent := t.TempDir()
	s.UploadRoot = filepath.Join(parent, "uploads")
	if err := os.Mkdir(s.UploadRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	put := func(id, name, body string) (int, string) {
		w := send(h, "PUT", "/api/hub/uploads/"+id+"?name="+name, body)
		return w.Code, w.Body.String()
	}
	entries := func(dir string) []string {
		t.Helper()
		es, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range es {
			names = append(names, e.Name())
		}
		return names
	}

	// Ids that aren't the hub's shape: never a directory, inside the root or out.
	for _, id := range []string{"..%2Fx", "..%2F..%2Fx", "%2Ftmp%2Fx", "abc", "ABCDEF012345", "0123456789ab0", "0123456789a."} {
		if code, body := put(id, "a.png", "png"); code != http.StatusBadRequest || !strings.Contains(body, "not an attachment") {
			t.Errorf("id %q: %d %s", id, code, body)
		}
	}
	// Types Dash doesn't take.
	for _, name := range []string{"x.exe", "x.sh", "x.html", ""} {
		if code, body := put("0123456789ab", name, "MZ"); code != http.StatusBadRequest || !strings.Contains(body, "not an attachment") {
			t.Errorf("name %q: %d %s", name, code, body)
		}
	}
	if got := entries(s.UploadRoot); len(got) != 0 {
		t.Fatalf("refused uploads left files in the upload root: %v", got)
	}

	// A good one, then the same again: 200, the first file kept as it was.
	const id = "0123456789ab"
	code, body := put(id, "drawing.png", "first")
	var first model.Attachment
	if code != http.StatusCreated || json.Unmarshal([]byte(body), &first) != nil || first.ID != id || first.Name != "drawing.png" {
		t.Fatalf("upload: %d %s", code, body)
	}
	code, body = put(id, "drawing.png", "second, ignored")
	var again model.Attachment
	if code != http.StatusOK || json.Unmarshal([]byte(body), &again) != nil || again.Path != first.Path || again.Size != first.Size {
		t.Fatalf("same upload again: %d %s", code, body)
	}
	if b, err := os.ReadFile(first.Path); err != nil || string(b) != "first" {
		t.Fatalf("resend changed the file: %q %v", b, err)
	}
	if got := entries(filepath.Join(s.UploadRoot, id)); len(got) != 2 { // the file and its org
		t.Fatalf("files after a resend: %v", got)
	}

	// The same id with another name: a conflict, nothing written.
	if code, body := put(id, "other.png", "x"); code != http.StatusConflict || !strings.Contains(body, "another file has the id") {
		t.Errorf("same id, another name: %d %s", code, body)
	}
	if got := entries(filepath.Join(s.UploadRoot, id)); len(got) != 2 {
		t.Fatalf("files after a conflict: %v", got)
	}

	// Over maxUpload: 400, and no directory left behind.
	big := strings.Repeat("x", maxUpload+1)
	if code, body := put("abcdef012345", "big.png", big); code != http.StatusBadRequest || !strings.Contains(body, "at most 20 MB") {
		t.Errorf("too big: %d %.200s", code, body)
	}
	if _, err := os.Stat(filepath.Join(s.UploadRoot, "abcdef012345")); !os.IsNotExist(err) {
		t.Errorf("a refused big upload left its directory: %v", err)
	}

	// Nothing anywhere but the upload root, nor in it but the one upload.
	if got := entries(parent); len(got) != 1 || got[0] != "uploads" {
		t.Errorf("written outside the upload root: %v", got)
	}
	if got := entries(s.UploadRoot); len(got) != 1 || got[0] != id {
		t.Errorf("upload root: %v", got)
	}
}
