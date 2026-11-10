package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/store"
)

// Routes panics at startup if two patterns conflict; build it here.
func TestRoutesRegister(t *testing.T) {
	parallel(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{Hub: hub.New(), Auth: noFailDelay(auth.New(st.Accounts())), Perms: perm.NewBroker(nil), Static: fstest.MapFS{"index.html": {Data: []byte("ok")}}}
	h := s.Routes()

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/projects", http.StatusUnauthorized},     // needs a session
		{"POST", "/mcp/permissions", http.StatusUnauthorized}, // needs a run token, not a session
		{"GET", "/mcp/permissions", http.StatusMethodNotAllowed},
		{"POST", "/mcp/dash", http.StatusUnauthorized},
		{"GET", "/mcp/dash", http.StatusMethodNotAllowed},
		{"GET", "/login.html", http.StatusNotFound}, // public, just missing from this test FS
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.want {
			t.Errorf("%s %s: want %d, got %d", tc.method, tc.path, tc.want, w.Code)
		}
	}
}

func TestUploadRoundTrip(t *testing.T) {
	parallel(t)

	s, _, _ := boardServer(t)
	s.UploadRoot = t.TempDir()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/uploads?name=../../Screen%20Shot.png", strings.NewReader("\x89PNG fake"))
	s.upload(w, asOwner(t, s, r))
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	var a model.Attachment
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Name != "Screen-Shot.png" || a.Type != "image/png" || filepath.Dir(filepath.Dir(a.Path)) != s.UploadRoot {
		t.Fatalf("unexpected attachment %+v", a)
	}
	sv := s.as(ownerCaller(t, s))
	got, err := sv.attachments([]string{a.ID})
	if err != nil || len(got) != 1 || got[0].Path != a.Path {
		t.Fatalf("resolve: %v %+v", err, got)
	}
	if _, err := sv.attachments([]string{"../etc"}); err == nil {
		t.Fatal("want bad ids rejected")
	}

	w = httptest.NewRecorder()
	s.upload(w, asOwner(t, s, httptest.NewRequest("POST", "/api/uploads?name=run.sh", strings.NewReader("echo"))))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want scripts rejected, got %d", w.Code)
	}
	if n := cleanUploadName("", "image/png"); n != "pasted.png" {
		t.Fatalf("pasted image name: %q", n)
	}
	if n := cleanUploadName("", "image/svg+xml"); n != "pasted.svg" {
		t.Fatalf("pasted svg name: %q", n)
	}
	if n := cleanUploadName(strings.Repeat("a", 100)+".png", ""); len(n) != 80 || !strings.HasSuffix(n, ".png") {
		t.Fatalf("long name keeps extension: %q", n)
	}
	w = httptest.NewRecorder()
	s.upload(w, asOwner(t, s, httptest.NewRequest("POST", "/api/uploads?name=a."+strings.Repeat("b", 90), strings.NewReader("x"))))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want long extension rejected, got %d", w.Code)
	}
}
