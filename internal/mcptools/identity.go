// Package mcptools wraps the in-process svc.Service as MCP tools served over
// stdio by `tickets_please mcp`. The package is intentionally thin: the
// transport layer is JSON ↔ Go structs + a session id threaded through
// context; all business logic stays in svc.
package mcptools

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"tickets_please/internal/config"
)

// Session holds the per-MCP-connection agent identity that the registry
// maintains. Each incoming connection (stdio or future HTTP) gets its own
// Session keyed by the MCP session ID supplied by mcp-go.
type Session struct {
	// AgentID is the svc-layer agent session id returned by svc.RegisterAgent.
	// This is what gets threaded into the context for every svc call.
	AgentID string
	// AgentKey is the agent's self-chosen unique key, e.g. "tickets_please_mcp:abc123".
	AgentKey string
	// AgentName is the display name surfaced to readers of the audit trail.
	AgentName string
	// Metadata are arbitrary key/value pairs stored alongside the agent record.
	Metadata map[string]string
	// ProjectSlug is the default project for this session (may be empty).
	ProjectSlug string
	// ProjectPath is the absolute filesystem path to the bound project repo.
	// Kept alongside ProjectSlug so the project can be re-resolved if the
	// project store cache evicts the slug.
	ProjectPath string
	// ExpiresAt is when the underlying svc session expires.
	ExpiresAt time.Time
}

// Registry is a session-keyed map of Sessions. It replaces the old per-process
// Identity singleton and is safe for concurrent use. In the stdio transport
// there is exactly one entry keyed by "stdio"; the upcoming HTTP transport will
// add and remove entries as clients connect and disconnect.
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewRegistry returns an empty Registry. cfg is accepted for future use
// (e.g. default TTL); it is not used at construction time today.
func NewRegistry(_ config.Config) *Registry {
	return &Registry{
		sessions: make(map[string]*Session),
	}
}

// Register stores sess under sessionID, overwriting any previous entry with
// the same ID. It returns an error only if sess is nil.
func (r *Registry) Register(sessionID string, sess *Session) error {
	if sess == nil {
		return fmt.Errorf("mcptools: Register: nil session")
	}
	r.mu.Lock()
	r.sessions[sessionID] = sess
	r.mu.Unlock()
	return nil
}

// Get returns the Session registered under sessionID, or (nil, false) if no
// entry exists.
func (r *Registry) Get(sessionID string) (*Session, bool) {
	r.mu.RLock()
	sess, ok := r.sessions[sessionID]
	r.mu.RUnlock()
	return sess, ok
}

// Touch updates LastSeenAt semantics — currently a no-op placeholder kept for
// the HTTP transport's heartbeat path. It is safe to call on a missing ID.
func (r *Registry) Touch(_ string) {}

// Remove drops the entry for sessionID. It is a no-op if the ID is unknown.
func (r *Registry) Remove(sessionID string) {
	r.mu.Lock()
	delete(r.sessions, sessionID)
	r.mu.Unlock()
}

// Len returns the number of sessions currently in the registry. Used in tests
// and diagnostics.
func (r *Registry) Len() int {
	r.mu.RLock()
	n := len(r.sessions)
	r.mu.RUnlock()
	return n
}

// DefaultStdioSession builds a Session from the cfg agent key/name env
// defaults. The returned Session has no AgentID yet — the caller must call
// svc.RegisterAgent, then fill in AgentID and ExpiresAt before registering.
func DefaultStdioSession(cfg config.Config) *Session {
	key := strings.TrimSpace(cfg.MCPAgentKey)
	if key == "" {
		key = fmt.Sprintf("tickets_please_mcp:%s", randomHex(8))
	}
	name := strings.TrimSpace(cfg.MCPAgentName)
	if name == "" {
		name = "tickets_please_mcp"
	}
	return &Session{
		AgentKey:  key,
		AgentName: name,
		Metadata: map[string]string{
			"client":     "tickets_please_mcp",
			"started_at": time.Now().UTC().Format(time.RFC3339),
		},
	}
}

// randomHex returns a lowercase hex string of length 2*n. Used to seed the
// default agent key. Falls back to a timestamp-based string if the OS rng
// fails (vanishingly unlikely; this code path is just for self-identification).
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
