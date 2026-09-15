package render

import (
	"strings"
	"testing"
)

const htmlCT = "text/html; charset=utf-8"
const mdCT = "text/markdown; charset=utf-8"

// Markdown is rendered to a self-contained HTML document at publish time and
// re-labeled text/html so it flows through the HTML viewer. GFM features render.
func TestPrepareMarkdownToHTML(t *testing.T) {
	in := []byte("# Title\n\nSome **bold** text and a [link](https://x).\n\n| a | b |\n|---|---|\n| 1 | 2 |\n")

	out, ct, warnings, err := Prepare(in, mdCT, Options{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ct != htmlCT {
		t.Fatalf("content type: got %q, want %q", ct, htmlCT)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	got := string(out)
	if !strings.Contains(got, "<!doctype html>") {
		t.Fatalf("markdown not wrapped in an HTML shell:\n%s", got)
	}
	if !strings.Contains(got, "<h1") || !strings.Contains(got, "Title</h1>") {
		t.Fatalf("heading not rendered:\n%s", got)
	}
	if !strings.Contains(got, "<strong>bold</strong>") {
		t.Fatalf("bold not rendered:\n%s", got)
	}
	if !strings.Contains(got, "<table>") {
		t.Fatalf("GFM table not rendered:\n%s", got)
	}
	// No mermaid used → the (large) mermaid runtime must NOT be inlined.
	if strings.Contains(got, "mermaid.initialize") {
		t.Fatalf("mermaid runtime inlined when no diagram present")
	}
}

// A ```mermaid fence becomes a <pre class="mermaid"> block and the vendored
// mermaid runtime is inlined with an init call — no external fetch.
func TestPrepareMarkdownMermaid(t *testing.T) {
	in := []byte("# Diagram\n\n```mermaid\ngraph TD; A-->B;\n```\n\ntrailing text\n")

	out, ct, _, err := Prepare(in, mdCT, Options{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ct != htmlCT {
		t.Fatalf("content type: got %q, want %q", ct, htmlCT)
	}
	got := string(out)
	if !strings.Contains(got, `<pre class="mermaid">`) {
		t.Fatalf("mermaid fence not converted to a mermaid pre block:\n%s", firstN(got, 800))
	}
	if !strings.Contains(got, "graph TD; A--&gt;B;") {
		t.Fatalf("diagram source not preserved (html-escaped) in the block:\n%s", firstN(got, 800))
	}
	if !strings.Contains(got, "mermaid.initialize") {
		t.Fatalf("mermaid runtime/init not inlined:\n%s", firstN(got, 400))
	}
	// The diagram must NOT be rendered as a normal code block.
	if strings.Contains(got, "language-mermaid") {
		t.Fatalf("mermaid rendered as a plain code block:\n%s", firstN(got, 800))
	}
	if !strings.Contains(got, "trailing text") {
		t.Fatalf("surrounding markdown lost:\n%s", firstN(got, 800))
	}
}

// A non-HTML, non-markdown type (SVG) passes through untouched with its content
// type preserved.
func TestPrepareSVGPassthrough(t *testing.T) {
	in := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="10" height="10"/></svg>`)

	out, ct, warnings, err := Prepare(in, "image/svg+xml", Options{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if string(out) != string(in) {
		t.Fatalf("svg passthrough altered input")
	}
	if ct != "image/svg+xml" {
		t.Fatalf("content type changed: %q", ct)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// A real React artifact (imports react + react-dom/client, uses JSX) is bundled
// self-contained in air-gap mode: the vendored React is inlined, no bare imports
// or import map remain, and it becomes a classic <script>.
const reactArtifact = `<!doctype html>
<html>
<head>
<script type="importmap">{"imports":{"react":"https://cdn/react","react-dom/client":"https://cdn/rdc"}}</script>
</head>
<body>
<div id="root"></div>
<script type="module">
import React from "react";
import { createRoot } from "react-dom/client";
function App() { return <h1 className="title">Hi {1 + 1}</h1>; }
createRoot(document.getElementById("root")).render(<App />);
</script>
</body>
</html>`

func TestPrepareReactAirGapBundles(t *testing.T) {
	out, ct, warnings, err := Prepare([]byte(reactArtifact), htmlCT, Options{Egress: false})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ct != htmlCT {
		t.Fatalf("content type: got %q", ct)
	}
	for _, w := range warnings {
		if strings.Contains(w, "unresolved import") {
			t.Fatalf("react/react-dom should be vendored, got unresolved warning: %v", warnings)
		}
	}
	got := string(out)
	low := strings.ToLower(got)
	if strings.Contains(low, `type="module"`) {
		t.Fatalf("air-gap output still has a module script")
	}
	if strings.Contains(low, "importmap") {
		t.Fatalf("air-gap output still has an import map")
	}
	if strings.Contains(got, `from "react"`) || strings.Contains(got, `from"react"`) {
		t.Fatalf("bare react import survived bundling:\n%s", firstN(got, 400))
	}
	// The vendored React really got inlined — its production build carries this
	// signature string.
	if !strings.Contains(got, "Minified React error") {
		t.Fatalf("vendored React not inlined into the bundle")
	}
	// Bundling react-dom means a large self-contained script.
	if len(out) < 100_000 {
		t.Fatalf("bundle unexpectedly small (%d bytes) — react-dom may not be inlined", len(out))
	}
	if !strings.Contains(got, `<div id="root"></div>`) {
		t.Fatalf("surrounding HTML not preserved")
	}
}

// The same React artifact in egress mode is only transpiled: imports and the
// import map are kept so the browser resolves deps from a CDN at view time.
func TestPrepareReactEgressKeepsImports(t *testing.T) {
	out, _, _, err := Prepare([]byte(reactArtifact), htmlCT, Options{Egress: true})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := string(out)
	low := strings.ToLower(got)
	if !strings.Contains(low, `type="module"`) {
		t.Fatalf("egress output must keep the module script")
	}
	if !strings.Contains(low, "importmap") {
		t.Fatalf("egress output must keep the import map")
	}
	if !strings.Contains(got, `"react"`) {
		t.Fatalf("egress output must keep the bare react import:\n%s", firstN(got, 400))
	}
	// It must NOT have inlined the vendored React bundle.
	if strings.Contains(got, "Minified React error") {
		t.Fatalf("egress mode should not inline vendored React")
	}
	// JSX must still be transpiled (no raw JSX tag).
	if strings.Contains(got, "<h1 className=") {
		t.Fatalf("JSX not transpiled in egress mode:\n%s", firstN(got, 400))
	}
}

// Tailwind: a Play-CDN <script src> is replaced by the vendored compiler in
// air-gap mode and left in place in egress mode.
const tailwindArtifact = `<!doctype html>
<html>
<head><script src="https://cdn.tailwindcss.com"></script></head>
<body><h1 class="text-3xl font-bold">Styled</h1></body>
</html>`

func TestPrepareTailwindAirGapInlines(t *testing.T) {
	out, ct, _, err := Prepare([]byte(tailwindArtifact), htmlCT, Options{Egress: false})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ct != htmlCT {
		t.Fatalf("content type: got %q", ct)
	}
	got := string(out)
	// The vendored compiler (~400KB) got inlined in place of the CDN <script src>.
	// (We assert on size, not URL-absence: the Play-CDN bundle embeds its own
	// "cdn.tailwindcss.com" URL in an internal template string, so a substring
	// check would false-positive.) The huge size delta from the ~130-byte input
	// is unambiguous proof the compiler was inlined.
	if len(out) < 300_000 {
		t.Fatalf("Tailwind not inlined (%d bytes)", len(out))
	}
	if !strings.Contains(got, `class="text-3xl font-bold"`) {
		t.Fatalf("document body not preserved")
	}
	// Regression (caught in browser QA): the inline must be a literal byte-splice,
	// NOT regexp ReplaceAll — Go's ReplaceAll expands `$` references and the
	// minified bundle has 823 `$`, so `$name`/`$1` sequences get deleted. That
	// both corrupts the compiler AND can collapse adjacent bytes into a spurious
	// `</script>` that breaks out of the inline element, dumping the source as
	// visible page text. Assert the bundle is inlined verbatim.
	if !strings.Contains(got, tailwindJS) {
		t.Fatalf("vendored Tailwind bundle was not inlined verbatim — likely $-expansion corruption")
	}
}

func TestPrepareTailwindEgressKeepsCDN(t *testing.T) {
	out, _, _, err := Prepare([]byte(tailwindArtifact), htmlCT, Options{Egress: true})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(string(out), `src="https://cdn.tailwindcss.com`) {
		t.Fatalf("egress mode must keep the Tailwind CDN script")
	}
	// Egress must NOT inline the compiler — the doc stays tiny.
	if len(out) > 5_000 {
		t.Fatalf("egress mode should not inline Tailwind (%d bytes)", len(out))
	}
}

// A self-contained plain-HTML document (no module script) passes through
// byte-for-byte, with no warnings.
func TestBundlePlainHTMLPassthrough(t *testing.T) {
	in := []byte(`<!doctype html><html><body><h1>hello</h1><script>console.log(1)</script></body></html>`)

	out, ct, warnings, err := Bundle(in, htmlCT)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if string(out) != string(in) {
		t.Fatalf("passthrough altered input:\n got %q\nwant %q", out, in)
	}
	if ct != htmlCT {
		t.Fatalf("content type: got %q, want %q", ct, htmlCT)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

// A non-HTML content type passes through untouched even if it happens to
// contain a module-script-looking string.
func TestBundleNonHTMLPassthrough(t *testing.T) {
	in := []byte(`<script type="module">import x from "react"</script>`)

	out, ct, warnings, err := Bundle(in, "text/plain; charset=utf-8")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if string(out) != string(in) {
		t.Fatalf("non-HTML passthrough altered input")
	}
	if ct != "text/plain; charset=utf-8" {
		t.Fatalf("content type changed: %q", ct)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

// An inline module using ONLY self-contained code (JSX transpiled to
// React.createElement, no external imports) is bundled into a classic inline
// script: no type="module", no import map, JSX transpiled, code inlined.
func TestBundleSelfContainedModule(t *testing.T) {
	in := []byte(`<!doctype html>
<html>
<head>
<script type="importmap">{"imports":{"react":"https://cdn/react"}}</script>
</head>
<body>
<div id="root"></div>
<script type="module">
const React = { createElement: (tag, props, ...kids) => ({ tag, props, kids }) };
function Hello() {
  return <h1 className="greeting">hi there</h1>;
}
const vnode = Hello();
document.getElementById("root").textContent = vnode.tag;
</script>
</body>
</html>`)

	out, ct, _, err := Bundle(in, htmlCT)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := string(out)

	if ct != htmlCT {
		t.Fatalf("content type: got %q, want %q", ct, htmlCT)
	}
	if strings.Contains(strings.ToLower(got), `type="module"`) || strings.Contains(strings.ToLower(got), `type='module'`) {
		t.Fatalf("output still has a module script:\n%s", got)
	}
	if strings.Contains(strings.ToLower(got), "importmap") {
		t.Fatalf("output still has an import map:\n%s", got)
	}
	// JSX must be transpiled — the raw JSX tag must be gone and a
	// createElement call must be present.
	if strings.Contains(got, "<h1 className=") {
		t.Fatalf("JSX was not transpiled:\n%s", got)
	}
	if !strings.Contains(got, "createElement") {
		t.Fatalf("expected transpiled createElement call in output:\n%s", got)
	}
	// The bundled code is inlined inside a classic <script> element.
	if !strings.Contains(got, "<script>") {
		t.Fatalf("expected a classic <script> in output:\n%s", got)
	}
	// The surrounding document is preserved.
	if !strings.Contains(got, `<div id="root"></div>`) {
		t.Fatalf("surrounding HTML not preserved:\n%s", got)
	}
}

// A module that imports an unresolvable bare specifier is NOT rewritten: the
// input is returned unchanged plus a warning naming the specifier (the
// fail-soft path that signals the dep-vendoring follow-up).
func TestBundleUnresolvedBareImportFailsSoft(t *testing.T) {
	in := []byte(`<!doctype html><html><body>
<script type="module">
import x from "somepkg";
document.body.textContent = x;
</script>
</body></html>`)

	out, ct, warnings, err := Bundle(in, htmlCT)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if string(out) != string(in) {
		t.Fatalf("unresolved import path must leave input unchanged:\n got %q\nwant %q", out, in)
	}
	if ct != htmlCT {
		t.Fatalf("content type changed: %q", ct)
	}
	if len(warnings) == 0 {
		t.Fatal("expected a warning naming the unresolved specifier, got none")
	}
	var named bool
	for _, wmsg := range warnings {
		if strings.Contains(wmsg, "somepkg") {
			named = true
		}
	}
	if !named {
		t.Fatalf("no warning named the unresolved specifier %q: %v", "somepkg", warnings)
	}
}
