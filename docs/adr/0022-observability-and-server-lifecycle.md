# 0022 — Observability (structured logs + Prometheus metrics) and server lifecycle

**Date**: 2026-09-11
**Status**: Accepted (implemented 2026-09-11)
**Deciders**: Vivek Agarwal
**Related**: [ADR-0002 — Auth model](0002-auth-model.md), [ADR-0009 — Postgres store](0009-postgres-store-adapter.md)

---

## Context

The production-readiness review graded operations a D+. The real server entrypoint
(`serve()` in `internal/cli/cli.go`) was a bare `http.ListenAndServe`, which gave the deployment:

- **No HTTP timeouts** — a single slow/idle client (Slowloris) could hold a connection and
  goroutine indefinitely; the 25 MiB body cap bounds size, not time.
- **No graceful shutdown** — SIGTERM (every rollout / task replacement) killed in-flight
  publishes and downloads mid-write.
- **No logging and no real metrics** — zero request/access logs anywhere, and `GET /metrics`
  returned a `# not yet implemented` stub, so a Prometheus/Datadog scrape yielded nothing. On
  launch there was no way to answer "is it up, how many 4xx/5xx, who hit what, why did that 500."

The audit trail (ADR-level, hash-chained) records artifact _access events_; it is not operational
telemetry. These are complementary, not substitutes.

---

## Decision

1. **Explicit server timeouts (pragmatic posture for a streaming file server).** `serve()` builds
   an explicit `*http.Server` with `ReadHeaderTimeout: 10s` (kills header-Slowloris, the cheap
   DoS), `ReadTimeout: 120s` (fits a 25 MiB upload on a slow-but-real link), `IdleTimeout: 120s`,
   and **`WriteTimeout: 0` deliberately** — `serveVersion` streams artifact bytes (≤25 MiB) via
   `io.Copy` to potentially slow VPN clients, and a blanket write deadline would cut a legitimate
   large download mid-stream. The residual narrow slow-_read_ hole is accepted for now; the
   documented follow-up is per-stream deadlines via `http.ResponseController.SetWriteDeadline`
   during the copy, which protects idle readers without capping legitimate large transfers. Values
   are named constants, not config surface (YAGNI).

2. **Graceful shutdown.** `signal.NotifyContext(SIGINT, SIGTERM)` drives a `srv.Shutdown(ctx)`
   with a 20s bounded drain, so a rollout finishes in-flight requests instead of resetting them. A
   second signal restores default handling (force-quit).

3. **Structured request logging (`slog`, stdlib).** A single `Observe` middleware wraps the router
   and emits one JSON line per request: method, **path only** (never the raw query string — the
   dev `/login?token=` credential and `?preview=` flag must not land in logs), status, bytes,
   duration, client IP, and a request id. The request id honors an inbound `X-Request-Id` (proxy
   trace continuity) or is generated, and is echoed on the response. `/health` and `/metrics` are
   passed through un-instrumented so probes and self-scrapes do not flood the logs. Level is
   `ARTIFACTA_LOG_LEVEL` (default `info`).

4. **Real Prometheus metrics via `prometheus/client_golang`.** The same middleware records
   `http_requests_total{method,route,status}`, `http_request_duration_seconds{method,route}`, and
   `http_requests_in_flight`, plus the standard Go runtime + process collectors, served from a
   private registry at `GET /metrics`. **This spends a dependency deliberately** (pure-Go, no cgo;
   the static-binary build is preserved) rather than hand-rolling an exposition format — the
   standard client is the boring, correct choice and aspora's Grafana/Datadog scrape it natively.

5. **Cardinality-safe route labels via the framework built-in.** The `route` label is the matched
   `ServeMux` pattern (`r.Pattern`, Go 1.23+), e.g. `/a/{slug}/raw` — bounded by construction, so
   a per-slug series explosion is impossible, and the label can never drift from the routing table
   the way a hand-maintained path normalizer would. Unmatched requests → `other`.

6. **Container-native healthcheck.** An `artifacta healthcheck` subcommand GETs `/health` and exits
   0/non-zero. The prod image is distroless (no shell/curl), so the compose/orchestrator probe
   invokes the binary itself.

7. **Fail loud on empty local-auth token.** `serve()` now errors if local (single-token) auth is
   selected with an empty token, instead of booting a server that silently rejects every request.
   `ARTIFACTA_TOKEN` / `ARTIFACTA_SUB` / `ARTIFACTA_EMAIL` gained env mappings so a containerized
   local-auth deploy can inject the identity without shipping a `config.json`.

---

## Consequences

### Positive

- The server survives rollouts (graceful drain) and slow-client pressure (timeouts).
- Operators get JSON request logs and a real `/metrics` scrape on day one — no more flying blind.
- Route labels are safe against a cardinality bomb without any maintenance burden.

### Negative / Neutral

- One new (pure-Go) dependency, `prometheus/client_golang`, and its transitive `x/sys` bump.
- The residual slow-read Slowloris window remains until per-stream write deadlines land (item 1).
- Logging is stdout JSON only — no log shipping / sampling / trace propagation beyond the request
  id; those are follow-ups if the fleet needs them.

---

## Alternatives Considered

- **Hand-rolled `/metrics` text exposition (no dependency):** rejected — more code, fewer series
  (no histograms / `go_*` runtime metrics), and it reinvents a solved, standard problem.
- **A hand-written route-path normalizer for labels:** rejected — it duplicates the routing table
  and silently drifts when routes change; `r.Pattern` is the runtime's own answer.
- **Blanket `WriteTimeout`:** rejected — it cuts legitimate large artifact downloads to slow
  clients. Per-stream deadlines (a follow-up) are the correct way to bound write-side slowness.
