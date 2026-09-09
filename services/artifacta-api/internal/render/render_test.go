package render

import (
	"strings"
	"testing"
)

const htmlCT = "text/html; charset=utf-8"

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
