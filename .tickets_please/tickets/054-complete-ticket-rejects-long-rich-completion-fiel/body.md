## Symptom

`complete_ticket` intermittently fails with:

```
invalid argument: required argument "work_summary" not found
```

even though `work_summary` (and `testing_evidence` and `learnings`) ARE all
supplied. The trigger is **the content of the fields, not their absence** —
long and/or richly-formatted markdown in those three fields causes the failure.
Short plain strings always succeed. The error message is therefore misleading:
it reports a missing required argument when the real problem is payload handling
of arguments that are present.

## Impact

- Completing a ticket with substantive `testing_evidence` / `work_summary` /
  `learnings` — exactly what the system asks for ("be thorough", "learnings are
  the point") — fails.
- The misleading error sends the agent hunting for a missing-argument bug that
  isn't there.
- The workaround degrades the data: the standing mitigation is to call
  `complete_ticket` with placeholder strings ("see follow-up comment") in all
  three fields, then `add_comment` with the real content. That leaves the
  structured, **searchable** completion fields empty, so `search_learnings`
  can't find the real learnings — defeating the core feature. Observed
  repeatedly against the remote host while completing tickets for other projects
  (ensemble: tickets T2/T6/T7 and a gofmt ticket all carry "see follow-up
  comment" in `learnings`, with the substance only in a comment).

## Reproduction

1. Client: Claude Code over HTTP/MCP (also plausibly stdio — confirm).
2. Call `complete_ticket` with multi-paragraph markdown containing headed
   sections, bulleted/nested lists, fenced code blocks, and/or bold (`**...**`)
   in `testing_evidence` / `work_summary` / `learnings`.
3. Observe: `invalid argument: required argument "work_summary" not found`.
4. Re-issue the identical call with each field set to a short one-line plain
   string → succeeds.

Threshold unknown — bisect whether it's total length, a specific markdown
construct (fenced code blocks and nested lists are the prime suspects), or an
encoding/escaping issue in the argument decode path.

## Suspected area

Argument parsing / JSON (or MCP envelope) handling for `complete_ticket` —
likely a length cap or a character/escaping issue that corrupts the multi-field
payload and surfaces as a spurious "required argument not found" on the second
field. Useful contrast: `add_comment` bodies (also long free text) do NOT
exhibit this, and long `move_ticket` comments are fine — so compare how
`add_comment` reads its single `body` arg vs how `complete_ticket` decodes its
three text fields.

## Acceptance

- `complete_ticket` accepts long, multi-paragraph, markdown-formatted
  `testing_evidence` / `work_summary` / `learnings` (code fences, nested lists,
  bold) without error; OR
- if a deliberate size/format limit exists, it is documented and the error
  message is accurate and actionable (e.g. "learnings exceeds N chars" / "field
  failed to parse"), never a misleading "required argument not found".
- A regression test covering a realistic long + rich completion payload.

## Provenance

Filed by hand-authoring this ticket directory into the in-repo store because the
local tickets_please MCP wasn't connected to the authoring session (only the
remote instance was). `body.embedding.json` is therefore absent —
reindex/embed so `search_tickets` surfaces this ticket. `created_by` reuses an
existing agent id from the registry.
