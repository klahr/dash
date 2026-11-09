package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWrite(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("x_total", "Things.", "kind")
	c.Inc("a")
	c.Add(2, `q"uote`)
	r.Counter("y_total", "Unlabelled.")
	h := r.Histogram("d_seconds", "Durations.", []float64{0.1, 1}, "route")
	h.Observe(0.05, "/a")
	h.Observe(0.5, "/a")
	h.Observe(5, "/a")
	r.GaugeValue("g", "A gauge.", func() float64 { return 3 })
	r.Gauge("skipped", "Nothing.", func() []Sample { return nil })

	var b strings.Builder
	r.Write(&b)
	want := `# HELP x_total Things.
# TYPE x_total counter
x_total{kind="a"} 1
x_total{kind="q\"uote"} 2
# HELP y_total Unlabelled.
# TYPE y_total counter
y_total 0
# HELP d_seconds Durations.
# TYPE d_seconds histogram
d_seconds_bucket{route="/a",le="0.1"} 1
d_seconds_bucket{route="/a",le="1"} 2
d_seconds_bucket{route="/a",le="+Inf"} 3
d_seconds_sum{route="/a"} 5.55
d_seconds_count{route="/a"} 3
# HELP g A gauge.
# TYPE g gauge
g 3
`
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}

func TestInstrument(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /things/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := Instrument(Route(mux))
	before := HTTPRequests.Value("GET", "/things/{id}", "418")
	otherBefore := HTTPRequests.Value("OTHER", "other", "404")
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/things/1", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/things/2", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("BREW", "/nowhere", nil))
	if got := HTTPRequests.Value("GET", "/things/{id}", "418"); got != before+2 {
		t.Errorf("by pattern: %v, want %v", got, before+2)
	}
	if got := HTTPRequests.Value("OTHER", "other", "404"); got != otherBefore+1 {
		t.Errorf("unmatched, unknown method: %v, want %v", got, otherBefore+1)
	}
}
