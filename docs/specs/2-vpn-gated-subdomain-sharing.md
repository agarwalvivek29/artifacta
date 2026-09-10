# Spec: VPN-gated no-login sharing + subdomain hosting

**Issue**: N/A (to be filed)
**Status**: Approved (Phase 1)
**Author**: Vivek Agarwal
**Date**: 2026-09-10
**Services Affected**: `artifacta-api`, `packages/schema`

---

## Summary

Let an owner publish an artifact that colleagues can open **without logging in** (gated by the
corporate VPN, not an identity), and reach it at a **subdomain** — `{slug}.artifacta.genorim.xyz`
automatically, or a chosen `{label}.artifacta.genorim.xyz`. This turns the "just serve an
internal HTML page" need into a first-class operation instead of a hand-rolled S3 bucket.

---

## Background and Motivation

Every artifact today is private-by-default and path-addressed (`/a/{slug}`). Internal teams
repeatedly want to host a static page for colleagues with a plain URL and no login. Because the
deployment already sits behind the VPN ([ADR-0011](../adr/0011-vpn-fronted-threat-model.md)),
"no login" can safely mean "anyone already on the VPN," not "public internet." See
[ADR-0017](../adr/0017-subdomain-artifact-addressing.md) and
[ADR-0018](../adr/0018-anonymous-vpn-gated-visibility.md).

---

## Scope

### In Scope (Phase 1 — code, no infra)

- New visibility `VISIBILITY_LINK`: `CanView` admits anonymous callers **only** for this level.
- Owner opt-in via `PATCH /artifacts/{slug}/visibility` `{"visibility":"link"}`.
- Subdomain resolution + serving: `{slug|label}.{RootDomain}/` serves the artifact bytes,
  behind the same `CanView` gate/audit/CSP; gated by `ARTIFACTA_ROOT_DOMAIN` (off by default).
- Custom labels: `Artifact.label`, `PATCH /artifacts/{slug}/label`, DNS-safe + reserved-word +
  uniqueness validation; `subdomain_url` reported in artifact metadata.
- Anonymous views still audited (empty principal).

### Out of Scope

- **Public-internet** exposure (egress beyond the VPN) — different threat model, separate ADR.
- The **infra** to actually route subdomains (wildcard DNS, wildcard TLS, reverse proxy) —
  Phase 2, requires explicit approval.
- Label deletion/transfer, expiry/TTL, per-viewer tokens, and CLI surface for label/link —
  follow-ups.
- Subdomain viewer _chrome_ (comments/version switcher on the subdomain): subdomains serve raw
  bytes; the full viewer stays at `/a/{slug}`.

---

## Acceptance Criteria

- [ ] Given a `LINK` artifact, when an anonymous caller GETs `{slug}.{root}/`, then its bytes
      are served (200) and a `VIEW` audit event is written.
- [ ] Given a `PRIVATE` artifact, when an anonymous caller GETs `{slug}.{root}/`, then 404 and
      no bytes leak (fail-closed preserved).
- [ ] Given every non-`LINK` level, when the caller is anonymous, then `CanView` denies (regression).
- [ ] Given an owner, when they `PATCH .../label` with a valid unclaimed label, then 200 with
      `subdomain_url`; invalid → 400; already-taken → 409; non-owner → 404; anonymous → 401.
- [ ] Given a claimed label, when a caller GETs `{label}.{root}/`, then the artifact is served.
- [ ] Given `ARTIFACTA_ROOT_DOMAIN` unset, then subdomain routing is inert and `/a/{slug}` is
      unchanged.
- [ ] An unknown subdomain, a multi-level host, or a non-hosting path on a subdomain → 404;
      the apex host passes through to normal routing.

---

## Technical Design

See [ADR-0017](../adr/0017-subdomain-artifact-addressing.md) (host-router rewrite, labels) and
[ADR-0018](../adr/0018-anonymous-vpn-gated-visibility.md) (`LINK` carve-out). Key files:
`packages/schema/proto/artifacta/v1/artifact.proto`, `internal/domain/{authz,label}.go`,
`internal/api/{hostrouter,server}.go`, `internal/infra/store.go`, `internal/config/config.go`.

```
GET {slug|label}.{root}/  ──hostRouter──▶ rewrite → /a/{slug}/raw ──▶ CanView ──allow──▶ bytes + audit(VIEW)
                                                                              └──deny──▶ 404 (fails closed)
```

## Phasing

- **Phase 1 (this spec):** all code above, testable via `Host:` header, no DNS. **Done.**
- **Phase 2 (separate, needs approval):** wildcard DNS + TLS + reverse proxy; flip
  `ARTIFACTA_ROOT_DOMAIN` on in stage.
