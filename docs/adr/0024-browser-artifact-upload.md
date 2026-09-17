# 0024 — Browser artifact upload (content-negotiated, toggle-gated)

**Date**: 2026-09-11
**Status**: Accepted (implemented 2026-09-11)
**Deciders**: Vivek Agarwal
**Related**: [spec 5](../specs/5-ui-artifact-upload.md), [ADR-0008 — Separate content origin](0008-separate-content-origin.md), [ADR-0013 — Artifact versioning](0013-artifact-versioning.md), [ADR-0023 — Flag-based render egress](0023-flag-based-render-egress-and-multiformat.md)

---

## Context

An artifact could be created in-app or pushed with `artifacta publish <file>`, but a signed-in user
with an existing artifact file had no way to bring it in through the browser. This is a small,
opt-in "I already have one, host it here" affordance next to the dashboard — deliberately minimal.

(Note: an earlier draft of spec 5 referenced this decision as ADR-0023; that number was taken by the
render-egress ADR shipped in v0.0.9, so the upload decision is recorded here as ADR-0024.)

---

## Decision

1. **Content-negotiated `POST /artifacts` — no new route; a native form submit.** The existing publish
   handler detects `multipart/form-data` (the browser form) versus a raw body (the CLI, unchanged).
   Both converge on one shared `storeArtifact` path: private-by-default v1, versioned (ADR-0013),
   `PUBLISH`-audited. HTML runs the same `render.Prepare` pipeline (ADR-0023) as any publish; the
   upload inherits the deployment's egress posture.
2. **Server-decided content type against an allowlist.** The stored type is derived from the file
   extension and validated against a fixed allowlist — `text/html`, `application/pdf`, `image/png`,
   `image/jpeg`, `image/gif`, `image/webp`, `image/svg+xml` — never taken from the client's multipart
   part header, so an upload can't assert an active type that escapes the sandbox. Anything else → 415.
3. **Gated behind `ARTIFACTA_UPLOAD_UI` (default off).** Off = the dashboard form isn't rendered and
   the multipart branch is refused (400); the raw-body CLI path is unaffected. Dark-launch per deploy.
4. **Same serving trust as every artifact.** Uploads are served only through the existing
   `CanView`-gated `/a/{slug}/raw` path — sandboxed iframe, artifact CSP, separate content origin
   (ADR-0008), audited. A PDF/image renders natively in the sandbox; nothing an upload can do exceeds
   an AI-generated artifact.
5. **CSRF backstop.** The multipart branch requires the request `Origin`/`Referer` (when present) to
   match the deployment host, on top of the already-`SameSite` session cookie.

---

## Consequences

### Positive

- Users can host an existing file in two clicks, reusing the whole publish/serve/audit machinery —
  no new storage, schema, or serving trust.
- Fully additive and dark by default: the raw CLI path is untouched and the surface stays off until
  an operator opts in.

### Negative

- One more request shape on `POST /artifacts` (multipart) to keep in mind when touching publish.

### Neutral / limits (v1)

- Single file only; **multi-file / folder / drag-and-drop is out of scope** (see the multi-file
  hosting spec) — it needs storage + srcdoc-viewer rework and is tracked separately.
- Uploading a _new version_ of an existing artifact from the UI is deferred (the API already supports
  `POST /artifacts/{slug}/versions`).
- Types beyond the allowlist (video, office docs, archives) are added later per demand.

---

## Alternatives Considered

### A dedicated `POST /uploads` route + client-side fetch

Rejected: a new route and client JS would mean a dashboard CSP change and a second code path to
authorize/audit. Content-negotiating the existing endpoint keeps one authorization + audit path and
needs no CSP change (a plain full-page form POST).

### Trust the client's multipart Content-Type

Rejected: a client could label an executable as an inert type (or vice-versa). Server-deciding the
type from the extension against an allowlist keeps the sandbox guarantees intact.

## Follow-up (0.0.14) — upload a new version

The browser upload path was extended from create-only to also **append a new version** of an
existing artifact: `POST /artifacts/{slug}/versions` accepts a multipart form (owner-only,
upload-UI on, same-origin) and appends an immutable v(n+1) through the same allowlist + render
pipeline. It is gated by a new `Artifact.via_upload` flag so that **only artifacts created via the
upload UI** are version-editable from the browser — a CLI/API-created artifact returns 409, keeping
its version history a CLI concern. Create and version-append share one `appendVersion` helper.
