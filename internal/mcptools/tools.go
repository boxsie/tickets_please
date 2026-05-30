package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"tickets_please/internal/domain"
	"tickets_please/internal/store"
	"tickets_please/internal/svc"
)

// Tools wraps the in-process svc.Service plus the per-session Registry into a
// single struct that registers all 31 tools against an *mcpserver.MCPServer.
//
// One Tools per process — the MCP binary builds it once, calls RegisterAll,
// and hands the server off to ServeStdio.
type Tools struct {
	svc      *svc.Service
	registry *Registry
	logger   *slog.Logger
	// Remote toggles server-managed project paths: create_project and
	// register_agent no longer require an explicit project_path, instead
	// deriving <remote_project_root>/<slug> automatically. Stdio (false)
	// keeps the original path-explicit behaviour because the LLM is running
	// on the same host as its repo and can name the local path.
	Remote bool
}

// NewTools constructs a Tools. The caller owns the lifecycle of svc and
// registry; Tools just borrows them.
func NewTools(s *svc.Service, registry *Registry, logger *slog.Logger) *Tools {
	if logger == nil {
		logger = slog.Default()
	}
	return &Tools{svc: s, registry: registry, logger: logger}
}

// sessionIDFromContext extracts the MCP session ID from the tool call context.
// When mcp-go provides a ClientSession (SSE, streamable-HTTP, or the stdlib
// stdio session whose SessionID() returns "stdio"), we use that directly.
// The constant "stdio" is also the synthetic fallback for any context where
// no ClientSession is attached — which today only happens in unit tests that
// call handlers directly without a live transport.
func (t *Tools) sessionIDFromContext(ctx context.Context) string {
	if sess := mcpserver.ClientSessionFromContext(ctx); sess != nil {
		return sess.SessionID()
	}
	return "stdio"
}

// resolveProjectSlug returns the project_id_or_slug to use for this call:
// the explicit param if supplied, otherwise the session's bound ProjectSlug
// (set by register_agent or the stdio MCP_PROJECT_SLUG default). Returns a
// helpful error if neither is set.
func (t *Tools) resolveProjectSlug(ctx context.Context, req mcp.CallToolRequest) (string, error) {
	if v := req.GetString("project_id_or_slug", ""); v != "" {
		return v, nil
	}
	sessionID := t.sessionIDFromContext(ctx)
	if sess, ok := t.registry.Get(sessionID); ok && sess.ProjectSlug != "" {
		return sess.ProjectSlug, nil
	}
	return "", fmt.Errorf("no project bound to this session — call register_agent or pass project_id_or_slug explicitly")
}

// RegisterAll attaches every tool's schema + handler to the supplied MCP
// server. The server is then served over stdio by the caller.
//
// Tool descriptions are copied verbatim from SPEC.md — the load-bearing
// "search_learnings before non-trivial work" / "get_project_summary before
// working" instructions are the unlock for LLM ergonomics; do not paraphrase.
func (t *Tools) RegisterAll(s *mcpserver.MCPServer) {
	// Projects (7)
	s.AddTool(mcp.NewTool("list_projects",
		mcp.WithDescription("List all ticket projects. Use this first to find the project you want to work in."),
	), t.handleListProjects)

	s.AddTool(mcp.NewTool("create_project",
		mcp.WithDescription("Create a new project. Slug must be unique and URL-safe. **Requires a `summary` field — a markdown document (≥200 chars) describing the project's goals, key components, and constraints.** This summary becomes the load-bearing context any future agent reads before working in this project. Be thorough. On a remote (HTTP) server, `project_path` is optional and the server stores the project at `<remote_project_root>/<slug>` automatically. Stdio clients must pass `project_path` — the absolute path of the local repo where `<project_path>/.tickets_please/` will be created. This is the bootstrap mutation: no session required."),
		mcp.WithString("slug", mcp.Required(), mcp.Description("URL-safe unique slug for the project")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Display name for the project")),
		mcp.WithString("project_path", mcp.Description("Absolute filesystem path of the repo where the project should live. Optional on a remote (HTTP) server: omit it and the server stores the project at `<remote_project_root>/<slug>`. Required for stdio clients (their local repo path). When supplied: an existing path is used as-is; a missing path is materialised provided it falls under the server's configured `remote_project_root`. `<project_path>/.tickets_please/` is then written inside.")),
		mcp.WithString("description", mcp.Description("One-line description")),
		mcp.WithString("summary", mcp.Required(), mcp.Description("Markdown summary (≥200 chars) — the load-bearing context doc")),
	), t.handleCreateProject)

	s.AddTool(mcp.NewTool("get_project",
		mcp.WithDescription("Fetch a project's full record (counts, attribution, timestamps, summary)."),
		mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug; optional if register_agent has bound a project to the session")),
	), t.handleGetProject)

	s.AddTool(mcp.NewTool("get_project_summary",
		mcp.WithDescription("Fetch just the project's summary markdown. **Read this before doing any non-trivial work in a project — it's the project's design context.**"),
		mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug; optional if register_agent has bound a project to the session")),
	), t.handleGetProjectSummary)

	s.AddTool(mcp.NewTool("load_project",
		mcp.WithDescription("Pre-warm a project into the server's in-memory cache. Useful before doing many operations against the same project. Optional — calls auto-load if needed."),
		mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug; optional if register_agent has bound a project to the session")),
	), t.handleLoadProject)

	s.AddTool(mcp.NewTool("update_project",
		mcp.WithDescription("Edit a project's name, description, or summary. Summary edits trigger re-embedding."),
		mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug; optional if register_agent has bound a project to the session")),
		mcp.WithString("name", mcp.Description("New name (optional)")),
		mcp.WithString("description", mcp.Description("New description (optional)")),
		mcp.WithString("summary", mcp.Description("New summary markdown (optional, ≥200 chars when supplied)")),
	), t.handleUpdateProject)

	s.AddTool(mcp.NewTool("delete_project",
		mcp.WithDescription("**Irreversibly delete** a project and everything in it: every phase, every ticket (including in-progress / testing / done ones), every comment, every embedding sidecar. The on-disk data dir survives but its project content is wiped, the project is unmounted, and it is removed from the persistent registry. Per-ticket completion immutability is a per-ticket rule; the project-level delete bypasses it."),
		mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug; optional if register_agent has bound a project to the session")),
	), t.handleDeleteProject)

	s.AddTool(mcp.NewTool("reembed_project",
		mcp.WithDescription("Delete all *.embedding.json sidecars in a project and enqueue async re-embed using the project's currently configured embedder. Use after switching embed_provider/embed_model in project.yaml, or to recover from corrupted sidecars."),
		mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug; optional if a session is bound")),
	), t.handleReembedProject)

	// Phases (7)
	s.AddTool(mcp.NewTool("list_phases",
		mcp.WithDescription("List phases in a project with active and total ticket counts."),
		mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug; optional if register_agent has bound a project to the session")),
	), t.handleListPhases)

	s.AddTool(mcp.NewTool("create_phase",
		mcp.WithDescription("Add a phase to a project for bigger bodies of work. Requires a `summary` (≥200 chars) — same load-bearing context doc as projects, scoped to this phase."),
		mcp.WithString("project_id_or_slug", mcp.Description("Parent project id or slug; optional if register_agent has bound a project to the session")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Phase display name")),
		mcp.WithString("description", mcp.Description("One-line description")),
		mcp.WithString("summary", mcp.Required(), mcp.Description("Markdown summary (≥200 chars)")),
	), t.handleCreatePhase)

	s.AddTool(mcp.NewTool("get_phase",
		mcp.WithDescription("Fetch a phase's full record."),
		mcp.WithString("project_id_or_slug", mcp.Description("Parent project id or slug; optional if register_agent has bound a project to the session")),
		mcp.WithString("phase_id_or_slug", mcp.Required(), mcp.Description("Phase id or slug")),
	), t.handleGetPhase)

	s.AddTool(mcp.NewTool("get_phase_summary",
		mcp.WithDescription("Fetch a phase's full summary markdown. Read this when entering a phase, the same way you'd read a project summary."),
		mcp.WithString("project_id_or_slug", mcp.Description("Parent project id or slug; optional if register_agent has bound a project to the session")),
		mcp.WithString("phase_id_or_slug", mcp.Required(), mcp.Description("Phase id or slug")),
	), t.handleGetPhaseSummary)

	s.AddTool(mcp.NewTool("update_phase",
		mcp.WithDescription("Edit a phase's name, description, or summary."),
		mcp.WithString("project_id_or_slug", mcp.Description("Parent project id or slug; optional if register_agent has bound a project to the session")),
		mcp.WithString("phase_id_or_slug", mcp.Required(), mcp.Description("Phase id or slug")),
		mcp.WithString("name", mcp.Description("New name (optional)")),
		mcp.WithString("description", mcp.Description("New description (optional)")),
		mcp.WithString("summary", mcp.Description("New summary markdown (optional, ≥200 chars when supplied)")),
	), t.handleUpdatePhase)

	s.AddTool(mcp.NewTool("delete_phase",
		mcp.WithDescription("Delete a phase. Refuses if any tickets are still assigned to it."),
		mcp.WithString("project_id_or_slug", mcp.Description("Parent project id or slug; optional if register_agent has bound a project to the session")),
		mcp.WithString("phase_id_or_slug", mcp.Required(), mcp.Description("Phase id or slug")),
	), t.handleDeletePhase)

	s.AddTool(mcp.NewTool("list_waves",
		mcp.WithDescription("List the waves in a phase (or in the phase-less area of a project) with per-wave ticket counts. A wave is a soft integer grouping on tickets — no enforcement, just organization. Use this to see how a body of work decomposes."),
		mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug; optional if register_agent has bound a project to the session")),
		mcp.WithString("phase_id_or_slug", mcp.Description("Phase id or slug; omit to see waves in the phase-less area")),
	), t.handleListWaves)

	// Tickets (7)
	s.AddTool(mcp.NewTool("list_tickets",
		mcp.WithDescription("List tickets in a project, optionally filtered by column or phase. Use `ready_only=true` to surface only unblocked tickets. Archived tickets are excluded by default; pass `include_archived=true` to include them."),
		mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug; optional if register_agent has bound a project to the session")),
		mcp.WithString("column", mcp.Description("Filter by column: todo | in_progress | testing | done")),
		mcp.WithString("phase_id_or_slug", mcp.Description("Filter by phase id/slug; pass \"-\" for phase-less only")),
		mcp.WithNumber("wave", mcp.Description("Filter by wave (0 = unassigned)")),
		mcp.WithBoolean("ready_only", mcp.Description("If true, only return unblocked tickets in todo/in_progress")),
		mcp.WithBoolean("include_archived", mcp.Description("If true, include archived tickets (default false)")),
		mcp.WithNumber("limit", mcp.Description("Page size, default 50, max 200")),
		mcp.WithString("cursor", mcp.Description("Opaque pagination cursor")),
	), t.handleListTickets)

	s.AddTool(mcp.NewTool("create_ticket",
		mcp.WithDescription("Create a new ticket in a project. Tickets always start in the `todo` column. Provide a clear title and a body that describes the work; both will be searchable. Optional `phase_id_or_slug`, `depends_on`, `parallelizable_with`."),
		mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug; optional if register_agent has bound a project to the session")),
		mcp.WithString("title", mcp.Required(), mcp.Description("Ticket title")),
		mcp.WithString("body", mcp.Description("Ticket body markdown")),
		mcp.WithString("phase_id_or_slug", mcp.Description("Optional phase id or slug")),
		mcp.WithNumber("wave", mcp.Description("Wave number (0 = unassigned)")),
		mcp.WithArray("depends_on", mcp.Description("Ticket ids this one depends on"), mcp.WithStringItems()),
		mcp.WithArray("parallelizable_with", mcp.Description("Ticket ids that can be worked in parallel"), mcp.WithStringItems()),
	), t.handleCreateTicket)

	s.AddTool(mcp.NewTool("get_ticket",
		mcp.WithDescription("Fetch a ticket by id, including its current column, completion fields if done, blockers, and who created/completed it."),
		mcp.WithString("ticket_id", mcp.Required(), mcp.Description("Ticket id")),
	), t.handleGetTicket)

	s.AddTool(mcp.NewTool("update_ticket",
		mcp.WithDescription("Edit a ticket's title or body. **Cannot** change the column — use `move_ticket` or `complete_ticket`."),
		mcp.WithString("ticket_id", mcp.Required(), mcp.Description("Ticket id")),
		mcp.WithString("title", mcp.Description("New title (optional)")),
		mcp.WithString("body", mcp.Description("New body markdown (optional)")),
		mcp.WithNumber("wave", mcp.Description("New wave number (optional)")),
	), t.handleUpdateTicket)

	s.AddTool(mcp.NewTool("move_ticket",
		mcp.WithDescription("Move a ticket between columns. Requires a comment explaining *why* you're moving it. Cannot be used to move to `done` — use `complete_ticket` for that."),
		mcp.WithString("ticket_id", mcp.Required(), mcp.Description("Ticket id")),
		mcp.WithString("target_column", mcp.Required(), mcp.Description("One of: todo, in_progress, testing")),
		mcp.WithString("comment", mcp.Required(), mcp.Description("Reason for the move; becomes a system_move comment")),
	), t.handleMoveTicket)

	s.AddTool(mcp.NewTool("complete_ticket",
		mcp.WithDescription("Mark a ticket done. Requires `testing_evidence` (what you tested and how), `work_summary` (what you actually changed), `learnings` (gotchas, surprises, insights for future work). Be thorough — `learnings` are searchable by future tickets."),
		mcp.WithString("ticket_id", mcp.Required(), mcp.Description("Ticket id")),
		mcp.WithString("testing_evidence", mcp.Required(), mcp.Description("What you tested and how (≥10 chars)")),
		mcp.WithString("work_summary", mcp.Required(), mcp.Description("What you actually changed (≥10 chars)")),
		mcp.WithString("learnings", mcp.Required(), mcp.Description("Gotchas, surprises, and insights for future work (≥10 chars)")),
	), t.handleCompleteTicket)

	s.AddTool(mcp.NewTool("assign_ticket_to_phase",
		mcp.WithDescription("Move a ticket between phases (or to no phase). Requires a comment explaining why — same audit-trail rule as `move_ticket`."),
		mcp.WithString("ticket_id", mcp.Required(), mcp.Description("Ticket id")),
		mcp.WithString("phase_id_or_slug", mcp.Description("Target phase id or slug; omit to make the ticket phase-less")),
		mcp.WithString("comment", mcp.Required(), mcp.Description("Reason for the reassignment; becomes a system_move comment")),
	), t.handleAssignTicketToPhase)

	s.AddTool(mcp.NewTool("delete_ticket",
		mcp.WithDescription("**Irreversibly delete** a non-`done` ticket and all of its body, comments, and embeddings. Refuses on `done` (completion is sacred — once a ticket is finished it stays finished, per SPEC's no-reopen/no-delete rule). Any other tickets in the same project that reference this one in `depends_on` or `parallelizable_with` are auto-updated to drop the reference, atomically with the delete — no dangling refs, no manual cleanup. For finished work that you regret, file a new ticket instead."),
		mcp.WithString("ticket_id", mcp.Required(), mcp.Description("Ticket id")),
	), t.handleDeleteTicket)

	s.AddTool(mcp.NewTool("archive_ticket",
		mcp.WithDescription("Archive a ticket — flips a separate `archived` flag without changing the column. Done tickets are explicitly allowed: completion fields stay frozen but the archived flag can flip. Archived tickets are excluded from `search_*` and `list_tickets` by default; pass `include_archived: true` to bring them back, or use `get_ticket` for direct id lookup (which always succeeds). Comment is required and is written as a `system_archive` audit comment. Vec index entries stay in place so `unarchive_ticket` is free."),
		mcp.WithString("ticket_id", mcp.Required(), mcp.Description("Ticket id")),
		mcp.WithString("comment", mcp.Required(), mcp.Description("Reason for archiving; becomes a system_archive comment")),
	), t.handleArchiveTicket)

	s.AddTool(mcp.NewTool("unarchive_ticket",
		mcp.WithDescription("Unarchive a previously-archived ticket. Comment is required (becomes a `system_unarchive` audit comment). The ticket re-enters default search and list surfaces immediately."),
		mcp.WithString("ticket_id", mcp.Required(), mcp.Description("Ticket id")),
		mcp.WithString("comment", mcp.Required(), mcp.Description("Reason for unarchiving; becomes a system_unarchive comment")),
	), t.handleUnarchiveTicket)

	s.AddTool(mcp.NewTool("apply_archive_policy",
		mcp.WithDescription("Walk a project's tickets, evaluate each against the per-project `archive` policy (set in `project.yaml`), and report what would be archived. **Dry-run by default**; pass `commit: true` to actually flip the flags. Refuses when the project's `archive.enabled` is false (opt-in gate). The report includes the resolved policy config so you can see exactly what thresholds were used."),
		mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug; optional if register_agent has bound a project to the session")),
		mcp.WithBoolean("commit", mcp.Description("If true, actually archive the matching tickets (writes system_archive comments). Default false = dry-run report only.")),
		mcp.WithNumber("limit", mcp.Description("Cap how many tickets to archive in one call. Default 500, max 5000.")),
	), t.handleApplyArchivePolicy)

	// Comments (2)
	s.AddTool(mcp.NewTool("add_comment",
		mcp.WithDescription("Add a free-form comment to a ticket. Comments are immutable once created."),
		mcp.WithString("ticket_id", mcp.Required(), mcp.Description("Ticket id")),
		mcp.WithString("body", mcp.Required(), mcp.Description("Comment body markdown")),
	), t.handleAddComment)

	s.AddTool(mcp.NewTool("list_comments",
		mcp.WithDescription("List all comments on a ticket, including system-generated move and completion entries, with author attribution."),
		mcp.WithString("ticket_id", mcp.Required(), mcp.Description("Ticket id")),
	), t.handleListComments)

	s.AddTool(mcp.NewTool("list_comments_scoped",
		mcp.WithDescription("List comments across a whole project (optionally narrowed to a phase or one ticket) with plain filters — author, system-vs-user, kind, time window. The direct way to surface operator feedback on your recent work: e.g. `exclude_author_id`=<your agent id> + `since`=<when you filed the tickets> returns just the human's notes in one call, no semantic guessing. By default system move/completion comments are excluded; pass `exclude_system=false` to include them. Sorted oldest-first; paginate via `cursor`."),
		mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug; optional if register_agent has bound a project to the session")),
		mcp.WithString("phase_id_or_slug", mcp.Description("Narrow to a phase (id or slug); \"-\" = phase-less tickets only")),
		mcp.WithString("ticket_id", mcp.Description("Narrow to a single ticket")),
		mcp.WithString("author_id", mcp.Description("Keep only comments by this exact author id")),
		mcp.WithString("author_name", mcp.Description("Keep only comments by this exact author display name")),
		mcp.WithString("exclude_author_id", mcp.Description("Drop comments by this author id (\"everything NOT mine\")")),
		mcp.WithArray("kinds", mcp.Description("Keep only these comment kinds: user, system_move, system_completion"), mcp.WithStringItems()),
		mcp.WithBoolean("exclude_system", mcp.Description("Drop auto-generated system_move/system_completion comments (default true)")),
		mcp.WithString("since", mcp.Description("Only comments created at/after this RFC3339 timestamp")),
		mcp.WithString("until", mcp.Description("Only comments created at/before this RFC3339 timestamp")),
		mcp.WithString("order", mcp.Description("Sort order by created_at: asc (default) or desc")),
		mcp.WithNumber("limit", mcp.Description("Page size, default 50, max 200")),
		mcp.WithString("cursor", mcp.Description("Opaque pagination cursor")),
	), t.handleListCommentsScoped)

	// Search (3)
	s.AddTool(mcp.NewTool("search_tickets",
		mcp.WithDescription("Semantic search over ticket titles and bodies in a project. Use when looking for related work. Archived tickets are excluded by default."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Natural-language query")),
		mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug to search inside; optional if register_agent has bound a project to the session")),
		mcp.WithArray("columns", mcp.Description("Optional column filter"), mcp.WithStringItems()),
		mcp.WithBoolean("include_archived", mcp.Description("If true, include archived tickets (default false)")),
		mcp.WithNumber("limit", mcp.Description("Max results, default 10, max 50")),
	), t.handleSearchTickets)

	s.AddTool(mcp.NewTool("search_learnings",
		mcp.WithDescription("Semantic search over completion learnings from past finished tickets. **Run this before starting non-trivial work — past you may have left notes.** Archived tickets' learnings are excluded by default."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Natural-language query")),
		mcp.WithString("project_id_or_slug", mcp.Description("Optional project id/slug to scope the search")),
		mcp.WithBoolean("include_archived", mcp.Description("If true, include learnings from archived tickets (default false)")),
		mcp.WithNumber("limit", mcp.Description("Max results, default 10, max 50")),
	), t.handleSearchLearnings)

	s.AddTool(mcp.NewTool("search_comments",
		mcp.WithDescription("Semantic search across comments. Comments on archived tickets are excluded by default."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Natural-language query")),
		mcp.WithString("project_id_or_slug", mcp.Description("Optional project id/slug to scope the search")),
		mcp.WithString("ticket_id", mcp.Description("Optional ticket id to scope to one ticket's comments")),
		mcp.WithBoolean("include_archived", mcp.Description("If true, include comments on archived tickets (default false)")),
		mcp.WithNumber("limit", mcp.Description("Max results, default 10, max 50")),
	), t.handleSearchComments)

	s.AddTool(mcp.NewTool("rate_search_result",
		mcp.WithDescription("Give thumbs-up / thumbs-down feedback on one or more search results. **Use this after every non-trivial search** — your ratings tune future ranking so good content surfaces first and rotten content sinks. `entry_keys` are the `<kind>:<id>` strings returned in each search hit's `entry_key` field. One call applies the same rating to every key; mixed ratings need separate calls. Partial success: a malformed or unknown key fails just that key, not the whole call."),
		mcp.WithArray("entry_keys", mcp.Required(), mcp.Description("Entry keys to rate (1..50). Each key is the `<kind>:<id>` string from a search hit's `entry_key` field."), mcp.WithStringItems()),
		mcp.WithString("rating", mcp.Required(), mcp.Description("'like' or 'dislike' — applied to every key in this call.")),
		mcp.WithString("reason", mcp.Description("Optional free-text justification (≤500 chars, truncated if longer). Stored for human review; not currently fed back into ranking.")),
		mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug; optional if register_agent has bound a project to the session.")),
	), t.handleRateSearchResult)

	// Introspection (2)
	s.AddTool(mcp.NewTool("who_am_i",
		mcp.WithDescription("Returns the current agent identity the MCP server has registered for this session. Useful for the LLM to confirm its own attribution before doing work."),
	), t.handleWhoAmI)

	s.AddTool(mcp.NewTool("register_agent",
		mcp.WithDescription("Self-register this MCP session with the server: declare the model, client, and bound project. **HTTP clients should call this once on connection** before any other tool call. Stdio clients pre-register at startup and can skip unless they want to override the defaults. Bind the project via either `project_slug` (preferred on a remote server — the server already knows where the project lives) or `project_path` (the absolute path of a local repo containing `.tickets_please/project.yaml`, used by stdio clients)."),
		mcp.WithString("model", mcp.Required(), mcp.Description("Model identifier, e.g. \"claude-opus-4-7\"")),
		mcp.WithString("model_version", mcp.Description("Optional model version string")),
		mcp.WithString("client_name", mcp.Required(), mcp.Description("Client name, e.g. \"Claude Code\"")),
		mcp.WithString("client_version", mcp.Description("Optional client version string")),
		mcp.WithString("project_slug", mcp.Description("Slug of the project to bind this session to. Preferred on a remote (HTTP) server: the server resolves the slug to its on-disk location via the mount registry or `<remote_project_root>/<slug>`. One of project_slug or project_path is required.")),
		mcp.WithString("project_path", mcp.Description("Absolute path to the repo whose .tickets_please/project.yaml binds this session. Required for stdio clients; remote clients should prefer project_slug.")),
		mcp.WithString("agent_key", mcp.Description("Optional unique agent key; defaults to <client>:<rand>")),
		mcp.WithString("agent_name", mcp.Description("Optional display name; defaults to client_name")),
	), t.handleRegisterAgent)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// jsonResult marshals v as JSON and wraps it in a single-text-content
// CallToolResult. Errors here are programming errors (not user input), so the
// JSON payload is "{}" rather than a tool error result.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return mcp.NewToolResultError("internal: json marshal: " + err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// callWithRetry looks up the per-session identity from the registry and
// invokes fn under a context carrying the svc-layer agent session id. If no
// session is registered for this MCP session, it returns ErrUnauthenticated
// immediately — the LLM should call register_agent to self-register, or for
// stdio the session is pre-registered at startup.
//
// If fn returns ErrUnauthenticated (the svc-layer AgentRecord has expired
// since the Session was cached), callWithRetry attempts a single silent
// refresh via refreshSession using the cached identity, then retries fn
// once. Mutating svc methods run requireSession before any state change, so
// an ErrUnauthenticated return guarantees the original call was a no-op and
// the retry is safe. A second ErrUnauthenticated is returned verbatim — no
// looping.
func (t *Tools) callWithRetry(ctx context.Context, fn func(ctx context.Context) error) error {
	sessionID := t.sessionIDFromContext(ctx)
	sess, ok := t.registry.Get(sessionID)
	if !ok {
		return fmt.Errorf("%w: no agent registered for session %q. "+
			"If this project does not exist yet, call create_project first (no session required — the bootstrap escape valve; remote clients pass slug+name+summary, stdio clients also pass project_path). "+
			"Then call register_agent (remote: project_slug; stdio: project_path) to bind this session.",
			domain.ErrUnauthenticated, sessionID)
	}
	err := fn(svc.WithSessionID(ctx, sess.AgentID))
	if err == nil || !errors.Is(err, domain.ErrUnauthenticated) {
		return err
	}
	newSess, refreshErr := t.refreshSession(ctx, sessionID, sess)
	if refreshErr != nil {
		return fmt.Errorf("%w: session expired and auto-refresh failed (%v); call register_agent",
			domain.ErrUnauthenticated, refreshErr)
	}
	return fn(svc.WithSessionID(ctx, newSess.AgentID))
}

// refreshSession mints a fresh svc-layer agent for the cached identity in
// prev, swaps it into the registry under sessionID, and returns the updated
// Session. The cached AgentKey, AgentName, and Metadata are reused so the
// audit trail stays continuous; ProjectSlug/ProjectPath copy through. The
// project mount lives in svc memory and survives the in-process retry, so
// no re-mount is needed.
func (t *Tools) refreshSession(ctx context.Context, sessionID string, prev *Session) (*Session, error) {
	agentID, expiresAt, err := t.svc.RegisterAgent(ctx, prev.AgentKey, prev.AgentName, prev.Metadata, 0)
	if err != nil {
		return nil, err
	}
	next := &Session{
		AgentID:     agentID,
		AgentKey:    prev.AgentKey,
		AgentName:   prev.AgentName,
		Metadata:    prev.Metadata,
		ProjectSlug: prev.ProjectSlug,
		ProjectPath: prev.ProjectPath,
		ExpiresAt:   expiresAt,
	}
	if err := t.registry.Register(sessionID, next); err != nil {
		return nil, err
	}
	t.logger.Info("auto-refreshed expired mcp session",
		"session_id", sessionID,
		"agent_key", prev.AgentKey,
		"expires_at", expiresAt.UTC().Format(time.RFC3339),
	)
	return next, nil
}

// errorResult builds an MCP error result from a domain-mapped error message.
func errorResult(err error) *mcp.CallToolResult {
	return mcp.NewToolResultError(formatError(err))
}

// requireStringArgs extracts the named string arguments from req in an
// encoding-robust way, returning an accurate error when any are missing or of
// the wrong type.
//
// Why this exists: mcp-go's CallToolRequest.RequireString reads arguments only
// via GetArguments(), which type-asserts req.Params.Arguments to a
// map[string]any and returns nil for any other shape. Most transports deliver
// a decoded map, but some envelopes hand the field over as json.RawMessage (or
// another non-map form). When that happens GetArguments() is nil and
// RequireString reports *every* field as `required argument %q not found` —
// even though the field is present, just not pre-decoded into a map. That's the
// misleading "work_summary not found" reported on long/rich completions: the
// payload is fine, the envelope shape isn't what RequireString assumed.
//
// requireStringArgs falls back to BindArguments (which marshals whatever
// Arguments holds back to JSON and re-decodes it) so a non-map envelope still
// resolves, and it reports all missing fields at once with an accurate message
// rather than a single misleading "not found". There is deliberately no length
// or formatting cap — long, richly-formatted markdown is exactly what the
// completion fields are meant to carry.
func requireStringArgs(req mcp.CallToolRequest, keys ...string) (map[string]string, error) {
	raw := req.GetArguments()
	if raw == nil {
		// Arguments arrived in a non-map form (e.g. json.RawMessage). Recover
		// it via BindArguments rather than emitting a misleading "not found".
		recovered := map[string]any{}
		if err := req.BindArguments(&recovered); err != nil {
			return nil, fmt.Errorf("could not decode tool arguments: %v", err)
		}
		raw = recovered
	}
	out := make(map[string]string, len(keys))
	var missing []string
	for _, k := range keys {
		v, ok := raw[k]
		if !ok || v == nil {
			missing = append(missing, k)
			continue
		}
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("argument %q must be a string, got %T", k, v)
		}
		out[k] = s
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("required argument(s) not found: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// ---- Projects ----

func (t *Tools) handleListProjects(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var out []*domain.Project
	err := t.callWithRetry(ctx, func(ctx context.Context) error {
		ps, err := t.svc.ListProjects(ctx)
		if err != nil {
			return err
		}
		out = ps
		return nil
	})
	if err != nil {
		return errorResult(err), nil
	}
	resp := make([]map[string]any, 0, len(out))
	for _, p := range out {
		resp = append(resp, formatProject(p))
	}
	return jsonResult(map[string]any{"projects": resp})
}

func (t *Tools) handleCreateProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	slug, err := req.RequireString("slug")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	name, err := req.RequireString("name")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	summary, err := req.RequireString("summary")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	projectPath := strings.TrimSpace(req.GetString("project_path", ""))
	if projectPath == "" {
		if !t.Remote {
			return mcp.NewToolResultError("invalid argument: project_path is required for stdio clients"), nil
		}
		root := strings.TrimSpace(t.svc.Cfg.RemoteProjectRoot)
		if root == "" {
			return mcp.NewToolResultError("invalid argument: project_path omitted but server has no remote_project_root configured"), nil
		}
		projectPath = filepath.Join(root, slug)
	}
	description := req.GetString("description", "")

	// CreateProjectAt is the bootstrap mutation — it doesn't go through
	// callWithRetry. If a session happens to be registered for this MCP
	// session we thread its agent ID into ctx so created_by gets attributed;
	// otherwise the call proceeds with no agent and svc's optionalSession
	// leaves created_by empty. This breaks the chicken-and-egg (register_agent
	// needs project.yaml; project.yaml only exists after create_project) —
	// call create_project from any client, then register_agent for everything
	// else.
	if sess, ok := t.registry.Get(t.sessionIDFromContext(ctx)); ok {
		ctx = svc.WithSessionID(ctx, sess.AgentID)
	}
	p, err := t.svc.CreateProjectAt(ctx, projectPath, slug, name, description, summary)
	if err != nil {
		return errorResult(err), nil
	}
	return jsonResult(formatProject(p))
}

func (t *Tools) handleGetProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	idOrSlug, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	var p *domain.Project
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.GetProject(ctx, idOrSlug)
		if err != nil {
			return err
		}
		p = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(formatProject(p))
}

func (t *Tools) handleGetProjectSummary(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	idOrSlug, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	var p *domain.Project
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.GetProject(ctx, idOrSlug)
		if err != nil {
			return err
		}
		p = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(map[string]any{
		"project_id_or_slug": idOrSlug,
		"summary":            p.Summary,
	})
}

func (t *Tools) handleLoadProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	idOrSlug, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	var res svc.LoadProjectResult
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.LoadProject(ctx, idOrSlug)
		if err != nil {
			return err
		}
		res = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(map[string]any{
		"project":             formatProject(res.Project),
		"handle":              res.Handle,
		"expires_at":          formatTime(res.ExpiresAt),
		"ticket_count":        res.TicketCount,
		"active_ticket_count": res.ActiveTicketCount,
	})
}

func (t *Tools) handleUpdateProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	idOrSlug, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	args := req.GetArguments()
	in := domain.UpdateProjectInput{}
	if v, ok := args["name"].(string); ok {
		in.Name = &v
	}
	if v, ok := args["description"].(string); ok {
		in.Description = &v
	}
	if v, ok := args["summary"].(string); ok {
		in.Summary = &v
	}
	var p *domain.Project
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.UpdateProject(ctx, idOrSlug, in)
		if err != nil {
			return err
		}
		p = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(formatProject(p))
}

func (t *Tools) handleDeleteProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	idOrSlug, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		return t.svc.DeleteProject(ctx, idOrSlug)
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(map[string]any{"deleted": idOrSlug})
}

func (t *Tools) handleReembedProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	idOrSlug, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		return t.svc.ReembedProject(ctx, idOrSlug)
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(map[string]any{"reembed_project": idOrSlug, "status": "re-embedding enqueued"})
}

// ---- Phases ----

func (t *Tools) handleListPhases(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	idOrSlug, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	var out []*domain.Phase
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		ps, err := t.svc.ListPhases(ctx, idOrSlug)
		if err != nil {
			return err
		}
		out = ps
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	resp := make([]map[string]any, 0, len(out))
	for _, p := range out {
		resp = append(resp, formatPhase(p))
	}
	return jsonResult(map[string]any{"phases": resp})
}

func (t *Tools) handleCreatePhase(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	idOrSlug, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	name, err := req.RequireString("name")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	summary, err := req.RequireString("summary")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	description := req.GetString("description", "")
	var p *domain.Phase
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.CreatePhase(ctx, idOrSlug, name, description, summary)
		if err != nil {
			return err
		}
		p = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(formatPhase(p))
}

func (t *Tools) handleGetPhase(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pid, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	phid, err := req.RequireString("phase_id_or_slug")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	var p *domain.Phase
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.GetPhase(ctx, pid, phid)
		if err != nil {
			return err
		}
		p = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(formatPhase(p))
}

func (t *Tools) handleGetPhaseSummary(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pid, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	phid, err := req.RequireString("phase_id_or_slug")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	var p *domain.Phase
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.GetPhase(ctx, pid, phid)
		if err != nil {
			return err
		}
		p = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(map[string]any{
		"project_id_or_slug": pid,
		"phase_id_or_slug":   phid,
		"summary":            p.Summary,
	})
}

func (t *Tools) handleUpdatePhase(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pid, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	phid, err := req.RequireString("phase_id_or_slug")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	args := req.GetArguments()
	in := domain.UpdatePhaseInput{}
	if v, ok := args["name"].(string); ok {
		in.Name = &v
	}
	if v, ok := args["description"].(string); ok {
		in.Description = &v
	}
	if v, ok := args["summary"].(string); ok {
		in.Summary = &v
	}
	var p *domain.Phase
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.UpdatePhase(ctx, pid, phid, in)
		if err != nil {
			return err
		}
		p = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(formatPhase(p))
}

func (t *Tools) handleDeletePhase(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pid, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	phid, err := req.RequireString("phase_id_or_slug")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		return t.svc.DeletePhase(ctx, pid, phid)
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(map[string]any{
		"deleted_phase":      phid,
		"project_id_or_slug": pid,
	})
}

func (t *Tools) handleListWaves(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pid, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	args := req.GetArguments()
	var phasePtr *string
	if v, ok := args["phase_id_or_slug"].(string); ok && v != "" {
		phasePtr = &v
	}
	var waves []domain.WaveSummary
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.ListWaves(ctx, pid, phasePtr)
		if err != nil {
			return err
		}
		waves = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	out := make([]map[string]any, 0, len(waves))
	for _, w := range waves {
		out = append(out, formatWaveSummary(w))
	}
	return jsonResult(map[string]any{"waves": out})
}

// ---- Tickets ----

func (t *Tools) handleListTickets(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pid, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	args := req.GetArguments()
	in := domain.ListTicketsInput{ProjectIDOrSlug: pid}
	if v, ok := args["column"].(string); ok && v != "" {
		col := domain.Column(v)
		in.Column = &col
	}
	if v, ok := args["phase_id_or_slug"].(string); ok && v != "" {
		in.PhaseIDOrSlug = &v
	}
	if v, ok := args["wave"]; ok {
		if f, fok := v.(float64); fok {
			n := int(f)
			in.Wave = &n
		}
	}
	if v, ok := args["ready_only"].(bool); ok {
		in.ReadyOnly = v
	}
	if v, ok := args["include_archived"].(bool); ok {
		in.IncludeArchived = v
	}
	if v, ok := args["limit"]; ok {
		if f, fok := v.(float64); fok {
			in.Limit = int(f)
		}
	}
	if v, ok := args["cursor"].(string); ok {
		in.Cursor = v
	}

	var tickets []*domain.Ticket
	var nextCursor string
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		ts, nc, err := t.svc.ListTickets(ctx, in)
		if err != nil {
			return err
		}
		tickets = ts
		nextCursor = nc
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	resp := make([]map[string]any, 0, len(tickets))
	for _, tk := range tickets {
		resp = append(resp, formatTicket(tk))
	}
	return jsonResult(map[string]any{
		"tickets":     resp,
		"next_cursor": nextCursor,
	})
}

func (t *Tools) handleCreateTicket(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pid, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	title, err := req.RequireString("title")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	args := req.GetArguments()
	in := domain.CreateTicketInput{
		ProjectIDOrSlug: pid,
		Title:           title,
		Body:            req.GetString("body", ""),
	}
	if v, ok := args["phase_id_or_slug"].(string); ok && v != "" {
		in.PhaseIDOrSlug = &v
	}
	if v, ok := args["wave"]; ok {
		if f, fok := v.(float64); fok {
			in.Wave = int(f)
		}
	}
	in.DependsOn = stringSliceFromAny(args["depends_on"])
	in.ParallelizableWith = stringSliceFromAny(args["parallelizable_with"])

	var tk *domain.Ticket
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.CreateTicket(ctx, in)
		if err != nil {
			return err
		}
		tk = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(formatTicket(tk))
}

func (t *Tools) handleGetTicket(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("ticket_id")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	var tk *domain.Ticket
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.GetTicket(ctx, id)
		if err != nil {
			return err
		}
		tk = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(formatTicket(tk))
}

func (t *Tools) handleUpdateTicket(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("ticket_id")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	args := req.GetArguments()
	in := domain.UpdateTicketInput{}
	if v, ok := args["title"].(string); ok {
		in.Title = &v
	}
	if v, ok := args["body"].(string); ok {
		in.Body = &v
	}
	if v, ok := args["wave"]; ok {
		if f, fok := v.(float64); fok {
			n := int(f)
			in.Wave = &n
		}
	}
	var tk *domain.Ticket
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.UpdateTicket(ctx, id, in)
		if err != nil {
			return err
		}
		tk = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(formatTicket(tk))
}

func (t *Tools) handleMoveTicket(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("ticket_id")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	target, err := req.RequireString("target_column")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	comment, err := req.RequireString("comment")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	var tk *domain.Ticket
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.MoveTicket(ctx, id, domain.Column(target), comment)
		if err != nil {
			return err
		}
		tk = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(formatTicket(tk))
}

func (t *Tools) handleCompleteTicket(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Decode all four fields through requireStringArgs so a non-map argument
	// envelope (json.RawMessage etc.) still resolves and the error names every
	// missing field accurately — RequireString would mis-report present-but-
	// non-map fields as "required argument not found". See requireStringArgs.
	args, err := requireStringArgs(req, "ticket_id", "testing_evidence", "work_summary", "learnings")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	id, te, ws, ln := args["ticket_id"], args["testing_evidence"], args["work_summary"], args["learnings"]
	var tk *domain.Ticket
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.CompleteTicket(ctx, id, te, ws, ln)
		if err != nil {
			return err
		}
		tk = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(formatTicket(tk))
}

func (t *Tools) handleAssignTicketToPhase(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("ticket_id")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	comment, err := req.RequireString("comment")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	args := req.GetArguments()
	var phasePtr *string
	if v, ok := args["phase_id_or_slug"].(string); ok && v != "" {
		phasePtr = &v
	}
	var tk *domain.Ticket
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.AssignTicketToPhase(ctx, id, phasePtr, comment)
		if err != nil {
			return err
		}
		tk = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(formatTicket(tk))
}

func (t *Tools) handleApplyArchivePolicy(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pid, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	args := req.GetArguments()
	in := svc.ApplyPolicyInput{ProjectIDOrSlug: pid}
	if v, ok := args["commit"].(bool); ok {
		in.Commit = v
	}
	if v, ok := args["limit"]; ok {
		if f, fok := v.(float64); fok {
			in.Limit = int(f)
		}
	}

	var report *svc.ApplyPolicyReport
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.ApplyArchivePolicy(ctx, in)
		if err != nil {
			return err
		}
		report = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(map[string]any{
		"considered":    report.Considered,
		"would_archive": entriesToMaps(report.WouldArchive),
		"archived":      entriesToMaps(report.Archived),
		"skipped":       entriesToMaps(report.Skipped),
		"config": map[string]any{
			"enabled":                report.Config.Enabled,
			"min_age_days":           report.Config.MinAgeDays,
			"min_retrievals":         report.Config.MinRetrievals,
			"dislike_ratio":          report.Config.DislikeRatio,
			"early_archive_age_days": report.Config.EarlyArchiveAgeDays,
			"auto_sweep_on_mount":    report.Config.AutoSweepOnMount,
		},
	})
}

// entriesToMaps renders the per-row report entries as JSON-shaped maps.
func entriesToMaps(entries []svc.ApplyPolicyEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"ticket_id": e.TicketID,
			"title":     e.Title,
			"reason":    e.Reason,
		})
	}
	return out
}

func (t *Tools) handleArchiveTicket(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return t.archiveFlipHandler(ctx, req, true)
}

func (t *Tools) handleUnarchiveTicket(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return t.archiveFlipHandler(ctx, req, false)
}

// archiveFlipHandler is the shared body for archive_ticket / unarchive_ticket
// — both take {ticket_id, comment} and route to ArchiveTicket / UnarchiveTicket
// on the service.
func (t *Tools) archiveFlipHandler(ctx context.Context, req mcp.CallToolRequest, wantArchived bool) (*mcp.CallToolResult, error) {
	args, err := requireStringArgs(req, "ticket_id", "comment")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	var tk *domain.Ticket
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		var out *domain.Ticket
		var err error
		if wantArchived {
			out, err = t.svc.ArchiveTicket(ctx, args["ticket_id"], args["comment"])
		} else {
			out, err = t.svc.UnarchiveTicket(ctx, args["ticket_id"], args["comment"])
		}
		if err != nil {
			return err
		}
		tk = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(formatTicket(tk))
}

func (t *Tools) handleDeleteTicket(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("ticket_id")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		return t.svc.DeleteTicket(ctx, id)
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(map[string]any{"deleted_ticket": id})
}

// ---- Comments ----

func (t *Tools) handleAddComment(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("ticket_id")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	body, err := req.RequireString("body")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	var c *domain.Comment
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.CreateComment(ctx, id, body)
		if err != nil {
			return err
		}
		c = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	return jsonResult(formatComment(c))
}

func (t *Tools) handleListComments(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("ticket_id")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	var cs []*domain.Comment
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.ListComments(ctx, id)
		if err != nil {
			return err
		}
		cs = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	out := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		out = append(out, formatComment(c))
	}
	return jsonResult(map[string]any{"comments": out})
}

func (t *Tools) handleListCommentsScoped(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	ticketID := strings.TrimSpace(req.GetString("ticket_id", ""))

	// Project scope: explicit param → session default. A single-ticket scope
	// still needs the project (comments live under a project mount); fall back
	// to the session binding when project_id_or_slug is omitted.
	pid, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}

	in := domain.ListCommentsScopedInput{
		ProjectIDOrSlug: pid,
		PhaseIDOrSlug:   strings.TrimSpace(req.GetString("phase_id_or_slug", "")),
		TicketID:        ticketID,
		AuthorID:        strings.TrimSpace(req.GetString("author_id", "")),
		AuthorName:      strings.TrimSpace(req.GetString("author_name", "")),
		ExcludeAuthorID: strings.TrimSpace(req.GetString("exclude_author_id", "")),
		Order:           strings.TrimSpace(req.GetString("order", "")),
		Cursor:          strings.TrimSpace(req.GetString("cursor", "")),
		ExcludeSystem:   true, // default; overridden below if explicitly set
	}
	if v, ok := args["exclude_system"].(bool); ok {
		in.ExcludeSystem = v
	}
	if v, ok := args["limit"]; ok {
		if f, fok := v.(float64); fok {
			in.Limit = int(f)
		}
	}
	for _, k := range stringSliceFromAny(args["kinds"]) {
		in.Kinds = append(in.Kinds, domain.CommentKind(k))
	}
	if v := strings.TrimSpace(req.GetString("since", "")); v != "" {
		ts, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return mcp.NewToolResultError("invalid argument: since must be an RFC3339 timestamp: " + perr.Error()), nil
		}
		in.Since = &ts
	}
	if v := strings.TrimSpace(req.GetString("until", "")); v != "" {
		ts, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return mcp.NewToolResultError("invalid argument: until must be an RFC3339 timestamp: " + perr.Error()), nil
		}
		in.Until = &ts
	}

	var (
		scoped     []svc.ScopedComment
		nextCursor string
	)
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, nc, err := t.svc.ListCommentsScoped(ctx, in)
		if err != nil {
			return err
		}
		scoped, nextCursor = out, nc
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	out := make([]map[string]any, 0, len(scoped))
	for _, sc := range scoped {
		m := formatComment(sc.Comment)
		m["ticket_title"] = sc.TicketTitle
		out = append(out, m)
	}
	return jsonResult(map[string]any{"comments": out, "next_cursor": nextCursor})
}

// ---- Search ----

func (t *Tools) handleRateSearchResult(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Same defensive arg-decode pattern as complete_ticket: the array field
	// must round-trip whether Arguments arrives as map[string]any or a
	// pre-encoded RawMessage. GetArguments() handles the map case; BindArguments
	// recovers the rest.
	args := req.GetArguments()
	if args == nil {
		recovered := map[string]any{}
		if err := req.BindArguments(&recovered); err != nil {
			return mcp.NewToolResultError("invalid argument: could not decode tool arguments: " + err.Error()), nil
		}
		args = recovered
	}

	rating, _ := args["rating"].(string)
	if rating == "" {
		return mcp.NewToolResultError("invalid argument: rating is required ('like' or 'dislike')"), nil
	}
	reason, _ := args["reason"].(string)

	rawKeys := stringSliceFromAny(args["entry_keys"])
	if len(rawKeys) == 0 {
		return mcp.NewToolResultError("invalid argument: entry_keys must be a non-empty array of strings"), nil
	}
	keys := make([]domain.EntryKey, 0, len(rawKeys))
	for _, k := range rawKeys {
		keys = append(keys, domain.EntryKey(k))
	}

	pid, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}

	in := svc.RateInput{
		ProjectIDOrSlug: pid,
		EntryKeys:       keys,
		Rating:          domain.Rating(rating),
		Reason:          reason,
	}

	var out svc.RateOutput
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		o, err := t.svc.RateSearchResult(ctx, in)
		if err != nil {
			return err
		}
		out = o
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}

	updated := make([]map[string]any, 0, len(out.Updated))
	for _, u := range out.Updated {
		updated = append(updated, map[string]any{
			"entry_key": string(u.EntryKey),
			"likes":     u.Likes,
			"dislikes":  u.Dislikes,
		})
	}
	rejected := make([]map[string]any, 0, len(out.Rejected))
	for _, r := range out.Rejected {
		rejected = append(rejected, map[string]any{
			"entry_key": string(r.EntryKey),
			"error":     r.Error,
		})
	}
	return jsonResult(map[string]any{
		"updated":  updated,
		"rejected": rejected,
	})
}

func (t *Tools) handleSearchTickets(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	q, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	pid, err := t.resolveProjectSlug(ctx, req)
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	args := req.GetArguments()
	in := domain.SearchTicketsInput{Query: q, ProjectIDOrSlug: pid}
	if v, ok := args["limit"]; ok {
		if f, fok := v.(float64); fok {
			in.Limit = int(f)
		}
	}
	if v, ok := args["include_archived"].(bool); ok {
		in.IncludeArchived = v
	}
	for _, s := range stringSliceFromAny(args["columns"]) {
		in.Columns = append(in.Columns, domain.Column(s))
	}
	var hits []svc.TicketHit
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.SearchTickets(ctx, in)
		if err != nil {
			return err
		}
		hits = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	resp := make([]map[string]any, 0, len(hits))
	keys := make([]string, 0, len(hits))
	for _, h := range hits {
		resp = append(resp, formatTicketHit(h))
		keys = append(keys, string(h.EntryKey))
	}
	body := map[string]any{"hits": resp}
	if hint := feedbackHint(keys); hint != nil {
		body["feedback_hint"] = hint
	}
	return jsonResult(body)
}

func (t *Tools) handleSearchLearnings(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	q, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	args := req.GetArguments()
	in := domain.SearchLearningsInput{Query: q}
	if v, ok := args["project_id_or_slug"].(string); ok {
		in.ProjectIDOrSlug = v
	}
	if v, ok := args["include_archived"].(bool); ok {
		in.IncludeArchived = v
	}
	if v, ok := args["limit"]; ok {
		if f, fok := v.(float64); fok {
			in.Limit = int(f)
		}
	}
	var hits []svc.LearningHit
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.SearchLearnings(ctx, in)
		if err != nil {
			return err
		}
		hits = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	resp := make([]map[string]any, 0, len(hits))
	keys := make([]string, 0, len(hits))
	for _, h := range hits {
		resp = append(resp, formatLearningHit(h))
		keys = append(keys, string(h.EntryKey))
	}
	body := map[string]any{"hits": resp}
	if hint := feedbackHint(keys); hint != nil {
		body["feedback_hint"] = hint
	}
	return jsonResult(body)
}

func (t *Tools) handleSearchComments(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	q, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	args := req.GetArguments()
	in := domain.SearchCommentsInput{Query: q}
	if v, ok := args["project_id_or_slug"].(string); ok {
		in.ProjectIDOrSlug = v
	}
	if v, ok := args["ticket_id"].(string); ok {
		in.TicketID = v
	}
	if v, ok := args["include_archived"].(bool); ok {
		in.IncludeArchived = v
	}
	if v, ok := args["limit"]; ok {
		if f, fok := v.(float64); fok {
			in.Limit = int(f)
		}
	}
	var hits []svc.CommentHit
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.SearchComments(ctx, in)
		if err != nil {
			return err
		}
		hits = out
		return nil
	})
	if cerr != nil {
		return errorResult(cerr), nil
	}
	resp := make([]map[string]any, 0, len(hits))
	keys := make([]string, 0, len(hits))
	for _, h := range hits {
		resp = append(resp, formatCommentHit(h))
		keys = append(keys, string(h.EntryKey))
	}
	body := map[string]any{"hits": resp}
	if hint := feedbackHint(keys); hint != nil {
		body["feedback_hint"] = hint
	}
	return jsonResult(body)
}

// ---- Introspection ----

// handleWhoAmI doesn't go through svc — it's pure process-state read of the
// per-session registry entry. SPEC §MCP server > Introspection.
//
// If no session is registered (e.g. HTTP client that hasn't called
// register_agent yet) we return a descriptive payload rather than an error,
// so the LLM can discover it needs to register.
func (t *Tools) handleWhoAmI(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sessionID := t.sessionIDFromContext(ctx)
	sess, ok := t.registry.Get(sessionID)
	if !ok {
		return jsonResult(map[string]any{
			"session_id": sessionID,
			"registered": false,
			"key":        nil,
			"name":       nil,
			"expires_at": nil,
		})
	}
	out := map[string]any{
		"session_id": sessionID,
		"registered": true,
		"key":        sess.AgentKey,
		"name":       sess.AgentName,
		"agent_id":   sess.AgentID,
		"expires_at": formatTime(sess.ExpiresAt),
		// Computed on read (not cached) so the value never lies as time
		// passes. The IsZero guard covers the bootstrap stdio Session,
		// which is registered before svc.RegisterAgent populates ExpiresAt.
		"expired": !sess.ExpiresAt.IsZero() && time.Now().After(sess.ExpiresAt),
	}
	if len(sess.Metadata) > 0 {
		out["metadata"] = sess.Metadata
		// Surface common metadata fields as top-level keys for convenience.
		for _, k := range []string{"model", "model_version", "client_name", "client_version"} {
			if v := sess.Metadata[k]; v != "" {
				out[k] = v
			}
		}
	}
	if sess.ProjectSlug != "" {
		out["project_slug"] = sess.ProjectSlug
	}
	if sess.ProjectPath != "" {
		out["project_path"] = sess.ProjectPath
	}
	return jsonResult(out)
}

// handleRegisterAgent lets an MCP client self-identify after connecting. The
// HTTP transport relies on this; stdio pre-registers at startup but can call
// it to override the defaults. The handler creates a fresh AgentRecord on
// every call (last-write-wins on the registry slot) so the audit trail
// always reflects the most recently declared identity.
func (t *Tools) handleRegisterAgent(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	model, err := req.RequireString("model")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	clientName, err := req.RequireString("client_name")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	projectPath := strings.TrimSpace(req.GetString("project_path", ""))
	projectSlug := strings.TrimSpace(req.GetString("project_slug", ""))
	model = strings.TrimSpace(model)
	clientName = strings.TrimSpace(clientName)
	if model == "" {
		return mcp.NewToolResultError("invalid argument: model required"), nil
	}
	if clientName == "" {
		return mcp.NewToolResultError("invalid argument: client_name required"), nil
	}
	if projectPath == "" && projectSlug == "" {
		return mcp.NewToolResultError("invalid argument: one of project_slug or project_path is required"), nil
	}
	if projectPath == "" {
		// Slug-only path: prefer an already-mounted project (it may live
		// outside remote_project_root, e.g. a legacy stdio create) and fall
		// back to the <remote_project_root>/<slug> convention used by
		// remote-mode create_project.
		if p, ok := t.svc.MountRepoPathForSlug(projectSlug); ok {
			projectPath = p
		} else {
			root := strings.TrimSpace(t.svc.Cfg.RemoteProjectRoot)
			if root == "" {
				return mcp.NewToolResultError("invalid argument: project_slug given but server has no remote_project_root configured; pass project_path instead"), nil
			}
			projectPath = filepath.Join(root, projectSlug)
		}
	}
	if !filepath.IsAbs(projectPath) {
		return mcp.NewToolResultError("invalid argument: project_path must be absolute"), nil
	}

	// No stat-precondition on projectPath itself: a missing dir is just a
	// missing project.yaml, handled below with the same "call create_project
	// first" hint. HTTP clients running on a different host than the server
	// frequently pass a path that exists on their machine but not the
	// server's — the bootstrap path through create_project will materialise
	// it (under remote_project_root). See projects.go:CreateProjectAt.
	projectYAML := filepath.Join(projectPath, ".tickets_please", "project.yaml")
	projectRec := &store.ProjectRecord{}
	if err := store.ReadYAML(projectYAML, projectRec); err != nil {
		if os.IsNotExist(err) {
			return mcp.NewToolResultError(fmt.Sprintf(
				"no .tickets_please/project.yaml at %s — this project does not exist yet. "+
					"Call create_project first (no session required — it's the bootstrap escape valve); "+
					"once project.yaml exists, register_agent works.",
				projectPath,
			)), nil
		}
		return mcp.NewToolResultError("invalid argument: read project.yaml: " + err.Error()), nil
	}

	// Mount the project in the Service registry so subsequent per-slug routing
	// can find it. Slug-collision errors (a different repo already holding this
	// slug) propagate to the user; idempotent re-registers are no-ops.
	mountedSlug, err := t.svc.RegisterProjectMount(ctx, projectPath)
	if err != nil {
		return mcp.NewToolResultError("register project mount: " + err.Error()), nil
	}
	if mountedSlug != projectRec.Slug {
		return mcp.NewToolResultError(fmt.Sprintf(
			"internal: mounted slug %q does not match project.yaml slug %q", mountedSlug, projectRec.Slug,
		)), nil
	}

	modelVersion := strings.TrimSpace(req.GetString("model_version", ""))
	clientVersion := strings.TrimSpace(req.GetString("client_version", ""))
	agentKey := strings.TrimSpace(req.GetString("agent_key", ""))
	agentName := strings.TrimSpace(req.GetString("agent_name", ""))

	if agentKey == "" {
		slug := strings.ToLower(strings.ReplaceAll(clientName, " ", "_"))
		agentKey = fmt.Sprintf("%s:%s", slug, randomHex(8))
	}
	if agentName == "" {
		agentName = clientName
	}

	metadata := map[string]string{
		"model":        model,
		"client_name":  clientName,
		"project_path": projectPath,
	}
	if modelVersion != "" {
		metadata["model_version"] = modelVersion
	}
	if clientVersion != "" {
		metadata["client_version"] = clientVersion
	}
	if host, hErr := os.Hostname(); hErr == nil && host != "" {
		metadata["hostname"] = host
	}

	agentID, expiresAt, err := t.svc.RegisterAgent(ctx, agentKey, agentName, metadata, 0)
	if err != nil {
		return errorResult(err), nil
	}

	sessionID := t.sessionIDFromContext(ctx)
	sess := &Session{
		AgentID:     agentID,
		AgentKey:    agentKey,
		AgentName:   agentName,
		Metadata:    metadata,
		ProjectSlug: projectRec.Slug,
		ProjectPath: projectPath,
		ExpiresAt:   expiresAt,
	}
	if err := t.registry.Register(sessionID, sess); err != nil {
		return mcp.NewToolResultError("internal: register session: " + err.Error()), nil
	}

	return jsonResult(map[string]any{
		"session_id":   sessionID,
		"agent_id":     agentID,
		"agent_key":    agentKey,
		"agent_name":   agentName,
		"project_slug": projectRec.Slug,
		"project_path": projectPath,
		"expires_at":   expiresAt.UTC().Format(time.RFC3339),
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// stringSliceFromAny coerces a JSON-decoded []any (or already-typed []string)
// into []string, dropping non-string elements silently. Returns nil for nil
// input so callers can `append([]string(nil), ...)` safely.
func stringSliceFromAny(v any) []string {
	if v == nil {
		return nil
	}
	switch s := v.(type) {
	case []string:
		return append([]string(nil), s...)
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	default:
		return nil
	}
}
