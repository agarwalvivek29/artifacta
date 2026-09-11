// Package render is the publish-time render-parity pipeline (FR15, ADR-0010).
//
// Claude-style artifacts are HTML documents whose behaviour lives in a single
// inline ES module — a `<script type="module">` that is often JSX/TSX and
// relies on an `<script type="importmap">` to resolve bare specifiers (react,
// etc.) against CDN URLs at load time. Our `/raw` responses are served under a
// strict CSP that allows `script-src 'unsafe-inline'` but forbids external
// hosts, so the browser would never fetch those CDN modules and the artifact
// would render blank.
//
// The air-gap render path closes that gap by transpiling + bundling the inline
// module into a single self-contained classic `<script>` at publish time — with
// its vendored dependencies (react, react-dom, …; see deps.go) inlined and any
// Tailwind Play-CDN reference replaced by the vendored compiler — so the stored
// artifact renders under the strict CSP with no third-party egress at view time.
//
// Two modes, selected by Options.Egress (ARTIFACTA_CDN_EGRESS):
//   - deny (default): bundle everything self-contained (air-gap).
//   - allow: transpile the module only and keep its imports + the import map, so
//     the browser resolves any dependency from a CDN at view time.
//
// A bare specifier that is not in the vendored set and cannot be resolved leaves
// the artifact unbundled plus a warning — fail-soft, never a failed publish.
package render

import (
	"fmt"
	"regexp"
	"strings"

	esbuild "github.com/evanw/esbuild/pkg/api"
)

// Regexes are compiled once. (?is): case-insensitive, `.` spans newlines.
//   - moduleScriptRe captures the inner source of the FIRST inline
//     `<script type="module">…</script>`.
//   - importmapRe matches any `<script type="importmap">…</script>` for removal.
var (
	moduleScriptRe = regexp.MustCompile(`(?is)<script\b[^>]*\btype\s*=\s*["']module["'][^>]*>(.*?)</script\s*>`)
	importmapRe    = regexp.MustCompile(`(?is)<script\b[^>]*\btype\s*=\s*["']importmap["'][^>]*>.*?</script\s*>`)
)

// Options controls how Prepare renders an artifact. Egress reports whether the
// deployment permits third-party CDN egress at view time (ARTIFACTA_CDN_EGRESS):
// false (the default, air-gapped) means self-contained bundles; true means the
// HTML path may keep an import map and let the browser resolve deps at view
// time. Markdown/Mermaid rendering is identical in both modes (fully inlined).
type Options struct {
	Egress bool
}

// Prepare is the publish-time entry point. It dispatches on content type:
//
//	text/markdown         → renderMarkdown (→ self-contained text/html)
//	text/html             → Bundle (inline-module bundling; egress-aware in a
//	                        later increment)
//	everything else       → passthrough (SVG, images, JSON, … served as-is)
//
// It never fails a publish: each branch is fail-soft and returns the original
// bytes plus a warning rather than an error on trouble.
func Prepare(input []byte, contentType string, opts Options) (out []byte, outContentType string, warnings []string, err error) {
	if isMarkdown(contentType) {
		return renderMarkdown(input)
	}
	return bundleHTML(input, contentType, opts)
}

// Bundle transpiles + bundles an artifact's inline ES module into a
// self-contained classic script so it renders under the strict /raw CSP.
//
// Returns the (possibly rewritten) bytes, the content type to store, any
// non-fatal warnings, and an error. In this increment err is always nil: the
// pipeline is fail-soft (see below) so a stored artifact always beats a failed
// publish. The signature keeps err for the follow-up increment.
//
// Behaviour:
//   - Non-HTML content type, or HTML with no inline `<script type="module">`:
//     passthrough — out == input, unchanged.
//   - Inline module present: esbuild bundles it (Loader TSX so JSX/TSX parse,
//     Bundle+IIFE+MinifySyntax, browser target). The resulting JS is inlined
//     back as a classic `<script>` (no type="module") and any
//     `<script type="importmap">` is stripped. The output is self-contained: no
//     module scripts, no external `src`, no bare-specifier imports remain.
//   - Unresolved bare imports (e.g. `import React from "react"` with nothing to
//     resolve them against): NOT a hard failure. We collect every such
//     specifier via an esbuild resolve plugin and, if any were seen, return the
//     input UNCHANGED with one warning per specifier. This is the hook for the
//     dep-vendoring follow-up.
//   - Any other unexpected esbuild error: fail OPEN to passthrough — return the
//     original input plus a warning. A stored (un-bundled) artifact is better
//     than a rejected publish.
func Bundle(input []byte, contentType string) (out []byte, outContentType string, warnings []string, err error) {
	return bundleHTML(input, contentType, Options{})
}

// bundleHTML is the HTML render path. It has two modes, chosen by opts.Egress:
//
//   - Air-gap (Egress=false, the default): the inline module is transpiled and
//     BUNDLED into a self-contained classic <script> with its vendored deps
//     inlined; the import map is stripped and a Tailwind Play-CDN reference is
//     replaced with the vendored compiler. The stored artifact renders with zero
//     third-party egress at view time.
//   - Egress (Egress=true): the inline module is only TRANSPILED (JSX/TSX → JS);
//     its imports and the import map are kept, so the browser resolves deps from
//     a CDN at view time. Tailwind's CDN script is left in place.
//
// Non-HTML content passes through untouched. Every failure is soft: on trouble
// the original bytes are stored (plus a warning) rather than failing the publish.
func bundleHTML(input []byte, contentType string, opts Options) (out []byte, outContentType string, warnings []string, err error) {
	// Never let an esbuild edge case panic take down a publish request.
	defer func() {
		if r := recover(); r != nil {
			out = input
			outContentType = contentType
			warnings = append(warnings, fmt.Sprintf("render: recovered from panic, stored unbundled: %v", r))
			err = nil
		}
	}()

	if !isHTML(contentType) {
		return input, contentType, nil, nil
	}

	// Air-gap mode inlines Tailwind for the whole document first (a plain HTML
	// artifact that only uses Tailwind classes, with no module, still needs it).
	// Egress mode leaves the CDN script in place.
	doc := input
	if !opts.Egress {
		doc = inlineTailwind(doc)
	}

	loc := moduleScriptRe.FindSubmatchIndex(doc)
	if loc == nil {
		return doc, contentType, nil, nil // no inline module → nothing to (un)bundle
	}
	// loc[2]:loc[3] is capture group 1 (the module source); loc[0]:loc[1] is the
	// whole <script>…</script> tag we will replace.
	moduleSrc := string(doc[loc[2]:loc[3]])
	if strings.TrimSpace(moduleSrc) == "" {
		// An empty body (e.g. a src-only module script) has nothing to process.
		return doc, contentType, nil, nil
	}

	if opts.Egress {
		return transpileHTML(doc, loc, moduleSrc, contentType)
	}

	js, unresolved, buildWarnings, buildErr := bundleModule(moduleSrc)

	// Fail-soft #1: unresolved bare specifiers (not in the vendored set). Store
	// the artifact as-is and surface each specifier. Under air-gap CSP such an
	// artifact renders degraded, which is the honest air-gap trade-off.
	if len(unresolved) > 0 {
		for _, spec := range unresolved {
			warnings = append(warnings, fmt.Sprintf("render: unresolved import %q — stored unbundled (not in the vendored set; publish under ARTIFACTA_CDN_EGRESS=allow to resolve from a CDN)", spec))
		}
		return doc, contentType, warnings, nil
	}
	// Fail-soft #2: any other esbuild error. Fail open to passthrough.
	if buildErr != nil {
		warnings = append(warnings, fmt.Sprintf("render: bundle failed, stored unbundled: %v", buildErr))
		return doc, contentType, warnings, nil
	}
	warnings = append(warnings, buildWarnings...)

	// Rewrite: swap the module script for a classic inline script carrying the
	// bundled JS, then strip the now-dead import map.
	classic := "<script>" + escapeClosingScript(js) + "</script>"
	rewritten := make([]byte, 0, len(doc)+len(classic))
	rewritten = append(rewritten, doc[:loc[0]]...)
	rewritten = append(rewritten, classic...)
	rewritten = append(rewritten, doc[loc[1]:]...)
	rewritten = importmapRe.ReplaceAll(rewritten, nil)

	return rewritten, contentType, warnings, nil
}

// transpileHTML is the egress-mode rewrite: it transpiles the module's JSX/TSX
// to JS while KEEPING its imports and the surrounding import map, so the browser
// resolves dependencies from a CDN at view time. The module stays a
// `<script type="module">`. Fail-soft: a transpile error stores the original.
func transpileHTML(doc []byte, loc []int, moduleSrc, contentType string) ([]byte, string, []string, error) {
	res := esbuild.Transform(moduleSrc, esbuild.TransformOptions{
		Loader:       esbuild.LoaderTSX,      // JSX/TSX → JS, imports left intact
		Format:       esbuild.FormatESModule, // keep import/export syntax
		Target:       esbuild.ES2020,
		MinifySyntax: true,
		LogLevel:     esbuild.LogLevelSilent,
	})
	if len(res.Errors) > 0 {
		return doc, contentType,
			[]string{fmt.Sprintf("render: transpile failed, stored unbundled: %s", res.Errors[0].Text)}, nil
	}
	var warnings []string
	for _, m := range res.Warnings {
		warnings = append(warnings, "esbuild: "+m.Text)
	}
	moduleTag := `<script type="module">` + escapeClosingScript(string(res.Code)) + "</script>"
	rewritten := make([]byte, 0, len(doc)+len(moduleTag))
	rewritten = append(rewritten, doc[:loc[0]]...)
	rewritten = append(rewritten, moduleTag...)
	rewritten = append(rewritten, doc[loc[1]:]...)
	return rewritten, contentType, warnings, nil
}

// bundleModule runs esbuild over a single inline module source, resolving any
// imports against the vendored dependency set (vendorPlugin). It returns the
// bundled JS, the list of bare specifiers it could NOT resolve (i.e. not in the
// vendored set, deduplicated, first-seen order), any esbuild warnings, and a
// fatal error for the unexpected case. Resolve misses are captured as
// `unresolved`, NOT as err, so the caller fails soft on them.
func bundleModule(src string) (js string, unresolved []string, warnings []string, err error) {
	seen := map[string]bool{}

	result := esbuild.Build(esbuild.BuildOptions{
		Stdin: &esbuild.StdinOptions{
			Contents:   src,
			Loader:     esbuild.LoaderTSX, // TSX parses both JSX and plain JS/TS
			Sourcefile: "artifact-module.tsx",
		},
		Bundle:       true,
		Format:       esbuild.FormatIIFE,
		Platform:     esbuild.PlatformBrowser,
		Target:       esbuild.ES2020,
		MinifySyntax: true,
		Write:        false,
		LogLevel:     esbuild.LogLevelSilent,
		// Vendored production builds already inline their env, but a future
		// "+common-libs" package may branch on process.env.NODE_ENV — define it
		// so such code takes the production path and never references `process`.
		Define:  map[string]string{"process.env.NODE_ENV": `"production"`},
		Plugins: []esbuild.Plugin{vendorPlugin(&unresolved, seen)},
	})

	for _, m := range result.Warnings {
		warnings = append(warnings, "esbuild: "+m.Text)
	}
	// If we recorded unresolved specifiers, the caller fails soft on those and
	// ignores any accompanying errors — don't also surface them as a fatal err.
	if len(unresolved) > 0 {
		return "", unresolved, warnings, nil
	}
	if len(result.Errors) > 0 {
		return "", nil, warnings, fmt.Errorf("esbuild: %s", result.Errors[0].Text)
	}
	if len(result.OutputFiles) == 0 {
		return "", nil, warnings, fmt.Errorf("esbuild produced no output")
	}
	return string(result.OutputFiles[0].Contents), nil, warnings, nil
}

// isHTML reports whether contentType denotes HTML (ignoring any charset etc.
// parameters). Matching on the media type prefix keeps `text/html; charset=…`
// working.
func isHTML(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/html")
}

// escapeClosingScript neutralises any literal `</script` inside the bundled JS
// (e.g. inside a string) so it cannot prematurely terminate the inline
// <script> element we wrap it in. The `<\/script` form is equivalent JS.
func escapeClosingScript(js string) string {
	return regexp.MustCompile(`(?i)</script`).ReplaceAllString(js, `<\/script`)
}
