"Export to CSV" button on the /projects analytics table. For the data nerd who wants to slice metrics in a spreadsheet.

## Acceptance

- `GET /projects/export.csv` returns text/csv with one row per project + a header row.
- Columns match the analytics table columns (sparkline data flattened to 4 columns `velocity_w1`…`velocity_w4`).
- Per-column dwell-time histograms exported as a separate `dwell_p50_todo`, `dwell_p90_todo`, etc. set of columns.
- Honours the same membership filter as the table (user only sees projects they can access).
- Filename: `tickets_please_projects_YYYY-MM-DD.csv`.
- Tests cover: csv structure, escaping (project names with commas), membership filter.

## Hints

- Use `encoding/csv` from stdlib; no extra dep.
- Generate the same metrics structs as the table — single source of truth.
