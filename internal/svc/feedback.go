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
// caller can short-circuit a follow-up read.
type RateOutputEntry struct {
	EntryKey domain.EntryKey
	Likes    int
	Dislikes int
	Error    string // populated only in the rejected slice
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
// reporting per-key success/rejection. Unknown keys (no such ticket / learning
// / comment in this project) are rejected; valid keys land in the store and
// their post-write counters are returned. A malformed-or-empty input is a
// hard error; everything else is partial-success.
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
		if !feedbackKeyExists(lp, kind, id) {
			out.Rejected = append(out.Rejected, RateOutputEntry{
				EntryKey: key,
				Error:    "unknown entry: no such " + string(kind) + " in this project",
			})
			continue
		}
		if err := mount.Feedback.RecordRating(ctx, key, in.Rating, in.Reason); err != nil {
			out.Rejected = append(out.Rejected, RateOutputEntry{
				EntryKey: key,
				Error:    err.Error(),
			})
			continue
		}
		rec, _ := mount.Feedback.Get(key)
		out.Updated = append(out.Updated, RateOutputEntry{
			EntryKey: key,
			Likes:    rec.Likes,
			Dislikes: rec.Dislikes,
		})
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
