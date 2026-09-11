package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestMux mounts a couple of routes on a real ServeMux so r.Pattern is
// populated exactly as it is in production (the label source under test).
func newTestMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /a/{slug}/raw", func(w http.ResponseWriter, r *http.Request) {
		// Stream in chunks to prove the recorder preserves streamed bytes.
		_, _ = io.WriteString(w, "hello ")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.WriteString(w, "world")
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func TestObserveMetricsLabelsByRoutePatternNotSlug(t *testing.T) {
	m := NewMetrics()
	h := Observe(newTestMux(), slog.New(slog.NewJSONHandler(io.Discard, nil)), m)

	// Two different slugs must collapse to ONE series keyed by the route template,
	// not two per-slug series (the cardinality-safety invariant).
	for _, slug := range []string{"abc123", "xyz789"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/a/"+slug+"/raw", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("slug %s: status = %d, want 200", slug, rr.Code)
		}
	}

	body := scrape(t, m)
	if strings.Contains(body, "abc123") || strings.Contains(body, "xyz789") {
		t.Fatalf("metrics leaked a per-slug label (cardinality bomb):\n%s", body)
	}
	if !strings.Contains(body, `route="/a/{slug}/raw"`) {
		t.Fatalf("expected route template label, got:\n%s", body)
	}
	// One template, two requests → the counter for that series must read 2.
	if !strings.Contains(body, `http_requests_total{method="GET",route="/a/{slug}/raw",status="200"} 2`) {
		t.Fatalf("expected the templated counter to total 2, got:\n%s", body)
	}
	// The in-flight gauge must balance back to 0 once requests finish.
	if !strings.Contains(body, "http_requests_in_flight 0") {
		t.Fatalf("expected in-flight gauge to settle at 0, got:\n%s", body)
	}
}

func TestRoutesServesRealMetricsWhenSet(t *testing.T) {
	// With Metrics set, GET /metrics serves the Prometheus registry, not the stub.
	s := &Server{Metrics: NewMetrics()}
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "not yet implemented") {
		t.Fatal("expected real metrics, got the stub")
	}
	if !strings.Contains(rr.Body.String(), "go_goroutines") {
		t.Fatalf("expected go_* runtime series, got:\n%s", rr.Body.String())
	}
}

func TestRoutesServesMetricsStubWhenNil(t *testing.T) {
	s := &Server{} // no Metrics
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rr.Body.String(), "not yet implemented") {
		t.Fatalf("expected the nil-Metrics stub, got:\n%s", rr.Body.String())
	}
}

func TestObservePreservesStreamedBytesAndSetsRequestID(t *testing.T) {
	m := NewMetrics()
	h := Observe(newTestMux(), slog.New(slog.NewJSONHandler(io.Discard, nil)), m)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/a/s1/raw", nil))

	if got := rr.Body.String(); got != "hello world" {
		t.Fatalf("streamed body = %q, want %q", got, "hello world")
	}
	if rr.Header().Get("X-Request-Id") == "" {
		t.Fatal("expected a generated X-Request-Id response header")
	}
}

func TestObserveHonorsInboundRequestID(t *testing.T) {
	h := Observe(newTestMux(), slog.New(slog.NewJSONHandler(io.Discard, nil)), NewMetrics())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/a/s1/raw", nil)
	req.Header.Set("X-Request-Id", "trace-42")
	h.ServeHTTP(rr, req)
	if got := rr.Header().Get("X-Request-Id"); got != "trace-42" {
		t.Fatalf("X-Request-Id = %q, want the inbound trace-42", got)
	}
}

func TestObserveLogsPathNotQueryString(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := Observe(newTestMux(), logger, NewMetrics())

	rr := httptest.NewRecorder()
	// A token in the query string must never reach the logs.
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/a/s1/raw?token=SECRET123", nil))

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, buf.String())
	}
	if strings.Contains(buf.String(), "SECRET123") {
		t.Fatalf("query string leaked into the log:\n%s", buf.String())
	}
	if entry["path"] != "/a/s1/raw" {
		t.Fatalf("logged path = %v, want /a/s1/raw", entry["path"])
	}
}

func TestObserveSkipsHealthAndMetrics(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := Observe(newTestMux(), logger, NewMetrics())

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Body.String() != "ok" {
		t.Fatalf("health body = %q, want ok", rr.Body.String())
	}
	if buf.Len() != 0 {
		t.Fatalf("expected /health to be un-logged, got:\n%s", buf.String())
	}
}

// scrape renders the registry in the text exposition format.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d", rr.Code)
	}
	return rr.Body.String()
}
