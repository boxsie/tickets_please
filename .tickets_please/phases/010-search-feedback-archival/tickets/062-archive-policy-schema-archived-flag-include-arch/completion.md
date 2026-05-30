## Testing evidence
go test ./... green; rebuilt + restarted. See ticket comment.

## Work summary
Archived flag on ticket; ArchiveConfig on project; Decide pure helper; archive_ticket/unarchive_ticket; include_archived everywhere. See ticket comment.

## Learnings
flipArchive shares state machine for both directions; ArchivePolicy lives on mount alongside QualityParams. See ticket comment.
