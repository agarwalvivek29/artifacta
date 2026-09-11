# 0023 — Flag-based render egress + multi-format render parity

**Date**: 2026-09-11
**Status**: Accepted (implemented 2026-09-11)
**Deciders**: Vivek Agarwal
**Related**: [ADR-0010 — Viewer render-parity harness](0010-render-parity-harness.md) (supersedes its default), [ADR-0011 — VPN-fronted threat model](0011-vpn-fronted-threat-model.md)

---

## Context

ArtifactA renders self-contained HTML first-class, but lagged Claude Artifacts on type support:
React/JSX, Markdown, Mermaid, and correct handling of SVG and other file types. ADR-0010 chose
**bundle-at-publish** for render parity and left the dependency-vendoring step as an explicit
follow-up (the `collectBareImports` seam in `internal/render/render.go`). This ADR fills that seam
and adds Markdown/Mermaid/SVG.

Two facts shaped the design:

1. **The viewer injects `/raw` bytes via `frame.srcdoc = html + BRIDGE`** (`internal/web/embed/viewer.html`).
   A `srcdoc` document **inherits the embedder's (viewer-shell) CSP**, NOT the `/raw` response CSP.
   Any air-gap CSP tightening must therefore target the shell CSP (`server.go` `viewer`), not only
   `/raw` — the primary viewing path runs through the shell.
2. **The live `/raw` CSP already permitted `https:` egress.** So a CDN-importmap artifact would load
   _if_ the network has egress. Bundling exists so artifacts render even in a truly air-gapped
   (no-egress) VPN deploy — ADR-0010's core bet.

ADR-0010 explicitly **rejected** live CDN fetches ("breaks the VPN/no-third-party-egress posture").
PRODUCT.md frames no-egress as the product's reason to exist.

---

## Decision

### 1. An operator posture flag: `ARTIFACTA_CDN_EGRESS = allow | deny` (default **deny**)

The flag controls **both** how artifacts are rendered and the served CSP, kept in lockstep:

|                             | **deny** (default, air-gapped)                                                                                                         | **allow** (CDN egress)                                                                                          |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| HTML render                 | esbuild **Build**: transpile + **bundle** the inline module with vendored deps inlined; strip the import map; inline vendored Tailwind | esbuild **Transform**: transpile JSX/TSX only; **keep** imports + the import map; leave the Tailwind CDN script |
| Served CSP (shell + `/raw`) | strict — no external hosts (`img-src 'self' data:`, `script/style-src 'unsafe-inline'`, `connect-src 'self'`)                          | permissive `https:` (today's policy)                                                                            |
| Dependencies                | bounded vendored set only; others fail soft                                                                                            | any package the import map/CDN resolves                                                                         |

The default is **deny** to match the compliance/air-gap thesis (ADR-0010/0011). `deny` is the
posture that "never stored on, nor retrievable from, third-party infra" extends to at view time.

### 2. In-memory vendored dependency resolution (no temp dir)

esbuild runs in-process, so vendored packages are served straight from the `go:embed` FS through an
`OnResolve`/`OnLoad` plugin (`internal/render/deps.go`) — **no filesystem writes, no temp dir, no
`NodePaths`**. This deliberately avoids a materialize-to-`/tmp` step that would fail on a read-only /
distroless rootfs (exactly this feature's compliance audience). Vendored set (pinned React 18.3.1):
`react`, `react-dom`, `react-dom/client`, `react/jsx-runtime`, `react/jsx-dev-runtime`, `scheduler`,
plus the Tailwind Play-CDN compiler and the Mermaid runtime. "+ common-libs" is additive: drop a
package into the embedded set. The set is bounded by construction in air-gap mode; the "any package"
path is egress mode.

### 3. Markdown/Mermaid render to self-contained HTML at publish

A `text/markdown` artifact is rendered with goldmark (GFM) into a styled, self-contained HTML
document and stored as `text/html`, so it flows through the existing sandboxed viewer, CSP, comments,
and versioning unchanged. Fenced `mermaid` blocks become `<pre class="mermaid">` and, when
present, the vendored Mermaid runtime is inlined — identical in both postures (fully inlined, zero
egress). SVG and other types pass through with their correct content type (served on direct `/raw`).

### 4. Content-type at publish comes from the file extension

The CLI stopped hardcoding `text/html`; it derives the type from an authoritative override table
(`.md`→`text/markdown`, `.svg`→`image/svg+xml`, …) so the API renders/serves each type correctly.
The table is authoritative because the distroless prod image ships no `/etc/mime.types`.

---

## Consequences

### Positive

- Reaches Claude-Artifacts **type** parity (HTML, React/JSX, SVG, Markdown, Mermaid) with a bounded,
  auditable dependency set — and, in air-gap mode, zero third-party egress at view time.
- Air-gap is now actually enforced (the CSP fix targets the shell header the srcdoc inherits).
- Works on a hardened read-only/distroless container (in-memory resolution, no temp dir).

### Negative

- Vendored assets grow the binary (react-dom ~130 KB, Tailwind ~400 KB, Mermaid ~3.3 MB embedded).
- Two render code paths (bundle vs transpile) must both be exercised by the parity harness.

### Neutral / known limitations

- **Render mode is baked into stored bytes at publish; CSP derives from the current global flag.**
  Flipping the flag after artifacts exist mismatches them (an artifact published under `allow`, with
  a CDN import map, renders blank under a later `deny` CSP). Guidance: choose the posture before
  publishing; re-publish to convert. A **per-version render-mode field** on `ArtifactVersion` (+
  per-version CSP) is the correct long-term fix — deferred (proto + migration).
- **JSX runtime is classic**: artifacts must import React (Claude's do). Automatic-runtime-without-
  import is a future enhancement.
- **SVG/non-HTML render on direct `/raw`**, not through the HTML-first comment viewer (a viewer
  content-type branch is a follow-up).
- **Egress mode is single-file**: Transform does not bundle multi-file sources (matches Claude's
  single-file artifact shape).

---

## Alternatives Considered

### Default `allow` (backward-compatible)

Rejected: preserves today's permissive CSP but ships an out-of-box deploy that leaks egress and does
not enforce the air-gap — the opposite of the product's pitch. `deny` is the correct default; `allow`
is the documented opt-in.

### Materialize a `node_modules` tree to an OS temp dir + esbuild `NodePaths`

Rejected: needs a writable temp dir and fails on a read-only/distroless rootfs — the moment an
operator hardens the container (a compliance deploy) React bundling breaks and artifacts render
blank. In-memory `OnLoad` resolution has none of that dependency. (Cross-model review reached the
same conclusion independently.)

### Vendor only React, rely on CDN for Tailwind

Rejected for air-gap: Claude's React artifacts overwhelmingly style with the Tailwind Play CDN; under
`deny` that host is blocked and every React artifact renders unstyled. Vendoring the Tailwind compiler
keeps air-gap parity real.
