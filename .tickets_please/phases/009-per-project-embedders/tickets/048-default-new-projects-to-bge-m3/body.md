## Goal

Update the bundled config example so newly created projects pick `bge-m3` (8192 token context, 1024 dim) by default. Existing projects keep whatever's in their `project.yaml` until re-embedded.

## Scope

- `examples/config.yaml`:
  - Change `ollama_model: nomic-embed-text` → `ollama_model: bge-m3`.
  - Add a comment block above the embedding section: "These are *defaults for newly created projects*. Per-project overrides live in each project's `.tickets_please/project.yaml` under `embed_provider` / `embed_model`. Change those + click Re-embed in Settings to migrate an existing project to a new model."
- Update `README.md` (any section that documents the embed config) to reflect that defaults are project-template values, not server-wide enforcement.

## Tests

- None — pure docs/config.

## Done when

- A fresh `make init-config` (which copies `examples/config.yaml` to `~/.tickets_please/config.yaml`) yields a file that creates bge-m3 projects by default.
- README's embed section reads correctly post-W5 architecture.
