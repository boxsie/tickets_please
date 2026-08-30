package svc

// feedback.go: thin service layer over the per-project FeedbackStore.
//
// The store itself (internal/store/feedback.go) owns the on-disk layout, the
// per-project flock, and atomic writes. This file owns the API surface the
// MCP layer drives: validating + resolving entry keys and partial-success
// reporting. T3 will add RecordRetrieval on every search hit.

import (
	"context"
	"fmt"

	"tickets_please/internal/cache"
	"tickets_please/internal/domain"
)

// RateInput is the per-call payload for RateSearchResult.
type RateInput struct {
	ProjectIDOrSlug string
	EntryKeys       []domain.EntryKey
	Rating          domain.Rating
	Reason          string
}

// RateOutputEntry is one record in the per-key updated/rejected response. For
// `updated` entries Likes/Dislikes carry the post-write counters so the
// caller can short-circuit a follow-up read, and ProjectSlug names the project
// the rating actually landed in — worth surfacing because a batch from a
// cross-project search fans out across several feedback stores.
type RateOutputEntry struct {
	EntryKey    domain.EntryKey
	ProjectSlug string
	Likes       int
	Dislikes    int
	Error       string // populated only in the rejected slice
}

// RateOutput is the partial-success result of RateSearchResult.
type RateOutput struct {
	Updated  []RateOutputEntry
	Rejected []RateOutputEntry
}

// rateMaxEntryKeys mirrors the tool-schema cap; enforced server-side too so a
// client that bypasses the schema can't load the store unboundedly.
const rateMaxEntryKeys = 50

// RateSearchResult applies the same rating to every entry key in the input,
// reporting per-key success/rejection. Valid keys land in the feedback store of
// the project that owns them and their post-write counters are returned. Keys
// no mounted project owns are rejected. A malformed-or-empty input is a hard
// error; everything else is partial-success.
//
// CROSS-PROJECT: ProjectIDOrSlug is where we look first, not a fence. Search
// can return hits from other projects (`cross_project` on search_learnings /
// search_comments), and the whole point of a rating is often that such a hit
// was wrong — so a rating has to be able to land wherever the entry lives, or
// the only un-ratable results are exactly the ones most in need of a
// correction. Keys the primary project doesn't own fall through to a walk over
// the other mounts; the rating is recorded against the owning project's store.
// The in-project case never walks.
//
// Feedback writes go to feedback.yaml only, but the per-project flock
// serialises with all other project mutations, so this isn't free under
// contention.
func (s *Service) RateSearchResult(ctx context.Context, in RateInput) (RateOutput, error) {
	if _, _, err := s.requireSession(ctx); err != nil {
		return RateOutput{}, err
	}
	if len(in.EntryKeys) == 0 {
		return RateOutput{}, fmt.Errorf("%w: entry_keys must be non-empty", domain.ErrInvalidArgument)
	}
	if len(in.EntryKeys) > rateMaxEntryKeys {
		return RateOutput{}, fmt.Errorf("%w: entry_keys must be <= %d, got %d",
			domain.ErrInvalidArgument, rateMaxEntryKeys, len(in.EntryKeys))
	}
	if in.Rating != domain.RatingLike && in.Rating != domain.RatingDislike {
		return RateOutput{}, fmt.Errorf("%w: rating must be 'like' or 'dislike', got %q",
			domain.ErrInvalidArgument, in.Rating)
	}
	if in.ProjectIDOrSlug == "" {
		return RateOutput{}, fmt.Errorf("%w: project_id_or_slug required", domain.ErrInvalidArgument)
	}

	lp, _, err := s.Cache.Get(ctx, in.ProjectIDOrSlug)
	if err != nil {
		return RateOutput{}, err
	}

	// Resolve via the cache's slug (canonical), not the input's slug-or-id —
	// the mount registry is slug-keyed.
	slug := lp.Project.Slug
	mount := s.mountForSlug(slug)
	if mount == nil || mount.Feedback == nil {
		return RateOutput{}, fmt.Errorf("%w: feedback store not available for project %q",
			domain.ErrFailedPrecondition, slug)
	}

	// Owner lookups are memoised across the batch: hits from one cross-project
	// sweep cluster into a handful of projects, so the walk usually runs once
	// per extra project rather than once per key.
	owners := map[string]*rateTarget{slug: {slug: slug, lp: lp, mount: mount}}

	out := RateOutput{}
	for _, raw := range in.EntryKeys {
		key := domain.EntryKey(raw)
		kind, id, ok := domain.ParseEntryKey(string(key))
		if !ok {
			out.Rejected = append(out.Rejected, RateOutputEntry{
				EntryKey: key,
				Error:    "malformed entry key (expected '<kind>:<id>' with kind in {ticket,learning,comment})",
			})
			continue
		}
		target := s.rateTargetFor(ctx, kind, id, owners)
		if target == nil {
			out.Rejected = append(out.Rejected, RateOutputEntry{
				EntryKey: key,
				Error:    "unknown entry: no such " + string(kind) + " in any mounted project",
			})
			continue
		}
		if target.mount.Feedback == nil {
			out.Rejected = append(out.Rejected, RateOutputEntry{
				EntryKey: key,
				Error:    "feedback store not available for project " + target.slug,
			})
			continue
		}
		if err := target.mount.Feedback.RecordRating(ctx, key, in.Rating, in.Reason); err != nil {
			out.Rejected = append(out.Rejected, RateOutputEntry{
				EntryKey: key,
				Error:    err.Error(),
			})
			continue
		}
		rec, _ := target.mount.Feedback.Get(key)
		out.Updated = append(out.Updated, RateOutputEntry{
			EntryKey:    key,
			ProjectSlug: target.slug,
			Likes:       rec.Likes,
			Dislikes:    rec.Dislikes,
		})
	}
	return out, nil
}

// rateTarget is a resolved rating destination: the project that owns an entry,
// plus the handles needed to check membership and write the rating.
type rateTarget struct {
	slug  string
	lp    *cache.LoadedProject
	mount *ProjectMount
}

// rateTargetFor finds the project that owns an entry. Already-resolved
// projects in `owners` are tried first (the caller seeds it with the primary
// project, so an in-project batch never walks); on a miss it walks the
// remaining mounts, loading each one's cache entry to ask the authoritative
// question — is this id actually in that project?
//
// Returns nil when no mounted project owns the entry. A project that isn't
// mounted at all is indistinguishable from a non-existent entry here; that's
// the same blind spot search itself has, since an unmounted project can't
// return hits either.
func (s *Service) rateTargetFor(ctx context.Context, kind domain.EntryKind, id string, owners map[string]*rateTarget) *rateTarget {
	for _, t := range owners {
		if feedbackKeyExists(t.lp, kind, id) {
			return t
		}
	}
	var slugs []string
	_ = s.WalkProjectMounts(func(slug string, mount *ProjectMount) error {
		if _, seen := owners[slug]; !seen && mount != nil {
			slugs = append(slugs, slug)
		}
		return nil
	})
	for _, slug := range slugs {
		lp, _, err := s.Cache.Get(ctx, slug)
		if err != nil {
			// A project that won't load can't be asked; skip it rather than
			// failing the whole batch over an unrelated mount.
			if s.Logger != nil {
				s.Logger.Warn("svc: rating owner lookup skipped a project", "slug", slug, "err", err)
			}
			continue
		}
		t := &rateTarget{slug: slug, lp: lp, mount: s.mountForSlug(slug)}
		owners[slug] = t
		if t.mount != nil && feedbackKeyExists(lp, kind, id) {
			return t
		}
	}
	return nil
}

// FeedbackCounts returns the like/dislike tallies for a set of entry keys in a
// project, for inline display next to search hits. It's read-only display data,
// so no session is required and unknown / never-rated keys are simply absent
// from the returned map (callers treat a miss as zero counts). A project with
// no feedback store yet yields an empty map, never an error.
func (s *Service) FeedbackCounts(ctx context.Context, projectIDOrSlug string, keys []domain.EntryKey) (map[domain.EntryKey]domain.FeedbackRecord, error) {
	out := make(map[domain.EntryKey]domain.FeedbackRecord, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	lp, _, err := s.Cache.Get(ctx, projectIDOrSlug)
	if err != nil {
		return nil, err
	}
	mount := s.mountForSlug(lp.Project.Slug)
	if mount == nil || mount.Feedback == nil {
		return out, nil
	}
	for _, k := range keys {
		if rec, ok := mount.Feedback.Get(k); ok {
			out[k] = rec
		}
	}
	return out, nil
}

// feedbackKeyExists reports whether (kind, id) addresses a real entity in the
// loaded project. Ticket/learning kinds both resolve via lp.Tickets (learnings
// are 1:1 with their parent ticket); comment kinds walk lp.Comments.
//
// Holds lp.Lock briefly in read mode — the map reads are O(1) but the cache
// may have an in-flight write we shouldn't race.
func feedbackKeyExists(lp *cache.LoadedProject, kind domain.EntryKind, id string) bool {
	lp.Lock.RLock()
	defer lp.Lock.RUnlock()
	switch kind {
	case domain.EntryKindTicket, domain.EntryKindLearning:
		_, ok := lp.Tickets[id]
		return ok
	case domain.EntryKindComment:
		for _, cs := range lp.Comments {
			for _, c := range cs {
				if c.ID == id {
					return true
				}
			}
		}
		return false
	}
	return false
}
