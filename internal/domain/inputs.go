package domain

// Input structs accepted by `svc.Service` methods. Each method takes one of
// these (or simple positional args for trivial calls). Fields use pointer
// types when "leave unchanged" needs to be distinguishable from "set to the
// zero value".

// CreateTicketInput is accepted by Service.CreateTicket. New tickets always
// land in the `todo` column; column is not a settable field.
type CreateTicketInput struct {
	ProjectIDOrSlug    string
	Title              string
	Body               string
	DependsOn          []string
	ParallelizableWith []string
	// PhaseIDOrSlug is nil for phase-less tickets that sit directly under
	// the project.
	PhaseIDOrSlug *string
	// Wave is the soft integer grouping. 0 = unassigned.
	Wave int
}

// UpdateTicketInput is accepted by Service.UpdateTicket. Pointer fields are
// optional: nil means "leave unchanged". Column is intentionally absent —
// MoveTicket / CompleteTicket own column transitions. Phase is intentionally
// absent — AssignTicketToPhase owns phase transitions.
type UpdateTicketInput struct {
	Title *string
	Body  *string
	// Wave: nil = leave unchanged; non-nil = set to this value (a *int
	// pointing at 0 means "set to unassigned").
	Wave *int
}

// ListTicketsInput is accepted by Service.ListTickets.
//
// PhaseIDOrSlug uses a sentinel string to express "phase-less only":
//   - nil          → any phase, including phase-less
//   - *"-"         → phase-less only
//   - *"<id|slug>" → that specific phase
//
// Wave likewise uses pointer-with-sentinel semantics:
//   - nil          → any wave
//   - *N (incl. 0) → exactly that wave (0 = unassigned only)
type ListTicketsInput struct {
	ProjectIDOrSlug string
	Column          *Column
	PhaseIDOrSlug   *string
	Wave            *int
	ReadyOnly       bool
	// Limit: 0 = default 50; capped at 200.
	Limit  int
	Cursor string
}

// SearchTicketsInput is accepted by Service.SearchTickets. Project filter is
// required in v1.
type SearchTicketsInput struct {
	Query           string
	ProjectIDOrSlug string
	Columns         []Column
	// Limit: 0 = default 10; capped at 50.
	Limit int
}

// SearchCommentsInput is accepted by Service.SearchComments.
type SearchCommentsInput struct {
	Query           string
	ProjectIDOrSlug string
	// TicketID optionally narrows search to a single ticket's comments.
	TicketID string
	Kinds    []CommentKind
	// Limit: 0 = default 10; capped at 50.
	Limit int
}

// SearchLearningsInput is accepted by Service.SearchLearnings. Searches over
// completed tickets only.
type SearchLearningsInput struct {
	Query string
	// ProjectIDOrSlug optionally scopes the search to a single project.
	// Empty means search across all projects (the learnings index is
	// resident, so this is cheap).
	ProjectIDOrSlug string
	// Limit: 0 = default 10; capped at 50.
	Limit int
}

// UpdateProjectInput is accepted by Service.UpdateProject. Pointer fields are
// optional: nil means "leave unchanged". Summary edits trigger re-embedding.
type UpdateProjectInput struct {
	Name          *string
	Description   *string
	Summary       *string
	EmbedProvider *string
	EmbedModel    *string
}

// UpdatePhaseInput is accepted by Service.UpdatePhase. Pointer fields are
// optional: nil means "leave unchanged".
type UpdatePhaseInput struct {
	Name        *string
	Description *string
	Summary     *string
}
