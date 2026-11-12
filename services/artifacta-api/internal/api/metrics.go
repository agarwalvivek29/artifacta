package api

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the Prometheus registry and the HTTP instruments recorded by the
// Observe middleware. It is created once per server (NewMetrics) and exposed at
// GET /metrics. A nil *Metrics is a no-op everywhere — the server keeps working
// (and /metrics falls back to its stub), which keeps tests that build a bare
// Server unchanged.
type Metrics struct {
	reg       *prometheus.Registry
	requests  *prometheus.CounterVec
	duration  *prometheus.HistogramVec
	inFlight  prometheus.Gauge
	saKeyAuth *prometheus.CounterVec
}

// NewMetrics builds a private registry (not the global default, so tests and
// multiple servers never collide on duplicate registration) with the standard Go
// runtime + process collectors plus the three HTTP instruments.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m := &Metrics{
		reg: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests by method, matched route template, and status.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency by method and matched route template.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "In-flight HTTP requests.",
		}),
		saKeyAuth: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "artifacta_sa_key_auth_total",
			Help: "Service-account API key authentications by result (ok, unknown, revoked, expired, disabled, error).",
		}, []string{"result"}),
	}
	reg.MustRegister(m.requests, m.duration, m.inFlight, m.saKeyAuth)
	return m
}

// Handler serves the registry in the Prometheus text exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}

// observe records one finished request. route is the matched ServeMux pattern
// (bounded, cardinality-safe); status is the numeric HTTP status as a string.
func (m *Metrics) observe(method, route, status string, seconds float64) {
	if m == nil {
		return
	}
	m.requests.WithLabelValues(method, route, status).Inc()
	m.duration.WithLabelValues(method, route).Observe(seconds)
}

// recordSAKeyAuth counts one API-key authentication attempt by its bounded
// result label (ADR-0028).
func (m *Metrics) recordSAKeyAuth(result string) {
	if m == nil {
		return
	}
	m.saKeyAuth.WithLabelValues(result).Inc()
}
