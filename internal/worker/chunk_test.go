package worker

import (
	"strings"
	"testing"

	"tickets_please/internal/vecindex"
)

func TestChunkLearningText_ParagraphsHeadingsAndShortRuns(t *testing.T) {
	longA := strings.Repeat("alpha ", 20)
	longB := strings.Repeat("beta ", 20)
	got := chunkLearningText("# First\n\n" + longA + "\n\nsmall\n\n## Second\n\n" + longB + "\n")

	if len(got) != 2 {
		t.Fatalf("chunks = %d, want 2: %#v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "# First\n\n") {
		t.Errorf("first heading detached: %q", got[0])
	}
	if !strings.Contains(got[0], "\n\nsmall") {
		t.Errorf("short run was not merged backward: %q", got[0])
	}
	if !strings.HasPrefix(got[1], "## Second\n\n") {
		t.Errorf("second heading detached: %q", got[1])
	}
}

func TestChunkLearningText_EmptyAndWindowsNewlines(t *testing.T) {
	if got := chunkLearningText(" \n\n\t"); got != nil {
		t.Fatalf("empty chunks = %#v, want nil", got)
	}
	got := chunkLearningText("# Heading\r\n\r\n" + strings.Repeat("body ", 20))
	if len(got) != 1 || strings.Contains(got[0], "\r") {
		t.Fatalf("Windows newline normalization = %#v", got)
	}
}

func TestChunkedLearnings_PinnedQueryExpectations(t *testing.T) {
	tests := []struct {
		query         string
		targetRepeats int
		wantWholeTop  string
	}{
		{"gotchas when live-testing against the real dogfood project", 1, "competitor"},
		{"search scoping bound project cross-project", 50, "buried"},
		{"how do I add a new MCP tool without breaking the tool count tests", 50, "buried"},
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			buried := strings.Repeat("noise ", 40) + "\n\n" +
				strings.Repeat(tc.query+" ", tc.targetRepeats) + strings.Repeat("neutral ", 20)
			competitor := tc.query + " noise"
			queryVec := pinnedVector(tc.query, tc.query)

			whole := vecindex.New()
			whole.Upsert(vecindex.Entry{ID: "buried", Kind: vecindex.KindTicketLearnings, Vec: pinnedVector(buried, tc.query)})
			whole.Upsert(vecindex.Entry{ID: "competitor", Kind: vecindex.KindTicketLearnings, Vec: pinnedVector(competitor, tc.query)})

			chunked := vecindex.New()
			chunked.Upsert(vecindex.Entry{ID: "buried", Kind: vecindex.KindTicketLearnings, Vecs: pinnedVectors(chunkLearningText(buried), tc.query)})
			chunked.Upsert(vecindex.Entry{ID: "competitor", Kind: vecindex.KindTicketLearnings, Vecs: pinnedVectors(chunkLearningText(competitor), tc.query)})

			wholeHits := whole.Search(queryVec, vecindex.KindTicketLearnings, "", 10)
			if len(wholeHits) != 2 || wholeHits[0].ID != tc.wantWholeTop {
				t.Fatalf("whole-doc hits = %+v, want top %q", wholeHits, tc.wantWholeTop)
			}
			chunkHits := chunked.Search(queryVec, vecindex.KindTicketLearnings, "", 10)
			if len(chunkHits) != 2 || chunkHits[0].ID != "buried" {
				t.Fatalf("chunked hits = %+v, want one hit per entry with buried top", chunkHits)
			}
		})
	}
}

func pinnedVectors(chunks []string, query string) [][]float32 {
	out := make([][]float32, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, pinnedVector(chunk, query))
	}
	return out
}

func pinnedVector(text, query string) []float32 {
	return []float32{float32(strings.Count(text, query)), float32(strings.Count(text, "noise"))}
}
