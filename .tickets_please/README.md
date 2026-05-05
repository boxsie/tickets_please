# `.tickets_please/` — the per-repo data directory

This directory is the on-disk store for everything `tickets_please` tracks about *this repo's* project. It's intentionally human-readable and committed to git so the project's ticket history travels with the repo.

Agent sessions and the cross-repo mount registry live in a separate **central** data root at `~/.tickets_please/` (configurable via the `data_root` key) — they're user-scoped, not repo-scoped, so a single server hosting multiple repos doesn't duplicate them.

## Layout (per-repo)

```
.tickets_please/
├── project.yaml                         # id, slug, name, description, attribution, timestamps
├── summary.md                           # required ≥200-char markdown context doc
├── summary.embedding.json               # 768-float JSON array
├── tickets/                             # phase-less tickets sit here
│   └── <NNN>-<slug>/
│       ├── ticket.yaml
│       ├── body.md
│       ├── body.embedding.json
│       ├── completion.md                # only when done
│       ├── learnings.embedding.json     # only when done
│       └── comments/
│           ├── <ts>-<short-id>-<kind>.md
│           └── <ts>-<short-id>-<kind>.embedding.json
├── phases/                              # only present if the project uses phases
│   └── <NNN>-<phase-slug>/
│       ├── phase.yaml
│       ├── summary.md
│       ├── summary.embedding.json
│       └── tickets/
│           └── ...
├── .lock                                # per-data-dir flock (gitignored)
└── .staging/                            # transient atomicity scratch (gitignored)
```

## Layout (central `~/.tickets_please/`)

```
~/.tickets_please/
├── agents/<session-uuid>.yaml           # one file per agent session, active or expired
├── registry.yaml                        # absolute paths the server has mounted
├── config.yaml                          # optional user-level config
└── .staging/                            # transient atomicity scratch for agents/ writes
```

## Rules of the road

- **Don't hand-edit `.staging/`.** It's where atomic multi-file writes assemble before renaming into place. The server cleans it up; if you find leftovers after a crash, run `tickets_please check` (or just `rm -rf .tickets_please/.staging/`).
- **Embeddings are regenerable.** Delete any `*.embedding.json` and the next worker pass re-creates it.
- **Comments are immutable.** Once written, a comment file stays put. Typos get a follow-up comment, not an edit.
- **Hand-edits are allowed.** If you patch a `ticket.yaml` directly, the server will pick it up the next time the project is loaded (fsnotify or restart).

## Cold-starting a fresh repo

A freshly-`init`'d data dir has only `.staging/` and this README. The project record is written by `create_project` (the bootstrap escape valve — no session required) with `project_path` pointing at the repo root.

## Where the spec lives

See [`../SPEC.md`](../SPEC.md) — the **Data layout** section is canonical.
