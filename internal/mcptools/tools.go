package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
)

// Tools wraps the in-process svc.Service plus the MCP-side identity into a
// single struct that registers all 28 tools against an *mcpserver.MCPServer.
//
// One Tools per process — the MCP binary builds it once, calls RegisterAll,
// and hands the server off to ServeStdio.
type Tools struct {
	svc      *svc.Service
	identity *Identity
	logger   *slog.Logger
}

// NewTools constructs a Tools. The caller owns the lifecycle of svc and
// identity; Tools just borrows them.
func NewTools(s *svc.Service, id *Identity, logger *slog.Logger) *Tools {
	if logger == nil {
		logger = slog.Default()
	}
	return &Tools{svc: s, identity: id, logger: logger}
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
		mcp.WithDescription("Create a new project. Slug must be unique and URL-safe. **Requires a `summary` field — a markdown document (≥200 chars) describing the project's goals, key components, and constraints.** This summary becomes the load-bearing context any future agent reads before working in this project. Be thorough."),
		mcp.WithString("slug", mcp.Required(), mcp.Description("URL-safe unique slug for the project")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Display name for the project")),
		mcp.WithString("description", mcp.Description("One-line description")),
		mcp.WithString("summary", mcp.Required(), mcp.Description("Markdown summary (≥200 chars) — the load-bearing context doc")),
	), t.handleCreateProject)

	s.AddTool(mcp.NewTool("get_project",
		mcp.WithDescription("Fetch a project's full record (counts, attribution, timestamps, summary)."),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Project id or slug")),
	), t.handleGetProject)

	s.AddTool(mcp.NewTool("get_project_summary",
		mcp.WithDescription("Fetch just the project's summary markdown. **Read this before doing any non-trivial work in a project — it's the project's design context.**"),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Project id or slug")),
	), t.handleGetProjectSummary)

	s.AddTool(mcp.NewTool("load_project",
		mcp.WithDescription("Pre-warm a project into the server's in-memory cache. Useful before doing many operations against the same project. Optional — calls auto-load if needed."),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Project id or slug")),
	), t.handleLoadProject)

	s.AddTool(mcp.NewTool("update_project",
		mcp.WithDescription("Edit a project's name, description, or summary. Summary edits trigger re-embedding."),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Project id or slug")),
		mcp.WithString("name", mcp.Description("New name (optional)")),
		mcp.WithString("description", mcp.Description("New description (optional)")),
		mcp.WithString("summary", mcp.Description("New summary markdown (optional, ≥200 chars when supplied)")),
	), t.handleUpdateProject)

	s.AddTool(mcp.NewTool("delete_project",
		mcp.WithDescription("Delete a project. Refuses if any tickets are still active."),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Project id or slug")),
	), t.handleDeleteProject)

	// Phases (7)
	s.AddTool(mcp.NewTool("list_phases",
		mcp.WithDescription("List phases in a project with active and total ticket counts."),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Project id or slug")),
	), t.handleListPhases)

	s.AddTool(mcp.NewTool("create_phase",
		mcp.WithDescription("Add a phase to a project for bigger bodies of work. Requires a `summary` (≥200 chars) — same load-bearing context doc as projects, scoped to this phase."),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Parent project id or slug")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Phase display name")),
		mcp.WithString("description", mcp.Description("One-line description")),
		mcp.WithString("summary", mcp.Required(), mcp.Description("Markdown summary (≥200 chars)")),
	), t.handleCreatePhase)

	s.AddTool(mcp.NewTool("get_phase",
		mcp.WithDescription("Fetch a phase's full record."),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Parent project id or slug")),
		mcp.WithString("phase_id_or_slug", mcp.Required(), mcp.Description("Phase id or slug")),
	), t.handleGetPhase)

	s.AddTool(mcp.NewTool("get_phase_summary",
		mcp.WithDescription("Fetch a phase's full summary markdown. Read this when entering a phase, the same way you'd read a project summary."),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Parent project id or slug")),
		mcp.WithString("phase_id_or_slug", mcp.Required(), mcp.Description("Phase id or slug")),
	), t.handleGetPhaseSummary)

	s.AddTool(mcp.NewTool("update_phase",
		mcp.WithDescription("Edit a phase's name, description, or summary."),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Parent project id or slug")),
		mcp.WithString("phase_id_or_slug", mcp.Required(), mcp.Description("Phase id or slug")),
		mcp.WithString("name", mcp.Description("New name (optional)")),
		mcp.WithString("description", mcp.Description("New description (optional)")),
		mcp.WithString("summary", mcp.Description("New summary markdown (optional, ≥200 chars when supplied)")),
	), t.handleUpdatePhase)

	s.AddTool(mcp.NewTool("delete_phase",
		mcp.WithDescription("Delete a phase. Refuses if any tickets are still assigned to it."),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Parent project id or slug")),
		mcp.WithString("phase_id_or_slug", mcp.Required(), mcp.Description("Phase id or slug")),
	), t.handleDeletePhase)

	s.AddTool(mcp.NewTool("list_waves",
		mcp.WithDescription("List the waves in a phase (or in the phase-less area of a project) with per-wave ticket counts. A wave is a soft integer grouping on tickets — no enforcement, just organization. Use this to see how a body of work decomposes."),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Project id or slug")),
		mcp.WithString("phase_id_or_slug", mcp.Description("Phase id or slug; omit to see waves in the phase-less area")),
	), t.handleListWaves)

	// Tickets (7)
	s.AddTool(mcp.NewTool("list_tickets",
		mcp.WithDescription("List tickets in a project, optionally filtered by column or phase. Use `ready_only=true` to surface only unblocked tickets."),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Project id or slug")),
		mcp.WithString("column", mcp.Description("Filter by column: todo | in_progress | testing | done")),
		mcp.WithString("phase_id_or_slug", mcp.Description("Filter by phase id/slug; pass \"-\" for phase-less only")),
		mcp.WithNumber("wave", mcp.Description("Filter by wave (0 = unassigned)")),
		mcp.WithBoolean("ready_only", mcp.Description("If true, only return unblocked tickets in todo/in_progress")),
		mcp.WithNumber("limit", mcp.Description("Page size, default 50, max 200")),
		mcp.WithString("cursor", mcp.Description("Opaque pagination cursor")),
	), t.handleListTickets)

	s.AddTool(mcp.NewTool("create_ticket",
		mcp.WithDescription("Create a new ticket in a project. Tickets always start in the `todo` column. Provide a clear title and a body that describes the work; both will be searchable. Optional `phase_id_or_slug`, `depends_on`, `parallelizable_with`."),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Project id or slug")),
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

	// Search (4)
	s.AddTool(mcp.NewTool("search_projects",
		mcp.WithDescription("Semantic search over project summaries. Use when picking a project to work in or finding related projects."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Natural-language query")),
		mcp.WithNumber("limit", mcp.Description("Max results, default 10, max 50")),
	), t.handleSearchProjects)

	s.AddTool(mcp.NewTool("search_tickets",
		mcp.WithDescription("Semantic search over ticket titles and bodies in a project. Use when looking for related work."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Natural-language query")),
		mcp.WithString("project_id_or_slug", mcp.Required(), mcp.Description("Project id or slug to search inside")),
		mcp.WithArray("columns", mcp.Description("Optional column filter"), mcp.WithStringItems()),
		mcp.WithNumber("limit", mcp.Description("Max results, default 10, max 50")),
	), t.handleSearchTickets)

	s.AddTool(mcp.NewTool("search_learnings",
		mcp.WithDescription("Semantic search over completion learnings from past finished tickets. **Run this before starting non-trivial work — past you may have left notes.**"),
		mcp.WithString("query", mcp.Required(), mcp.Description("Natural-language query")),
		mcp.WithString("project_id_or_slug", mcp.Description("Optional project id/slug to scope the search")),
		mcp.WithNumber("limit", mcp.Description("Max results, default 10, max 50")),
	), t.handleSearchLearnings)

	s.AddTool(mcp.NewTool("search_comments",
		mcp.WithDescription("Semantic search across comments."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Natural-language query")),
		mcp.WithString("project_id_or_slug", mcp.Description("Optional project id/slug to scope the search")),
		mcp.WithString("ticket_id", mcp.Description("Optional ticket id to scope to one ticket's comments")),
		mcp.WithNumber("limit", mcp.Description("Max results, default 10, max 50")),
	), t.handleSearchComments)

	// Introspection (1)
	s.AddTool(mcp.NewTool("who_am_i",
		mcp.WithDescription("Returns the current agent identity the MCP server has registered for this session. Useful for the LLM to confirm its own attribution before doing work."),
	), t.handleWhoAmI)
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

// callWithRetry invokes fn under the identity-attached context, transparently
// re-registering once if svc returns ErrUnauthenticated, then retrying. After
// one failed retry the original/second error is returned to the caller as an
// MCP error result. SPEC §Agent identity & sessions > MCP integration: "the
// LLM never sees the failure".
func (t *Tools) callWithRetry(ctx context.Context, fn func(ctx context.Context) error) error {
	ctx2 := t.identity.AttachContext(ctx)
	err := fn(ctx2)
	if err == nil {
		return nil
	}
	if !errors.Is(err, domain.ErrUnauthenticated) {
		return err
	}
	t.logger.Info("session unauthenticated; re-registering")
	if rerr := t.identity.Register(ctx, t.svc); rerr != nil {
		return fmt.Errorf("re-register agent: %w", rerr)
	}
	ctx3 := t.identity.AttachContext(ctx)
	return fn(ctx3)
}

// errorResult builds an MCP error result from a domain-mapped error message.
func errorResult(err error) *mcp.CallToolResult {
	return mcp.NewToolResultError(formatError(err))
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
	description := req.GetString("description", "")

	var p *domain.Project
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.CreateProject(ctx, slug, name, description, summary)
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

func (t *Tools) handleGetProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	idOrSlug, err := req.RequireString("project_id_or_slug")
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
	idOrSlug, err := req.RequireString("project_id_or_slug")
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
	idOrSlug, err := req.RequireString("project_id_or_slug")
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
	idOrSlug, err := req.RequireString("project_id_or_slug")
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
	idOrSlug, err := req.RequireString("project_id_or_slug")
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

// ---- Phases ----

func (t *Tools) handleListPhases(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	idOrSlug, err := req.RequireString("project_id_or_slug")
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
	idOrSlug, err := req.RequireString("project_id_or_slug")
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
	pid, err := req.RequireString("project_id_or_slug")
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
	pid, err := req.RequireString("project_id_or_slug")
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
	pid, err := req.RequireString("project_id_or_slug")
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
	pid, err := req.RequireString("project_id_or_slug")
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
	pid, err := req.RequireString("project_id_or_slug")
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
	pid, err := req.RequireString("project_id_or_slug")
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
	pid, err := req.RequireString("project_id_or_slug")
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
	id, err := req.RequireString("ticket_id")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	te, err := req.RequireString("testing_evidence")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	ws, err := req.RequireString("work_summary")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	ln, err := req.RequireString("learnings")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
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

// ---- Search ----

func (t *Tools) handleSearchProjects(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	q, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	limit := req.GetInt("limit", 0)
	var hits []svc.ProjectHit
	cerr := t.callWithRetry(ctx, func(ctx context.Context) error {
		out, err := t.svc.SearchProjects(ctx, q, limit)
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
	for _, h := range hits {
		resp = append(resp, formatProjectHit(h))
	}
	return jsonResult(map[string]any{"hits": resp})
}

func (t *Tools) handleSearchTickets(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	q, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError("invalid argument: " + err.Error()), nil
	}
	pid, err := req.RequireString("project_id_or_slug")
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
	for _, h := range hits {
		resp = append(resp, formatTicketHit(h))
	}
	return jsonResult(map[string]any{"hits": resp})
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
	for _, h := range hits {
		resp = append(resp, formatLearningHit(h))
	}
	return jsonResult(map[string]any{"hits": resp})
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
	for _, h := range hits {
		resp = append(resp, formatCommentHit(h))
	}
	return jsonResult(map[string]any{"hits": resp})
}

// ---- Introspection ----

// handleWhoAmI doesn't go through svc — it's pure process-state read of the
// MCP's cached identity. SPEC §MCP server > Introspection.
func (t *Tools) handleWhoAmI(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return jsonResult(map[string]any{
		"key":        t.identity.Key,
		"name":       t.identity.Name,
		"session_id": t.identity.SessionID(),
		"expires_at": formatTime(t.identity.ExpiresAt()),
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
