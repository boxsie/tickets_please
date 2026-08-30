package svc

import "testing"

func TestNormalizeLabel(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Frames &amp; Chassis", "Frames & Chassis"},
		{"  spaced  ", "spaced"},
		{"plain", "plain"},
		{"&lt;foo&gt;", "<foo>"},
		{"already & decoded", "already & decoded"},
		{"&amp;amp;", "&amp;"},
		{"", ""},
	}
	for _, c := range cases {
		got := normalizeLabel(c.in)
		if got != c.want {
			t.Errorf("normalizeLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCreatePhase_DecodesPreEscapedAmpersand(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)

	ph, err := s.CreatePhase(ctx, slug, "Shaders &amp; Shared Textures", "&amp; rationale", validPhaseSummary())
	if err != nil {
		t.Fatalf("CreatePhase: %v", err)
	}
	if ph.Name != "Shaders & Shared Textures" {
		t.Fatalf("Name = %q, want decoded form", ph.Name)
	}
	if ph.Description != "& rationale" {
		t.Fatalf("Description = %q, want decoded form", ph.Description)
	}
	// Slug derives from the decoded name — `&` → stripped, no `amp` left over.
	if ph.Slug != "shaders-shared-textures" {
		t.Fatalf("Slug = %q, want shaders-shared-textures", ph.Slug)
	}
}
