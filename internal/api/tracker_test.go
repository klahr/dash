package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tracker"
	"github.com/klahr/dash/internal/tracker/trackertest"
)

func TestTrackerAPI(t *testing.T) {
	fake := trackertest.Register(t, "fake")
	fake.RequireToken("b1", "tok-secret-1")
	s, _, p := boardServer(t)
	m := &tracker.Manager{Store: raw(s), PollEvery: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); m.StopAll() })
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s.Trackers = m
	h := s.mux()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if body != "" {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, asOwner(t, s, r))
		return w
	}
	base := "/api/projects/" + p.ID + "/tracker"
	draft, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "existing"})
	if err != nil {
		t.Fatal(err)
	}

	w := call("GET", base, "")
	var st struct {
		Config   model.TrackerConfig    `json:"config"`
		Backends []tracker.Registration `json:"backends"`
		Running  bool                   `json:"running"`
		Unlinked int                    `json:"unlinked"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &st) != nil || !st.Config.Native() || len(st.Backends) < 2 || st.Backends[0].Name != "dash" || st.Unlinked != 1 {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}

	// Test connection: with the wrong token, then the right one.
	if w := call("POST", base+"/test", `{"backend":"fake","config":{"board":"b1"},"credentials":{"token":"nope"}}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Fatalf("test, wrong token: %d %s", w.Code, w.Body)
	}
	if w := call("POST", base+"/test", `{"backend":"fake","config":{"board":"b1"},"credentials":{"token":"tok-secret-1"}}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("test: %d %s", w.Code, w.Body)
	}

	// Switching from Dash's board needs the export decision.
	body := `{"backend":"fake","config":{"board":"b1"},"credentials":{"token":"tok-secret-1"}}`
	w = call("PUT", base, body)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"needs":"export"`) || !strings.Contains(w.Body.String(), `"open_todos":1`) {
		t.Fatalf("switch without a decision: %d %s", w.Code, w.Body)
	}
	if m.Running(p.ID) {
		t.Fatal("switched silently")
	}
	w = call("PUT", base, strings.TrimSuffix(body, "}")+`,"export":true}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "tok-secret-1") || strings.Contains(w.Body.String(), "trsec_") {
		t.Fatalf("switch: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.Config.Backend != "fake" || !st.Config.HasCredentials || !st.Running || len(st.Config.StatusMap) == 0 {
		t.Fatalf("after the switch: %s", w.Body)
	}
	if w := call("POST", base+"/sync", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("sync: %d %s", w.Code, w.Body)
	}
	if got, _ := raw(s).GetTodo(draft.ID); got.ExternalRef == nil {
		t.Fatal("the open todo wasn't exported")
	}

	// Editing the mapping keeps the saved credentials (the sync still works).
	w = call("PUT", base, `{"backend":"fake","config":{"board":"b1"},"status_map":{"draft":"Ideas","queued":"Todo"}}`)
	if w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	if w := call("POST", base+"/sync", ""); !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("sync after editing: %s", w.Body)
	}
	if w := call("PUT", base, `{"backend":"fake","config":{"board":"b1"},"status_map":{"bogus":"x"}}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d %s", w.Code, w.Body)
	}

	// The webhook secret is answered once, and the public webhook checks it.
	w = call("POST", base+"/webhook-secret", "")
	var sec struct{ Secret, Path string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &sec) != nil || !strings.HasPrefix(sec.Secret, "trsec_") {
		t.Fatalf("rotate: %d %s", w.Code, w.Body)
	}
	routes := s.Routes()
	hook := func(secret string) int {
		r := httptest.NewRequest("POST", sec.Path, strings.NewReader(`{}`))
		r.Header.Set("X-Dash-Tracker-Secret", secret)
		r.Header.Set("Origin", "https://tracker.example") // not a browser of ours: no session, no CSRF check
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		return w.Code
	}
	if c := hook("wrong"); c != http.StatusUnauthorized {
		t.Fatalf("bad secret: %d", c)
	}
	if c := hook(sec.Secret); c != http.StatusOK {
		t.Fatalf("good secret: %d", c)
	}
	// The rest of the project's API still needs a session.
	r := httptest.NewRequest("GET", base, nil)
	rw := httptest.NewRecorder()
	routes.ServeHTTP(rw, r)
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("tracker settings without a session: %d", rw.Code)
	}

	// Back to Dash: needs confirmation, then unlinks.
	if w := call("PUT", base, `{"backend":"dash"}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"needs":"confirm"`) {
		t.Fatalf("back without confirming: %d %s", w.Code, w.Body)
	}
	if w := call("PUT", base, `{"backend":"dash","confirm":true}`); w.Code != 200 {
		t.Fatalf("back: %d %s", w.Code, w.Body)
	}
	if got, _ := raw(s).GetTodo(draft.ID); got.ExternalRef != nil || m.Running(p.ID) {
		t.Fatal("still linked after going back to Dash")
	}
	w = call("GET", base, "")
	if !strings.Contains(w.Body.String(), "board.switched") {
		t.Fatalf("switches not logged: %s", w.Body)
	}

	if w := call("GET", "/api/projects/nope/tracker", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", w.Code)
	}
}

func TestTrackerAPIOff(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.mux()
	r := asOwner(t, s, httptest.NewRequest("PUT", "/api/projects/"+p.ID+"/tracker", strings.NewReader(`{"backend":"dash"}`)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("without a manager: %d %s", w.Code, w.Body)
	}
	r = asOwner(t, s, httptest.NewRequest("GET", "/api/projects/"+p.ID+"/tracker", nil))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("get without a manager: %d %s", w.Code, w.Body)
	}
}
