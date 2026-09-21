# Spec: Edit artifact title/description + viewer-side new-version upload

**Issue**: #11 (TBD — GitHub issue not yet created)
**Status**: Review
**Author**: Vivek Agarwal
**Date**: 2026-09-22
**Services Affected**: artifacta-api

---

## Summary

Two owner-facing gaps closed for the 0.1.0 stable release:

1. An artifact's **title and description** could be set only at publish time — there was no way
   to edit them afterwards. Adds `PATCH /artifacts/{slug}`, an `artifacta edit` CLI command, and
   an owner-only Edit control in the viewer.
2. An owner **viewing** an upload-created artifact had no way to upload a new version from the
   viewer (only the dashboard offered it). Surfaces the existing new-version upload in the viewer.

---

## Background and Motivation

Title/description are display + **searchable** metadata (ADR-0025). Publishing a typo, or wanting
to describe an artifact after the fact, left the owner stuck — the only recourse was republishing
under a new slug. Editing them is a cheap, high-value mutation that touches no versioned bytes.

Separately, browser new-version upload already exists (ADR-0024, 0.0.14) but only on the
dashboard. A UI-first user viewing their own uploaded artifact reasonably expects to update it
right there rather than navigate back to the dashboard or drop to the CLI.

---

## Scope

### In Scope

- **`PATCH /artifacts/{slug}`** (owner-only) updating `title` and/or `description`. Partial
  update via JSON pointers: a present `title` is trimmed and must be non-empty; a present
  `description` is trimmed and may be empty (clears it); at least one field required. Emits a new
  **`AUDIT_ACTION_EDIT`** audit event. The versioned bytes are immutable and untouched.
- **`artifacta edit <slug> [--title ...] [--description ...]`** — remote-only, sends only the
  flags passed (so editing one field never blanks the other).
- **Viewer (owner-only)**: an Edit dialog (prefilled from metadata, PATCHes on save) and, for an
  upload-created artifact on a deployment with the upload UI enabled, an "Upload a new version"
  dialog that reuses the existing `POST /artifacts/{slug}/versions`. The viewer's metadata
  response gains `via_upload` and `upload_enabled` so the (static, non-templated) viewer can gate
  the button.

### Out of Scope

- Dashboard title/description editing (viewer-only for now; a possible fast-follow).
- Editing any immutable field (slug, versioned bytes, owner) or visibility/label (already have
  their own endpoints).

---

## Design

- **Schema-first**: add `AUDIT_ACTION_EDIT = 5` to `audit.proto` (additive, backward-compatible);
  regenerate. Editing is a mutation and is audited like every other; reusing `SHARE` would be
  semantically wrong.
- **Handler `updateArtifact`** mirrors `setVisibility`: `ownedArtifact` gate (401 unauth / 404
  non-owner-or-missing, fail-closed) → mutate `art.Title`/`art.Description` → `PutArtifact` →
  `audit(EDIT)` → 200 `{slug,title,description}`. Route `PATCH /artifacts/{slug}` sits below the
  more specific `/visibility` and `/label` PATCH routes (Go 1.22 precedence).
- **CLI `editRemote`** PATCHes the bare slug path with a `map[string]string` of only the set
  fields, matching the server's pointer-based partial update.
- **Viewer** reuses the Share dialog's overlay chrome and the `el`/`api`/`showToast` helpers; the
  new-version dialog reuses the dashboard's native multipart form → 303-back mechanism.

---

## Testing

- API: edit persists (both fields, partial, clear-description), empty-title → 400, empty body →
  400, non-owner → 404, unauthenticated → 401, an `AUDIT_ACTION_EDIT` row is written; metadata
  response includes `via_upload` + `upload_enabled`.
- CLI: `editRemote` PATCHes the right path with the Bearer token and sends only the set fields.
- Browser: against a mock backend serving the real `viewer.html` — owner sees Edit (+ new-version
  when upload-created and upload UI on); non-owner and CLI-created and upload-off cases hide the
  right controls; the Edit dialog prefills, PATCHes the exact body, guards empty titles; the
  new-version dialog submits a multipart POST and reloads.
