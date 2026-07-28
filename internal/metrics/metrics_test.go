package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExposition(t *testing.T) {
	r := New("1.2.3")
	r.RecordHTTP("web", "success", 10*time.Millisecond)
	r.RecordHTTP("web", "success", 30*time.Millisecond)
	r.RecordHTTP("web", "client_error", 5*time.Millisecond)
	r.RecordMCP("kb_search", true)
	r.RecordMCP("kb_save", false)
	r.AuthFailure("mcp")
	r.RateLimitRejected()
	r.RateLimitBlocked()
	r.AddCollector(func(context.Context) []Family {
		return []Family{Gauge("ken_kb_entries", "entries", 42)}
	})

	var b strings.Builder
	r.WriteText(context.Background(), &b)
	out := b.String()

	for _, want := range []string{
		`# TYPE ken_http_requests_total counter`,
		`ken_http_requests_total{surface="web",outcome="success"} 2`,
		`ken_http_requests_total{surface="web",outcome="client_error"} 1`,
		`# TYPE ken_http_request_duration_seconds histogram`,
		`ken_http_request_duration_seconds_bucket{surface="web",le="0.005"} 1`,
		`ken_http_request_duration_seconds_bucket{surface="web",le="0.01"} 2`,
		`ken_http_request_duration_seconds_bucket{surface="web",le="0.05"} 3`,
		`ken_http_request_duration_seconds_bucket{surface="web",le="+Inf"} 3`,
		`ken_http_request_duration_seconds_sum{surface="web"} 0.045`,
		`ken_http_request_duration_seconds_count{surface="web"} 3`,
		`ken_mcp_tool_calls_total{tool="kb_search",outcome="success"} 1`,
		`ken_mcp_tool_calls_total{tool="kb_save",outcome="error"} 1`,
		`ken_auth_failures_total{surface="mcp"} 1`,
		`ken_ratelimit_rejected_total 1`,
		`ken_ratelimit_blocked_total 1`,
		`ken_build_info{version="1.2.3"} 1`,
		`ken_kb_entries 42`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition missing %q\n---\n%s", want, out)
		}
	}
}

func TestOutcomeClass(t *testing.T) {
	for code, want := range map[int]string{
		100: "informational", 200: "success", 204: "success",
		301: "redirection", 404: "client_error", 503: "server_error",
	} {
		if got := OutcomeClass(code); got != want {
			t.Errorf("OutcomeClass(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestCountingMiddleware(t *testing.T) {
	r := New("t")
	h := r.Counting("web", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	// /metrics is a skip path — must NOT be counted.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/metrics", nil))

	var b strings.Builder
	r.WriteText(context.Background(), &b)
	if !strings.Contains(b.String(), `ken_http_requests_total{surface="web",outcome="client_error"} 1`) {
		t.Errorf("counting middleware did not record exactly one 404:\n%s", b.String())
	}
}

func TestEscapeLabel(t *testing.T) {
	r := New("t")
	r.RecordMCP(`weird"tool`, true)
	var b strings.Builder
	r.WriteText(context.Background(), &b)
	if !strings.Contains(b.String(), `tool="weird\"tool"`) {
		t.Errorf("label not escaped:\n%s", b.String())
	}
}

// A histogram must bucket cumulatively, preserve the mean via _sum/_count, and
// emit the mandatory le="+Inf" bucket equal to the count. This is what makes
// histogram_quantile() work — the property the summary it replaced could not give.
func TestHistogramBucketsAreCumulative(t *testing.T) {
	r := New("x")
	// One fast (1ms), one mid (40ms), one slow (3s) request on the same surface.
	r.RecordHTTP("web", "success", 1*time.Millisecond)
	r.RecordHTTP("web", "success", 40*time.Millisecond)
	r.RecordHTTP("web", "success", 3*time.Second)

	var b strings.Builder
	r.WriteText(context.Background(), &b)
	out := b.String()

	// le=0.005 catches only the 1ms; le=0.05 catches 1ms+40ms; le=5 catches all;
	// +Inf and _count are the total. Cumulative, strictly non-decreasing.
	for _, want := range []string{
		`ken_http_request_duration_seconds_bucket{surface="web",le="0.005"} 1`,
		`ken_http_request_duration_seconds_bucket{surface="web",le="0.05"} 2`,
		`ken_http_request_duration_seconds_bucket{surface="web",le="1"} 2`,
		`ken_http_request_duration_seconds_bucket{surface="web",le="5"} 3`,
		`ken_http_request_duration_seconds_bucket{surface="web",le="+Inf"} 3`,
		`ken_http_request_duration_seconds_count{surface="web"} 3`,
		`# TYPE ken_http_request_duration_seconds histogram`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("histogram missing %q\n---\n%s", want, out)
		}
	}
}

// The per-tool MCP latency histogram is emitted with the tool label.
func TestMCPDurationHistogram(t *testing.T) {
	r := New("x")
	r.RecordMCPDuration("kb_search", 12*time.Millisecond)
	var b strings.Builder
	r.WriteText(context.Background(), &b)
	out := b.String()
	for _, want := range []string{
		`# TYPE ken_mcp_tool_duration_seconds histogram`,
		`ken_mcp_tool_duration_seconds_bucket{tool="kb_search",le="0.025"} 1`,
		`ken_mcp_tool_duration_seconds_count{tool="kb_search"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mcp duration histogram missing %q\n---\n%s", want, out)
		}
	}
}
