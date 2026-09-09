// Package web embeds the viewer and dashboard into the binary so a single
// artifact serves everything. v0 ships a minimal sandboxed-iframe loader; the
// forked artifact-runtime (apps/viewer) builds into embed/ in a later phase.
//
// The dashboard and sign-in pages are parsed as html/template (NOT text/template)
// so user-controlled fields such as artifact titles are auto-escaped — this is a
// security requirement, since titles flow straight from publisher input.
package web

import (
	_ "embed"
	"html/template"
	"io"
)

//go:embed embed/viewer.html
var ViewerHTML string

//go:embed embed/dashboard.html
var dashboardHTML string

//go:embed embed/signin.html
var signinHTML string

var (
	dashboardTmpl = template.Must(template.New("dashboard").Parse(dashboardHTML))
	signinTmpl    = template.Must(template.New("signin").Parse(signinHTML))
)

// ArtifactView is the presentation projection of an artifact row in the
// dashboard: just what the template renders. Title is escaped by html/template.
type ArtifactView struct {
	Slug       string
	Title      string
	Visibility string
	Version    int32
}

// DashboardData is the view model for the authenticated dashboard, grouping the
// caller's artifacts into the three FR18 sections.
type DashboardData struct {
	Email  string
	Mine   []ArtifactView
	Shared []ArtifactView
	Org    []ArtifactView
}

// RenderDashboard writes the authenticated dashboard for data to w.
func RenderDashboard(w io.Writer, data DashboardData) error {
	return dashboardTmpl.Execute(w, data)
}

// RenderSignin writes the unauthenticated sign-in landing page to w.
func RenderSignin(w io.Writer) error {
	return signinTmpl.Execute(w, nil)
}
