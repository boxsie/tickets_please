## Goal

Tilt search ranking by feedback signal without overwhelming the embedding similarity that's still the primary ordering force.

## Scope

### Scoring formula

For each candidate hit:

```
quality   = (likes + α) / (likes + dislikes + α + β)        // α = β = 2
multiplier = 0.5 + 0.5 * quality                            // ∈ [0.5, 1.0]
final     = cosine_similarity * multiplier
```

With `α = β = 2`, a never-rated entry has `quality = 0.5` → `multiplier = 0.75`, so unrated content gets a small reputational drag but isn't crushed. A 5/0 record → `quality ≈ 0.78` → `multiplier ≈ 0.89`. A 0/3 record → `quality ≈ 0.29` → `multiplier ≈ 0.64`. Small but meaningful at the top-k boundary.

### Implementation

Add `internal/svc/feedback_score.go` (or similar) with:

```go
type QualityParams struct {
    Alpha float64
    Beta  float64
    MinMultiplier float64  // 0.5
}

func (p QualityParams) Multiplier(likes, dislikes int) float64
```

Apply in `SearchTickets` / `SearchLearnings` / `SearchComments`: after the vec index returns candidates, look each up in the project's feedback store and multiply scores before truncating to top-k. Resort by adjusted score.

Top-k expansion: because re-scoring can reorder, fetch `top-k * 2` (capped at 50) from the vec index before applying the multiplier, then truncate to the requested `k`. This avoids losing high-quality entries that almost-but-not-quite made the cosine cut.

### Config knobs

Per-project in `project.yaml`:

```yaml
feedback:
  alpha: 2.0
  beta: 2.0
  min_multiplier: 0.5
  enabled: true   # off → behave as today (cosine-only)
```

Defaults applied when missing. `enabled: false` is the kill switch for users who want to A/B compare or disable entirely.

### Surfacing the adjusted score

The hit `score` field reports the **final** (adjusted) score, not raw cosine. Add a sibling `raw_score` field so debugging and external tooling can see the delta. Document in SPEC.

### Tests

- Pure-cosine baseline (feedback empty): scores unchanged vs pre-T4.
- Single-entry like bump: multiplier increases predictably.
- Single-entry dislike bump: multiplier decreases.
- Reordering: construct two candidates with cosine 0.85 and 0.80; load feedback so the 0.80-candidate has high likes; assert it ranks first.
- `enabled: false` short-circuits the multiplier entirely.
- The top-k expansion fetches `2k` before truncation (assert via vec-index mock).

## Out of scope

- Decay / time-weighted likes (a like from a year ago counts the same as today's — boring intentionally; revisit if data argues otherwise).
- Query-conditional weighting (per-(result, query) feedback — phase out-of-scope).
- A `rerank_only` flag to disable the cosine sort and rank purely by quality — not useful for now.

## Critical files

- `internal/svc/feedback_score.go` (new)
- `internal/svc/search.go` — apply in all three search methods
- `internal/svc/search_test.go` — new tests; existing tests may need updates if they asserted exact score numbers (the prior of 0.75 changes baseline)
- `internal/domain/project.go` — `FeedbackConfig` block on Project
- `internal/store/project.go` — serialisation
- `SPEC.md` — document the formula and `raw_score` vs `score`

Depends on T1.
