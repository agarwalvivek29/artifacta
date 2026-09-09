# ADR 0002 — Auth model: pluggable OIDC/local/forward-auth + per-artifact RBAC

Status: accepted
Date: 2026-07-07
Supersedes: none

## Context

The monorepo template's default authentication (CORE_RULES Rule 10) is a shared-secret
**JWT + API-key** middleware applied to all backend endpoints — a service-to-service
("service mesh") default. `BOOTSTRAP.md` Phase 3.4 explicitly invites confirming or
objecting to this default.

here.now is not a service-mesh backend. It is a **user-facing artifact host** where:

- Viewers are end users authenticated against the operator's identity provider, not
  services holding a shared API key.
- Access is decided **per artifact** (private / invited / org), not per endpoint.
- Self-hosters range from a solo user (local auth) to an org (OIDC) to homelab setups
  behind an identity-aware proxy (trusted forward-auth header).

A single shared-secret JWT/API-key model cannot express per-artifact RBAC and does not fit
end-user authentication.

## Decision

here.now uses a pluggable identity `Provider` interface (`internal/api.Auth`) with adapters:

- **local** — single-user token/session (v0 default; zero-dependency self-host).
- **OIDC** — any issuer (later).
- **trusted forward-auth header** — delegate identity to an upstream identity-aware proxy (later).

The **authorization** decision is separate and always in the app: `domain.CanView`
evaluates the artifact's visibility + grants against the verified identity. Identity is
never client-asserted; grants bind to the immutable subject (`sub`), not a mutable email.
Exempt paths remain `GET /health` and `GET /metrics`.

The template's JWT + API-key mechanism is **not removed** — it remains available for any
future internal service-to-service calls between here.now services — but it is not the
user-facing auth model.

## Consequences

- Divergence from CORE_RULES Rule 10's default is intentional and recorded here (Rule 2:
  exceptions must live in an ADR).
- `packages/schema/proto/artifacta/v1` defines `Identity`; the service imports it (schema-first).
- New viewer routes must pass through the `Provider` + `CanView`; never add an unauthenticated
  content route.
