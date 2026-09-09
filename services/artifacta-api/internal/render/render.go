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
// Bundle closes that gap by transpiling + bundling the inline module into a
// single self-contained classic `<script>` at publish time, so the stored
// artifact renders under the strict CSP with no network at view time.
//
// INCREMENT 1 (this file): pipeline + esbuild integration + fail-soft. It
// handles artifacts whose module is fully self-contained (no external
// dependencies). When the module imports bare specifiers we cannot yet resolve
// (react, etc.), Bundle deliberately does NOT rewrite the artifact — it returns
// the input unchanged plus a warning naming each unresolved specifier. That
// warning list is the signal for the FOLLOW-UP increment, which will vendor the
// dependencies and feed them to esbuild's resolver.
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

	loc := moduleScriptRe.FindSubmatchIndex(input)
	if loc == nil {
		return input, contentType, nil, nil // no inline module → nothing to bundle
	}
	// loc[2]:loc[3] is capture group 1 (the module source); loc[0]:loc[1] is the
	// whole <script>…</script> tag we will replace.
	moduleSrc := string(input[loc[2]:loc[3]])
	if strings.TrimSpace(moduleSrc) == "" {
		// An empty body (e.g. a src-only module script) has nothing to inline;
		// leave the document untouched rather than emit an empty classic script.
		return input, contentType, nil, nil
	}

	js, unresolved, buildWarnings, buildErr := bundleModule(moduleSrc)

	// Fail-soft #1: unresolved bare specifiers. Store the artifact as-is and
	// surface the specifiers so the dep-vendoring increment can act on them.
	if len(unresolved) > 0 {
		for _, spec := range unresolved {
			warnings = append(warnings, fmt.Sprintf("render: unresolved import %q — stored unbundled (dep vendoring is a follow-up)", spec))
		}
		return input, contentType, warnings, nil
	}
	// Fail-soft #2: any other esbuild error. Fail open to passthrough.
	if buildErr != nil {
		warnings = append(warnings, fmt.Sprintf("render: bundle failed, stored unbundled: %v", buildErr))
		return input, contentType, warnings, nil
	}
	warnings = append(warnings, buildWarnings...)

	// Rewrite: swap the module script for a classic inline script carrying the
	// bundled JS, then strip now-dead import maps.
	classic := "<script>" + escapeClosingScript(js) + "</script>"
	rewritten := make([]byte, 0, len(input)+len(classic))
	rewritten = append(rewritten, input[:loc[0]]...)
	rewritten = append(rewritten, classic...)
	rewritten = append(rewritten, input[loc[1]:]...)
	rewritten = importmapRe.ReplaceAll(rewritten, nil)

	return rewritten, contentType, warnings, nil
}

// bundleModule runs esbuild over a single inline module source. It returns the
// bundled JS, the list of bare specifiers it could not resolve (deduplicated,
// first-seen order), any esbuild warnings, and a fatal error for the
// unexpected case. Resolve failures are captured as `unresolved`, NOT as err.
func bundleModule(src string) (js string, unresolved []string, warnings []string, err error) {
	seen := map[string]bool{}

	// collectBareImports intercepts every non-entry import. In increment 1 we
	// have no vendored modules, so any import is by definition unresolvable: we
	// record its specifier and mark it External so the build still completes
	// (letting us report every offender at once instead of aborting on the
	// first). The bundle produced in that case is discarded by the caller.
	collectBareImports := esbuild.Plugin{
		Name: "collect-bare-imports",
		Setup: func(b esbuild.PluginBuild) {
			b.OnResolve(esbuild.OnResolveOptions{Filter: `.*`}, func(a esbuild.OnResolveArgs) (esbuild.OnResolveResult, error) {
				if a.Kind == esbuild.ResolveEntryPoint {
					return esbuild.OnResolveResult{}, nil // the stdin entry itself
				}
				if !seen[a.Path] {
					seen[a.Path] = true
					unresolved = append(unresolved, a.Path)
				}
				return esbuild.OnResolveResult{Path: a.Path, External: true}, nil
			})
		},
	}

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
		Plugins:      []esbuild.Plugin{collectBareImports},
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
