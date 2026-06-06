// Package projects hosts the per-project page-level templ components: the
// project list (Index), the dashboard (Detail), the in-place summary editor
// (Summary + SummaryView/SummaryEdit partials), the per-project search page
// + results partial, the settings page, and the new/load forms.
//
// Each templ component takes a typed *Props struct that mirrors the payload
// the legacy html/template handler put in PageOpts.Body. The mirror lives
// here (not in package web) so this package never imports web — that would
// cycle web → components/pages/projects → web. Handlers convert their
// existing internal types into these mirrors at the render boundary.
package projects

import (
	"time"

	"tickets_please/internal/domain"
	"tickets_please/internal/web/components/pages/phases"
	"tickets_please/internal/web/components/partials"
)

//go:generate go run github.com/a-h/templ/cmd/templ generate

// IndexProps is the payload for the Index page (templates/pages/projects/index.tmpl).
type IndexProps struct {
	Projects []*domain.Project
}

// DetailProps is the dashboard payload (templates/pages/projects/detail.tmpl).
// Mirrors web.projectDetailData plus the bits of chrome the page reaches into
// (just CSRF, used by the danger-zone delete form).
type DetailProps struct {
	Project *domain.Project
	// PhaseLead is the lead phases-with-waves block — the same collapsible
	// component the phases index renders, including the Unphased section. It
	// replaces the old tiny phases table at the bottom of the dashboard.
	PhaseLead       phases.PhaseListProps
	Metrics         DashboardMetrics
	StatusSegments  []StatusSegment
	ReadyTickets    []*domain.Ticket
	RecentActivity  []ActivityItem
	RecentLearnings []LearningExcerpt
	CSRF            string
}

// DashboardMetrics is the row of stat cards at the top of the dashboard.
type DashboardMetrics struct {
	Total      int
	Active     int
	InProgress int
	Done       int
}

// StatusSegment is one slice of the stacked status bar.
type StatusSegment struct {
	Column  domain.Column
	Label   string
	Count   int
	Percent int
}

// ActivityItem is one row in the "Recent activity" list.
type ActivityItem struct {
	Ticket *domain.Ticket
}

// LearningExcerpt is one row in the "Recent learnings" section.
type LearningExcerpt struct {
	Ticket  *domain.Ticket
	Excerpt string
}

// SummaryProps is the payload for the Summary page + partials. Mode is "view"
// or "edit" — the page swaps between SummaryView and SummaryEdit on that.
type SummaryProps struct {
	Project   *domain.Project
	Mode      string
	Summary   string
	FormError string
	CSRF      string
}

// SearchProps is the payload for the Search page + results partial. Mirrors
// web.projectSearchData. Hit slices are interface{} so we don't drag
// internal/svc into this package — the templ files type-assert as they iterate.
type SearchProps struct {
	Project      *domain.Project
	Query        string
	Kind         string
	Limit        int
	TicketHits   []TicketHit
	CommentHits  []CommentHit
	LearningHits []LearningHit
	Err          string
}

// TicketHit mirrors svc.TicketHit (subset). The mirror exists so the templ
// page doesn't import svc — the handler converts at the render boundary.
type TicketHit struct {
	Ticket *domain.Ticket
	Score  float32
}

// CommentHit mirrors svc.CommentHit.
type CommentHit struct {
	Comment     *domain.Comment
	TicketTitle string
	Score       float32
}

// LearningHit mirrors svc.LearningHit (subset). CompletedAt drives the
// relative-time stamp on the hit; the completing agent isn't carried on the
// svc hit, so learning hits show time without an author.
type LearningHit struct {
	TicketID    string
	Title       string
	Learnings   string
	Score       float32
	CompletedAt time.Time
}

// SettingsProps is the payload for the Settings page.
type SettingsProps struct {
	Project   *domain.Project
	FormError string
	Submitted SettingsSubmitted
	Status    EmbedStatus
	CSRF      string
}

// SettingsSubmitted captures the user-typed form values for the settings form.
type SettingsSubmitted struct {
	Name          string
	Description   string
	EmbedProvider string
	EmbedModel    string
}

// EmbedStatus is the "what's on disk vs. what's configured" panel above the
// settings form.
type EmbedStatus struct {
	SidecarPresent   bool
	SidecarProvider  string
	SidecarModel     string
	SidecarDim       int
	ExpectedProvider string
	ExpectedModel    string
}

// LoadProps is the payload for the Load page (mount existing repo). Picker
// is reused from the shared partials package — the load page and the
// /api/fs htmx swap render the same component.
type LoadProps struct {
	FormError string
	Path      string
	Picker    partials.FSPickerProps
	CSRF      string
}

// NewProps is the payload for the create-project page.
type NewProps struct {
	FormError string
	Submitted NewSubmitted
	CSRF      string
}

// NewSubmitted captures the user-typed form values for create-project so a
// validation failure round-trips them.
type NewSubmitted struct {
	Slug        string
	Name        string
	Description string
	Summary     string
}

// SearchKinds is the canonical tab order on the search page. Kept here so
// both the page template and any test that wants to know the available tabs
// see the same list.
var SearchKinds = []string{"learnings", "tickets", "comments"}
