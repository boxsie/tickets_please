First brick of multi-user auth. Adds the identity types that everything else in W2 hangs off.

## Acceptance

- `domain.User{ID, Email, GitHubLogin, GoogleSub, DisplayName, AvatarURL, CreatedAt, LastLoginAt}` added.
- `domain.Membership{UserID, ProjectID, Role, GrantedBy, GrantedAt}` with `Role` enum: `owner`, `member`, `viewer`.
- On-disk store at `~/.tickets_please/users/{id}.yaml` and `~/.tickets_please/memberships/{project_id}/{user_id}.yaml` (per-project membership dir so deleting a project cleans up memberships).
- `internal/store/users.go` with `WalkUsers`, `ReadUser`, `WriteUser`, `FindUserByOAuthSubject(provider, sub)`.
- `internal/store/memberships.go` with `ListMembershipsForUser`, `ListMembersOfProject`, `GrantMembership`, `RevokeMembership`.
- Tests cover: round-trip, idempotent grant, revoke removes, lookup by OAuth subject.
- No HTTP, no OAuth yet — just the data layer.

## Hints

- Mirror the existing agent-store pattern in `internal/store/agents.go` for file layout + locking.
- `GitHubLogin` and `GoogleSub` are nullable — a user may have only one provider linked initially.
