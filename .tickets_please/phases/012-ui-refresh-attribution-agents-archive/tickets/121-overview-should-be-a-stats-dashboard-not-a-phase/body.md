The project overview (`projects/detail.templ`) led with the full collapsible phase/wave `PhaseList` — effectively the phases page pasted on top. The overview should be stats/metadata + general usage info.

Fix: lead with the headline metric cards (added a 5th "Phases" card + "% complete" / "N unphased" sub-hints), keep status distribution, and render phases as a *compact* summary card (`phases.PhaseSummary`: name + mini-bar/counts, one row each) with a "Browse all phases →" header link instead of the wave drill-down. Extended `ui.CardProps` with an optional header `Href`/`LinkLabel`.
