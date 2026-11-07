package api

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestQRCode(t *testing.T) {
	s := &Server{}
	get := func(text string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.qrCode(w, httptest.NewRequest("GET", "/api/qr?text="+url.QueryEscape(text), nil))
		return w
	}

	w := get("https://example.com/preview?todo=abc")
	if w.Code != http.StatusOK {
		t.Fatalf("valid URL: %d %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type: %q", ct)
	}
	if !bytes.HasPrefix(w.Body.Bytes(), []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("body is not a PNG: % x", w.Body.Bytes()[:min(8, w.Body.Len())])
	}
	img, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != b.Dy() || b.Dx() < 200 || b.Dx() > qrSize {
		t.Fatalf("unexpected image size %v", b)
	}
	// The quiet zone leaves the corner white.
	if r, g, b, _ := img.At(0, 0).RGBA(); r != 0xffff || g != 0xffff || b != 0xffff {
		t.Fatalf("corner pixel is not white: %v", img.At(0, 0))
	}

	for name, text := range map[string]string{
		"no scheme": "example.com/preview",
		"too long":  "https://example.com/" + strings.Repeat("a", 1024),
	} {
		if w := get(text); w.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", name, w.Code)
		}
	}
}
