# 0027 — External Artifact Links Open in a New Tab

**Date**: 2026-09-22
**Status**: Proposed
**Deciders**: Vivek Agarwal
**Issue**: Artifact viewer navigation containment (bug: clicking a link inside an artifact broke the viewer)

---

## Context

Artifacts are untrusted, user-authored HTML rendered inside a **null-origin sandboxed
iframe** in the viewer shell (`services/artifacta-api/internal/web/embed/viewer.html`):

```
sandbox="allow-scripts"
```

The isolation this buys us is deliberate (see ADR-0008 separate-content-origin and
ADR-0011 vpn-fronted-threat-model): the artifact can run scripts but has a null origin,
cannot reach the shell's cookies/storage, and — because `allow-top-navigation` and
`allow-popups` are absent — cannot navigate the top window or open windows.

That last property produced a poor behaviour for a **legitimate** case. A `srcdoc`
document inherits its base URL from the shell (`/a/<slug>`), and the frame has no valid
same-origin destination, so _relative_ links reloaded the whole viewer inside the frame
("artifacta inside artifacta"). Those are now contained (relative links/forms swallowed,
`#id` links scrolled, `<meta refresh>` stripped). But an **absolute external link**
(`https://example.com`) still navigated the small artifact frame **in place** — replacing
the artifact with the external site inside a frame that has no back button. The user's
only recovery is browser-back, which unloads the whole shell.

The expected behaviour — matching every comparable artifact/sandbox viewer (Claude.ai
artifacts, CodeSandbox, JSFiddle) — is for an external link to **open in a new tab**.

### Why not a parent-mediated `window.open`

Keeping the sandbox as-is and forwarding the click to the shell via `postMessage` does not
work reliably: user activation does **not** propagate across a `postMessage` boundary from
a null-origin frame, so the shell's `window.open` is treated as programmatic and blocked by
the popup blocker. The open must happen **inside the frame**, on the real user gesture,
which requires the frame's sandbox to permit popups.

### Why `allow-popups-to-escape-sandbox` and not `allow-popups` alone

With `allow-popups` only, the opened tab **inherits** the sandbox: it becomes a null-origin,
script-restricted top-level page. Real destinations (a Google Doc, a login page) break
because they cannot access their own origin. `allow-popups-to-escape-sandbox` makes the new
tab a normal browsing context so external sites work as they would from any link.

---

## Decision

Add `allow-popups allow-popups-to-escape-sandbox` to the artifact frame's `sandbox`, and in
the injected bridge intercept clicks on absolute web links (`http(s):` or protocol-relative
`//host`) to open them with:

```js
window.open(href, "_blank", "noopener,noreferrer");
```

- The **artifact frame itself is unchanged**: still `allow-scripts`, still null-origin. The
  new tokens only affect **windows the artifact opens**, never the frame's own privileges.
- `noopener` severs `window.opener` (prevents reverse tab-nabbing of the shell tab).
- `noreferrer` (plus the frame's existing `referrerpolicy="no-referrer"`) stops the internal
  `…/a/<slug>` URL leaking to the destination.
- Relative links and forms remain **swallowed** (contained); fragments still scroll; only
  absolute web links reach `window.open`. Other schemes (`mailto:`, `tel:`, `data:`,
  `javascript:`, `blob:`) keep their default in-frame behaviour.

---

## Consequences

### Positive

- External links behave as users expect — a new tab — without losing the artifact.
- The nesting/replace-in-frame failure mode for external links is gone.
- Isolation of the artifact frame is preserved; the change is scoped to opened popups.

### Negative

- The artifact can now open popups (including scripted `window.open` on a user gesture) and
  those popups escape the sandbox. Residual abuse surface: a malicious artifact could open a
  new tab to a chosen URL (phishing) or to inline `data:`/`blob:` content. This is the same
  capability any ordinary hyperlink already grants, and modern browsers block top-level
  `data:` navigation; `noopener` prevents the popup from manipulating the opener.
- Popups are still subject to the browser's popup blocker off a non-gesture path; a blocked
  `window.open` simply does nothing (the link does not open) — an acceptable degradation.

### Neutral

- Behaviour now depends on `window.open` semantics rather than in-frame navigation; covered
  by the viewer QA cases for fragment / relative / external / form links.

---

## Alternatives Considered

### Keep `sandbox="allow-scripts"`, forward clicks to the shell

Rejected: user activation does not cross the `postMessage` boundary, so the shell's
`window.open` is popup-blocked — external links would silently fail to open.

### `allow-popups` without escape

Rejected: the opened tab inherits the null-origin sandbox, breaking real external
destinations that need their own origin (auth, storage).

### Rewrite links to `target="_blank"` at load time

Equivalent end state but less robust for dynamically-added links and harder to constrain to
web schemes; intercepting the click centralises the policy (fragment / relative / external /
other-scheme) in one place.
