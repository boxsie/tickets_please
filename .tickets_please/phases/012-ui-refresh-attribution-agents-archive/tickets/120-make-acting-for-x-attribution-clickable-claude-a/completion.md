## Testing evidence
go test ./... green. handlers_users_test.go: TestUser_Detail_Renders (seeded user → identity + "Member of"), _ShowsMemberships (granted membership → project name + role), _NotFound (unknown id → 404), TestComment_AuthorLinks (agent acting-for a user → comment author has href="/agents/{aid}" + href="/u/{uid}" + "(for "). metadata_test.go TestTicketMetadata_AttributionLinks (created-by agent → /agents/{id}, acting-for → /u/{id}). Existing comment-author tests unchanged + passing. gofmt clean. Live: /u/nobody-here → 404; ticket-detail comment authors + metadata emit href="/agents/{id}" class attr-link; a linked /agents/{id} resolves 200.

## Work summary
svc.GetUserProfile (user + cross-project memberships) in invitations.go. New users page pkg (data.go + detail.templ), handleUserDetail, GET /u/{id} route. attribution.AgentLink/UserLink components. CommentRowProps + structured fields populated in toTemplRow; comment.templ renders linked author. metadata.go agentID/actingForUserID helpers; metadata.templ links created_by/completed_by/acting-for. CSS for .attr-link + .user-detail/.user-meta/.user-membership-list. Committed 0e2eafa.

## Learnings
Clickable acting-for + the missing /u/{id} user page. This was billed as "pure presentation" but actually required building the /u/{user} page from scratch (only /agents/{id} existed from W3).

Key facts:

- NO svc USER ACCESSOR EXISTED. Added svc.GetUserProfile(userID) → {User, Memberships}. Building blocks already there: store.UserStore.ReadUser(id) (returns domain.ErrNotFound for unknown — maps straight to 404 via classifyServiceError), UserRecord.ToDomain(), store.MembershipStore.ListMembershipsForUser(userID) (scans every project dir). Resolve each membership's ProjectID → slug/name via a ListProjects id-map (project may be unmounted → empty slug, fall back to the id). Put it in svc/invitations.go alongside the other user/membership methods.

- USERS ARE NOT MINTED IN TESTS via any svc method — they come from the OAuth login flow. To seed one in a web test, write straight to the store: deps.Service.UserStore.WriteUser(&store.UserRecord{ID,Email,DisplayName,CreatedAt,LastLoginAt}). UserStore/MembershipStore are PUBLIC fields on *svc.Service, so tests reach them directly. MembershipStore.GrantMembership takes (ctx, *MembershipRecord) and returns (record, err) — not positional args.

- ACTING-FOR AGENT NEEDS THE USER'S MEMBERSHIP TO MUTATE. RegisterAgent(..., actingForUserID) makes the agent inherit that user's per-project access (authorizeActingFor). So a test that comments as an acting-for agent must FIRST GrantMembership(user, project) or CreateComment fails "forbidden: user X has no membership on this project". This is the acting-for authorization seam working as designed.

- LINK COMPONENTS: added attribution.AgentLink(id,name) + UserLink(id,name) — anchor when id!="" else plain text, so call sites drop them in unconditionally. The attribution package is the shared cycle-free home (imports only domain+reltime) every reference surface already uses.

- COMMENT AUTHOR: kept AuthorLabel (the composed "Claude (for Dan)" string) for the avatar data-initial + the no-agent fallback, but ADDED structured AgentID/AgentName/ForUserID/ForUserName to CommentRowProps, populated in toTemplRow from c.Author / c.AuthorFor. comment.templ renders the links inside the existing .comment-author span — the ticket explicitly said "anchors inside the span are fine", and the existing comment-author tests (which assert on the class + name text) still pass because the name text lives inside the anchor.

- METADATA BLOCK: agentID(ref)/actingForUserID(t) helpers added next to the existing agentName/actingForName, so created_by/completed_by link to /agents/{id} and the Acting-for row links to /u/{id}. actingForUserID mirrors actingForName's completer-over-creator preference exactly so the linked id matches the shown name.

This was the final ticket of phase 012 ui-refresh-attribution-agents-archive — the phase is now 100% complete.
