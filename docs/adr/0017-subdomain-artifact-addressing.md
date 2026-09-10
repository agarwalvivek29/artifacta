# 0017 — Subdomain artifact addressing

**Date**: 2026-09-10
**Status**: Accepted
**Deciders**: Vivek Agarwal
**Issue**: N/A
**Relates to**: [0008](./0008-separate-content-origin.md) (separate content origin), [0018](./0018-anonymous-vpn-gated-visibility.md) (the no-login level these subdomains usually serve)

---

## Context

Artifacts are addressed only by path today: `https://{host}/a/{slug}`. For a "host a
page / just serve this HTML file" use case (an internal dev wants a plain URL that serves a
static page to colleagues), a per-artifact **subdomain** is the natural surface —
`https://{slug}.artifacta.genorim.xyz/` serving the bytes at the root, like any static host.

A subdomain per artifact also aligns with [ADR-0008](./0008-separate-content-origin.md): each
artifact gets its own origin, so a malicious artifact cannot script the app origin or reach
another artifact's storage — origin isolation the browser enforces for free.

---

## Decision

Add **host-based addressing** alongside path addressing. When a `RootDomain` is configured
(e.g. `artifacta.genorim.xyz`), a request to `{sub}.{RootDomain}` resolves `{sub}` to an
artifact and serves it.

- **Two subdomain forms**:
  - **Auto** — every artifact is reachable at `{slug}.{RootDomain}` with no extra state (the
    128-bit slug is already a valid lower-case DNS label).
  - **Custom label** — an owner may claim `{label}.{RootDomain}`. `Artifact` gains a
    `string label` (schema-first, `packages/schema/proto/artifacta/v1`). Labels are globally
    unique, validated by `domain.ValidLabel` (RFC-1123 DNS label, 1–63 chars, not in a
    reserved-word blocklist — `www`, `api`, `login`, … — so a user can't shadow a platform
    surface). Owner-only endpoint `PATCH /artifacts/{slug}/label` (400 invalid, 409 taken).
- **Serving is a rewrite, not a new handler**: a `hostRouter` wraps the mux. On a subdomain
  it resolves `{sub}` → slug (slug space checked first, so a label can't shadow a real slug),
  then rewrites the request onto the canonical serving path:
  - `/` and `/raw` → `/a/{slug}/raw` (the page, at the root)
  - `/v/{n}` and `/v/{n}/raw` → `/a/{slug}/v/{n}/raw`
  - any other path → 404 (subdomains serve the artifact's bytes, not the app UI or JSON API)
- **The subdomain is an address, never a bypass.** Rewritten requests flow through the same
  `serveVersion → domain.CanView` gate, audit, and CSP. A `PRIVATE` artifact on its subdomain
  stays a not-leaking 404 to an anonymous caller; only [ADR-0018](./0018-anonymous-vpn-gated-visibility.md)'s
  `LINK` level admits them. Apex/IP/unknown hosts pass through to normal path routing.
- **Config-gated & off by default**: `ARTIFACTA_ROOT_DOMAIN` (empty ⇒ feature dark). The code
  ships and is fully testable via the `Host` header with **no DNS dependency**.

---

## Consequences

**Easier**: a clean per-artifact hosting URL; origin isolation per artifact (ADR-0008);
zero-friction auto subdomains; the whole feature is one wrapper + one rewrite, reusing every
existing security gate.

**Harder / deferred to Phase 2 (infra, requires explicit approval)**:

- **Wildcard DNS** `*.{RootDomain}` and a **wildcard TLS certificate**.
- A **reverse proxy / ingress** (e.g. Caddy/Traefik) terminating TLS and forwarding `Host`.
  There is no proxy in `infra/docker-compose.yml` today; adding one is a CI/CD-infra change.
- Label lifecycle beyond claim (rename releases the old label; deletion/transfer, and any
  abuse/rate limits on label churn, are follow-ups).

**Trade-offs**: labels are a global namespace (squatting is possible → reserved blocklist +
owner-only claim); multi-level hosts (`a.b.{RootDomain}`) are intentionally unsupported.
