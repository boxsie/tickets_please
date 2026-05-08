// Package store implements the filesystem storage primitives that every
// service-layer mutation goes through. The on-disk yaml records defined here
// are intentionally NOT the same shape as the hydrated `domain.*` types — they
// carry only the fields stored in their `*.yaml` file. Sibling markdown files
// (`summary.md`, `body.md`, `completion.md`, comment bodies) carry the prose
// and are loaded separately by the cache layer (T04).
package store

import (
	"time"

	"tickets_please/internal/domain"
)

// ProjectRecord is what's stored in `projects/<slug>/project.yaml`. The
// human-readable summary lives in the sibling `summary.md` file.
type ProjectRecord struct {
	ID               string    `yaml:"id"`
	Slug             string    `yaml:"slug"`
	Name             string    `yaml:"name"`
	Description      string    `yaml:"description,omitempty"`
	EmbedProvider    string    `yaml:"embed_provider,omitempty"`
	EmbedModel       string    `yaml:"embed_model,omitempty"`
	CreatedByAgentID *string   `yaml:"created_by,omitempty"`
	CreatedAt        time.Time `yaml:"created_at"`
	UpdatedAt        time.Time `yaml:"updated_at"`
}

// PhaseRecord is what's stored in `projects/<slug>/phases/<NNN>-<slug>/phase.yaml`.
// The summary lives in the sibling `summary.md` file.
type PhaseRecord struct {
	ID               string    `yaml:"id"`
	ProjectID        string    `yaml:"project_id"`
	Slug             string    `yaml:"slug"`
	Number           int       `yaml:"number"`
	Name             string    `yaml:"name"`
	Description      string    `yaml:"description,omitempty"`
	CreatedByAgentID *string   `yaml:"created_by,omitempty"`
	CreatedAt        time.Time `yaml:"created_at"`
	UpdatedAt        time.Time `yaml:"updated_at"`
}

// TicketRecord is what's stored in a ticket dir's `ticket.yaml`. Body and
// completion sections live as sibling markdown files.
type TicketRecord struct {
	ID                 string        `yaml:"id"`
	ProjectID          string        `yaml:"project_id"`
	Number             int           `yaml:"number"`
	Title              string        `yaml:"title"`
	Column             domain.Column `yaml:"column"`
	PhaseID            *string       `yaml:"phase_id,omitempty"`
	Wave               int           `yaml:"wave,omitempty"`
	DependsOn          []string      `yaml:"depends_on,omitempty"`
	ParallelizableWith []string      `yaml:"parallelizable_with,omitempty"`
	CreatedByAgentID   *string       `yaml:"created_by,omitempty"`
	CompletedByAgentID *string       `yaml:"completed_by,omitempty"`
	CompletedAt        *time.Time    `yaml:"completed_at,omitempty"`
	CreatedAt          time.Time     `yaml:"created_at"`
	UpdatedAt          time.Time     `yaml:"updated_at"`
}

// CommentRecord is the YAML frontmatter portion of a comment file. The
// markdown body is the file content after the closing `---`.
type CommentRecord struct {
	ID            string             `yaml:"id"`
	TicketID      string             `yaml:"ticket_id"`
	Kind          domain.CommentKind `yaml:"kind"`
	AuthorAgentID *string            `yaml:"author_id,omitempty"`
	FromColumn    *domain.Column     `yaml:"from_column,omitempty"`
	ToColumn      *domain.Column     `yaml:"to_column,omitempty"`
	CreatedAt     time.Time          `yaml:"created_at"`
}

// AgentRecord is the full agent yaml at `agents/<session-uuid>.yaml`. Agents
// have no sidecar files — the record IS the full domain shape.
type AgentRecord struct {
	ID         string            `yaml:"id"`
	Key        string            `yaml:"key"`
	Name       string            `yaml:"name"`
	Metadata   map[string]string `yaml:"metadata,omitempty"`
	CreatedAt  time.Time         `yaml:"created_at"`
	ExpiresAt  time.Time         `yaml:"expires_at"`
	LastSeenAt time.Time         `yaml:"last_seen_at"`
}

// ToDomain converts an AgentRecord to its domain equivalent. Trivial here
// because agents have no sidecar files; included for symmetry with future
// (T04) record→domain conversions.
func (r *AgentRecord) ToDomain() *domain.Agent {
	if r == nil {
		return nil
	}
	return &domain.Agent{
		ID:         r.ID,
		Key:        r.Key,
		Name:       r.Name,
		Metadata:   r.Metadata,
		CreatedAt:  r.CreatedAt,
		ExpiresAt:  r.ExpiresAt,
		LastSeenAt: r.LastSeenAt,
	}
}
