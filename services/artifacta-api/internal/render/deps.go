// Vendored dependency resolution for the air-gap render path (ADR-0023).
//
// In air-gap mode (ARTIFACTA_CDN_EGRESS=deny, the default) an artifact must
// render with zero third-party egress at view time, so any dependency its inline
// module imports has to be bundled IN at publish time. esbuild runs in-process
// here, so we serve the vendored packages straight from the go:embed FS through
// an OnResolve/OnLoad plugin — no filesystem writes, no temp dir, no NodePaths.
// That deliberately avoids a materialize-to-/tmp step that would fail on a
// read-only/distroless rootfs (exactly this feature's compliance audience).
//
// The vendored set is bounded by construction: only these specifiers resolve in
// air-gap mode. Anything else is reported unresolved and the artifact fails soft
// (stored unbundled). Operators who want "any package" use egress mode instead.
package render

import (
	_ "embed"
	"regexp"

	esbuild "github.com/evanw/esbuild/pkg/api"
)

//go:embed vendor/react.production.min.js
var reactJS string

//go:embed vendor/react-dom.production.min.js
var reactDOMJS string

//go:embed vendor/scheduler.production.min.js
var schedulerJS string

//go:embed vendor/react-jsx-runtime.production.min.js
var reactJSXRuntimeJS string

//go:embed vendor/tailwind-browser.min.js
var tailwindJS string

// vendored maps a bare import specifier to the CJS/JS source served for it.
// react-dom.production requires "react" and "scheduler"; react/jsx-runtime
// requires "react" — all route back through this same map. react-dom/client and
// react/jsx-dev-runtime are thin synthetic re-exports over the production
// bundles (the standalone entry files are tiny wrappers we don't need to vendor
// separately). Pinned to React 18.3.1.
var vendored = map[string]string{
	"react":                 reactJS,
	"react-dom":             reactDOMJS,
	"scheduler":             schedulerJS,
	"react/jsx-runtime":     reactJSXRuntimeJS,
	"react-dom/client":      `var m=require("react-dom");exports.createRoot=m.createRoot;exports.hydrateRoot=m.hydrateRoot;`,
	"react/jsx-dev-runtime": `var r=require("react/jsx-runtime");exports.Fragment=r.Fragment;exports.jsxDEV=r.jsx;`,
}

const vendorNamespace = "hn-vendor"

// vendorPlugin resolves + loads the vendored dependency set from memory. Any
// bare specifier NOT in the set is recorded in *unresolved (deduped via seen)
// and marked External so the build still completes and the caller can fail soft
// on it, exactly like the pre-vendoring behaviour did for every specifier.
//
// The OnResolve callback runs for imports in every namespace (no Namespace
// filter) so that requires nested inside a vendored package — react-dom pulling
// in "react" and "scheduler" — also route back here.
func vendorPlugin(unresolved *[]string, seen map[string]bool) esbuild.Plugin {
	return esbuild.Plugin{
		Name: "hn-vendor",
		Setup: func(b esbuild.PluginBuild) {
			b.OnResolve(esbuild.OnResolveOptions{Filter: `.*`}, func(a esbuild.OnResolveArgs) (esbuild.OnResolveResult, error) {
				if a.Kind == esbuild.ResolveEntryPoint {
					return esbuild.OnResolveResult{}, nil
				}
				if _, ok := vendored[a.Path]; ok {
					return esbuild.OnResolveResult{Path: a.Path, Namespace: vendorNamespace}, nil
				}
				if !seen[a.Path] {
					seen[a.Path] = true
					*unresolved = append(*unresolved, a.Path)
				}
				return esbuild.OnResolveResult{Path: a.Path, External: true}, nil
			})
			b.OnLoad(esbuild.OnLoadOptions{Filter: `.*`, Namespace: vendorNamespace}, func(a esbuild.OnLoadArgs) (esbuild.OnLoadResult, error) {
				contents := vendored[a.Path]
				loader := esbuild.LoaderJS
				return esbuild.OnLoadResult{Contents: &contents, Loader: loader}, nil
			})
		},
	}
}

// tailwindScriptRe matches a Tailwind Play-CDN script tag (any attribute order,
// optional ?plugins= query) so air-gap mode can inline the vendored compiler in
// its place. Claude artifacts style with this exact runtime.
var tailwindScriptRe = regexp.MustCompile(`(?is)<script\b[^>]*\bsrc\s*=\s*["'][^"']*cdn\.tailwindcss\.com[^"']*["'][^>]*>\s*</script\s*>`)

// inlineTailwind replaces a Tailwind Play-CDN <script src> with the vendored
// Tailwind browser build inlined, so class-based styling works with no egress.
// It is a no-op when the document does not reference the CDN.
//
// Splicing is by byte index, NOT regexp ReplaceAll: Go's ReplaceAll expands `$`
// references in the replacement, and the minified Tailwind bundle is full of `$`
// (823 of them) — ReplaceAll would corrupt the JS and drop the `</script`
// escaping. escapeClosingScript neutralises the one `</script>` the bundle
// carries inside a template string so the inline element can't be closed early.
func inlineTailwind(doc []byte) []byte {
	locs := tailwindScriptRe.FindAllIndex(doc, -1)
	if locs == nil {
		return doc
	}
	inlined := []byte("<script>" + escapeClosingScript(tailwindJS) + "</script>")
	out := make([]byte, 0, len(doc)+len(inlined))
	prev := 0
	for _, loc := range locs {
		out = append(out, doc[prev:loc[0]]...)
		out = append(out, inlined...)
		prev = loc[1]
	}
	out = append(out, doc[prev:]...)
	return out
}
