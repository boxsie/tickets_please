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
	CommentKindUser             CommentKind = "user"
	CommentKindSystemMove       CommentKind = "system_move"
	CommentKindSystemCompletion CommentKind = "system_completion"
	CommentKindSystemArchive    CommentKind = "system_archive"
	CommentKindSystemUnarchive  CommentKind = "system_unarchive"
)

// AgentRef is the flat attribution summary attached to entities that were
// created or completed by an agent. It carries just enough to display
// "<name>" without dragging the full Agent metadata blob through every read.
type AgentRef struct {
	ID   string
	Name string
}

// UserRef is the flat attribution summary for a registered human a record is
// linked to — an agent acting on their behalf, or the user a ticket was
// created/completed "for". Carries just enough to render "<DisplayName>" and
// link to `/u/{UserID}` without dragging the full User blob through reads.
type UserRef struct {
	UserID      string
	DisplayName string
}

// Agent is the full record of a registered agent session. Agents
// self-identify; the system records their claim, it does not authenticate it.
//
// ActingFor, when non-nil, links the agent's MCP-key identity to a registered
// human: the agent's actions are taken on that user's behalf and inherit the
// user's per-project membership for authorization. Nil means a plain
// key-only agent (today's default) with no membership constraint.
type Agent struct {
	ID         string
	Key        string
	Name       string
	Metadata   map[string]string
	ActingFor  *UserRef
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
	ID              string
	ProjectID       string
	Title           string
	Body            string
	Column          Column
	TestingEvidence *string
	WorkSummary     *string
	Learnings       *string
	CompletedAt     *time.Time
	CreatedBy       *AgentRef
	CompletedBy     *AgentRef
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// CreatedFor / CompletedFor link the ticket to the human an acting-for
	// agent was bound to when it created / completed the ticket. Nil for
	// tickets authored by plain key-only agents (the common case and every
	// pre-bridge ticket). Distinct from CreatedBy/CompletedBy, which always
	// name the agent.
	CreatedFor         *UserRef
	CompletedFor       *UserRef
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

// Role is the per-project access level a User holds via a Membership.
// `owner` controls project settings + grant; `member` reads + mutates
// tickets/comments; `viewer` is read-only. Enforcement is done by the
// route-guard middleware in W2; the data layer just stores the string.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
)

// User is a registered human identity. Provider fields (GitHubLogin,
// GoogleSub) are pointers because a user may have only one linked
// initially and a subsequent OAuth flow can attach the other.
type User struct {
	ID          string
	Email       string
	GitHubLogin *string
	GoogleSub   *string
	DisplayName string
	AvatarURL   string
	CreatedAt   time.Time
	LastLoginAt time.Time
}

// Membership grants a User a Role on a Project. The pair (UserID, ProjectID)
// is the natural key — at most one Membership per pair. GrantedBy is the
// user id of whoever performed the grant; empty for bootstrap grants the
// system performed itself (first-login-wins / env override).
type Membership struct {
	UserID    string
	ProjectID string
	Role      Role
	GrantedBy string
	GrantedAt time.Time
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
	// AuthorFor links the comment to the human an acting-for agent was bound
	// to when it authored the comment. Nil for plain key-only authorship.
	AuthorFor *UserRef
	CreatedAt time.Time
}
