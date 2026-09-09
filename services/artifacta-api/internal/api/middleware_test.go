package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// okHandler is a trivial 200 handler used as the wrapped "next".
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestRateLimitAllowsBudgetThen429(t *testing.T) {
	const budget = 3
	h := rateLimit(okHandler, budget)

	// The first `budget` requests from one client must pass.
	for i := 0; i < budget; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/a/x", nil)
		req.RemoteAddr = "203.0.113.7:5555"
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i+1, rr.Code)
		}
	}

	// The next request in the same window is over budget → 429.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/a/x", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("over-budget request: got %d, want 429", rr.Code)
	}
}

func TestRateLimitIsPerClientIP(t *testing.T) {
	h := rateLimit(okHandler, 1)

	// Exhaust one client's budget.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/a/x", nil)
	req.RemoteAddr = "198.51.100.1:1000"
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("first client first request: got %d, want 200", rr.Code)
	}

	// A different client IP still has its own full budget.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/a/x", nil)
	req.RemoteAddr = "198.51.100.2:1000"
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("second client first request: got %d, want 200", rr.Code)
	}
}

func TestMaxBytesRejectsOverLimitBody(t *testing.T) {
	const limit = 16

	// A handler that fully reads the body; MaxBytesReader makes an over-limit
	// read fail, which we translate to 413.
	reader := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	h := maxBytes(reader, limit)

	// Under the limit: accepted.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("short"))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("under-limit body: got %d, want 200", rr.Code)
	}

	// Over the limit: rejected via MaxBytesReader.
	rr = httptest.NewRecorder()
	body := strings.Repeat("x", limit+64)
	req = httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge && rr.Code != http.StatusBadRequest {
		t.Fatalf("over-limit body: got %d, want 413 or 400", rr.Code)
	}
}
