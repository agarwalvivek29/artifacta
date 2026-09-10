# 0019 — Invite-by-email grants

**Date**: 2026-09-10
**Status**: Accepted
**Deciders**: Vivek Agarwal
**Issue**: N/A
**Relates to**: [0002](./0002-auth-model.md) (per-artifact RBAC), [0007](./0007-identity-and-auth.md) (identity = OIDC sub), [0011](./0011-vpn-fronted-threat-model.md) (VPN perimeter)

---

## Context

Grants bind to the immutable subject (`grantee_sub`): the proto says as much — _"Grants
bind to Sub, never to a mutable email."_ That is correct for authorization, but it makes
**inviting** awkward: an owner does not know a colleague's `sub` until that colleague has
signed in at least once. The Share UI (the owner panel) needs to invite people the natural
way — **by email** — before they have ever appeared.

---

## Decision

Let a grant identify its grantee by **subject and/or email**. `Grant` gains
`string grantee_email` (schema-first). Authorization matches on **either**:

- `domain.CanView` for `INVITED` admits a caller when a grant on that slug matches the
  caller's `sub`, **or** matches the caller's **email** (case-insensitive) when the grant
  carries a `grantee_email`. The empty-string guards ensure an empty grant email can never
  match an empty caller email — so this never widens access by accident.
- **Owner-only endpoints**: `POST /artifacts/{slug}/grants` accepts `{"email":"…"}` (the
  Share-UI path) or `{"grantee_sub":"…"}`; duplicate invites are an idempotent 201.
  `DELETE /artifacts/{slug}/grants/{grantee}` revokes by email or sub.
- **Owner-only disclosure**: the artifact metadata (`GET /artifacts/{slug}`) returns the
  grantee list only to the owner — who an artifact is shared with is itself sensitive.

Email is only trustworthy because it is a **verified claim from the OIDC IdP** ([ADR-0007](./0007-identity-and-auth.md))
and the deployment sits **behind the VPN** ([ADR-0011](./0011-vpn-fronted-threat-model.md));
the app never trusts a client-asserted email. Within that model, matching a verified email
is acceptable and is what makes invite-before-first-login work.

---

## Consequences

**Easier**: owners invite by email in the Share panel; the invitee gets access the moment
they sign in with that email, with no prior coordination of subject ids.

**Trade-offs / risks**:

- Email is mutable at the IdP; if an address is reassigned to a different person, an
  outstanding email grant would follow the address. Mitigations: it is a verified IdP claim,
  the perimeter is the VPN, grants are owner-managed and revocable, and subject-based grants
  remain available for the strict case.
- "Shared with me" (the dashboard section) still lists by `sub` only, so an email-only grant
  does not surface there until the grant is bound to a subject. Upgrading an email grant to
  its `sub` on first successful view is a straightforward follow-up.
- `CanView` now has two match paths for `INVITED`; both are slug-scoped and covered by tests,
  and the anonymous fail-closed guarantee (and the `LINK` carve-out, [ADR-0018](./0018-anonymous-vpn-gated-visibility.md))
  are unchanged.
