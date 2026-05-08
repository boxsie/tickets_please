## Goal

Wipe-and-rebuild a project's embeddings using the project's currently configured embedder. Same routine handles manual button + cold-start auto-rebuild + UpdateProject-triggered migration.

## Scope

- `internal/svc/projects.go` — add:
  ```go
  func (s *Service) ReembedProject(ctx context.Context, idOrSlug string) error
  ```
  Steps (mirror DeleteProject's prelude):
  1. Resolve mount (`s.Cache.Get(idOrSlug)`); `requireSession(ctx)`.
  2. If `project.yaml`'s `embed_provider/model` differs from the mount's existing `Embed`, rebuild the mount's `Embed` from the new config, probe new dim, replace the four indexes (new dim → new index size). Stop and recreate the mount's worker (W2-T2).
  3. `Worker.Flush(ctx)` first (load-bearing per DeleteTicket learnings), then `BeginOp`.
  4. Walk project tree (`store.WalkProjects/WalkPhases/WalkTickets/WalkComments`); for each source compute the sibling `<stem>.embedding.json` path and `os.Remove` it (ignore `os.ErrNotExist`).
  5. Call `s.hydrateMount(slug, st)` — the existing `upsertOrEnqueue` re-enqueues every source via the mount's worker.
  6. Return immediately. Worker drains async.

- `internal/svc/projects.go` — add `ReembedAllProjects(ctx) (queued int, err error)` iterating cached mounts via `WalkProjectMounts`.

- `internal/svc/projects.go` — `UpdateProject`: when the request changes `EmbedProvider` or `EmbedModel`, write `project.yaml`, then call `ReembedProject` before returning success.

## Tests

- `internal/svc/projects_reembed_test.go`: seed a project with sidecars; call ReembedProject; assert sidecars vanish then reappear after `Worker.Flush(ctx)`.
- Test the UpdateProject auto-trigger: change embed_model via UpdateProject; old sidecars gone; worker rebuilds with new metadata.
- ReembedAllProjects with two mounts (one nomic-shaped, one bge-shaped) — both rebuild on their own dims independently.

## Done when

- `make build` + `go test ./...` green.
- The single-line happy path: change a project's embed_model in the YAML by hand, restart, and (via W2-T3 staleness detection) the project re-embeds without anyone calling this method explicitly. Verifies that ReembedProject and the auto-staleness path are equivalent.
