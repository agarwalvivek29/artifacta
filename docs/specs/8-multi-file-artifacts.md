# Spec: Multi-file artifacts (roadmap)

**Issue**: N/A (maintainer-requested roadmap)
**Status**: Proposed (design only — not yet scheduled)
**Author**: Vivek Agarwal
**Date**: 2026-09-11
**Services Affected**: `artifacta-api` (storage, publish, serving, viewer, schema)

---

## Summary

Let an artifact be **more than one file** — an entry document plus sibling assets (`app.js`,
`styles.css`, `data.json`, images, fonts) addressed by relative path — so ArtifactA can host built
SPAs, small static sites, and reports-with-assets, not only single self-contained files. This is the
next capability for a general "artifact hosting platform." It is a **deliberate, multi-layer change**
and is intentionally scoped out of the single-file upload button (ADR-0024) and the render-parity
work (ADR-0023).

---

## Background and Motivation

Today an artifact is exactly one blob per version: `Blob.Get(slug, n)` / `Blob.Put(slug, n, reader)`,
served at `/a/{slug}/raw` and injected into the viewer as `frame.srcdoc`. Anything referencing a
sibling file — `<script src="./app.js">`, `<img src="./logo.png">`, `fetch('./data.json')` — **404s**,
because there is no per-file addressing. This also forces the air-gap posture (ADR-0023) to inline or
`data:`-encode every asset; multi-file would let assets be first-class same-origin siblings.

The dominant Claude-artifact shape is single-file, which we support well. But a hosting platform that
wants to hold a `vite build` output, a mini docs site, or an artifact that loads a CSV needs
multi-file. That is the gap this spec captures.

---

## Scope

### In Scope (when scheduled)

- Publish a **set of files** (a directory, a zip, or multipart with multiple parts) as one artifact
  version, with one designated **entry** (default `index.html`).
- Address every file: `GET /a/{slug}/<path>` (and `/a/{slug}/v/{n}/<path>`), each `CanView`-gated,
  audited, and served with its own content type — same trust as today's `/raw`.
- Relative references inside the entry resolve to sibling files.
- Immutability per version (ADR-0013) extends to the whole file set.

### Out of Scope

- Server-side execution / SSR (artifacts stay static, sandboxed).
- A full build step on the server (we host built output; we don't run `npm build`).
- Per-file ACLs (visibility stays per-artifact).

---

## Design sketch (the five ripples)

1. **Storage** — key blobs by `(slug, version, path)` instead of `(slug, version)`. Either a
   path-aware `BlobStore` (`Put(slug, n, path, r)` / `Get(slug, n, path)`) or store one bundle
   (tar/zip) per version plus a manifest and serve entries out of it. The blob backends (fs + S3,
   ADR-0006) both extend naturally to a path prefix.
2. **Schema** — `ArtifactVersion` gains a **file manifest** (path → content-type, size) and an
   `entrypoint`. Proto change + regen (schema-first, Rule 12).
3. **Publish** — accept a directory/zip/multipart-many. CLI: `artifacta publish ./dir` (walk + upload)
   or `publish app.zip`. API: a bundle upload or repeated multipart parts. Content types decided
   server-side per file (extension allowlist, as ADR-0024 does for single uploads).
4. **Serving** — `GET /a/{slug}/<path>` resolves within the version, fails closed on traversal
   (`..`), 404s a missing path without leaking. `/raw` stays an alias for the entrypoint.
5. **Viewer** — the biggest ripple. `frame.srcdoc` has no base URL, so relative asset URLs can't
   resolve; switch to `<iframe src="/a/{slug}/">` (real same-origin base) inside the existing sandbox.
   That means the comment/anchor **BRIDGE** (currently injected into `srcdoc`) must be delivered
   differently (e.g. injected server-side into the served entry, or via a `postMessage` shim), and
   the CSP model re-validated for a real-origin document. This is where most of the risk lives.

```
publish ./site/  ──▶  POST /artifacts (bundle)         GET /a/{slug}/            → index.html (entry)
   index.html                │ manifest: {index.html,   GET /a/{slug}/app.js      → app.js
   app.js                    │            app.js,        GET /a/{slug}/logo.png    → logo.png
   logo.png            ──────┘            logo.png}      (each CanView-gated + audited + typed)
```

Bonus: with real sibling files, esbuild could bundle a genuine **multi-file** React app at publish,
retiring ADR-0023's "single-file only" egress limitation.

---

## Open questions (resolve at scheduling)

- Bundle-per-version (simpler storage, unpack on read) vs blob-per-path (simpler serving, more keys)?
- How the comment BRIDGE survives the srcdoc → `src` viewer change without weakening isolation.
- Entry selection: always `index.html`, or explicit at publish?
- Size/inode caps per artifact (total bytes, file count) to bound abuse.

---

## Rollout

Additive and versioned: existing single-blob artifacts keep working (a one-file manifest). Likely
gated behind its own toggle during dark-launch, and worth a dedicated ADR for the storage + viewer
decisions before implementation. Estimated a **multi-week** initiative (schema + storage + viewer
rework), not a single-PR change — hence tracked here rather than bundled into the upload button.
