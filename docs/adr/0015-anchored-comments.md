# 0015 — Anchored comments (comment on selected text)

**Date**: 2026-08-25
**Status**: Accepted
**Deciders**: Vivek Agarwal
**Issue**: N/A
**Relates to**: [0014](./0014-artifact-comments.md) (adds an anchor to the comment),
[0013](./0013-artifact-versioning.md) (anchors resolve against immutable version content),
[0004](./0004-single-file-bundle.md) / [0010](./0010-render-harness.md) (the served bundle we
inject the bridge into), [0008](./0008-content-origin.md) (the sandbox we postMessage across)

---

## Context

Comments shipped (ADR-0014) as a flat, version-pinned list in a side drawer. That is not enough:
a note like "this number looks wrong" is meaningless if the reader can't tell _which_ number.
Every tool that does this well — Notion, Google Docs, Claude's own artifacts, hypothes.is —
**anchors the comment to a selected span of text**: you highlight, a "＋ Comment" affordance
appears, the comment binds to that span, the span is highlighted in the document, and clicking
either the highlight or the comment focuses the other.

Two facts about our system shape the design:

- **The artifact is sandboxed.** It renders in an `<iframe sandbox="allow-scripts">` via `srcdoc`
  — a **null (opaque) origin**. The parent viewer shell therefore **cannot** read
  `iframe.contentDocument` or the selection inside it. Cross-frame `postMessage` is the only
  channel (ADR-0008 keeps this boundary; we do not add `allow-same-origin`).
- **Version content is immutable** (ADR-0013). A comment is already pinned to a version, and that
  version's bytes never change. So a _content-based_ anchor (the quoted text + surrounding
  context) can never drift — the fragility that plagues DOM-path anchors on living documents does
  not exist here.

---

## Decision

Anchor each comment to a text selection, using a content-based anchor and a postMessage bridge.

### 1. Anchor model — W3C-style text anchor (content-based, not DOM paths)

Extend `Comment` (schema-first) with an optional `TextAnchor`:

```
message TextAnchor {
  string quote  = 1;  // the exact selected text
  string prefix = 2;  // a few chars immediately before (disambiguates repeats)
  string suffix = 3;  // a few chars immediately after
  int32  start  = 4;  // char offset into the document's text content (fast path)
  int32  end    = 5;
}
message Comment { ... existing ... ; TextAnchor anchor = 9; }
```

- **Content-based, not structural.** We store what was quoted (plus a little context and a
  position hint), never a CSS/XPath into the artifact's DOM. Re-locating is: try `start..end`,
  verify the text there equals `quote`; if not, search for `prefix+quote+suffix`, then `quote`.
- **Anchor optional.** A comment with no anchor is a **page-level** note (the existing behaviour),
  still supported. Selection-anchored is the primary path; page-level is the fallback.
- Because content is immutable per version, an anchor made on vN stays valid on vN forever.
  Comments and their anchors do not migrate across versions — feedback on v1 stays on v1.

### 2. The sandbox bridge — a tiny injected script + a fixed message protocol

At serve time (`serveVersion`), append a small, self-contained `<script>` to the bundle before it
is handed to the iframe. It is the **only** code that can see the selection, because it runs
inside the null-origin document. Protocol (all messages carry a `source:"artifacta"` tag; the parent
validates shape and **never trusts them for authorization** — see Security):

- iframe → parent `hn-selection`: `{ quote, prefix, suffix, start, end, rect }` on select; `rect`
  (in iframe viewport coords) lets the parent place the "＋ Comment" button. Empty selection →
  `hn-selection-cleared`.
- parent → iframe `hn-anchors`: `[{ id, quote, prefix, suffix, start, end, resolved }]`. The
  bridge locates each and wraps it in `<mark class="hn-hl" data-id>` (resolved → dimmed). This is
  a client-side, in-DOM decoration only; it never mutates stored content.
- iframe → parent `hn-focus`: `{ id }` when a highlight is clicked → parent scrolls the drawer to
  that comment.
- parent → iframe `hn-scroll`: `{ id }` when a comment is clicked → bridge scrolls the mark into
  view and pulses it.

No CSP change: the bridge is inline script, already permitted by `script-src 'unsafe-inline'`, and
`postMessage` is not gated by CSP. The iframe stays `allow-scripts` only.

### 3. Viewer shell UX (Notion-like)

Select text in the artifact → floating **＋ Comment** button at the selection → composer opens
bound to that anchor → posting shows the quote above the comment and paints the highlight.
Clicking a comment scrolls+pulses its highlight; clicking a highlight focuses its comment.
Resolving dims the highlight (owner-only resolve is unchanged from ADR-0014).

---

## Consequences

### Positive

- Comments become legible: every note points at exactly the text it's about, both ways.
- Content-based anchoring + immutable versions = anchors that never rot; no re-anchoring engine.
- No new trust surface: authorship/authz stay server-side (ADR-0014); the bridge is display-only.

### Negative

- We inject code into the served artifact. It runs in the untrusted sandbox alongside artifact JS
  (see Security). The bridge must be tiny, dependency-free, and defensive.
- Locating a quote is O(document length) per anchor; fine for single-file artifacts, revisit if
  documents get large.

### Neutral

- The two pre-existing (pre-anchor) comments render as page-level notes — graceful, no migration.

## Security

The bridge runs in the **null-origin sandbox**, so a malicious/buggy artifact can read its own
selection and forge `hn-selection`/`hn-focus` messages. This is contained by design:

- The parent treats every inbound message as **untrusted display metadata**: it validates the
  `source` tag + shape, and uses it only to position UI and label a draft. Comment creation is
  still authenticated server-side from the session (never from the iframe), and the anchor is just
  text the server stores verbatim.
- Worst case: a hostile artifact spoofs an anchor and mislocates a highlight in _its own_ view.
  No cross-artifact or cross-user impact; and the artifact author is the owner regardless.
- The parent posts `hn-anchors` with `targetOrigin` appropriate to the srcdoc frame and ignores
  messages whose `event.source` is not the artifact frame.

## Alternatives Considered

### DOM-path / block-id anchors (Notion's real model)

Rejected: Notion anchors to block IDs because its documents are structured and mutable. Our
artifacts are arbitrary immutable HTML with no block model, so a content quote is both simpler and
more robust here.

### `allow-same-origin` so the parent can read the selection directly

Rejected: that collapses the sandbox (ADR-0008) — the artifact could then reach the parent's DOM,
cookies, and same-origin APIs. The postMessage bridge keeps full isolation.

### Store a rendered-range/XPath instead of a quote

Rejected: brittle even on immutable content (whitespace/normalisation), and leaks DOM structure
into the data model. Text-quote + position hint re-locates deterministically.
