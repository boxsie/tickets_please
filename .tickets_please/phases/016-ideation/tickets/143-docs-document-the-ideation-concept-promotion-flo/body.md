Close the loop: teach humans + future agents what ideas are and how they flow. Do this last so it reflects what actually shipped.

## Changes
- **`SPEC.md`** — add an "Ideation / ticket kinds" section: the `kind` axis (`work`/`idea`), orthogonal to `column` and `archived`; ideas hidden by default, surfaced via `include_ideas` / `list_ideas` / `search_ideas`; lifecycle gating (no direct `done`); `promote_idea` as the one forward path.
- **`README.md`** — short blurb in the feature/tool list: how to throw a spitball (`create_idea`) and promote it.
- **`CLAUDE.md`** (this repo) — note the kind axis in the conventions so future agents don't hijack tickets to ideate again.
- **MCP tool descriptions** — make sure `create_idea`/`list_ideas`/`search_ideas`/`promote_idea` and the `include_ideas` params read clearly on their own (they're the model's only docs at call time).

## Acceptance
- A fresh reader of SPEC.md understands kinds vs columns vs archived and the promote flow.
- Tool descriptions stand alone.
- No stale references to "ideation phase hijacking" left implying it's the recommended path.
