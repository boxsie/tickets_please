package svc

// feedback_score.go: the Bayesian-smoothed quality multiplier that tilts
// search ranking by feedback signal.
//
// Formula:
//
//   quality    = (likes + alpha) / (likes + dislikes + alpha + beta)   // ∈ (0, 1)
//   multiplier = min_multiplier + (1 - min_multiplier) * quality        // ∈ [min_multiplier, 1.0]
//   final     = cosine_similarity * multiplier
//
// Defaults α = β = 2 give an unrated entry quality = 0.5 → multiplier = 0.75,
// so unrated content drags slightly but isn't crushed. Constants are
// configurable per-project via project.yaml's `feedback` block.

import (
	"sort"

	"tickets_please/internal/domain"
	"tickets_please/internal/vecindex"
)

// QualityParams configures the multiplier formula. Zero values are NOT useful
// defaults — call defaultQualityParams() when no per-project config is set.
type QualityParams struct {
	// Alpha is the prior "likes" the formula adds to every entry. Larger
	// values pull the multiplier toward 1 / (alpha+beta) for sparse data.
	Alpha float64
	// Beta is the prior "dislikes". α = β makes the unrated baseline 0.5.
	Beta float64
	// MinMultiplier is the lower bound of the score multiplier — a never-
	// rated entry's score becomes cosine * (min + (1-min)*0.5).
	MinMultiplier float64
	// Enabled is the kill switch. False → pure-cosine ranking, no rescore.
	Enabled bool
}

// defaultQualityParams returns the canonical α=β=2, min=0.5, enabled values.
// Used by mounts whose project.yaml lacks an explicit feedback block.
func defaultQualityParams() QualityParams {
	return QualityParams{Alpha: 2.0, Beta: 2.0, MinMultiplier: 0.5, Enabled: true}
}

// Multiplier returns the score multiplier for an entry with the given
// like/dislike counts. Result is clamped to [MinMultiplier, 1.0].
func (p QualityParams) Multiplier(likes, dislikes int) float64 {
	if !p.Enabled {
		return 1.0
	}
	if p.Alpha <= 0 {
		p.Alpha = 2.0
	}
	if p.Beta <= 0 {
		p.Beta = 2.0
	}
	if p.MinMultiplier < 0 {
		p.MinMultiplier = 0
	}
	if p.MinMultiplier > 1 {
		p.MinMultiplier = 1
	}
	n := float64(likes) + float64(dislikes) + p.Alpha + p.Beta
	if n == 0 {
		return p.MinMultiplier + (1.0-p.MinMultiplier)*0.5
	}
	quality := (float64(likes) + p.Alpha) / n
	return p.MinMultiplier + (1.0-p.MinMultiplier)*quality
}

// applyQualityMultiplier walks `hits` (an already-ranked-by-cosine slice
// returned from vecindex.Index.Search), looks each id up in the mount's
// feedback store, multiplies its score by the formula's multiplier, and
// returns the re-sorted slice. When mount.Feedback is nil or
// params.Enabled is false the input is returned unchanged — the multiplier
// path silently no-ops so search remains cosine-only on misconfigured mounts.
//
// `kind` selects which entry-key form to look up (ticket / learning / comment).
//
// Returned hits carry the FINAL (adjusted) score in Hit.Score; the original
// cosine score is preserved in a parallel rawScores map keyed by id so the
// MCP layer can surface it as `raw_score`.
func applyQualityMultiplier(hits []vecindex.Hit, kind vecindex.Kind, mount *ProjectMount, params QualityParams) ([]vecindex.Hit, map[string]float32) {
	rawScores := make(map[string]float32, len(hits))
	for _, h := range hits {
		rawScores[h.ID] = h.Score
	}
	if !params.Enabled || mount == nil || mount.Feedback == nil || len(hits) == 0 {
		return hits, rawScores
	}
	adjusted := make([]vecindex.Hit, len(hits))
	copy(adjusted, hits)
	for i := range adjusted {
		key := entryKeyForKindID(kind, adjusted[i].ID)
		if key == "" {
			continue
		}
		// Negative cosine means anti-correlation; scaling by a positive
		// multiplier would invert "higher likes → higher rank" semantics
		// (a less-negative number sorts higher). Pass anti-correlated hits
		// through unchanged so they sit at their raw negative score and
		// sort below positively-scored content naturally.
		if adjusted[i].Score <= 0 {
			continue
		}
		rec, ok := mount.Feedback.Get(key)
		if !ok {
			rec = domain.FeedbackRecord{}
		}
		adjusted[i].Score = float32(float64(adjusted[i].Score) * params.Multiplier(rec.Likes, rec.Dislikes))
	}
	sort.SliceStable(adjusted, func(i, j int) bool {
		if adjusted[i].Score != adjusted[j].Score {
			return adjusted[i].Score > adjusted[j].Score
		}
		return adjusted[i].ID < adjusted[j].ID
	})
	return adjusted, rawScores
}

// entryKeyForKindID maps a vecindex.Kind + id into the corresponding
// `<kind>:<id>` feedback entry key. Unknown kinds (or summary-index hits,
// which the search RPCs don't rate) return the empty string so the caller
// short-circuits.
func entryKeyForKindID(kind vecindex.Kind, id string) domain.EntryKey {
	switch kind {
	case vecindex.KindTicketBody:
		return domain.TicketEntryKey(id)
	case vecindex.KindTicketLearnings:
		return domain.LearningEntryKey(id)
	case vecindex.KindComment:
		return domain.CommentEntryKey(id)
	}
	return ""
}

// mountQualityParams returns the QualityParams to apply for a mount. Nil
// mount (registry-empty stdio fallback) returns disabled params so the
// fallback path stays pure-cosine. A nilled-out (LRU-evicted) mount.Feedback
// is handled inside applyQualityMultiplier.
func mountQualityParams(mount *ProjectMount) QualityParams {
	if mount == nil {
		return QualityParams{} // Enabled=false zero-value → no rescore.
	}
	return mount.QualityParams
}

// expandedRawLimit returns the over-fetch ceiling for a re-scoring search.
// We fetch up to 2 × the requested limit from the vec index so the rescore
// can promote near-misses that a high quality multiplier would push into
// top-k. Capped at searchMaxLimit (50) — a hard ceiling regardless of input.
// In practice clampSearchLimit at the search-method edge already constrains
// limit to ≤ searchMaxLimit, so this cap only kicks in for contrived inputs.
func expandedRawLimit(limit int) int {
	const factor = 2
	out := limit * factor
	if out > searchMaxLimit {
		out = searchMaxLimit
	}
	return out
}
