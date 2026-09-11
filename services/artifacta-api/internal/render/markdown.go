// Markdown rendering for the publish-time pipeline (render-parity, ADR-0023).
//
// A Markdown artifact is rendered to a self-contained HTML document AT PUBLISH
// TIME and stored as text/html, so it flows through the exact same sandboxed
// viewer, CSP, comments, and versioning as any other HTML artifact — the viewer
// shell only knows how to srcdoc HTML (viewer.html), so meeting it with HTML is
// what makes Markdown "just work" everywhere.
//
// Mermaid: fenced ```mermaid blocks become <pre class="mermaid"> and, when at
// least one is present, the vendored mermaid runtime is inlined plus an init
// call. Everything is inlined (no external fetch at view time) so it renders
// identically under the strict air-gap CSP and the permissive one — ADR-0010's
// no-third-party-egress bet holds for diagrams too.
package render

import (
	_ "embed"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// mermaidJS is the vendored Mermaid runtime, inlined into documents that use it
// so diagrams render with zero network egress at view time.
//
//go:embed vendor/mermaid.min.js
var mermaidJS string

// fencedMermaidRe captures the body of a ```mermaid fenced code block. (?is):
// case-insensitive, `.` spans newlines. The fence must start at a line start.
var fencedMermaidRe = regexp.MustCompile("(?is)(?m)^[ \\t]*```+[ \\t]*mermaid[ \\t]*\\r?\\n(.*?)\\r?\\n[ \\t]*```+[ \\t]*$")

// isMarkdown reports whether contentType denotes Markdown (ignoring parameters
// like charset). The CLI/API label .md/.markdown as text/markdown.
func isMarkdown(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "text/markdown") || strings.Contains(ct, "text/x-markdown")
}

// renderMarkdown converts a Markdown document to a self-contained HTML document
// and returns it with content type text/html. It is fail-soft: any conversion
// problem returns the original bytes (as text/markdown) plus a warning rather
// than failing the publish, mirroring the HTML pipeline's contract.
//
// Pipeline:
//
//	raw md ──▶ extract ```mermaid fences ──▶ placeholder tokens
//	                                          │
//	          goldmark (GFM) renders body ◀───┘
//	                    │
//	   substitute tokens ▶ <pre class="mermaid">…escaped diagram…</pre>
//	                    │
//	   wrap in styled shell (+ inline mermaid runtime iff any diagrams)
func renderMarkdown(input []byte) (out []byte, outContentType string, warnings []string, err error) {
	defer func() {
		if r := recover(); r != nil {
			out = input
			outContentType = "text/markdown; charset=utf-8"
			warnings = append(warnings, fmt.Sprintf("render: markdown recovered from panic, stored raw: %v", r))
			err = nil
		}
	}()

	src := string(input)

	// 1. Pull ```mermaid fences out before goldmark sees them, so their diagram
	//    source is never mangled by Markdown processing. Each is replaced by a
	//    unique placeholder that survives rendering as a lone paragraph.
	var diagrams []string
	src = fencedMermaidRe.ReplaceAllStringFunc(src, func(match string) string {
		body := fencedMermaidRe.FindStringSubmatch(match)[1]
		token := fmt.Sprintf("HNMERMAIDPLACEHOLDER%dHN", len(diagrams))
		diagrams = append(diagrams, body)
		return token
	})

	// 2. Render Markdown → HTML with GFM (tables, strikethrough, task lists,
	//    autolinks). Raw HTML in the source stays escaped (goldmark default, no
	//    WithUnsafe): the sandbox is the security boundary, but escaping keeps a
	//    Markdown artifact from smuggling arbitrary markup past the renderer.
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	var buf strings.Builder
	if convErr := md.Convert([]byte(src), &buf); convErr != nil {
		return input, "text/markdown; charset=utf-8",
			[]string{fmt.Sprintf("render: markdown convert failed, stored raw: %v", convErr)}, nil
	}
	body := buf.String()

	// 3. Swap each placeholder for a mermaid block. goldmark wraps a lone
	//    placeholder in <p>…</p>; replace that form first, then any bare
	//    occurrence as a fallback. The diagram text is HTML-escaped so the
	//    browser decodes it back to raw text in the element's textContent, which
	//    is exactly what Mermaid parses — this keeps diagrams that use `<`, `&`
	//    etc. intact.
	for i, d := range diagrams {
		token := fmt.Sprintf("HNMERMAIDPLACEHOLDER%dHN", i)
		block := `<pre class="mermaid">` + html.EscapeString(d) + `</pre>`
		body = strings.ReplaceAll(body, "<p>"+token+"</p>", block)
		body = strings.ReplaceAll(body, token, block)
	}

	// 4. Assemble the self-contained document.
	var out2 strings.Builder
	out2.WriteString(markdownShellHead)
	out2.WriteString(body)
	if len(diagrams) > 0 {
		out2.WriteString("\n<script>")
		out2.WriteString(escapeClosingScript(mermaidJS))
		out2.WriteString("</script>\n<script>mermaid.initialize({startOnLoad:true});</script>\n")
	}
	out2.WriteString(markdownShellTail)

	return []byte(out2.String()), "text/html; charset=utf-8", nil, nil
}

// markdownShellHead / markdownShellTail wrap the rendered body in a minimal,
// self-contained styled document. The CSS is inline (no external stylesheet) so
// it renders under the strict air-gap CSP. Colors adapt to the viewer's theme
// via prefers-color-scheme.
const markdownShellHead = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
:root { color-scheme: light dark; }
body { margin: 0; background: #fff; color: #1a1a1a;
  font: 16px/1.7 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; }
.md { max-width: 46rem; margin: 0 auto; padding: 2.5rem 1.25rem 4rem; }
.md h1, .md h2, .md h3 { line-height: 1.25; margin: 1.8em 0 .6em; font-weight: 650; }
.md h1 { font-size: 2rem; } .md h2 { font-size: 1.5rem; } .md h3 { font-size: 1.2rem; }
.md p, .md li { overflow-wrap: anywhere; }
.md a { color: #0b62d6; }
.md code { font: .9em ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  background: rgba(130,130,150,.15); padding: .15em .4em; border-radius: 4px; }
.md pre { background: #f5f5f7; padding: 1rem 1.15rem; border-radius: 8px; overflow-x: auto; }
.md pre code { background: none; padding: 0; }
.md pre.mermaid { background: none; padding: 0; text-align: center; }
.md blockquote { margin: 1em 0; padding: .2em 1em; border-left: 3px solid rgba(130,130,150,.5);
  color: #555; }
.md table { border-collapse: collapse; width: 100%; margin: 1em 0; display: block; overflow-x: auto; }
.md th, .md td { border: 1px solid rgba(130,130,150,.35); padding: .5em .75em; text-align: left; }
.md img { max-width: 100%; }
.md hr { border: 0; border-top: 1px solid rgba(130,130,150,.35); margin: 2em 0; }
@media (prefers-color-scheme: dark) {
  body { background: #16161a; color: #e6e6ea; }
  .md a { color: #6ea8fe; } .md pre { background: #1f1f26; } .md blockquote { color: #a8a8b3; }
}
</style>
</head>
<body>
<main class="md">
`

const markdownShellTail = `</main>
</body>
</html>
`
