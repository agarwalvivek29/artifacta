package api

import (
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/domain"
)

// NewLogger builds the structured JSON logger used for request logging. level is
// one of debug/info/warn/error (default info). Logs go to stdout so a container
// runtime collects them.
func NewLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

// responseRecorder wraps http.ResponseWriter to capture the status code and the
// number of bytes written, without buffering the body — Write passes straight
// through so io.Copy streaming is preserved. It forwards Flush and, via Unwrap,
// lets http.ResponseController reach the underlying writer.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (r *responseRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush forwards to the underlying writer when it supports flushing (keeps
// streaming responses live).
func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the wrapped writer so http.NewResponseController can find
// optional interfaces (e.g. deadline control) on the real connection.
func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Observe wraps next with request logging + Prometheus metrics. A single
// recorder captures status and bytes for both. The request id is taken from an
// inbound X-Request-Id (proxy trace continuity) or generated, and echoed back on
// the response. /health and /metrics are passed through un-instrumented so probes
// and self-scrapes don't flood the logs or the series. logger and m may be nil.
func Observe(next http.Handler, logger *slog.Logger, m *Metrics) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; p == "/health" || p == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		reqID := r.Header.Get("X-Request-Id")
		if reqID == "" {
			reqID = domain.NewSlug()
		}
		w.Header().Set("X-Request-Id", reqID)

		if m != nil {
			m.inFlight.Inc()
			defer m.inFlight.Dec()
		}

		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		dur := time.Since(start)

		// r.Pattern is populated by ServeMux with the matched route template
		// (e.g. "GET /a/{slug}/raw"), which is bounded — safe as a metric label.
		// Strip the leading method token (it's already a separate label) so the
		// route label is just the path template.
		route := r.Pattern
		if i := strings.IndexByte(route, ' '); i >= 0 {
			route = route[i+1:]
		}
		if route == "" {
			route = "other"
		}
		m.observe(r.Method, route, strconv.Itoa(rec.status), dur.Seconds())

		// Log r.URL.Path only — never RawQuery, which can carry the dev
		// /login?token= credential and the ?preview= flag.
		logger.Info("http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration_ms", dur.Milliseconds(),
			"ip", clientIP(r.RemoteAddr),
			"request_id", reqID,
		)
	})
}
