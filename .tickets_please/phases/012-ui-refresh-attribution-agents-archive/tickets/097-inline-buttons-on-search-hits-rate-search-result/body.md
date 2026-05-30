The search-feedback phase made `rate_search_result` available to LLMs via MCP, but not to humans. Add inline rating so a human reviewing search results can also nudge the ranking.

## Acceptance

- Each search hit (tickets / learnings / comments tabs) gets two small buttons: 👍 / 👎.
- Click → POST to `/p/{slug}/search/rate` with `{entry_key, rating, query?}`. Handler wraps the existing `RateSearchResult` service method.
- Confirmed feedback shows the button in a sticky "rated" state with a "thanks" micro-toast.
- Aggregate counts displayed inline next to the score: e.g. `0.84 · 👍 3 · 👎 1` (only if counts > 0).
- Optional comment field appears below the row when 👎 clicked — short "what was wrong?" textarea, submits same endpoint with `reason`. ESC dismisses.
- Tests cover: rating POST happy path, error path, count rendering.

## Hints

- Search hits already have the entry key implicitly (the search response builds `entry_key` per [[search-feedback-phase]]). Make sure the templ component receives it.
- The "thanks" toast uses the new `Toast` component from the lib.
