package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func connector(t *testing.T, s *Server) (st struct {
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url"`
	Connected int    `json:"connected"`
	Local     string `json:"local"`
}) {
	t.Helper()
	w := httptest.NewRecorder()
	s.connectorStatus(w, httptest.NewRequest("GET", "/api/connector", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return st
}

func TestConnectorStatus(t *testing.T) {
	off, _, _ := boardServer(t)
	off.Addr = ":9000"
	if st := connector(t, off); st.Enabled || st.URL != "" || st.Local != "http://127.0.0.1:9000" {
		t.Errorf("without -public-url: %+v", st)
	}

	h, s, _, _ := remoteServer(t)
	if st := connector(t, s); !st.Enabled || st.URL != publicURL+"/mcp" || st.Connected != 0 {
		t.Errorf("before sign-in: %+v", st)
	}
	oauthToken(t, h)
	oauthToken(t, h)
	if st := connector(t, s); st.Connected != 2 {
		t.Errorf("after two sign-ins: %+v", st)
	}
}

func TestLocalURL(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1:7788": "http://127.0.0.1:7788",
		"0.0.0.0:80":     "http://127.0.0.1:80",
		"[::1]:7788":     "http://[::1]:7788",
		"":               "http://127.0.0.1:7788",
	} {
		if got := localURL(addr); got != want {
			t.Errorf("localURL(%q) = %q, want %q", addr, got, want)
		}
	}
}
