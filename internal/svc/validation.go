package svc

import (
	"fmt"
	"regexp"
	"strings"

	"tickets_please/internal/domain"
)

// slugRE is the SPEC-mandated server-side validation regex for project and
// phase slugs. SPEC §Project loading: lowercase letters, digits, dashes,
// underscores; must start and end with [a-z0-9]; 2-64 chars total.
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}[a-z0-9]$`)

// summaryMinLen is the SPEC-mandated minimum length (after trim) of project
// and phase summary text. Both play the same load-bearing context-doc role
// per SPEC §Project summary / §Phases.
const summaryMinLen = 200

// requireNonEmptyTrimmed returns ErrInvalidArgument when val is empty after a
// strings.TrimSpace. The field name shows up verbatim in the error so the
// LLM-side caller knows which input it muffed.
func requireNonEmptyTrimmed(field, val string) error {
	if strings.TrimSpace(val) == "" {
		return fmt.Errorf("%w: %s required", domain.ErrInvalidArgument, field)
	}
	return nil
}

// requireMinLen returns ErrInvalidArgument when val is shorter than min after
// strings.TrimSpace. SPEC §Validation: completion fields require ≥10 chars
// each so a `.` doesn't satisfy the rule.
func requireMinLen(field, val string, min int) error {
	t := strings.TrimSpace(val)
	if len(t) < min {
		return fmt.Errorf("%w: %s must be at least %d characters", domain.ErrInvalidArgument, field, min)
	}
	return nil
}

// requireSlug returns ErrInvalidArgument when val doesn't match the SPEC's
// slug grammar. The error message embeds the regex verbatim so the LLM-side
// caller can see what shape it should be matching.
func requireSlug(field, val string) error {
	if !slugRE.MatchString(val) {
		return fmt.Errorf("%w: %s %q does not match ^[a-z0-9][a-z0-9_-]{0,62}[a-z0-9]$", domain.ErrInvalidArgument, field, val)
	}
	return nil
}

// requireSummary returns ErrInvalidArgument when val (post-trim) is shorter
// than summaryMinLen. Two callers — project summaries and phase summaries —
// pre-T14 used slightly different "meaningful (project|) context" wording.
// `field` is the leading field label ("summary" / "phase summary") and is
// also used as the trailing noun phrase: project callers pass "summary" so
// the message says "...meaningful project context"; phase callers pass
// "phase summary" so it says "...meaningful context". This preserves the
// exact pre-refactor strings.
func requireSummary(field, val string) error {
	t := strings.TrimSpace(val)
	if len(t) < summaryMinLen {
		var trailing string
		if field == "phase summary" {
			trailing = "meaningful context"
		} else {
			trailing = "meaningful project context"
		}
		return fmt.Errorf("%w: %s must be at least %d characters of %s", domain.ErrInvalidArgument, field, summaryMinLen, trailing)
	}
	return nil
}

// requireMoveTargetColumn rejects an empty target column or ColumnDone. The
// `done` rejection message points at CompleteTicket so the LLM caller knows
// where to go — SPEC §Validation explicitly calls out this self-documenting
// message.
func requireMoveTargetColumn(c domain.Column) error {
	if strings.TrimSpace(string(c)) == "" {
		return fmt.Errorf("%w: target_column required", domain.ErrInvalidArgument)
	}
	switch c {
	case domain.ColumnTodo, domain.ColumnInProgress, domain.ColumnTesting:
		return nil
	case domain.ColumnDone:
		return fmt.Errorf("%w: target column %q not allowed; use CompleteTicket to mark a ticket done", domain.ErrInvalidArgument, string(c))
	default:
		return fmt.Errorf("%w: unknown target column %q", domain.ErrInvalidArgument, string(c))
	}
}
