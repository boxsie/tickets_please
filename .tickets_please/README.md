# `.tickets_please/` — the data directory

This directory is the on-disk store for everything `tickets_please` tracks. It's intentionally human-readable and committed to git so the project's ticket history travels with the repo.

## Layout

```
.tickets_please/
├── agents/
│   └── <session-uuid>.yaml          # one file per agent session (active or expired)
├── projects/
│   └── <slug>/
│       ├── project.yaml             # id, name, description, attribution, timestamps
│       ├── summary.md               # required ≥200-char markdown context doc
│       ├── summary.embedding.json   # 768-float JSON array
│       ├── tickets/                 # phase-less tickets
│       │   └── <NNN>-<slug>/
│       │       ├── ticket.yaml
│       │       ├── body.md
│       │       ├── body.embedding.json
│       │       ├── completion.md             # only when done
│       │       ├── learnings.embedding.json  # only when done
│       │       └── comments/
│       │           ├── <ts>-<short-id>-<kind>.md
│       │           └── <ts>-<short-id>-<kind>.embedding.json
│       └── phases/                  # only present if the project uses phases
│           └── <NNN>-<phase-slug>/
│               ├── phase.yaml
│               ├── summary.md
│               ├── summary.embedding.json
│               └── tickets/
│                   └── ...
└── .staging/                        # transient atomicity scratch; gitignored
```

## Rules of the road

- **Don't hand-edit `.staging/`.** It's where atomic multi-file writes assemble before renaming into place. The server cleans it up; if you find leftovers after a crash, run `tickets_please check` (or just `rm -rf .tickets_please/.staging/`).
- **Embeddings are regenerable.** Delete any `*.embedding.json` and the next worker pass re-creates it.
- **Comments are immutable.** Once written, a comment file stays put. Typos get a follow-up comment, not an edit.
- **Hand-edits are allowed.** If you patch a `ticket.yaml` directly, the server will pick it up the next time the project is loaded (fsnotify or restart).

## Where the spec lives

See [`../SPEC.md`](../SPEC.md) — Data layout section is canonical.
