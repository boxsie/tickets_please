Owners can invite other users to their projects and assign roles. Required so the multi-user threat model is actually usable.

## Acceptance

- `domain.Invitation{ID, ProjectID, Email, Role, Token, CreatedBy, CreatedAt, ExpiresAt, AcceptedAt}`.
- Store: `~/.tickets_please/invitations/{project_id}/{id}.yaml`.
- Routes:
  - `GET /p/{slug}/members` — list current members + pending invitations + invite form. Owner-only.
  - `POST /p/{slug}/members/invite` — creates Invitation with random `Token`, 7-day expiry. If SMTP configured, emails the magic link; else displays the link inline for manual sharing (homelab-friendly).
  - `GET /auth/invite/{token}` — accept flow: if not logged in, redirect to login then back here; if logged in, upsert Membership and redirect to project.
  - `POST /p/{slug}/members/{user_id}/role` — update role. Owner-only.
  - `POST /p/{slug}/members/{user_id}/remove` — revoke membership. Owner-only.
- UI: members table with role dropdown, "Resend invite" / "Cancel invite" actions, "Invite by email" form with role select.
- SMTP config block in `~/.tickets_please/config.yaml` (host/port/user/pass/from); if empty, invitations render inline-only.
- Tests cover: invite created → accept flow upserts membership → role change reflected.

## Hints

- Reuse the email-template approach in existing summary editors (markdown rendered server-side).
- Token: 32 bytes base64; one-use; deleted on accept.
