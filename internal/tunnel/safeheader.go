package tunnel

import (
	"mime"
	"net/http"
	"strings"
)

// A runner is a computer the hub doesn't trust: anyone who may register one
// can run a modified dash there. What it answers is served on the hub's
// origin, where a page that runs script would act with the viewer's
// session. So the hub never lets a runner choose how its answer is
// rendered: SafeRunnerHeader keeps only the content types the dashboard
// reads from runners (JSON, plain text such as logs and diffs, event
// streams, raster images such as uploads) and makes anything else a
// download, and it always forbids sniffing and script.

// RunnerCSP is the Content-Security-Policy of every answer from a runner.
const RunnerCSP = "default-src 'none'; sandbox"

// safeRunnerTypes are the media types a runner's answer may keep.
var safeRunnerTypes = map[string]bool{
	"application/json":  true,
	"text/plain":        true,
	"text/event-stream": true,
	"image/png":         true,
	"image/jpeg":        true,
	"image/gif":         true,
	"image/webp":        true,
}

// SafeRunnerHeader makes h, the header of a runner's answer, safe to serve
// on the hub's origin (see above); code is its status.
func SafeRunnerHeader(h http.Header, code int) {
	h.Del("Set-Cookie")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", RunnerCSP)
	h.Del("Content-Security-Policy-Report-Only")
	if code < 200 || code == http.StatusNoContent || code == http.StatusNotModified {
		return // no body (e.g. a socket's 101)
	}
	ct := h.Get("Content-Type")
	mt, _, err := mime.ParseMediaType(ct)
	mt = strings.ToLower(mt)
	switch {
	case ct == "":
		// No type: nosniff keeps the browser from guessing one, but make it
		// a download all the same.
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", "attachment")
	case err == nil && safeRunnerTypes[mt]:
		if strings.HasPrefix(mt, "text/") || mt == "application/json" {
			h.Set("Content-Type", mt+"; charset=utf-8")
		} else {
			h.Set("Content-Type", mt)
		}
		if d := h.Get("Content-Disposition"); d != "" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(d)), "attachment") {
			h.Del("Content-Disposition")
		}
	default:
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", "attachment")
	}
}
