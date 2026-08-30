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
		// The rate-after-search reflex — the other half of the feedback loop.
		// Absent before ticket d40cf2c6; pinned so it can't silently regress.
		"rate_search_result",
		"ready_only=true",
		"complete_ticket",
		"Every column move requires a non-empty comment",
		"frozen",
		"immutable",
		// Bootstrapping section: the cold-start flow must live in the
		// persistent context every turn. After the auth-soft CreateProject
		// landed, the bootstrap is just "call create_project, then register_agent"
		// — the section pins those phrases plus the escape-valve framing.
		"Bootstrapping a new project",
		"create_project",
		"bootstrap escape valve",
		"register_agent",
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
