## Testing evidence
Ran npm --prefix frontend run typecheck successfully, npm --prefix frontend run test -- --run successfully, npm --prefix frontend run build successfully, and go test ./... successfully. The drawer comments section calls list_comments, renders user/system_move/system_completion labels with author and timestamp data, rejects empty local submissions, calls add_comment through the API, and invalidates the comments query after save so the new comment appears without a full page reload.

## Work summary
Added Comment API types and list/create comment client calls, plus a comments section in the ticket drawer with chronological comment rendering, author attribution, system kind labels, add-comment textarea, empty-comment error display, save state, and comments query invalidation.

## Learnings
Comments fit naturally inside the drawer because the ticket id is already selected there. Keep comments on their own TanStack Query key so adding a comment can refresh the drawer without disturbing the board ticket list.
