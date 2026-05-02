// Package mcptools wraps the in-process svc.Service as MCP tools served over
// stdio by `tickets_please mcp`. The package is intentionally thin: the
// transport layer is JSON ↔ Go structs + a session id threaded through
// context; all business logic stays in svc.
package mcptools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/svc"
)

// Identity is the MCP server's self-asserted agent identity. The MCP layer
// registers itself once at startup, caches the returned session id, and
// attaches it to every tool call's context. SPEC §Agent identity & sessions >
// MCP integration.
//
// Re-registration replaces SessionID + ExpiresAt in place; concurrent reads of
// SessionID are safe because handlers always go through AttachContext (which
// reads under the mutex).
type Identity struct {
	// Key is the agent's self-chosen unique key. Defaults to
	// `tickets_please_mcp:<random-8-hex>` so two MCP processes against the
	// same data dir don't collide on the active-key uniqueness check.
	Key string
	// Name is the display name surfaced to readers of the audit trail.
	Name string

	mu        sync.RWMutex
	sessionID string
	expiresAt time.Time
}

// NewIdentity returns an Identity ready to Register. Key/Name come from cfg if
// set; otherwise we fall back to the SPEC defaults. The random suffix is
// chosen here (not at register time) so the same Identity Re-registers under
// the same Key when its session expires — the audit trail sees one continuous
// agent across re-auths.
func NewIdentity(cfg config.Config) Identity {
	key := strings.TrimSpace(cfg.MCPAgentKey)
	if key == "" {
		key = fmt.Sprintf("tickets_please_mcp:%s", randomHex(8))
	}
	name := strings.TrimSpace(cfg.MCPAgentName)
	if name == "" {
		name = "tickets_please_mcp"
	}
	return Identity{Key: key, Name: name}
}

// Register calls svc.RegisterAgent with the cached Key/Name and stashes the
// resulting session id + expiry. Safe to call repeatedly (e.g. after an
// ErrUnauthenticated bubble-up): the previous session is left on disk for the
// audit trail and a fresh one takes over.
//
// Metadata records the binary's role + a short fingerprint of the start-time
// so post-mortem readers can tell which process registered which session.
func (id *Identity) Register(ctx context.Context, s *svc.Service) error {
	if s == nil {
		return fmt.Errorf("mcptools: nil svc.Service")
	}
	metadata := map[string]string{
		"client":     "tickets_please_mcp",
		"started_at": time.Now().UTC().Format(time.RFC3339),
	}
	sessionID, expiresAt, err := s.RegisterAgent(ctx, id.Key, id.Name, metadata, 0)
	if err != nil {
		return fmt.Errorf("register agent: %w", err)
	}
	id.mu.Lock()
	id.sessionID = sessionID
	id.expiresAt = expiresAt
	id.mu.Unlock()
	return nil
}

// AttachContext returns a context carrying the cached session id under the
// key svc.WithSessionID uses. Handlers call this before every svc method.
func (id *Identity) AttachContext(ctx context.Context) context.Context {
	id.mu.RLock()
	sid := id.sessionID
	id.mu.RUnlock()
	if sid == "" {
		return ctx
	}
	return svc.WithSessionID(ctx, sid)
}

// SessionID returns the cached session id (may be empty if Register hasn't
// run yet). Used by the `who_am_i` tool.
func (id *Identity) SessionID() string {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.sessionID
}

// ExpiresAt returns the cached session expiry. Used by the `who_am_i` tool.
func (id *Identity) ExpiresAt() time.Time {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.expiresAt
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
