# here.now — Launch Task List

> Pending work to reach launch. Companion: [task-tree.md](task-tree.md) (dependency tree + critical path).
> Sources of truth: [PRODUCT.md](../PRODUCT.md), [ARCHITECTURE.md](../ARCHITECTURE.md), [ADR-0002](../docs/adr/0002-auth-model.md).

## Launch bar

**Primary bar (the product thesis):** one real team can self-host here.now and replace vendor
share links for sensitive artifacts — PRODUCT.md's "Now (v1)" milestone made real. Requires
multi-user identity, real sharing, artifacts that actually render, and a deployable safe on the
public internet (no VPN assumed).

**Minimal bar (single-user private dogfood):** operator publishes private links to themselves.
Drops Scopes B and C entirely plus D2/D3. Items still needed for this bar are tagged **[su]**.

## Where we are

Correct spine, ~30% of a launch. Domain model, `CanView` authz gate, hash-chained audit, and the
wedge skeleton are done and sound. The four things that make it a _product_ are unbuilt:
artifacts don't render beyond self-contained HTML, no multi-user identity (single shared token),
no way to share with a named person, and zero tests on security-critical code.

Estimate (1 focused engineer; engineering estimate, not a sourced figure):

- Single-user dogfood: ~2–4 weeks.
- Multi-user team launch: ~6–10 weeks. Long pole: render parity (A1).

## Legend

- ✅ done · 🔴 launch-blocker · 🟡 recommended · ⚪ defer to v2
- **[su]** = still required even for the minimal single-user bar
- Size: **S** small · **M** medium · **L** large

---

## Scope A — Viewer & Render Parity _(the make-or-break)_

| #   | Task                                                                                                                            | Size | Status      |
| --- | ------------------------------------------------------------------------------------------------------------------------------- | ---- | ----------- |
| A1  | Dependency-resolution harness so React + import-map + Tailwind artifacts render (bundle-at-publish, or pinned import-map + SRI) | L    | 🔴 **[su]** |
| A2  | Serve `/raw` from a cookieless separate content origin                                                                          | M    | 🟡 **[su]** |
| A3  | CSP that permits real artifact needs without breaking sandbox isolation                                                         | S    | 🔴 **[su]** |
| A4  | Viewer loading / empty / denied states (beyond the current one-liner)                                                           | S    | 🟡          |

## Scope B — Identity / Auth

| #   | Task                                                                                  | Size | Status |
| --- | ------------------------------------------------------------------------------------- | ---- | ------ |
| B1  | OIDC **browser** adapter: auth-code + PKCE, JWKS verify, `/callback` → session cookie | M    | 🔴     |
| B2  | OIDC **CLI** adapter: device flow (RFC 8628) + refresh token in OS keychain           | M    | 🔴     |
| B3  | CLI → HTTP API client refactor (CLI stops touching the file store directly)           | M    | 🔴     |
| B4  | Trusted forward-auth header adapter (homelab alternative to OIDC)                     | S    | ⚪     |

`local` single-token auth stays as-is and is all the single-user bar needs — all of Scope B is
skippable for **[su]**.

## Scope C — Sharing / RBAC

| #   | Task                                                                           | Size | Status |
| --- | ------------------------------------------------------------------------------ | ---- | ------ |
| C1  | `POST /publish` API endpoint (so a remote/OIDC CLI can publish)                | M    | 🔴     |
| C2  | `artifacta share` + `POST /share` grant creation — unblocks INVITED end-to-end | M    | 🔴     |
| C3  | Owner Share UI                                                                 | M    | 🟡     |
| C4  | Set-visibility endpoint (private / invited / org)                              | S    | 🟡     |
| C5  | Grant revoke                                                                   | S    | 🟡     |

## Scope D — Dashboard ("my artifacts")

| #   | Task                                                           | Size | Status |
| --- | -------------------------------------------------------------- | ---- | ------ |
| D1  | `ListByGrantee` + `domain.ListVisibleTo` (mirror of `CanView`) | S    | 🔴     |
| D2  | Server-rendered dashboard: Mine / Shared with me / Org         | M    | 🟡     |
| D3  | Real login page (replaces the cookie-setter stub)              | S    | 🔴     |

## Scope E — Storage & Durability

| #   | Task                                                             | Size | Status                                                      |
| --- | ---------------------------------------------------------------- | ---- | ----------------------------------------------------------- |
| E1  | `Blob.Get` → `io.ReadCloser` (streaming)                         | S    | 🟡 **[su]** — do now, while fs is the only adapter          |
| E2  | Atomic metadata writes (temp-file + rename) for crash safety     | S    | 🟡 **[su]**                                                 |
| E3  | Postgres store adapter                                           | L    | ⚪ (only if launch team is large / "shared-with-me" scales) |
| E4  | S3-compatible blob adapter (backend-only, **no presigned URLs**) | M    | ⚪                                                          |
| E5  | Data-dir backup guidance (audit log especially)                  | S    | 🟡                                                          |

## Scope F — Deploy / Self-host / Ops _(self-host **is** the product)_

| #   | Task                                                                      | Size | Status      |
| --- | ------------------------------------------------------------------------- | ---- | ----------- |
| F1  | Production docker-compose: persistent volumes, env config                 | M    | 🔴 **[su]** |
| F2  | TLS termination (Caddy / reverse proxy) — public internet, no VPN assumed | S    | 🔴 **[su]** |
| F3  | Real `/metrics` (currently a stub) + structured request logging           | S    | 🟡          |
| F4  | Config via env: base URL, OIDC issuer, secrets                            | S    | 🔴          |
| F5  | Deploy quickstart docs                                                    | S    | 🔴 **[su]** |

## Scope G — Testing & Security _(launch gate)_

| #   | Task                                                                                                            | Size | Status      |
| --- | --------------------------------------------------------------------------------------------------------------- | ---- | ----------- |
| G1  | Unit tests for `CanView` — every visibility × identity × grant, fail-closed                                     | M    | 🔴 **[su]** |
| G2  | E2E for `/raw` gating: anon→404, owner→200, invited, org, deny→404, existence-not-leaked, health/metrics exempt | M    | 🔴 **[su]** |
| G3  | Publish / ls e2e                                                                                                | S    | 🟡 **[su]** |
| G4  | Upload size limit + rate-limit on `/raw`                                                                        | S    | 🟡 **[su]** |
| G5  | Security review pass (`/review` / `/cso`)                                                                       | S    | 🔴 **[su]** |
| G6  | Audit-chain integrity verifier tool (supports the compliance claim)                                             | S    | 🟡          |

---

## Blocker summary

- Team-launch blockers (🔴): ~13.
- Of those, ~9 are **[su]** — still required for the minimal single-user bar.
- Do-anytime / independent: E1, E2, G4, F3, G6.
