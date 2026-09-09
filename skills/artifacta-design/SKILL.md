---
name: artifacta-design
description: >
  Load this BEFORE authoring any artifact — an HTML page, report, dashboard, doc, slide,
  or data viz — that will be published to ArtifactA (the here.now host). It does two jobs:
  (1) pushes the design past the bare-minimum an LLM emits by default, calibrated to the
  content; (2) makes the artifact valid and resilient inside ArtifactA's sandboxed viewer
  (null-origin iframe, strict CSP, no host runtime, single file) and compliant with its
  data-governance rules (no phone-home). Fires on: "publish to ArtifactA / here.now",
  "make/build an artifact, page, report, or dashboard to host on ArtifactA", or right
  before `artifacta publish`. This is ArtifactA's equivalent of Claude's /artifact-design.
---

# artifacta-design — design artifacts worth hosting

An LLM asked for "a page" ships the default: one system font, a flat wall of text, no
hierarchy, no theme, the same blue link. On ArtifactA that default is worse than bland —
half of it silently breaks in the sandbox. This skill fixes both. Read it before you write
the first line of the artifact, not after.

You have two jobs. Do both.

---

## Job 1 — Design it well (calibrate; don't ship the default)

Approach it as a design lead: make deliberate, subject-specific choices. Match the
**treatment** to what the artifact is — a memo, an internal report, a dashboard, a landing
page each want a different level of polish. Utilitarian is fine; _unconsidered_ is not.

**The anti-bare-minimum bar.** Before it's done, every artifact has all of these:

- **A type system.** Pair a display face and a body face (a mono too if there's code/data).
  Set a type scale and stay on it. `text-wrap: balance` on headings, ~65ch body measure,
  letter-spacing on uppercase labels. Never leave it at unstyled `system-ui` 16px.
- **A chosen palette (4–6 named tokens).** Neutrals with a slight hue bias toward the accent
  read as chosen; pure `#888` grey reads as inherited. One accent, used with restraint.
  Semantic colors (good/warn/critical) are separate from the accent.
- **Both light and dark**, driven by `prefers-color-scheme` (the viewer does NOT inject a
  theme — see Job 2). Define tokens once; never declare a color only inside the dark block.
- **Real hierarchy and spacing.** Lay out with flex/grid + `gap`, not stray margins. Wide
  content (tables, code, diagrams) scrolls inside its own `overflow-x:auto` box; the page
  never scrolls sideways. `font-variant-numeric: tabular-nums` for aligned digits.
- **Real content.** Use the actual data/copy, never lorem. Words are design material:
  active voice, specific labels, name things as the reader recognizes them.
- **Responsive + accessible.** Works at 360px wide. Visible keyboard focus. Tap targets
  ≥44px, no hover-to-discover (mobile has no hover). Body/background contrast ≥ 4.5:1.
- **States, if interactive.** Loading, empty, and error each get a designed treatment, not
  a blank or a raw string.

**Avoid the AI-slop tells** (unless the user asked for one): warm-cream + serif + terracotta;
near-black with a lone acid-green pop; the purple→blue gradient hero; Inter/Space Grotesk as
the "safe" face; emoji as section bullets; everything centered; `rounded-lg` on every card.
Spend boldness in one place and keep the rest quiet.

**Method:** pick the subject and audience, sketch a 3-line plan (color / type / layout),
then build to it. Charts/tables get the same care as type — see the `dataviz` guidance if
you're plotting anything.

---

## Job 2 — Make it valid inside ArtifactA's sandbox (this is the part that breaks)

ArtifactA serves your artifact inside a **sandboxed, null-origin iframe** (`sandbox
allow-scripts`, no `allow-same-origin`) under a strict Content-Security-Policy. Design within
this contract or the artifact renders broken:

- **Storage APIs throw.** `localStorage`, `sessionStorage`, `indexedDB`, `document.cookie`,
  the Cache API — all unavailable in a null origin and they _throw_, not return null. Keep
  state in memory for the life of the view. If you must touch storage, wrap every access in
  `try/catch` and render correctly with nothing stored. Never gate core content on it.
- **No host runtime.** There is no `window.claude`, no parent messaging, no top-level
  navigation, no injected capabilities. The artifact is fully self-contained and standalone.
- **One file.** Everything is a single HTML document — inline your CSS and JS, or load it at
  runtime over https. No sibling files, no multi-asset bundles.
- **CSP: inline is fine, `eval` is not.** Inline `<style>` and `<script>` work. External
  `https:` styles, scripts, fonts, images, and `fetch`/XHR work (egress is allowed). But
  there is **no `unsafe-eval`** — anything that compiles code at runtime is blocked. The big
  trap: **the Tailwind Play CDN (`cdn.tailwindcss.com`) uses `eval` and will silently do
  nothing.** Ship precompiled/hand-written CSS instead. Same for any template engine or
  charting lib that `eval`s. `data:` URLs are allowed for images/fonts, not for scripts.
- **ES modules are bundled at publish.** An inline `<script type="module">` with
  self-contained code is bundled and inlined for you. A module that imports a _bare_
  specifier (`import React from "react"`) with nothing to resolve it is stored unbundled and
  won't run — use an ESM CDN URL (`https://esm.sh/react`) in an import-map, or keep the
  module dependency-free.
- **Egress is allowed, but design to degrade.** You may pull Google Fonts, an ESM CDN, or
  images over https at view time. But a security-minded operator may run ArtifactA fully
  air-gapped — so **never let a CDN be load-bearing for legibility.** Always give fonts a
  real fallback stack; feature-detect; make the artifact readable if every external request
  fails. Inline what actually matters.
- **Own your theme.** The viewer is a bare frame — it does not stamp a theme on you. Respond
  to `prefers-color-scheme` yourself and paint an explicit background (a transparent body
  borrows the host ground and can render one theme's text on the other's ground).

### Data-governance rules — non-negotiable on ArtifactA

ArtifactA exists so sensitive data stays on infrastructure the operator owns. An artifact
that phones home defeats the entire product.

- **No third-party analytics, trackers, telemetry, pixels, beacons, or "phone-home"** of any
  kind. `connect-src https:` is open _technically_ — do not use it to exfiltrate. The only
  network calls an artifact makes are to fetch its own presentation assets (fonts, libs).
- Assume the artifact may carry **PII or confidential data**. No external logging. If it's
  sensitive, say so on the page (a `CONFIDENTIAL` marker) and keep it print-clean.

---

## A valid, theme-aware starting skeleton

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Specific Artifact Name</title>
    <!-- Google Fonts OK (egress), but the fallback stack must carry it alone -->
    <link
      rel="stylesheet"
      href="https://fonts.googleapis.com/css2?family=…&display=swap"
    />
    <style>
      :root {
        --bg: #f6f7fb;
        --ink: #14142b;
        --muted: #585a72;
        --line: #e3e5ef;
        --accent: #e8265c;
      }
      @media (prefers-color-scheme: dark) {
        :root {
          --bg: #0e0e24;
          --ink: #eaebf6;
          --muted: #a6a9c8;
          --line: #2a2a54;
          --accent: #ff5c82;
        }
      }
      body {
        margin: 0;
        background: var(--bg);
        color: var(--ink);
        font:
          16px/1.6 "Your Body Face",
          system-ui,
          sans-serif;
      }
    </style>
  </head>
  <body>
    <!-- real content, real hierarchy -->
    <script>
      // in-memory only; storage throws in the sandbox
      try {
        /* optional localStorage read */
      } catch {
        /* render fine without it */
      }
    </script>
  </body>
</html>
```

---

## Pre-publish checklist (run before `artifacta publish`)

- [ ] Treatment calibrated to the content; not the LLM default.
- [ ] Type system + chosen palette + both themes present; scans cleanly at 360px.
- [ ] No Tailwind Play CDN or any `eval`-based lib; CSS is precompiled/inline.
- [ ] No `localStorage`/cookies gating content; any storage access is `try/catch`.
- [ ] No external analytics/trackers/beacons; the only network calls fetch presentation assets.
- [ ] Fonts have real fallback stacks; the page is legible with every external request blocked.
- [ ] Single self-contained file; body sets an explicit background; keyboard focus is visible.
- [ ] Real content throughout; confidentiality marked if the data warrants it.

Then publish: `artifacta publish <file>` (see the `artifacta-publish` skill).
