// Package domain holds the hand-written, hydrated in-memory types that
// `svc.Service` returns to its callers. These shapes are independent of the
// on-disk yaml record types (which live in `internal/store`); the cache layer
// assembles records + sibling markdown files into the structs defined here.
//
// No yaml/json tags live on these types — they are pure Go shapes. The disk
// record types own round-tripping to/from yaml.
package domain

import "time"

// Column is the lifecycle state of a ticket on the Trello-style board.
type Column string

const (
	ColumnTodo       Column = "todo"
	ColumnInProgress Column = "in_progress"
	ColumnTesting    Column = "testing"
	ColumnDone       Column = "done"
)

// CommentKind distinguishes free-form user comments from system-generated
// audit-trail entries (column moves and completions).
type CommentKind string

const (
	CommentKindUser              CommentKind = "user"
	CommentKindSystemMove        CommentKind = "system_move"
	CommentKindSystemCompletion  CommentKind = "system_completion"
	CommentKindSystemArchive     CommentKind = "system_archive"
	CommentKindSystemUnarchive   CommentKind = "system_unarchive"
)

// AgentRef is the flat attribution summary attached to entities that were
// created or completed by an agent. It carries just enough to display
// "<name>" without dragging the full Agent metadata blob through every read.
type AgentRef struct {
	ID   string
	Name string
}

// Agent is the full record of a registered agent session. Agents
// self-identify; the system records their claim, it does not authenticate it.
type Agent struct {
	ID         string
	Key        string
	Name       string
	Metadata   map[string]string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
}

// Project is the hydrated form of a project — the yaml record fields plus the
// loaded summary markdown.
type Project struct {
	ID          string
	Slug        string
	Name        string
	Description string
	Summary     string
	CreatedBy   *AgentRef
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Phase is the hydrated form of a phase — the yaml record fields plus the
// loaded summary markdown plus computed ticket counts.
type Phase struct {
	ID          string
	ProjectID   string
	Slug        string
	Name        string
	Description string
	Summary     string
	Number      int
	CreatedBy   *AgentRef
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// Computed at read time by the cache layer.
	TicketCount       int
	ActiveTicketCount int
}

// Ticket is the hydrated form of a ticket — the yaml record fields plus the
// loaded body markdown and (when done) the parsed completion sections.
//
// Wave is a soft integer grouping inside a phase or project. 0 means
// "unassigned" — the default when no wave was specified.
type Ticket struct {
	ID                 string
	ProjectID          string
	Title              string
	Body               string
	Column             Column
	TestingEvidence    *string
	WorkSummary        *string
	Learnings          *string
	CompletedAt        *time.Time
	CreatedBy          *AgentRef
	CompletedBy        *AgentRef
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DependsOn          []string
	ParallelizableWith []string
	// BlockedBy is computed at read time: the subset of DependsOn whose
	// tickets are not yet in `done`.
	BlockedBy []string
	PhaseID   *string
	Wave      int
	// Archived is independent of Column. A `done` ticket can be archived
	// (the common case) while staying frozen for completion-field edits;
	// flipping the flag is its own audited action, not a freeze violation.
	// Archived tickets are excluded from search_*/list_tickets by default;
	// `include_archived: true` brings them back. get_ticket returns archived
	// tickets unconditionally — direct lookup by id is always allowed.
	Archived   bool
	ArchivedAt *time.Time
}

// WaveSummary describes a single wave inside a phase or the phase-less area
// of a project.
type WaveSummary struct {
	Wave              int
	TicketCount       int
	ActiveTicketCount int
}

// Comment is the hydrated form of a comment — its frontmatter plus the
// loaded markdown body.
type Comment struct {
	ID         string
	TicketID   string
	Kind       CommentKind
	Body       string
	FromColumn *Column
	ToColumn   *Column
	Author     *AgentRef
	CreatedAt  time.Time
}
