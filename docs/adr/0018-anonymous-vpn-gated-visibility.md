# 0018 — Anonymous, VPN-gated visibility (LINK)

**Date**: 2026-09-10
**Status**: Accepted
**Deciders**: Vivek Agarwal
**Issue**: N/A
**Relates to**: [0002](./0002-auth-model.md) (per-artifact RBAC), [0011](./0011-vpn-fronted-threat-model.md) (VPN perimeter), [0017](./0017-subdomain-artifact-addressing.md) (the subdomain these are usually served on)

---

## Context

The core invariant is **private-by-default, fail-closed**: `domain.CanView` denies any caller
with no authenticated subject, and PRODUCT.md lists as a non-goal _"v1 does not implement
external (non-account) public sharing."_ But there is a real, recurring internal need: share
an artifact (e.g. a static HTML page) with a colleague **without making them log in** — the
"just serve this file" case that would otherwise get a hand-rolled S3 bucket.

The key realization: this does **not** require public-internet exposure. Per
[ADR-0011](./0011-vpn-fronted-threat-model.md) the deployment already sits **behind the
corporate VPN**, and the VPN is the network perimeter — app auth is retained as
defense-in-depth. So "no login" can mean _"anyone who can already reach the host (i.e. is on
the VPN)"_, not _"anyone on the internet."_

---

## Decision

Add one visibility level, `VISIBILITY_LINK` (schema-first,
`packages/schema/proto/artifacta/v1`), meaning **no login required; the perimeter is the
network (VPN, ADR-0011) plus the unguessable 128-bit slug, not an authenticated identity.**

- **The single carve-out in `CanView`**: `LINK` returns `true` even for a `nil`/empty-subject
  caller. It is placed **before** the anonymous fail-closed guard and is the _only_ path by
  which an anonymous caller reaches an allow. Every other level (`PRIVATE`/`INVITED`/`ORG`/
  `UNSPECIFIED`) still fails closed for anonymous callers — locked by a regression test.
- **Opt-in, owner-only**: default stays `PRIVATE`; an owner sets `LINK` via the existing
  `PATCH /artifacts/{slug}/visibility` (`{"visibility":"link"}`).
- **Still audited**: an anonymous view of a `LINK` artifact writes a `VIEW` audit event with
  an empty/anonymous principal. The tamper-evident audit thesis holds even without identity —
  we lose _who_, never _that it was viewed_.
- **Scope: internal-dev first.** This is for internal, low-sensitivity hosting on the VPN. It
  is **not** public-internet sharing; egress to the open internet is out of scope and would be
  a separate decision (different threat model, different defaults).

This annotates — does not blanket-reverse — the PRODUCT.md non-goal: _internal, VPN-gated,
no-login_ is materially different from _public-internet_ sharing, which remains a non-goal.

---

## Consequences

**Easier**: the "serve an internal HTML page" use case is a first-class, one-flag operation
(`visibility=link`, optionally on a subdomain per ADR-0017) instead of shadow infra.

**Harder / risks**:

- It is a deliberate hole in the fail-closed invariant. Mitigations: it is the _only_
  anonymous-allow path, isolated to one branch, guarded by a regression test asserting every
  other level still denies anonymous; opt-in and owner-only; still audited; and the network
  perimeter (VPN) remains the real boundary.
- Slug is now load-bearing for `LINK` artifacts (it is the secret). Slugs are 128-bit random,
  and content routes are IP-rate-limited (120/min) to blunt enumeration.
- If the deployment were ever exposed outside the VPN, `LINK` artifacts would become
  world-readable. That is why scope is explicitly VPN-side and this ADR is bound to
  [ADR-0011](./0011-vpn-fronted-threat-model.md); relaxing the perimeter must revisit this.
