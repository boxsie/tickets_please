User: "we should have an agents page where we can see all the registrations and the work they did". This is the index.

## Acceptance

- New route `GET /agents` rendering an agents-index page. Sidebar entry (above the project picker, since it's app-global).
- Table columns: Name, Provider/Model, Key (truncated, copyable), Registered, Last seen (live-ticking via Phase 1 W3), Acting for (user link if bound), Tickets created, Tickets completed, Comments.
- Default sort by Last seen desc. Sortable columns.
- Search/filter box (client-side filter on name/model/key).
- Row click → `/agents/{id}` detail.
- Tests cover the handler renders all agents from `ListAgents`.

## Hints

- Reuse the new `Table` component from [[component-library-skeleton]].
- The agents page is per-server, not per-project — it does NOT depend on having a project selected in the sidebar.
