package httpjson

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestError(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, http.StatusTeapot, "no coffee")
	if rec.Code != http.StatusTeapot {
		t.Fatalf("code = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if got, want := rec.Body.String(), "{\"error\":\"no coffee\"}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}
