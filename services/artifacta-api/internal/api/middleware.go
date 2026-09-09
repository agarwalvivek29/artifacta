package api

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// This file holds transport-level guards that sit in front of the content
// routes: a per-client rate limiter and a request body-size cap. They are
// deliberately dependency-free (stdlib only) and hold no business logic.

// rateLimiter is a minimal in-memory fixed-window rate limiter keyed by client
// IP. Each client gets `perMinute` requests per 60s window; when a window's
// budget is spent, further requests in that window are rejected with 429. State
// is a mutex-guarded map — adequate for a single process; a distributed deploy
// would swap this for a shared store (e.g. Redis).
type rateLimiter struct {
	perMinute int
	mu        sync.Mutex
	windows   map[string]*window
}

// window tracks one client's request count within the current fixed window.
type window struct {
	start time.Time
	count int
}

// allow reports whether a request from `ip` fits inside the current window's
// budget, advancing the counter (and rolling the window over when it expires).
func (rl *rateLimiter) allow(ip string, now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	w := rl.windows[ip]
	if w == nil || now.Sub(w.start) >= time.Minute {
		rl.windows[ip] = &window{start: now, count: 1}
		return true
	}
	if w.count >= rl.perMinute {
		return false
	}
	w.count++
	return true
}

// rateLimit wraps next with a fixed-window per-client-IP rate limiter allowing
// `perMinute` requests per client per minute. The client key is the IP from
// RemoteAddr with the port stripped. Over-budget requests get HTTP 429.
func rateLimit(next http.Handler, perMinute int) http.Handler {
	rl := &rateLimiter{perMinute: perMinute, windows: make(map[string]*window)}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(clientIP(r.RemoteAddr), time.Now()) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP extracts the bare IP from a RemoteAddr ("host:port"), falling back to
// the raw value when it carries no port (e.g. a unix socket or a test stub).
func clientIP(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// maxBytes caps an incoming request body at n bytes by wrapping it in an
// http.MaxBytesReader; reads past the limit fail and handlers surface that as a
// 4xx. It is harmless on GETs (no body) and is wired in now so future
// upload/POST routes inherit the guard. Note: full upload-size enforcement
// (per-plan quotas, streaming limits) lands with the publish API (FR1); this is
// only the transport-level ceiling.
func maxBytes(next http.Handler, n int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, n)
		}
		next.ServeHTTP(w, r)
	})
}
