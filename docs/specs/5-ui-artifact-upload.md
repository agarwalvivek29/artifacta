# Spec: Upload an artifact from the dashboard UI

**Issue**: N/A (maintainer-requested)
**Status**: Implemented (2026-09-11)
**Author**: Vivek Agarwal
**Date**: 2026-09-11
**Services Affected**: `artifacta-api` (dashboard `web/`, the `POST /artifacts` handler, raw serving, config)

---

## Summary

Add a **minimal, opt-in** "Upload" affordance to the dashboard so a signed-in user can bring an
existing artifact **file** (HTML, or a small allowlist of static types) into ArtifactA, instead
of only creating one in-app or publishing via the CLI. It reuses the existing publish pipeline —
the upload becomes an ordinary private, versioned artifact. Gated behind a config toggle.

---

## Background and Motivation

Today an artifact is either created in-app or pushed with `artifacta publish <file>`. Users who
already have an artifact file (downloaded/exported from somewhere) have no way to bring it in
through the browser. "Create an artifact" covers authoring, but not "I already have one, host it
here." Vivek asked for this as a **minimal add-on** next to the create flow — available, opt-in.

---

## Decisions (confirmed)

1. **File types: HTML + a static allowlist** — `text/html`, `application/pdf`, `image/png`,
   `image/jpeg`, `image/gif`, `image/webp`, `image/svg+xml`. Everything else is rejected in v1.
2. **Endpoint: content-negotiate `POST /artifacts`** — the same endpoint detects
   `multipart/form-data` (browser) vs a raw body (CLI). No new route, no client JS.
3. **Rollout: behind a toggle** — `ARTIFACTA_UPLOAD_UI` (default off). Off = the form isn't
   rendered and the multipart branch is refused.

---

## Scope

### In Scope

- A file picker + "Upload" button on the authenticated dashboard, shown only when the toggle is on.
- Uploading a file of an **allowlisted type**; it becomes a private-by-default, versioned artifact
  owned by the uploader — identical lifecycle to a CLI publish.
- Reusing the existing size cap, storage, and audit path; HTML additionally runs `render.Bundle`.
- Serving uploaded non-HTML through the **same** CanView-gated, sandboxed, separate-origin path
  with the stored content type.
- Clear, up-front presentation of constraints (max size, accepted types, private-by-default) and
  clear error states (too large, wrong type, not signed in).

### Out of Scope (v1, deferred)

- Types beyond the allowlist (video, office docs, archives) — add later per demand.
- Drag-and-drop, multi-file, or folder upload — a single file input is enough for v1.
- Uploading a **new version** of an existing artifact from the UI (API supports it via
  `POST /artifacts/{slug}/versions`; a UI for it is a follow-up).
- Client-side JavaScript — v1 is a plain HTML form, so it needs no dashboard CSP change.

---

## Acceptance Criteria

- [ ] With the toggle **on** and a signed-in user, picking an allowlisted file and submitting
      creates a new **private** artifact (v1) owned by them, served with the correct content type;
      they land on/see its link.
- [ ] With the toggle **off**, the dashboard shows no upload form and a multipart `POST /artifacts`
      is refused (400/404) — the raw CLI path still works.
- [ ] A file over the 25 MiB cap → **413**, clear "file too large (max 25 MiB)" message, nothing stored.
- [ ] A non-allowlisted type → rejected with a clear message, nothing stored.
- [ ] An unauthenticated upload → fails closed (**401** / sign-in) — no anonymous uploads.
- [ ] An uploaded HTML file runs through `render.Bundle` and serves under the same strict CSP as an
      AI-generated artifact; an uploaded PDF/image serves with its own content type in the
      sandboxed viewer. Both go through the existing `CanView` gate + audit — no new serving trust.
- [ ] Constraints (max size, accepted types, "private by default") are shown next to the control
      **before** submit.
- [ ] The existing raw-body `POST /artifacts` (CLI) is unchanged (regression test).

---

## Technical Design

### Content-negotiated endpoint (no new route, no JS)

`POST /artifacts` already authenticates, reads the body under the 25 MiB `maxBytes` guard, runs
`render.Bundle`, stores at v1 private, audits `PUBLISH`. Extend it:

```
POST /artifacts        (session cookie for the browser; Bearer for the CLI — unchanged)
  Content-Type: multipart/form-data          ← NEW browser upload (only when ARTIFACTA_UPLOAD_UI)
    part "file"  = the artifact bytes (+ filename)
    part "title" = optional; defaults to the filename
  Content-Type: <anything else>              ← existing raw-body path (CLI), UNCHANGED
```

Refactor the current body of `publish` into a shared `createArtifact(owner, title, ct, body)` so
both paths converge on one code path:

```
publish(w, r):
  who := Identify(r); if !ok → 401
  if multipart/form-data:
      if !uploadUI → 400 "upload disabled"
      f, hdr := r.FormFile("file")                 # under the same maxBytes guard → 413 on overflow
      body := read f
      ct := detectContentType(body, hdr.Filename)  # server-decided; see allowlist
      if ct not in allowlist → 415 "unsupported type"
      title := form "title" or hdr.Filename
  else:                                             # current CLI raw path, unchanged
      title := ?title; ct := header; body := read r.Body
  → createArtifact(owner, title, ct, body)
  browser (multipart): 303 redirect to /a/<slug>
  api (raw):           201 {slug, url}
```

`createArtifact`: HTML → `render.Bundle` (fail-soft, as today); non-HTML → store bytes as-is with
the detected content type. Then `Blob.Put` → `AddVersion` → `PutArtifact(private, v1)` → audit.

**Content-type is server-decided**, from `http.DetectContentType` on the first 512 bytes plus the
filename extension, validated against the allowlist — never taken raw from the client. Store the
canonical type (`text/html; charset=utf-8`, `application/pdf`, `image/png`, …).

### Serving (reuse the existing raw path)

`GET /a/{slug}/raw` already serves the artifact's stored `ContentType` through the CanView gate on
the separate content origin (ADR-0008) inside a sandboxed iframe under the artifact CSP. Non-HTML
inherits this unchanged: a PDF renders in the browser's PDF viewer, an image renders as an image,
all sandboxed. SVG is served as `image/svg+xml`; any script it carries is contained by the same
sandbox/CSP that already governs HTML artifacts (no new privilege).

### The UI (dashboard `internal/web/`)

Rendered only when `ARTIFACTA_UPLOAD_UI` is on — a plain form near the "Mine"/create area:

```
[ Upload an artifact ]
  <form method="POST" action="/artifacts" enctype="multipart/form-data">
    <input type="file" name="file"
           accept=".html,.htm,text/html,application/pdf,image/png,image/jpeg,image/gif,image/webp,image/svg+xml" required>
    <input type="text" name="title" placeholder="Title (optional)">
    <button>Upload</button>
  </form>
  small print: "HTML, PDF, or image, up to 25 MiB. Private by default — you control who sees it."
```

Full-page form POST → no fetch/inline JS → no dashboard CSP change. `DashboardData` gets an
`UploadEnabled bool` the template branches on.

### Config

Add `UploadUI bool` (`ARTIFACTA_UPLOAD_UI`, default false) to `config.Config`; `serve()` sets it
on the `Server`. The `Server` carries `UploadUI` — the handler gates the multipart branch and the
dashboard passes it into `DashboardData`.

### Constraints and how they are presented

| Constraint             | Enforcement                                       | Presented to the user                                              |
| ---------------------- | ------------------------------------------------- | ------------------------------------------------------------------ |
| Max **25 MiB**         | existing `maxPublishBytes` / `maxBytes` → 413     | small print + clear 413 message                                    |
| **Allowlisted types**  | server-side `detectContentType` + allowlist → 415 | small print "HTML, PDF, or image"; `accept=` hint; clear rejection |
| **Private by default** | `createArtifact` sets `VISIBILITY_PRIVATE`        | small print "Private by default — you control who sees it"         |
| **Signed-in only**     | `Identify` → 401 / sign-in                        | anonymous root already shows the sign-in page                      |
| **Feature off**        | `UploadUI` gate → no form, multipart refused      | form simply absent                                                 |

---

## Security Considerations

- **Same serving trust as every artifact.** Uploads are served only through the existing
  `CanView`-gated `/a/{slug}/raw` path — sandboxed iframe, artifact CSP, separate content origin
  (ADR-0008), audited. Nothing an upload can do exceeds an AI-generated artifact.
- **Content type is server-decided** (sniff + extension against an allowlist), never the client's
  header, so an upload can't set an active type that escapes the sandbox. Non-allowlisted → 415.
- **SVG**: served `image/svg+xml`; embedded script is contained by the same sandbox/CSP as HTML
  artifacts (no new privilege). If review wants belt-and-suspenders, serve images with
  `Content-Disposition` or a script-blocking CSP — decide in `/plan-design-review`.
- **Size cap** (25 MiB) reuses the guard → 413 (bytes are buffered for bundling/sniffing, as today).
- **Title** is user-controlled but rendered escaped in the dashboard (existing behavior/tests).
- **Auth**: session-required; endpoint fails closed (401) for anonymous.
- **CSRF**: a state-changing cookie-authed browser POST — add an origin/referer check on the
  multipart branch (the session cookie is already `SameSite`); confirm in review.
- **Toggle default off** keeps the surface dark until an operator opts in.

---

## Observability

- Reuse the existing `PUBLISH` audit event — an upload is a publish, correctly indistinguishable.
- `/metrics` (0.0.5) already counts the `POST /artifacts` route; the multipart branch inherits it.
- Log auth method + subject on success (existing); never log file bytes or query strings.

---

## Testing Plan

- **api unit/integration:** multipart upload of HTML → 201/redirect, stored private v1, bytes
  served, `PUBLISH` audited; multipart upload of PDF + PNG → correct stored content type, served
  back; oversize → 413 (nothing stored); non-allowlisted type → 415; anonymous → 401; toggle off
  → multipart refused; raw-body CLI path unchanged (regression).
- **e2e:** browser-style multipart POST → `GET /a/{slug}/raw` as owner returns the bytes with the
  right content type; anon → 404.
- **dashboard render:** form present with constraint copy when toggle on; absent when off; anon
  root doesn't leak it.
- Run `/plan-design-review` (UI) before implementing; `/qa` after.

---

## Migration / Rollout Plan

- **Breaking changes:** none — additive. Raw-body publish untouched; multipart branch is new and
  gated. No schema/proto change, no migration.
- **Feature flag:** `ARTIFACTA_UPLOAD_UI` (default off) — dark-launch, enable per deployment.
- **Rollback:** revert the PR; nothing persisted is version-specific.

---

## References

- Reuses `s.publish` (`internal/api/server.go`), the 25 MiB `maxBytes` guard, `render.Prepare`
  (the egress-aware successor to `render.Bundle`, ADR-0010/ADR-0023), the raw serving path, the blob
  backend (ADR-0006), and the dashboard (`internal/web/`). Both the CLI and upload paths share
  `Server.storeArtifact`.
- Decision recorded in [ADR-0024](../adr/0024-browser-artifact-upload.md) (content-negotiated
  endpoint, server-decided content-type allowlist, toggle gate, CSRF backstop).
- Related ADRs: [0008](../adr/0008-separate-content-origin.md) (content origin / sandbox),
  [0013](../adr/0013-artifact-versioning.md) (versions).
