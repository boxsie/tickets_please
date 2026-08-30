package svc

// archive_decide.go: the W3 archive policy decision matrix.
//
// Decision rules (per entry, mirrors the SPEC phase summary):
//
//   NEVER archive if archived already.
//   NEVER archive if policy is disabled at the project level.
//   ARCHIVE if age ≥ min_age_days
//           AND retrievals ≥ min_retrievals
//           AND (no recent feedback OR dislike ratio over threshold).
//   EARLY ARCHIVE if age ≥ early_archive_age_days
//                AND total_ratings ≥ 3
//                AND dislike_ratio ≥ configured threshold.
//
// Pure function, no side effects, no I/O. T6 wraps it in a sweep that flips
// the `archived` flag and writes an audited system_archive comment.

import (
	"time"

	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// ArchiveDecision is the outcome of evaluating one ticket against the policy.
type ArchiveDecision struct {
	// Archive is true when the ticket should be flipped to archived.
	Archive bool
	// Reason is a short human-readable string explaining why; written into
	// the system_archive comment by the T6 sweep, or surfaced in the dry-run
	// report.
	Reason string
}

// ArchivePolicy is the merged set of knobs the Decide helper evaluates.
// Resolved from a project.yaml `archive` block via resolveArchivePolicy().
type ArchivePolicy struct {
	Enabled             bool
	MinAgeDays          int
	MinRetrievals       int
	DislikeRatio        float64
	EarlyArchiveAgeDays int
	AutoSweepOnMount    bool
}

// defaultArchivePolicy returns the canonical defaults from SPEC.md.
//
//	enabled: false               # opt-in
//	min_age_days: 180
//	min_retrievals: 3
//	dislike_ratio: 0.5
//	early_archive_age_days: 30
//	auto_sweep_on_mount: false
func defaultArchivePolicy() ArchivePolicy {
	return ArchivePolicy{
		Enabled:             false,
		MinAgeDays:          180,
		MinRetrievals:       3,
		DislikeRatio:        0.5,
		EarlyArchiveAgeDays: 30,
		AutoSweepOnMount:    false,
	}
}

// resolveArchivePolicy merges a per-project ArchiveConfigRecord onto the
// canonical defaults. Each missing field inherits the default value, so a
// project that only opts in (`enabled: true`) gets the default thresholds.
func resolveArchivePolicy(rec *store.ArchiveConfigRecord) ArchivePolicy {
	out := defaultArchivePolicy()
	if rec == nil {
		return out
	}
	if rec.Enabled != nil {
		out.Enabled = *rec.Enabled
	}
	if rec.MinAgeDays != nil {
		out.MinAgeDays = *rec.MinAgeDays
	}
	if rec.MinRetrievals != nil {
		out.MinRetrievals = *rec.MinRetrievals
	}
	if rec.DislikeRatio != nil {
		out.DislikeRatio = *rec.DislikeRatio
	}
	if rec.EarlyArchiveAgeDays != nil {
		out.EarlyArchiveAgeDays = *rec.EarlyArchiveAgeDays
	}
	if rec.AutoSweepOnMount != nil {
		out.AutoSweepOnMount = *rec.AutoSweepOnMount
	}
	return out
}

// archiveAgeAnchor returns the timestamp the policy should compute age
// against. We prefer CompletedAt for done tickets (the natural "this work is
// finished and unchanging" anchor), falling back to UpdatedAt for non-done
// tickets that have stagnated.
func archiveAgeAnchor(t *domain.Ticket) time.Time {
	if t == nil {
		return time.Time{}
	}
	if t.CompletedAt != nil {
		return *t.CompletedAt
	}
	return t.UpdatedAt
}

// dislikeRatio returns dislikes / (likes + dislikes). Returns 0 when there
// are no ratings at all.
func dislikeRatio(rec domain.FeedbackRecord) float64 {
	total := rec.Likes + rec.Dislikes
	if total == 0 {
		return 0
	}
	return float64(rec.Dislikes) / float64(total)
}

// Decide evaluates a single ticket against the policy and feedback record,
// returning whether to archive plus a human-readable reason. Pure function;
// the T6 sweep applies the result.
func Decide(t *domain.Ticket, fb domain.FeedbackRecord, cfg ArchivePolicy, now time.Time) ArchiveDecision {
	if t == nil {
		return ArchiveDecision{}
	}
	if !cfg.Enabled {
		return ArchiveDecision{Reason: "policy disabled"}
	}
	if t.Archived {
		return ArchiveDecision{Reason: "already archived"}
	}
	anchor := archiveAgeAnchor(t)
	if anchor.IsZero() {
		return ArchiveDecision{Reason: "no age anchor"}
	}
	ageDays := int(now.Sub(anchor).Hours() / 24)
	if ageDays < 0 {
		ageDays = 0
	}

	totalRatings := fb.Likes + fb.Dislikes
	ratio := dislikeRatio(fb)

	// Early-archive: signal-driven. Younger than the long-term threshold but
	// the LLM consumers have rated it badly enough that holding onto it just
	// pollutes top-k.
	if ageDays >= cfg.EarlyArchiveAgeDays && totalRatings >= 3 && ratio >= cfg.DislikeRatio {
		return ArchiveDecision{
			Archive: true,
			Reason:  decisionReason("early", ageDays, fb.Retrievals, ratio, cfg),
		}
	}

	// Long-term archive: age + retrievals threshold. The
	// "min_retrievals" gate means we only archive things the LLMs have HAD
	// the chance to see; a brand-new ticket that's never been searched yet
	// stays around even if it's old.
	if ageDays >= cfg.MinAgeDays && fb.Retrievals >= cfg.MinRetrievals {
		// No-recent-feedback OR dislike-ratio condition.
		if totalRatings == 0 || ratio >= cfg.DislikeRatio {
			return ArchiveDecision{
				Archive: true,
				Reason:  decisionReason("aged", ageDays, fb.Retrievals, ratio, cfg),
			}
		}
	}
	return ArchiveDecision{Reason: "below thresholds"}
}

// decisionReason renders a short reason string used in the sweep report and
// the system_archive audit comment.
func decisionReason(kind string, ageDays, retrievals int, ratio float64, cfg ArchivePolicy) string {
	switch kind {
	case "early":
		return formatReason("early", ageDays, retrievals, ratio)
	case "aged":
		return formatReason("aged", ageDays, retrievals, ratio)
	}
	return kind
}

// formatReason is split from decisionReason so future kinds can add their
// own logic without touching the printf one-liner.
func formatReason(kind string, ageDays, retrievals int, ratio float64) string {
	return kind + ": age=" + itoa(ageDays) + "d retrievals=" + itoa(retrievals) + " dislike_ratio=" + ftoa(ratio)
}

// itoa / ftoa: tiny stringifiers to keep the package import list lean.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func ftoa(f float64) string {
	// 0.dd precision is plenty for the reason string.
	whole := int(f * 100)
	if whole < 0 {
		return "-" + ftoa(-f)
	}
	hundredths := whole % 100
	tens := whole / 100
	out := itoa(tens) + "."
	if hundredths < 10 {
		out += "0"
	}
	out += itoa(hundredths)
	return out
}
