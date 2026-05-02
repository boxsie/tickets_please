## Goal

Replace the single-`Store` model in `Service` with a registry of project stores keyed by slug, populated as clients call `register_agent`. Each project store is rooted at its own repo's `.tickets_please/` (the flat layout from the first ticket).

## Why

A centralised server serves many projects from many repos. There's no single "data dir" for project data anymore — each project lives wherever its repo is. The server holds N stores in memory, one per active project.

## Scope

- `internal/svc/service.go`:
  - `Service` no longer holds a single `Store`. It holds:
    - `agentStore *store.AgentStore` (central, from T2)
    - `projectStores sync.Map` keyed by slug → `*ProjectMount` (struct holding `*store.Store`, `RepoPath`, `LastTouchedAt`)
    - `learningsIndex`, `summariesIndex` (resident, aggregated — see "cross-project resident indexes" ticket)
  - New methods: `RegisterProjectMount(slug, repoPath)` opens a `Store` rooted at `<repoPath>/.tickets_please/`, validates `project.yaml`, adds to map. `ResolveProjectStore(slug)` returns `*Store` or error.
- `internal/cache/projectcache.go`:
  - Resolution uses `Service.ResolveProjectStore(slug)` instead of walking a `projects/` dir.
  - `WalkProjects()` repurposed: iterates `Service.projectStores` (the in-memory registry), not disk.
- `internal/mcptools/tools.go` (`register_agent` handler from T4): wire the call into `RegisterProjectMount` so registering an agent also mounts the project.

## LRU / eviction

- Use the existing `project_idle_minutes` (default 15) and `max_loaded_projects` (default 16) config.
- Eviction: oldest-touched mount kicked out, store closed, indexes notified to drop that project's contributions.
- On re-resolve after eviction, re-mount from `RepoPath` (still remembered in the registry — eviction doesn't drop the path, just the open store).

Wait — that's a tension. If we evict the path too, clients have to re-register. If we keep the path, evicted projects can re-mount silently. Decision: **keep the path** (it's tiny). Evict only the open Store + index entries.

## Verification

- Two clients register agents with two different `project_path`s. `ResolveProjectStore` returns each correctly. Each tool call hits the right project.
- Same project_path registered from two clients: single mount entry, ref-counted to keep alive until both disconnect.
- Mount eviction: register 17 projects, the oldest gets its Store closed; re-call against it transparently re-opens.
- Race-detector clean under concurrent register/resolve.

## Notes

- Slug collision across two repos: error on second registration. Tell the user "slug 'foo' is already mounted at `/other/path`". Don't silently rebind.
- Project.yaml's `id` (UUID) is the deeper identity; slug collisions should also check the UUID. If UUIDs differ, it's a real conflict.
