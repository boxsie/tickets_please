package svc

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// TestResolveTicketRef covers the shortcode→UUID resolver: UUID passthrough,
// "<slug>/<number>" and bare "<number>" forms (incl. zero-padded), and the
// error paths (unbound bare number, missing ticket, empty ref).
func TestResolveTicketRef(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)

	var ids []string
	for i := 0; i < 3; i++ {
		tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
			ProjectIDOrSlug: slug, Title: fmt.Sprintf("ticket %d", i),
		})
		if err != nil {
			t.Fatalf("CreateTicket: %v", err)
		}
		ids = append(ids, tk.ID)
	}

	// Ground-truth number→id from a direct store walk; cross-check the resolver
	// against it rather than assuming the allocation scheme.
	st, err := s.ResolveProjectStore(ctx, slug)
	if err != nil {
		t.Fatalf("ResolveProjectStore: %v", err)
	}
	numToID := map[int]string{}
	if err := st.WalkTickets(slug, func(_, _ string, tr *store.TicketRecord) error {
		numToID[tr.Number] = tr.ID
		return nil
	}); err != nil {
		t.Fatalf("WalkTickets: %v", err)
	}
	if len(numToID) != 3 {
		t.Fatalf("expected 3 tickets on disk, got %d", len(numToID))
	}

	for num, id := range numToID {
		// "<slug>/<num>" works regardless of the default slug.
		if got, err := s.ResolveTicketRef(ctx, "", fmt.Sprintf("%s/%d", slug, num)); err != nil || got != id {
			t.Fatalf("ResolveTicketRef(%s/%d) = %q, %v; want %q", slug, num, got, err, id)
		}
		// bare "<num>" resolves against the default slug.
		if got, err := s.ResolveTicketRef(ctx, slug, strconv.Itoa(num)); err != nil || got != id {
			t.Fatalf("ResolveTicketRef(default=%s, %d) = %q, %v; want %q", slug, num, got, err, id)
		}
		// zero-padded shortcodes (the form that appears in commit messages).
		if got, err := s.ResolveTicketRef(ctx, "", fmt.Sprintf("%s/%03d", slug, num)); err != nil || got != id {
			t.Fatalf("ResolveTicketRef(%s/%03d) = %q, %v; want %q", slug, num, got, err, id)
		}
	}

	// UUID passes through untouched (no existence check at this layer).
	if got, err := s.ResolveTicketRef(ctx, slug, ids[0]); err != nil || got != ids[0] {
		t.Fatalf("ResolveTicketRef(uuid) = %q, %v; want %q", got, err, ids[0])
	}
	// An arbitrary opaque non-shortcode id also passes through unchanged.
	if got, err := s.ResolveTicketRef(ctx, slug, "not-a-shortcode-xyz"); err != nil || got != "not-a-shortcode-xyz" {
		t.Fatalf("ResolveTicketRef(opaque) = %q, %v; want passthrough", got, err)
	}

	// Bare number with no default slug → invalid argument.
	if _, err := s.ResolveTicketRef(ctx, "", "1"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("bare number without slug: want ErrInvalidArgument, got %v", err)
	}

	// Missing ticket → not found, message names slug + number.
	_, err = s.ResolveTicketRef(ctx, slug, fmt.Sprintf("%s/9999", slug))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing ticket: want ErrNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "9999") || !strings.Contains(err.Error(), slug) {
		t.Fatalf("not-found error should name slug + number, got %q", err.Error())
	}

	// Empty ref → invalid argument.
	if _, err := s.ResolveTicketRef(ctx, slug, ""); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("empty ref: want ErrInvalidArgument, got %v", err)
	}

	// Truncated UUID prefix (the id[:8] stub the web UI shows) resolves to the
	// full UUID, both bare (against defaultSlug) and as "<slug>/<prefix>".
	full := ids[0]
	pfx := full[:8]
	if got, err := s.ResolveTicketRef(ctx, slug, pfx); err != nil || got != full {
		t.Fatalf("ResolveTicketRef(prefix %q) = %q, %v; want %q", pfx, got, err, full)
	}
	if got, err := s.ResolveTicketRef(ctx, "", slug+"/"+pfx); err != nil || got != full {
		t.Fatalf("ResolveTicketRef(%s/%s) = %q, %v; want %q", slug, pfx, got, err, full)
	}
	// A hex prefix matching nothing falls back to passthrough (not an error) so
	// genuine opaque ids still reach the downstream existence check.
	if got, err := s.ResolveTicketRef(ctx, slug, "ffffffff"); err != nil || got != "ffffffff" {
		t.Fatalf("ResolveTicketRef(unmatched prefix) = %q, %v; want passthrough", got, err)
	}
}

// TestMatchPrefix covers the pure prefix matcher that backs resolveTicketPrefix.
// Crafted ids let us exercise the ambiguous (>1 match) decision deterministically
// — random UUIDs in a live store effectively never share a 4+ hex prefix.
func TestMatchPrefix(t *testing.T) {
	ids := []string{
		"abcd1234-0000-0000-0000-000000000001",
		"abcd5678-0000-0000-0000-000000000002",
		"ef009999-0000-0000-0000-000000000003",
	}
	if got := matchPrefix(ids, "ef00"); len(got) != 1 || got[0] != ids[2] {
		t.Fatalf("matchPrefix(ef00) = %v; want [%s]", got, ids[2])
	}
	if got := matchPrefix(ids, "ABCD"); len(got) != 2 { // case-insensitive, ambiguous
		t.Fatalf("matchPrefix(ABCD) = %v; want 2 matches", got)
	}
	if got := matchPrefix(ids, "deadbeef"); len(got) != 0 {
		t.Fatalf("matchPrefix(deadbeef) = %v; want none", got)
	}
}

func TestIsUUIDPrefix(t *testing.T) {
	cases := map[string]bool{
		"31ca06c1":                             true,  // the id[:8] stub
		"abc":                                  false, // too short (<4)
		"1234":                                 false, // pure digits → number-shortcode, not a prefix
		"12ab":                                 true,  // hex with a letter
		"534adaa9-950a-468a-b490-1b269ac6f25c": false, // full UUID (dashes, too long)
		"not-a-shortcode-xyz":                  false, // non-hex
		"deadbeefdeadbeefdeadbeefdeadbeef0":    false, // 33 chars (>31)
	}
	for s, want := range cases {
		if got := isUUIDPrefix(s); got != want {
			t.Errorf("isUUIDPrefix(%q) = %v; want %v", s, got, want)
		}
	}
}

func TestParseTicketShortcode(t *testing.T) {
	cases := []struct {
		ref, defaultSlug  string
		wantSlug, wantNum string
		wantShortcode     bool
	}{
		{"tickets-please/76", "bound", "tickets-please", "76", true},
		{"tickets-please/076", "bound", "tickets-please", "076", true},
		{"76", "bound", "bound", "76", true},
		{"76", "", "", "76", true},                                       // bare number, slug empty → caller errors
		{"534adaa9-950a-468a-b490-1b269ac6f25c", "bound", "", "", false}, // UUID
		{"tickets-please/not-a-number", "bound", "", "", false},
		{"foo", "bound", "", "", false},
		{"/76", "bound", "", "", false}, // empty slug segment
	}
	for _, c := range cases {
		slug, num, isSc := parseTicketShortcode(c.ref, c.defaultSlug)
		if isSc != c.wantShortcode || slug != c.wantSlug || num != c.wantNum {
			t.Errorf("parseTicketShortcode(%q, %q) = (%q, %q, %v); want (%q, %q, %v)",
				c.ref, c.defaultSlug, slug, num, isSc, c.wantSlug, c.wantNum, c.wantShortcode)
		}
	}
}
