# Spec: Render parity — React/JSX, Markdown, Mermaid, SVG (flag-based egress)

**Issue**: #7
**Status**: Implemented
**Author**: Vivek Agarwal
**Date**: 2026-09-11
**Services Affected**: artifacta-api

---

## Summary

Extend the publish-time render pipeline to reach Claude-Artifacts **type** parity — React/JSX,
Markdown, Mermaid, and correct SVG/multi-type handling — governed by an operator posture flag
`ARTIFACTA_CDN_EGRESS=allow|deny` (default deny) that trades third-party egress for full
self-containment. See [ADR-0023](../adr/0023-flag-based-render-egress-and-multiformat.md).

---

## Background and Motivation

The feature-comparison gap with Claude Artifacts looked large but was shallow in the code: the render
pipeline already integrated esbuild (JSX transpile) and left a `collectBareImports` seam for
dependency vendoring. Compliance teams (the product's target) generate exactly the artifacts this
unlocks — dashboards and reports as React/HTML, docs as Markdown with Mermaid diagrams — and need them
hosted with no third-party egress. Without this, those artifacts render blank or unstyled on a
self-hosted, air-gapped ArtifactA.

---

## Scope

### In Scope

- Operator flag `ARTIFACTA_CDN_EGRESS=allow|deny` (default deny) gating render strategy AND served CSP.
- Air-gap (deny) HTML: bundle inline module + vendored deps (React 18.3.1 family) via an in-memory
  esbuild plugin; strip import map; inline vendored Tailwind compiler.
- Egress (allow) HTML: transpile only; keep imports + import map; keep Tailwind CDN.
- Markdown → self-contained styled HTML (goldmark GFM); Mermaid fences → inlined vendored runtime.
- Content-type at publish derived from file extension (authoritative table); SVG/other passthrough.
- CSP tightened in deny mode on BOTH the viewer-shell header (inherited by srcdoc) and `/raw`.

### Out of Scope

- Per-version render-mode field on `ArtifactVersion` + per-version CSP (documented flip-flag
  limitation; correct long-term fix, deferred — proto + migration).
- Viewer content-type branch for non-HTML (SVG renders on direct `/raw` for now).
- "+ common-libs" vendoring (recharts/lucide/…): additive later.
- Arbitrary offline npm resolution (that is egress mode by design).
- Runtime capabilities (state persistence, page-calls-model).

---

## Design

Entry point generalized to `render.Prepare(input, contentType, Options{Egress})`, dispatching on
content type: markdown → `renderMarkdown` (→ text/html); html → `bundleHTML` (air-gap bundle vs
egress transpile); else passthrough. Vendored deps served in-memory from `go:embed` via an
`OnResolve`/`OnLoad` plugin (`internal/render/deps.go`) — no temp dir, so it works on a read-only /
distroless rootfs. The API threads `Options{Egress: s.Egress}` at both publish paths and derives the
CSP from the same flag via `Server.contentCSP`.

```
publish ─▶ Prepare(bytes, ct, {Egress})
             ├─ text/markdown ─▶ goldmark(GFM) + inline Mermaid ─▶ text/html
             ├─ text/html ──┬─ deny: esbuild Build + vendored deps + Tailwind inline ─▶ self-contained
             │              └─ allow: esbuild Transform, keep imports + importmap
             └─ else ───────▶ passthrough (svg, …)
serve ─▶ CSP = contentCSP(Egress): deny=no-external-hosts, allow=permissive https:
```

Everything is fail-soft: a render error stores the original bytes plus a warning, never failing the
publish.

---

## Testing

- Unit (`internal/render`): react artifact bundles self-contained (vendored React inlined, no bare
  imports / import map remain); same artifact in egress keeps imports + import map, inlines nothing;
  Tailwind inlined (deny) / kept (allow); markdown→HTML; mermaid fence inlines runtime; svg
  passthrough; unresolved non-vendored import fails soft.
- Unit (`internal/cli`): content-type derived per extension; remote publish sends derived type.
- Unit (`internal/api`): CSP pinned for both postures on the shell and `/raw`.
- Manual/E2E: `artifacta serve` under each posture; publish a React (Tailwind) artifact, a `.md` with
  a Mermaid block, and a `.svg`; confirm React mounts + styled, Mermaid draws, SVG renders.

---

## Rollout

Default `deny` is backward-safe for the compliance posture; existing artifacts that relied on CDN
egress should be published under `allow` or re-published. Flag documented in `.env.example`. The
server prints the active posture at startup.
