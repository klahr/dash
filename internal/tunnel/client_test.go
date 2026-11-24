package tunnel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHubJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/echo":
			if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Content-Type") != "application/json" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = io.Copy(w, r.Body)
		case "/none":
			w.WriteHeader(http.StatusNoContent)
		case "/revoked":
			http.Error(w, `{"error":"unknown token"}`, http.StatusUnauthorized)
		case "/refused":
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"name taken"}`)
		case "/big":
			_, _ = io.WriteString(w, `{"a":"`+strings.Repeat("x", 100)+`"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	var out struct{ A string }
	if _, err := HubJSON(ctx, nil, srv.URL, "tok", "POST", "/echo", map[string]string{"a": "b"}, &out, 0); err != nil || out.A != "b" {
		t.Errorf("echo: %+v, %v", out, err)
	}
	out.A = "kept"
	if res, err := HubJSON(ctx, nil, srv.URL, "tok", "POST", "/none", nil, &out, 0); err != nil || res.StatusCode != 204 || out.A != "kept" {
		t.Errorf("204: %+v, %v", out, err)
	}
	if _, err := HubJSON(ctx, nil, srv.URL, "tok", "GET", "/revoked", nil, &out, 0); !errors.Is(err, ErrRevoked) {
		t.Errorf("401: %v", err)
	}
	var he *HubError
	if _, err := HubJSON(ctx, nil, srv.URL, "", "GET", "/refused", nil, &out, 0); !errors.As(err, &he) || he.Status != 409 || err.Error() != "name taken" {
		t.Errorf("409: %v", err)
	}
	if _, err := HubJSON(ctx, nil, srv.URL, "", "GET", "/missing", nil, &out, 0); !errors.As(err, &he) || he.Status != 404 {
		t.Errorf("404: %v", err)
	}
	if _, err := HubJSON(ctx, nil, srv.URL, "", "GET", "/big", nil, &out, 50); err == nil {
		t.Error("answer over the limit decoded")
	}
	if _, err := HubJSON(ctx, nil, "http://dash.example.com", "", "GET", "/", nil, nil, 0); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("plain http: %v", err)
	}
}
