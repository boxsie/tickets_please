package mcptools

import (
	"strings"
	"testing"
)

// TestServerInstructionsContainsLoadBearingPhrases pins the cross-tool
// reflexes the LLM needs to see every turn. If you trim these, the model
// stops doing the right thing automatically.
func TestServerInstructionsContainsLoadBearingPhrases(t *testing.T) {
	wants := []string{
		"get_project_summary",
		"search_learnings",
		"ready_only=true",
		"complete_ticket",
		"Every column move requires a non-empty comment",
		"frozen",
		"immutable",
		// Bootstrapping section (Bootstrap UX phase, T2): the cold-start
		// flow must live in the persistent context every turn.
		"Bootstrapping a new project",
		"create_project",
		"pre-registers",
	}
	for _, w := range wants {
		if !strings.Contains(ServerInstructions, w) {
			t.Errorf("ServerInstructions missing load-bearing phrase %q", w)
		}
	}
}

func TestServerInstructionsHasReasonableLength(t *testing.T) {
	// Long enough to be useful, short enough to not bloat every turn.
	const min, max = 1000, 5000
	n := len(ServerInstructions)
	if n < min || n > max {
		t.Errorf("ServerInstructions length %d outside expected range [%d, %d]", n, min, max)
	}
}
