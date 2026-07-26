// Package metrics is Ken's tiny, dependency-free Prometheus exposition: a handful
// of atomic counters plus on-scrape gauge collectors, rendered as text/plain
// exposition format. It deliberately does NOT use prometheus/client_golang — that
// would add ~1-2 MB and several transitive deps; Ken values a flat footprint, so
// the counters are plain atomics and the encoder is ~100 lines. Metrics are
// pull-based and in-memory: Ken stores nothing on disk for them.
package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Label is a single name=value pair on a metric series.
type Label struct{ Name, Value string }

// Series is one labelled value within a metric family.
type Series struct {
	Labels []Label
	Value  float64
}

// Family is a metric family: one HELP/TYPE header plus N series. Collectors return
// these for on-scrape gauges.
type Family struct {
	Name, Type, Help string
	Series           []Series
}

// Gauge is a convenience constructor for a single-series unlabelled gauge family.
func Gauge(name, help string, v float64) Family {
	return Family{Name: name, Type: "gauge", Help: help, Series: []Series{{Value: v}}}
}

// Collector produces gauge families at scrape time (e.g. DB row counts). It is
// given the scrape request's context so a slow DB can't wedge the handler.
type Collector func(context.Context) []Family

// Registry holds Ken's counters and gauge collectors. The zero value is not
// usable; call New.
type Registry struct {
	start   time.Time
	version string

	httpReq  *counterVec // labels: surface, outcome
	httpDur  *summaryVec // labels: surface
	mcpCalls *counterVec // labels: tool, outcome
	authFail *counterVec // labels: surface
	rlReject atomic.Int64
	rlBlock  atomic.Int64

	mu         sync.Mutex
	collectors []Collector
}

// New builds an empty registry stamped with the build version.
func New(version string) *Registry {
	return &Registry{
		start:    time.Now(),
		version:  version,
		httpReq:  newCounterVec("surface", "outcome"),
		httpDur:  newSummaryVec("surface"),
		mcpCalls: newCounterVec("tool", "outcome"),
		authFail: newCounterVec("surface"),
	}
}

// AddCollector registers a gauge source evaluated on each scrape.
func (r *Registry) AddCollector(c Collector) {
	r.mu.Lock()
	r.collectors = append(r.collectors, c)
	r.mu.Unlock()
}

// --- increments (hot path: atomic; a series' first use takes a brief lock) ---

// RecordHTTP records one served HTTP request and its duration for a surface.
func (r *Registry) RecordHTTP(surface, outcome string, d time.Duration) {
	r.httpReq.inc(surface, outcome)
	r.httpDur.observe(d, surface)
}

// RecordMCP records one MCP tool call and whether it succeeded.
func (r *Registry) RecordMCP(tool string, ok bool) { r.mcpCalls.inc(tool, outcome(ok)) }

// AuthFailure records a rejected authentication on a surface (e.g. a bad token).
func (r *Registry) AuthFailure(surface string) { r.authFail.inc(surface) }

// RateLimitRejected records a 429 throttle.
func (r *Registry) RateLimitRejected() { r.rlReject.Add(1) }

// RateLimitBlocked records a 403 refusal from an auto-blocked IP.
func (r *Registry) RateLimitBlocked() { r.rlBlock.Add(1) }

func outcome(ok bool) string {
	if ok {
		return "success"
	}
	return "error"
}

// OutcomeClass maps an HTTP status to a coarse, low-cardinality outcome bucket
// (the conventional informational/success/redirection/client_error/server_error).
func OutcomeClass(code int) string {
	switch {
	case code < 200:
		return "informational"
	case code < 300:
		return "success"
	case code < 400:
		return "redirection"
	case code < 500:
		return "client_error"
	default:
		return "server_error"
	}
}

// --- HTTP counting middleware (web surface only; the streaming MCP surface is
// tracked by per-tool counters instead, to avoid SSE skewing request duration) ---

// Counting wraps next, recording each served request under surface. Health and
// metrics paths are skipped so probes/scrapes don't inflate the counters.
func (r *Registry) Counting(surface string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if skipPath(req.URL.Path) {
			next.ServeHTTP(w, req)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, req)
		r.RecordHTTP(surface, OutcomeClass(rec.code), time.Since(start))
	})
}

func skipPath(p string) bool { return p == "/healthz" || p == "/health" || p == "/metrics" }

type statusRecorder struct {
	http.ResponseWriter
	code  int
	wrote bool
}

func (s *statusRecorder) WriteHeader(c int) {
	if !s.wrote {
		s.code, s.wrote = c, true
	}
	s.ResponseWriter.WriteHeader(c)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer's Flush/etc.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// --- exposition ---

// WriteText renders the current metrics in Prometheus text exposition format.
func (r *Registry) WriteText(ctx context.Context, w io.Writer) {
	var b strings.Builder
	writeCounter(&b, "ken_http_requests_total", "HTTP requests by surface and outcome.", r.httpReq.snapshot())
	writeSummary(&b, "ken_http_request_duration_seconds", "HTTP request duration by surface.", r.httpDur.snapshot())
	writeCounter(&b, "ken_mcp_tool_calls_total", "MCP tool calls by tool and outcome.", r.mcpCalls.snapshot())
	writeCounter(&b, "ken_auth_failures_total", "Authentication failures by surface.", r.authFail.snapshot())
	writeFamily(&b, Family{Name: "ken_ratelimit_rejected_total", Type: "counter", Help: "Requests throttled (429) by the rate limiter.", Series: []Series{{Value: float64(r.rlReject.Load())}}})
	writeFamily(&b, Family{Name: "ken_ratelimit_blocked_total", Type: "counter", Help: "Requests refused (403) from auto-blocked IPs.", Series: []Series{{Value: float64(r.rlBlock.Load())}}})

	writeFamily(&b, Family{Name: "ken_build_info", Type: "gauge", Help: "Build info; constant 1, version in the label.", Series: []Series{{Labels: []Label{{"version", r.version}}, Value: 1}}})
	writeFamily(&b, Family{Name: "ken_uptime_seconds", Type: "gauge", Help: "Seconds since process start.", Series: []Series{{Value: time.Since(r.start).Seconds()}}})
	writeFamily(&b, Family{Name: "ken_goroutines", Type: "gauge", Help: "Current number of goroutines.", Series: []Series{{Value: float64(runtime.NumGoroutine())}}})
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms) // small STW cost, negligible at scrape cadence
	writeFamily(&b, Family{Name: "ken_memory_heap_bytes", Type: "gauge", Help: "Heap memory in use (bytes).", Series: []Series{{Value: float64(ms.HeapAlloc)}}})

	r.mu.Lock()
	cs := append([]Collector(nil), r.collectors...)
	r.mu.Unlock()
	for _, c := range cs {
		for _, f := range c(ctx) {
			writeFamily(&b, f)
		}
	}
	_, _ = io.WriteString(w, b.String())
}

func writeCounter(b *strings.Builder, name, help string, series []Series) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
	for _, s := range series {
		fmt.Fprintf(b, "%s%s %s\n", name, fmtLabels(s.Labels), fmtVal(s.Value))
	}
}

func writeFamily(b *strings.Builder, f Family) {
	if f.Help != "" {
		fmt.Fprintf(b, "# HELP %s %s\n", f.Name, f.Help)
	}
	if f.Type != "" {
		fmt.Fprintf(b, "# TYPE %s %s\n", f.Name, f.Type)
	}
	for _, s := range f.Series {
		fmt.Fprintf(b, "%s%s %s\n", f.Name, fmtLabels(s.Labels), fmtVal(s.Value))
	}
}

func writeSummary(b *strings.Builder, name, help string, series []summarySnapshot) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s summary\n", name, help, name)
	for _, s := range series {
		lbl := fmtLabels(s.Labels)
		fmt.Fprintf(b, "%s_sum%s %s\n", name, lbl, fmtVal(s.SumSeconds))
		fmt.Fprintf(b, "%s_count%s %d\n", name, lbl, s.Count)
	}
}

func fmtLabels(ls []Label) string {
	if len(ls) == 0 {
		return ""
	}
	parts := make([]string, len(ls))
	for i, l := range ls {
		parts[i] = l.Name + `="` + escapeLabel(l.Value) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabel(v string) string {
	if !strings.ContainsAny(v, `\"`+"\n") {
		return v
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return strings.ReplaceAll(v, "\n", `\n`)
}

func fmtVal(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func lessLabels(a, b []Label) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i].Value != b[i].Value {
			return a[i].Value < b[i].Value
		}
	}
	return len(a) < len(b)
}

// --- counterVec: a small labelled counter (bounded label sets keep the map tiny) ---

type counterVec struct {
	names []string
	mu    sync.Mutex
	m     map[string]*counterSeries
}

type counterSeries struct {
	vals []string
	n    atomic.Int64
}

func newCounterVec(names ...string) *counterVec {
	return &counterVec{names: names, m: map[string]*counterSeries{}}
}

func (c *counterVec) inc(vals ...string) {
	key := strings.Join(vals, "|") // printable separator (label values never contain '|')
	c.mu.Lock()
	s := c.m[key]
	if s == nil {
		s = &counterSeries{vals: append([]string(nil), vals...)}
		c.m[key] = s
	}
	c.mu.Unlock()
	s.n.Add(1)
}

func (c *counterVec) snapshot() []Series {
	c.mu.Lock()
	out := make([]Series, 0, len(c.m))
	for _, s := range c.m {
		labels := make([]Label, len(c.names))
		for i, n := range c.names {
			labels[i] = Label{n, s.vals[i]}
		}
		out = append(out, Series{Labels: labels, Value: float64(s.n.Load())})
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return lessLabels(out[i].Labels, out[j].Labels) })
	return out
}

// --- summaryVec: sum (as nanoseconds) + count per label set ---

type summaryVec struct {
	names []string
	mu    sync.Mutex
	m     map[string]*summarySeries
}

type summarySeries struct {
	vals    []string
	sumNano atomic.Int64
	count   atomic.Int64
}

type summarySnapshot struct {
	Labels     []Label
	SumSeconds float64
	Count      int64
}

func newSummaryVec(names ...string) *summaryVec {
	return &summaryVec{names: names, m: map[string]*summarySeries{}}
}

func (s *summaryVec) observe(d time.Duration, vals ...string) {
	key := strings.Join(vals, "|")
	s.mu.Lock()
	ss := s.m[key]
	if ss == nil {
		ss = &summarySeries{vals: append([]string(nil), vals...)}
		s.m[key] = ss
	}
	s.mu.Unlock()
	ss.sumNano.Add(int64(d))
	ss.count.Add(1)
}

func (s *summaryVec) snapshot() []summarySnapshot {
	s.mu.Lock()
	out := make([]summarySnapshot, 0, len(s.m))
	for _, ss := range s.m {
		labels := make([]Label, len(s.names))
		for i, n := range s.names {
			labels[i] = Label{n, ss.vals[i]}
		}
		out = append(out, summarySnapshot{Labels: labels, SumSeconds: float64(ss.sumNano.Load()) / 1e9, Count: ss.count.Load()})
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return lessLabels(out[i].Labels, out[j].Labels) })
	return out
}
