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
	parallel(t)

	off, _, _ := boardServer(t)
	if st := connector(t, off); st.Enabled || st.URL != "" {
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
