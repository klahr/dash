package api

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/klahr/dash/internal/buildinfo"
	"github.com/klahr/dash/internal/metrics"
	"github.com/klahr/dash/internal/model"
)

// Operations endpoints, for Docker, load balancers and Prometheus:
//
//   - /healthz: 200 when the database answers and has every migration
//     applied, else 503. No sign-in, on every host.
//   - /metrics: Prometheus metrics. On the dashboard's listener only with
//     MetricsToken, as a bearer token; also on a separate listener of its
//     own (OpsHandler, -metrics-addr).

// HealthTimeout is how long /healthz waits for the database.
const HealthTimeout = 3 * time.Second

// schemaVersioner is the store's side of /healthz.
type schemaVersioner interface {
	SchemaVersion(ctx context.Context) (have, want int, err error)
}

// health is /healthz's answer.
type health struct {
	Status  string            `json:"status"` // "ok" or "unavailable"
	Version string            `json:"version"`
	Checks  map[string]string `json:"checks"` // check → "ok" or what's wrong
}

// healthz answers whether this Dash can serve: its database answers and
// its schema is current. What's wrong is only named, not detailed (anyone
// may ask); the log has the error.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	h := health{Status: "ok", Version: buildinfo.Version, Checks: map[string]string{}}
	fail := func(check, why string, err error) {
		h.Status, h.Checks[check] = "unavailable", why
		slog.Warn("health check failed", "check", check, "err", err)
	}
	if sv, ok := s.Store.(schemaVersioner); ok {
		ctx, cancel := context.WithTimeout(r.Context(), HealthTimeout)
		defer cancel()
		have, want, err := sv.SchemaVersion(ctx)
		switch {
		case err != nil:
			fail("database", "unreachable", err)
		case have != want:
			h.Checks["database"] = "ok"
			fail("migrations", "pending", errSchema{have, want})
		default:
			h.Checks["database"], h.Checks["migrations"] = "ok", "ok"
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	code := http.StatusOK
	if h.Status != "ok" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, h)
}

type errSchema struct{ have, want int }

func (e errSchema) Error() string {
	return fmt.Sprintf("database schema is version %d, this Dash needs %d", e.have, e.want)
}

// metricsAuth serves the metrics to callers with token as their bearer
// token (any caller when token is empty).
func metricsAuth(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
				http.Error(w, "metrics need the metrics token", http.StatusUnauthorized)
				return
			}
		}
		metrics.Default.ServeHTTP(w, r)
	})
}

// opsMux serves /healthz, and /metrics when metricsOn, behind token.
func (s *Server) opsMux(metricsOn bool, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	if metricsOn {
		mux.Handle("GET /metrics", metricsAuth(token))
	}
	return metrics.Route(mux)
}

// ops answers /healthz, and /metrics when MetricsToken is set, before
// sign-in and org subdomains are considered; everything else goes to next.
func (s *Server) ops(next http.Handler) http.Handler {
	ops := s.opsMux(s.MetricsToken != "", s.MetricsToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/metrics" && s.MetricsToken != "" {
			ops.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// OpsHandler is the separate operations listener's handler (-metrics-addr):
// /healthz, and /metrics behind token when it isn't empty.
func (s *Server) OpsHandler(token string) http.Handler {
	return metrics.Instrument(s.opsMux(true, token))
}

// RegisterMetrics adds this Dash's gauges to r: what it is, its runners,
// tasks and merge queue. Call it once, before serving /metrics.
func (s *Server) RegisterMetrics(r *metrics.Registry) {
	started := float64(time.Now().Unix())
	r.Gauge("dash_build_info", "Always 1, labelled with the running Dash's version.", func() []metrics.Sample {
		return []metrics.Sample{{Labels: []string{buildinfo.Version}, Value: 1}}
	}, "version")
	r.GaugeValue("dash_start_time_seconds", "When this Dash started, in seconds since the Unix epoch.",
		func() float64 { return started })
	r.GaugeValue("dash_goroutines", "Goroutines running now.", func() float64 { return float64(runtime.NumGoroutine()) })
	runners := func(connected bool) func() []metrics.Sample {
		return func() []metrics.Sample {
			rh := s.fleet()
			if rh == nil {
				return nil // not a hub
			}
			reg, conn := rh.Counts()
			if connected {
				return []metrics.Sample{{Value: float64(conn)}}
			}
			return []metrics.Sample{{Value: float64(reg)}}
		}
	}
	r.Gauge("dash_runners_registered", "Runners registered with this hub.", runners(false))
	r.Gauge("dash_runners_connected", "Runners connected to this hub now.", runners(true))
	if tc, ok := s.Store.(interface {
		TodoCounts() map[model.TodoStatus]int
	}); ok {
		r.Gauge("dash_tasks", "Tasks in each status now (queued, in_progress = running, failed, …), in every org.", func() []metrics.Sample {
			n := tc.TodoCounts()
			out := make([]metrics.Sample, 0, len(taskStatuses))
			for _, st := range taskStatuses {
				out = append(out, metrics.Sample{Labels: []string{string(st)}, Value: float64(n[st])})
			}
			return out
		}, "status")
	}
	r.Gauge("dash_merge_queue_length", "Approved tasks waiting in or being landed by the merge queue.", func() []metrics.Sample {
		if s.Merges == nil {
			return nil
		}
		return []metrics.Sample{{Value: float64(s.Merges.Pending())}}
	})
}

// taskStatuses are the statuses dash_tasks always has a series for.
var taskStatuses = []model.TodoStatus{
	model.StatusDraft, model.StatusQueued, model.StatusInProgress, model.StatusReview,
	model.StatusMerging, model.StatusMerged, model.StatusFailed,
}
