No way to archive a whole phase at once. Added:
- `svc.ArchivePhase(project, phase, comment)` — snapshots the phase's non-archived tickets under the cache read lock, archives each via `ArchiveTicket` (own audit comment/commit/event), skips already-archived, returns an archived-vs-skipped report. Phase record left in place.
- Web: POST `/p/{slug}/phases/{phase}/archive` + an amber `.archive-zone` disclosure with confirm on the phase detail page (warn-level, reversible — not the red danger-zone).
- MCP tool `archive_phase` (Phases group → 8; total 36). Unit test `TestArchivePhase_BulkArchivesActiveTickets`.
