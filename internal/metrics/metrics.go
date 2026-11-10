// Package metrics keeps Dash's Prometheus metrics and writes them in the
// Prometheus text exposition format (version 0.0.4), without the client
// library and its dependencies.
//
// Counters and histograms are updated where things happen (sign-in
// lockouts, webhook deliveries, HTTP requests); gauges are read when
// /metrics is scraped, from functions registered with Registry.Gauge.
package metrics

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Default is the registry Dash's packages count into and /metrics serves.
var Default = NewRegistry()

// Dash's own counters, in Default.
var (
	// SignInLockouts counts lockouts as they start, by scope: "ip" (one
	// address), "global" (all addresses) or "2fa" (one user's codes).
	SignInLockouts = Default.Counter("dash_signin_lockouts_total",
		"Sign-in lockouts started, by scope (ip, global, 2fa).", "scope")
	// WebhookDeliveries counts notification webhook deliveries, after
	// retries, by result: "ok" or "failed".
	WebhookDeliveries = Default.Counter("dash_webhook_deliveries_total",
		"Outgoing notification webhook deliveries after retries, by result (ok, failed).", "result")
	// PushDeliveries counts Web Push deliveries to browsers, after
	// retries, by result: "ok", "failed" or "gone" (the browser dropped
	// the subscription, which is then deleted).
	PushDeliveries = Default.Counter("dash_push_deliveries_total",
		"Web Push deliveries to browsers after retries, by result (ok, failed, gone).", "result")
	// TasksFailed counts tasks moving to failed.
	TasksFailed = Default.Counter("dash_tasks_failed_total",
		"Tasks that have failed since the hub started.")
	// HTTPRequests and HTTPDuration are kept by Instrument.
	HTTPRequests = Default.Counter("dash_http_requests_total",
		"HTTP requests answered, by method, route pattern and status code.", "method", "route", "code")
	HTTPDuration = Default.Histogram("dash_http_request_duration_seconds",
		"Time to answer HTTP requests, by method and route pattern.",
		[]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}, "method", "route")
)

// Sample is one value of a gauge, with its label values in the order the
// gauge's labels were given.
type Sample struct {
	Labels []string
	Value  float64
}

// Registry holds metrics, written in the order they were registered.
type Registry struct {
	mu      sync.Mutex
	metrics []metric
	names   map[string]bool
}

type metric interface {
	write(w io.Writer)
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{names: map[string]bool{}} }

func (r *Registry) add(name string, m metric) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names[name] {
		panic("metrics: " + name + " registered twice")
	}
	r.names[name] = true
	r.metrics = append(r.metrics, m)
}

// Counter registers a counter with these label names.
func (r *Registry) Counter(name, help string, labels ...string) *Counter {
	c := &Counter{name: name, help: help, labels: labels, vals: map[string]*counterVal{}}
	r.add(name, c)
	return c
}

// Histogram registers a histogram with these upper bounds (ascending) and
// label names.
func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) *Histogram {
	h := &Histogram{name: name, help: help, labels: labels, buckets: buckets, vals: map[string]*histVal{}}
	r.add(name, h)
	return h
}

// Gauge registers a gauge whose samples fn returns at each scrape. fn must
// be safe to call from any goroutine; nil samples write nothing.
func (r *Registry) Gauge(name, help string, fn func() []Sample, labels ...string) {
	r.add(name, &gauge{name: name, help: help, labels: labels, fn: fn})
}

// GaugeValue registers a gauge without labels whose value fn returns.
func (r *Registry) GaugeValue(name, help string, fn func() float64) {
	r.Gauge(name, help, func() []Sample { return []Sample{{Value: fn()}} })
}

// Write writes every metric in the text exposition format.
func (r *Registry) Write(w io.Writer) {
	r.mu.Lock()
	ms := slices.Clone(r.metrics)
	r.mu.Unlock()
	for _, m := range ms {
		m.write(w)
	}
}

// ContentType is the text exposition format's.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// ServeHTTP writes the registry's metrics.
func (r *Registry) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", ContentType)
	w.Header().Set("Cache-Control", "no-store")
	r.Write(w)
}

// ---- counter ----

// Counter is a monotonically increasing count, per combination of label
// values.
type Counter struct {
	name, help string
	labels     []string
	mu         sync.Mutex
	vals       map[string]*counterVal
}

type counterVal struct {
	labels []string
	n      float64
}

// Inc adds one for these label values (as many as the counter's labels).
func (c *Counter) Inc(values ...string) { c.Add(1, values...) }

// Add adds n (≥ 0) for these label values.
func (c *Counter) Add(n float64, values ...string) {
	if len(values) != len(c.labels) {
		panic(fmt.Sprintf("metrics: %s takes %d label values, got %d", c.name, len(c.labels), len(values)))
	}
	k := strings.Join(values, "\xff")
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.vals[k]
	if v == nil {
		v = &counterVal{labels: slices.Clone(values)}
		c.vals[k] = v
	}
	v.n += n
}

// Value is the count for these label values (tests).
func (c *Counter) Value(values ...string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v := c.vals[strings.Join(values, "\xff")]; v != nil {
		return v.n
	}
	return 0
}

func (c *Counter) write(w io.Writer) {
	header(w, c.name, c.help, "counter")
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.labels) == 0 && len(c.vals) == 0 {
		fmt.Fprintf(w, "%s 0\n", c.name) // a counter without labels starts at 0
		return
	}
	for _, k := range sortedKeys(c.vals) {
		v := c.vals[k]
		fmt.Fprintf(w, "%s%s %s\n", c.name, labelSet(c.labels, v.labels), num(v.n))
	}
}

// ---- histogram ----

// Histogram counts observations into buckets, per combination of label
// values.
type Histogram struct {
	name, help string
	labels     []string
	buckets    []float64
	mu         sync.Mutex
	vals       map[string]*histVal
}

type histVal struct {
	labels []string
	counts []uint64 // per bucket, not cumulative
	count  uint64
	sum    float64
}

// Observe records v for these label values.
func (h *Histogram) Observe(v float64, values ...string) {
	if len(values) != len(h.labels) {
		panic(fmt.Sprintf("metrics: %s takes %d label values, got %d", h.name, len(h.labels), len(values)))
	}
	k := strings.Join(values, "\xff")
	h.mu.Lock()
	defer h.mu.Unlock()
	hv := h.vals[k]
	if hv == nil {
		hv = &histVal{labels: slices.Clone(values), counts: make([]uint64, len(h.buckets))}
		h.vals[k] = hv
	}
	if i := sort.SearchFloat64s(h.buckets, v); i < len(h.buckets) {
		hv.counts[i]++
	}
	hv.count++
	hv.sum += v
}

func (h *Histogram) write(w io.Writer) {
	header(w, h.name, h.help, "histogram")
	h.mu.Lock()
	defer h.mu.Unlock()
	names := append(slices.Clone(h.labels), "le")
	for _, k := range sortedKeys(h.vals) {
		hv := h.vals[k]
		var cum uint64
		for i, b := range h.buckets {
			cum += hv.counts[i]
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, labelSet(names, append(slices.Clone(hv.labels), num(b))), cum)
		}
		fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, labelSet(names, append(slices.Clone(hv.labels), "+Inf")), hv.count)
		ls := labelSet(h.labels, hv.labels)
		fmt.Fprintf(w, "%s_sum%s %s\n", h.name, ls, num(hv.sum))
		fmt.Fprintf(w, "%s_count%s %d\n", h.name, ls, hv.count)
	}
}

// ---- gauge ----

type gauge struct {
	name, help string
	labels     []string
	fn         func() []Sample
}

func (g *gauge) write(w io.Writer) {
	samples := g.fn()
	if samples == nil {
		return
	}
	header(w, g.name, g.help, "gauge")
	for _, s := range samples {
		fmt.Fprintf(w, "%s%s %s\n", g.name, labelSet(g.labels, s.Labels), num(s.Value))
	}
}

// ---- format ----

func header(w io.Writer, name, help, typ string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, helpEscaper.Replace(help), name, typ)
}

var (
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	valueEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
)

func labelSet(names, values []string) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		v := ""
		if i < len(values) {
			v = values[i]
		}
		b.WriteString(n)
		b.WriteString(`="`)
		b.WriteString(valueEscaper.Replace(v))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func num(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "+Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	case math.IsNaN(f):
		return "NaN"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
