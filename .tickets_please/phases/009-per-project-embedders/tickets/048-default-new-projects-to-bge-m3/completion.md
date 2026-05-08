## Testing evidence
No tests required (pure docs/config). `make build` ran clean post-merge. Manual verification: `examples/config.yaml` now has `ollama_model: bge-m3` with the new comment block above explaining "these are template defaults for new projects, per-project overrides live in `.tickets_please/project.yaml`". README quickstart updated to `ollama pull bge-m3`; tech-stack line updated; "Vector search" highlight bullet updated.

## Work summary
`examples/config.yaml`: switched `ollama_model: nomic-embed-text` to `bge-m3`; added a comment block above the embedding section explaining the values are project-template defaults (overridable in each `project.yaml` under `embed_provider`/`embed_model`); added a one-liner above `ollama_model` noting bge-m3's 8192-token context and 1024-dim vectors. `README.md`: quickstart step 3 changed `ollama pull nomic-embed-text` to `ollama pull bge-m3` and reframed the comment to mention per-project overrides; "Vector search" highlight bullet clarifies that each project picks its own provider/model in project.yaml; tech-stack line updated to `Ollama (bge-m3 by default)`.

## Learnings
When config defaults shift from "server-wide" to "template for new projects + per-project override", the README needs a framing pass beyond just the model-name swap — readers are easily confused about whether defaults apply to existing projects (they don't, until Re-embed). The comment block in `examples/config.yaml` does most of the work; the README needed only small reframings. Don't rewrite unrelated sections during a docs change.
