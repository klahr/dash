package mcptools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInitializeNegotiatesVersion(t *testing.T) {
	s := &Server{Name: "t"}
	for asked, want := range map[string]string{
		"2025-06-18": "2025-06-18",
		"2024-11-05": "2024-11-05",
		"2099-01-01": ProtocolVersions[0], // unknown: answer ours, don't echo
		"":           ProtocolVersions[0],
	} {
		res, rerr := s.Handle(Request{Method: "initialize", Params: []byte(`{"protocolVersion":"` + asked + `"}`)})
		if rerr != nil {
			t.Fatal(rerr)
		}
		if got := res.(map[string]any)["protocolVersion"]; got != want {
			t.Errorf("asked %q: got %v, want %s", asked, got, want)
		}
	}
}

func TestHTTPRejectsUnsupportedVersionHeader(t *testing.T) {
	s := &Server{Name: "t"}
	for v, want := range map[string]int{"": http.StatusOK, "2025-06-18": http.StatusOK, "1999-01-01": http.StatusBadRequest} {
		r := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		if v != "" {
			r.Header.Set("MCP-Protocol-Version", v)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("version %q: %d", v, w.Code)
		}
	}
}
