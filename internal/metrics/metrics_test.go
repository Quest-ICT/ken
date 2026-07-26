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
		`# TYPE ken_http_request_duration_seconds summary`,
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
