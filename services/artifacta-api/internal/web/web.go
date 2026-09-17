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
	// Created is the artifact's creation date as YYYY-MM-DD (UTC) — a sortable,
	// display-friendly column for the dashboard's list view.
	Created string
	// ContentType is the stored media type, surfaced as a "kind" column/badge
	// (html, markdown, pdf, image, …) in the list view.
	ContentType string
	// Description is the publisher's optional free-text metadata, shown as a
	// subtitle and included in the dashboard's client-side search.
	Description string
	// OwnerEmail is the publisher's email. Shown on Shared-with-me / Org cards so
	// the viewer can see who owns an artifact they don't own (may be empty on
	// pre-existing artifacts).
	OwnerEmail string
	// IsOwner marks the caller's own artifacts (the Mine section), which get the
	// owner-only Share control. False for Shared-with-me and Org cards.
	IsOwner bool
}

// DashboardData is the view model for the authenticated dashboard, grouping the
// caller's artifacts into the three FR18 sections. OrgName/LogoURL brand the
// navbar (cosmetic; empty falls back to the ArtifactA wordmark).
type DashboardData struct {
	Email   string
	OrgName string
	LogoURL string
	Mine    []ArtifactView
	Shared  []ArtifactView
	Org     []ArtifactView
	// UploadEnabled renders the "Upload an artifact" form when the operator has
	// turned on ARTIFACTA_UPLOAD_UI (ADR-0024). Off = the form is absent.
	UploadEnabled bool
}

// RenderDashboard writes the authenticated dashboard for data to w.
func RenderDashboard(w io.Writer, data DashboardData) error {
	return dashboardTmpl.Execute(w, data)
}

// RenderSignin writes the unauthenticated sign-in landing page to w.
func RenderSignin(w io.Writer) error {
	return signinTmpl.Execute(w, nil)
}
