# 0023 — Browser artifact upload via content-negotiated `POST /artifacts`

**Date**: 2026-09-11
**Status**: Proposed (decision agreed; implementation deferred pending capability expansion)
**Deciders**: Vivek Agarwal
**Spec**: [docs/specs/5-ui-artifact-upload.md](../specs/5-ui-artifact-upload.md)

---

## Context

An artifact today is either authored in-app or pushed with `artifacta publish <file>` (raw body
to `POST /artifacts`). Users who already hold an artifact file (downloaded/exported elsewhere)
have no way to bring it in from the browser. We want a **minimal, opt-in** dashboard upload that
does not fork the publish pipeline or weaken the serving trust model. Two forces shape the design:

- The publish path already does the right things — auth, a 25 MiB cap, render bundling for HTML
  (ADR-0010), private-by-default versioned storage (ADR-0013), audit — so a second path would be
  duplication and drift risk.
- Uploaded bytes must be served under exactly the same containment as any artifact: the
  `CanView`-gated raw path, on the separate content origin (ADR-0008), inside a sandboxed iframe
  under the artifact CSP. No new serving trust may be introduced.

---

## Decision

1. **Content-negotiate the existing `POST /artifacts`.** The handler detects
   `multipart/form-data` (a browser `<form>` upload) versus the current raw body (the CLI) and
   converges both on one shared `createArtifact(owner, title, ct, body)`. No new route, and the
   raw-body contract is unchanged. A plain full-page HTML form means **no client JavaScript** and
   therefore no dashboard CSP change.
2. **The content type is decided by the server**, from `http.DetectContentType` plus the filename
   extension, validated against an **allowlist** — never taken from the client. v1 allowlist:
   `text/html`, `application/pdf`, `image/png`, `image/jpeg`, `image/gif`, `image/webp`,
   `image/svg+xml`. Anything else is refused (`415`). HTML runs through `render.Bundle`
   (fail-soft, as today); non-HTML is stored as-is with its canonical type.
3. **Serving is unchanged.** Non-HTML inherits the existing sandboxed, CanView-gated, audited raw
   path with the stored content type. SVG script is contained by the same sandbox/CSP that already
   governs HTML artifacts (no new privilege).
4. **Opt-in.** The feature is gated by `ARTIFACTA_UPLOAD_UI` (default off): off means the dashboard
   renders no form and the multipart branch is refused. The raw CLI path is always available.

---

## Consequences

### Positive

- Reuses the whole publish/serve/blob/audit machinery — no parallel logic, minimal surface.
- One endpoint serves both the CLI (raw) and the browser (multipart); no JS, no CSP change.
- Broadening to static types is contained: the server decides the type against an allowlist, and
  bytes are served only through the existing sandbox — the threat model is unchanged.
- Dark by default; operators enable per deployment.

### Negative / Neutral

- `POST /artifacts` now branches on `Content-Type`; the handler carries a modest amount of new
  parsing (multipart, sniffing, allowlist). Covered by the spec's tests, incl. a raw-path
  regression.
- Non-HTML broadens the _kinds_ of bytes served. Mitigated by server-decided types + the existing
  sandbox; SVG in particular is called out for `/plan-design-review` (script-blocking CSP or
  `Content-Disposition` as belt-and-suspenders).
- Body is buffered (as publish does today) to bundle/sniff — bounded by the 25 MiB cap.

---

## Alternatives Considered

- **Dedicated `POST /artifacts/upload` endpoint.** Rejected: a new route with duplicated wiring
  for what is the same operation; content-negotiation keeps it to one contract.
- **JS `fetch` upload against the existing raw endpoint.** Rejected: needs inline/external script
  on the dashboard and CSP accommodation; a plain form is simpler and works with JS disabled.
- **HTML-only v1.** Considered; rejected in favor of a small static allowlist (PDF/images) because
  users bring more than HTML, and the sandbox already contains it safely.
- **Presigned / direct-to-storage upload.** Not applicable — ArtifactA forbids client-reachable
  presigned URLs (ADR-0006); bytes always pass through the app.

---

## References

- Spec: [5-ui-artifact-upload.md](../specs/5-ui-artifact-upload.md)
- [ADR-0006](0006-s3-blob-adapter-backend-only.md) (backend-only blob, no presigned URLs),
  [ADR-0008](0008-separate-content-origin.md) (separate content origin / sandbox),
  [ADR-0010](0010-render-parity-harness.md) (render bundling),
  [ADR-0013](0013-artifact-versioning.md) (versioned artifacts).
