Agents stay key-authenticated via MCP. But on user-owned projects, the UI should show "Claude (acting for Dan)" not just "Claude" — the agent's actions are taken on behalf of a registered user, and the audit trail needs to record that link.

## Acceptance

- `domain.Agent` gains optional `ActingFor *UserRef` (UserID + DisplayName).
- `register_agent` MCP tool gains optional `acting_for_user_id` param (or extracts it from a config sidecar at `~/.tickets_please/agents/agent-bindings.yaml`).
- When agent is acting for a user, server-side membership check applies — the agent inherits the user's project access (owner/member/viewer).
- Comment attribution renders "Claude (for Dan)" with both names linkable: Claude → `/agents/{id}`, Dan → `/u/{user}`.
- Tickets created/completed by an acting-for-user agent record both `created_by` (agent) and a new `created_for` (user) field on the record. Same for `completed_for`.
- Tests cover: agent without acting_for behaves as today; agent acting for user with no membership is rejected; agent acting for user with membership inherits role.

## Hints

- This is the cleanest place to enforce "an agent can only mutate projects its bound user has access to" — the MCP server already has the agent record at every tool call.
- Existing tickets without `created_for` render as today; new ones get both fields.
