package svc

import (
	"fmt"
	"strings"

	"tickets_please/internal/domain"
)

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
