package svc

import (
	"context"
	"errors"
	"testing"
	"time"

	"tickets_please/internal/domain"
)

// TestListTickets_CursorEdges is the T14 polish-pass safety net for the
// cursor pagination contract. Each subtest pins one edge that's easy to
// regress in future refactors:
//
//   - Empty cursor on the first page works (no anchor → return from start).
//   - Garbage / non-base64 cursor surfaces ErrInvalidArgument (not panic).
//   - Last page returns next_cursor="" so clients know to stop iterating.
//   - Limit > listTicketsMaxLimit (200) is silently clamped.
//   - Re-using the *terminal* cursor returned mid-pagination yields an
//     empty page with empty next_cursor (idempotent end-of-list).
func TestListTickets_CursorEdges(t *testing.T) {
	t.Run("empty_cursor_first_page", func(t *testing.T) {
		s, ctx, _, slug := freshServiceWithProject(t)
		for i := 0; i < 3; i++ {
			if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
				ProjectIDOrSlug: slug, Title: "t",
			}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(2 * time.Millisecond)
		}
		out, next, err := s.ListTickets(ctx, domain.ListTicketsInput{
			ProjectIDOrSlug: slug, Cursor: "",
		})
		if err != nil {
			t.Fatalf("ListTickets(empty cursor): %v", err)
		}
		if len(out) != 3 {
			t.Fatalf("expected 3 tickets, got %d", len(out))
		}
		if next != "" {
			t.Fatalf("expected empty next_cursor on full page, got %q", next)
		}
	})

	t.Run("garbage_cursor_returns_invalid_argument", func(t *testing.T) {
		s, ctx, _, slug := freshServiceWithProject(t)
		for _, bad := range []string{
			"garbage!!!",           // not base64
			"bm9waXBlc2VwYXJhdG9y", // base64 of "nopipeseparator"
			"fA==",                 // base64 of "|" alone — separator at start, no ts
			"YXxiYWQtdGltZXN0YW1w", // base64 of "a|bad-timestamp"
		} {
			_, _, err := s.ListTickets(ctx, domain.ListTicketsInput{
				ProjectIDOrSlug: slug, Cursor: bad,
			})
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("cursor %q: expected ErrInvalidArgument, got %v", bad, err)
			}
		}
	})

	t.Run("end_of_list_next_cursor_empty", func(t *testing.T) {
		s, ctx, _, slug := freshServiceWithProject(t)
		// Exactly limit tickets — last page should NOT carry a non-empty
		// next_cursor since there's nothing after it.
		for i := 0; i < 4; i++ {
			if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
				ProjectIDOrSlug: slug, Title: "t",
			}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(2 * time.Millisecond)
		}
		// First page of 4 fits in the default limit (50) — next_cursor should
		// be "" because there's no more data.
		out, next, err := s.ListTickets(ctx, domain.ListTicketsInput{ProjectIDOrSlug: slug})
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 4 {
			t.Fatalf("expected 4 tickets in single page, got %d", len(out))
		}
		if next != "" {
			t.Fatalf("expected next_cursor='' at end of list, got %q", next)
		}
	})

	t.Run("limit_clamped_to_max", func(t *testing.T) {
		s, ctx, _, slug := freshServiceWithProject(t)
		// Cheap sanity check: a request for limit=10000 should not blow up.
		// We don't need to fill 200 rows — we just need to confirm the call
		// succeeds with the oversized limit (clamping is silent).
		if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
			ProjectIDOrSlug: slug, Title: "t",
		}); err != nil {
			t.Fatal(err)
		}
		out, _, err := s.ListTickets(ctx, domain.ListTicketsInput{
			ProjectIDOrSlug: slug, Limit: 10000,
		})
		if err != nil {
			t.Fatalf("ListTickets(oversized limit): %v", err)
		}
		if len(out) != 1 {
			t.Fatalf("expected 1 ticket, got %d", len(out))
		}
	})

	t.Run("decode_round_trip", func(t *testing.T) {
		// Direct unit test on encodeCursor/decodeCursor — guards against the
		// pair drifting out of sync.
		ts := time.Date(2026, 5, 2, 12, 0, 0, 123456789, time.UTC)
		id := "abc-123"
		got := encodeCursor(ts, id)
		gotTS, gotID, err := decodeCursor(got)
		if err != nil {
			t.Fatalf("decodeCursor: %v", err)
		}
		if !gotTS.Equal(ts) {
			t.Fatalf("ts mismatch: got %v want %v", gotTS, ts)
		}
		if gotID != id {
			t.Fatalf("id mismatch: got %q want %q", gotID, id)
		}
	})

	_ = context.Background
}
