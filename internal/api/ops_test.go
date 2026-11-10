package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/metrics"
	"github.com/klahr/dash/internal/model"
)

func opsGet(h http.Handler, path string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHealthz(t *testing.T) {
	parallel(t)

	s, _, _ := boardServer(t)
	h := s.Routes()
	w := opsGet(h, "/healthz") // no session
	if w.Code != http.StatusOK {
		t.Fatalf("healthz = %d %s", w.Code, w.Body)
	}
	var got health
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" || got.Checks["database"] != "ok" || got.Checks["migrations"] != "ok" {
		t.Errorf("healthz = %+v", got)
	}

	// A closed database is unavailable.
	raw(s).Close()
	if w := opsGet(h, "/healthz"); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"database":"unreachable"`) {
		t.Errorf("closed database: %d %s", w.Code, w.Body)
	}
}

func TestMetricsNeedToken(t *testing.T) {
	parallel(t)

	s, _, _ := boardServer(t)
	// Without a token /metrics isn't served on the dashboard's listener:
	// it's a page behind sign-in like any other.
	if w := opsGet(s.Routes(), "/metrics"); w.Code == http.StatusOK && strings.Contains(w.Body.String(), "# TYPE") {
		t.Fatalf("/metrics served without a token: %s", w.Body)
	}

	s2, _, _ := boardServer(t)
	s2.MetricsToken = "0123456789abcdef0123"
	h := s2.Routes()
	if w := opsGet(h, "/metrics"); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
	if w := opsGet(h, "/metrics", "Authorization", "Bearer wrong"); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", w.Code)
	}
	if w := opsGet(h, "/metrics", "Authorization", "Bearer "+s2.MetricsToken); w.Code != http.StatusOK {
		t.Errorf("right token: %d %s", w.Code, w.Body)
	}
}

func TestOpsMetrics(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw(s).SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := raw(s).SetStatus(td.ID, model.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	failedBefore := metrics.TasksFailed.Value()
	if _, err := raw(s).SetStatus(td.ID, model.StatusFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	if got := metrics.TasksFailed.Value(); got != failedBefore+1 {
		t.Errorf("dash_tasks_failed_total %v → %v", failedBefore, got)
	}
	reg := metrics.NewRegistry()
	s.RegisterMetrics(reg)
	var b strings.Builder
	reg.Write(&b)
	for _, want := range []string{
		`dash_tasks{status="queued"} 0`,
		`dash_tasks{status="failed"} 1`,
		`dash_build_info{version=`,
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("metrics lack %q:\n%s", want, b.String())
		}
	}
	// Not a hub, no merge queue: no runner or merge queue series.
	if strings.Contains(b.String(), "dash_runners_connected") {
		t.Errorf("runner gauges without runners:\n%s", b.String())
	}

	// The separate listener serves both, the metrics behind its token, and
	// counts requests by their route.
	h := s.OpsHandler("")
	if w := opsGet(h, "/healthz"); w.Code != http.StatusOK {
		t.Errorf("ops healthz: %d", w.Code)
	}
	before := metrics.HTTPRequests.Value("GET", "/healthz", "200")
	opsGet(h, "/healthz")
	if got := metrics.HTTPRequests.Value("GET", "/healthz", "200"); got != before+1 {
		t.Errorf("healthz requests counted %v → %v", before, got)
	}
	if w := opsGet(h, "/metrics"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "dash_http_requests_total") {
		t.Errorf("ops metrics: %d %s", w.Code, w.Body)
	}
	if w := opsGet(s.OpsHandler("0123456789abcdef0123"), "/metrics"); w.Code != http.StatusUnauthorized {
		t.Errorf("ops metrics without its token: %d", w.Code)
	}
}
