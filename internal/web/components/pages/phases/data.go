// Package phases hosts the templ-backed page and component set for the
// /p/{slug}/phases/* routes. Wave 1 of the frontend migration ports the
// legacy html/template files in internal/web/templates/pages/phases/ to
// templ — class names, hrefs, and layout stay 1:1 with the originals so
// htmx and CSS keep working unchanged.
//
// The handler in internal/web/handlers_phases.go computes its own internal
// shapes (phaseWithWaves, waveSection, phaseDist) and converts them at the
// render boundary into the mirror props declared here. We keep these as
// separate types — rather than importing the handler-side structs — to avoid
// a `web → components → web` cycle. The mirroring is purely structural.
//
// Naming reminder (the templ gotcha): a `templ Foo(...)` becomes a Go func
// named Foo, so a struct can't share the name. Hence WaveSection (component)
// and WaveSectionProps (struct), PhaseRow (component) and PhaseRowProps
// (struct), etc.
package phases

import (
	"html/template"
	"strconv"

	"github.com/a-h/templ"

	"tickets_please/internal/domain"
)

// IndexProps is the typed payload for the phases-index page (Index template).
// Handler converts its phasesIndexData → IndexProps at the render boundary.
type IndexProps struct {
	Project *domain.Project
	Phases  []PhaseRowProps
	// Unphased carries the wave-bucketed tickets that belong to no phase, so
	// they get a home now that the board (their old surface) is gone. Empty
	// when there are no phase-less tickets — the UnphasedRow is then skipped.
	Unphased      []WaveSectionProps
	UnphasedTotal int
	// Focused is set when the index is filtered to a single wave via ?wave=N
	// (every phase body, and the Unphased section, is narrowed to that wave and
	// rendered open). FocusWaveLabel is the human label; AllWavesHref clears
	// the filter. OnlyUnphased (from ?phase=unphased) narrows further to just
	// the Unphased section.
	Focused        bool
	FocusWaveLabel string
	AllWavesHref   string
	OnlyUnphased   bool
}

// PhaseRowProps drives one collapsible <details> row on the index. Waves is
// the already-bucketed wave breakdown for the phase's tickets; Dist + Total
// drive the inline mini progress bar.
type PhaseRowProps struct {
	Phase *domain.Phase
	Waves []WaveSectionProps
	Dist  PhaseDist
	Total int
	// DefaultOpen renders the row's <details> expanded on first paint. The
	// project-overview lead block sets it for phases with open (non-done)
	// tickets; the index leaves it off (rows start collapsed, JS restores the
	// remembered state).
	DefaultOpen bool
}

// PhaseListProps drives the shared PhaseList block — the `.phase-list` of
// collapsible phase rows plus the trailing Unphased section. Reused by the
// phases index and the project-overview lead block so both render identically.
type PhaseListProps struct {
	ProjectID     string
	ProjectSlug   string
	Phases        []PhaseRowProps
	Unphased      []WaveSectionProps
	UnphasedTotal int
	// OpenAll forces every row open (the index's wave-focus mode). When false a
	// row opens iff its PhaseRowProps.DefaultOpen is set.
	OpenAll bool
}

// PhaseDist counts tickets per kanban column for a phase. Mirrors the
// handler's phaseDist so the templates can reach .Todo / .InProgress / etc.
type PhaseDist struct {
	Todo, InProgress, Testing, Done int
}

// WaveSectionProps is the input for the WaveSection shared component used by
// both the phases-index expanded view and the phase-detail page.
type WaveSectionProps struct {
	// ProjectSlug is the URL stem for ticket links (/tickets/{id}?slug=...).
	// We carry it on the wave props rather than threading the project
	// through the render tree because the wave is the smallest self-rendering
	// unit and pulling the slug here keeps the template free of parent refs.
	ProjectSlug  string
	Wave         int
	Tickets      []*domain.Ticket
	IsUnassigned bool
	// Focusable turns on the per-wave deep-link affordances: a stable
	// `id="w{n}"` anchor (so `…/phases/{phase}#w3` scrolls + highlights) and a
	// "Focus on this wave →" link. Set on the phase-detail page; left off on
	// the index, where many phases share the page and bare wave ids would
	// collide.
	Focusable bool
}

// DetailProps drives the phases/detail page — a single phase with its
// wave-bucketed tickets and the danger-zone delete form.
type DetailProps struct {
	Project *domain.Project
	Phase   *domain.Phase
	Waves   []WaveSectionProps
	// CSRF is the per-session token threaded through the danger-zone form.
	// We can't read it off Chrome from inside the page template because the
	// templ children context doesn't carry it — handler passes it explicitly.
	CSRF string
	// SummaryHTML is the pre-rendered markdown of phase.Summary. The handler
	// renders it via the components/md package before constructing this struct.
	SummaryHTML template.HTML
	// Focused is set when the page is filtered to a single wave via ?wave=N.
	// FocusWaveLabel is the human label ("Wave 3" / "Unassigned wave") and
	// AllWavesHref clears the filter (the path without the query). Waves is
	// already narrowed to the focused wave by the handler when Focused.
	Focused        bool
	FocusWaveLabel string
	AllWavesHref   string
	// ShowArchived reflects whether archived tickets are included in the wave
	// lists this render; ToggleHref flips it (preserving any ?wave focus).
	ShowArchived bool
	ToggleHref   string
}

// FormProps drives both phases/new and phases/edit. Mode is "new" or "edit";
// in edit mode Phase is non-nil and slug is rendered immutable.
type FormProps struct {
	Mode      string // "new" or "edit"
	Project   *domain.Project
	Phase     *domain.Phase
	FormError string
	Submitted FormSubmitted
	CSRF      string
}

// FormSubmitted mirrors the handler-side phaseFormSubmitted: round-tripped
// user input the form re-displays after a validation failure.
type FormSubmitted struct {
	Slug        string
	Name        string
	Description string
	Summary     string
}

// SummaryProps drives the phases/summary page plus its view/edit partials.
// Mode flips between "view" and "edit"; the page picks the right partial.
//
// SummaryHTML is the markdown source pre-rendered to safe HTML by the
// handler (see DetailProps for the cycle-avoidance rationale). Summary
// remains as the raw markdown string for the edit-mode textarea round-trip.
type SummaryProps struct {
	Project     *domain.Project
	Phase       *domain.Phase
	Mode        string // "view" or "edit"
	Summary     string
	SummaryHTML template.HTML
	FormError   string
	CSRF        string
}

// AssignPhaseFormProps is the typed input for the AssignPhaseForm partial —
// the reassign-ticket form embedded in ticket detail. The legacy
// mkAssignPhase func packed these into a map; here we name them.
type AssignPhaseFormProps struct {
	TicketID         string
	ProjectSlug      string
	CurrentPhaseSlug string
	Phases           []*domain.Phase
	CSRF             string
}

// Helpers below: small string/int formatters the .templ files reach via @-less
// call syntax. Kept here (not inline in templ) so future tweaks have one home.

// WaveChipText returns the chip label for a wave header — "W0" for the
// unassigned bucket, "W{n}" otherwise.
func WaveChipText(p WaveSectionProps) string {
	if p.IsUnassigned {
		return "W0"
	}
	return waveLabelN("W", p.Wave)
}

// WaveAnchorID returns the stable element id for a wave section on the
// phase-detail page — "w3" for a numbered wave, "w-unassigned" for wave 0.
// `…/phases/{phase}#w3` then scrolls to (and, via CSS :target, highlights) it.
func WaveAnchorID(p WaveSectionProps) string {
	if p.IsUnassigned {
		return "w-unassigned"
	}
	return "w" + itoa(p.Wave)
}

// WaveFocusHref returns the relative "?wave=N" link for the "Focus on this
// wave →" affordance. It's query-only so it resolves against whatever page
// it's rendered on — focusing a single wave on phase-detail, or the same wave
// across every phase on the index — without needing the path threaded in.
func WaveFocusHref(p WaveSectionProps) string {
	return "?wave=" + itoa(p.Wave)
}

// WaveTitle returns the wave's human-readable title.
func WaveTitle(p WaveSectionProps) string {
	if p.IsUnassigned {
		return "Unassigned wave"
	}
	return waveLabelN("Wave ", p.Wave)
}

// TicketCountLabel renders "{n} ticket" / "{n} tickets" — the small pluralised
// count beside each wave header.
func TicketCountLabel(n int) string {
	if n == 1 {
		return "1 ticket"
	}
	return itoa(n) + " tickets"
}

// indexLeadPhases returns the phase rows the index should render: all of them,
// except when ?phase=unphased narrowed the view to the Unphased section only
// (then none, so PhaseList renders just the Unphased row).
func indexLeadPhases(p IndexProps) []PhaseRowProps {
	if p.OnlyUnphased {
		return nil
	}
	return p.Phases
}

// UnphasedCountLabel renders the count suffix on the Unphased pseudo-phase
// summary — "unphased ticket" / "unphased tickets" (the leading number is
// rendered separately in a <strong>).
func UnphasedCountLabel(n int) string {
	if n == 1 {
		return "unphased ticket"
	}
	return "unphased tickets"
}

// PercentOf returns part/total as a 0-100 integer (floor); total==0 → 0.
// Matches the legacy funcMap helper of the same name so the inline
// style="width: X%" values stay byte-identical with the html/template output.
func PercentOf(part, total int) int {
	if total <= 0 {
		return 0
	}
	return part * 100 / total
}

// PhaseRowBarTitle renders the title attribute the legacy template put on the
// progress-bar wrapper. Kept as a helper so the templ stays declarative.
func PhaseRowBarTitle(d PhaseDist) string {
	return itoa(d.Todo) + " todo · " +
		itoa(d.InProgress) + " in progress · " +
		itoa(d.Testing) + " testing · " +
		itoa(d.Done) + " done"
}

// waveLabelN formats `<prefix><n>`.
func waveLabelN(prefix string, n int) string { return prefix + itoa(n) }

// WaveClass returns the wrapper section class for a wave — adds
// `muted-section` when the wave is the unassigned bucket so CSS can dim it.
func WaveClass(p WaveSectionProps) string {
	if p.IsUnassigned {
		return "phase-wave muted-section"
	}
	return "phase-wave"
}

// WaveChipClass returns the chip's class — adds the `-unassigned` variant
// when relevant. Kept next to WaveClass so the two style-name pairs stay
// co-located.
func WaveChipClass(p WaveSectionProps) string {
	if p.IsUnassigned {
		return "phase-wave-chip phase-wave-chip-unassigned"
	}
	return "phase-wave-chip"
}

// AssignPhaseAction builds the assign-phase form's action URL, including
// the `?slug=` hint when ProjectSlug is set so hostStoreForTicket can skip
// its O(mounts) walk on the redirect target. Pulled out of the .templ so
// the markup stays declarative.
func AssignPhaseAction(p AssignPhaseFormProps) string {
	base := "/tickets/" + p.TicketID + "/assign-phase"
	if p.ProjectSlug != "" {
		return base + "?slug=" + p.ProjectSlug
	}
	return base
}

// TicketCountLabelActiveTotal renders the "N active / M total" breadcrumb
// segment on the phase-detail header.
func TicketCountLabelActiveTotal(active, total int) string {
	return itoa(active) + " active / " + itoa(total) + " total"
}

// DeleteConfirmJS renders the onsubmit JS that prompts for a final yes/no
// before deleting a phase. The legacy template used an inline `onsubmit=
// "return confirm('Delete phase X? ...');"`; we re-emit the same shape so
// behaviour is byte-identical with the html/template version.
//
// Slugs are constrained to [a-z0-9-]+ server-side, so single-quote splicing
// is safe — but the templ.JSUnsafeFuncCall call site advertises the trust
// boundary anyway.
func DeleteConfirmJS(slug string) string {
	return "return confirm('Delete phase " + slug + "? Tickets must be reassigned or deleted first.');"
}

// BarWidthStyle returns the inline `width: X%` style for one progress-bar
// segment — the single inline-style escape hatch the W1 brief permits for
// server-computed widths. Returned as templ.SafeCSS so the templ runtime
// trusts the (server-derived, integer-shaped) value without sanitisation.
// Static colours come from the .phase-row-bar-{column} classes.
func BarWidthStyle(part, total int) templ.SafeCSS {
	return templ.SafeCSS("width: " + strconv.Itoa(PercentOf(part, total)) + "%")
}

// itoa is just strconv.Itoa, aliased so the helpers above read cleanly.
func itoa(n int) string { return strconv.Itoa(n) }

// _ pins the html/template dependency: SummaryHTML fields above need it.
var _ template.HTML
