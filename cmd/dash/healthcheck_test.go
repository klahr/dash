package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckHealth(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"dev"}`))
	}))
	defer ok.Close()
	if err := checkHealth(context.Background(), ok.URL+"/runner/version"); err != nil {
		t.Errorf("healthy hub: %v", err)
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer broken.Close()
	if err := checkHealth(context.Background(), broken.URL); err == nil {
		t.Error("a 502 counted as healthy")
	}

	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	if err := checkHealth(context.Background(), gone.URL); err == nil {
		t.Error("a closed port counted as healthy")
	}
}
