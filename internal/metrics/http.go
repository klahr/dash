package metrics

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type routeKey struct{}

// Instrument counts and times the requests next answers, into
// HTTPRequests and HTTPDuration. The route label is the ServeMux pattern
// that served the request (recorded by Route), so the label's values stay
// few: "other" when no pattern did (static files, redirects to sign in,
// refused requests). Hijacked connections (WebSockets, runner tunnels) are
// counted when they are upgraded, not timed.
func Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := new(string)
		r = r.WithContext(context.WithValue(r.Context(), routeKey{}, route))
		start := time.Now()
		rec := &recorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		label := *route
		if label == "" {
			label = "other"
		}
		m := method(r.Method)
		HTTPRequests.Inc(m, label, strconv.Itoa(rec.code))
		if !rec.hijacked {
			HTTPDuration.Observe(time.Since(start).Seconds(), m, label)
		}
	})
}

// method is r's method as a label: anything but the standard ones is
// "OTHER", so made-up methods can't add series.
func method(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions:
		return m
	}
	return "OTHER"
}

// Route records, for Instrument, the pattern of mux that serves each
// request. Wrap the innermost ServeMux with it.
func Route(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r) // sets r.Pattern on r itself
		if p, ok := r.Context().Value(routeKey{}).(*string); ok && r.Pattern != "" {
			// "GET /api/todos/{tid}" → "/api/todos/{tid}": the method is a
			// label of its own.
			_, path, found := strings.Cut(r.Pattern, " ")
			if !found {
				path = r.Pattern
			}
			*p = path
		}
	})
}

type recorder struct {
	http.ResponseWriter
	code     int
	wrote    bool
	hijacked bool
}

func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.code, r.wrote = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer (flushing,
// deadlines, hijacking).
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Hijack passes on to the real writer (WebSocket upgraders that assert
// http.Hijacker); the request counts as switching protocols.
func (r *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := http.NewResponseController(r.ResponseWriter).Hijack()
	if err == nil {
		r.hijacked, r.wrote, r.code = true, true, http.StatusSwitchingProtocols
	}
	return c, rw, err
}

// Flush passes on to the real writer (server-sent events, streamed logs).
func (r *recorder) Flush() {
	r.wrote = true
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}
