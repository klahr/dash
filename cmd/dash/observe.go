package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// newLogger is Dash's logger: slog's text handler (key=value, for people)
// or its JSON handler (one object per line, for log shippers: Loki,
// Vector, Datadog, CloudWatch …), at level and above.
func newLogger(w io.Writer, format, level string) (*slog.Logger, error) {
	var lv slog.Level // info
	if strings.TrimSpace(level) == "" {
		level = "info"
	}
	if err := lv.UnmarshalText([]byte(strings.ToUpper(strings.TrimSpace(level)))); err != nil {
		return nil, fmt.Errorf("log level %q: want debug, info, warn or error", level)
	}
	opts := &slog.HandlerOptions{Level: lv}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	}
	return nil, fmt.Errorf("log format %q: want text or json", format)
}

// minMetricsToken is the shortest DASH_METRICS_TOKEN Dash accepts.
const minMetricsToken = 16

// checkMetrics checks the metrics settings: a token, if any, long enough
// to guess at, and a separate listener (-metrics-addr) reachable only from
// this computer unless the token protects it.
func checkMetrics(addr, token string) error {
	if token != "" && len(token) < minMetricsToken {
		return fmt.Errorf("DASH_METRICS_TOKEN is too short: use at least %d characters, e.g. from `openssl rand -hex 24`", minMetricsToken)
	}
	if addr == "" {
		return nil
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("-metrics-addr %q: want host:port, e.g. 127.0.0.1:9090", addr)
	}
	if token == "" && !isLoopback(addr) {
		return errors.New("-metrics-addr " + addr + " is reachable from other computers: listen on 127.0.0.1, or set DASH_METRICS_TOKEN for Prometheus to send as its bearer token")
	}
	return nil
}

// serveOps serves h (/healthz and /metrics) on addr until ctx is done. It
// returns once listening, or why it can't.
func serveOps(ctx context.Context, addr string, h http.Handler) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics listener", "err", err)
		}
	}()
	go func() { <-ctx.Done(); _ = srv.Close() }()
	return nil
}

// maxLogFile is the size at which openLogFile moves a log aside.
const maxLogFile = 10 << 20

// openLogFile opens path for appending the log, first moving a log over
// maxLogFile to path.1 (replacing the one there).
func openLogFile(path string) (*os.File, error) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogFile {
		_ = os.Rename(path, path+".1")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}
