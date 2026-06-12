## Testing evidence
Focused: go test ./internal/svc -run TestUpdateTicket_(ReplacesDependencyLists|DependencyValidation|TitleOnly_KeepsBody); go test ./internal/mcptools -run TestUpdateTicket_RawMessageEnvelope_(RichBody|DependencyLists)|TestRegisterAllTools. Broader: go test ./internal/svc; go test ./internal/mcptools; go test ./internal/web; go test ./...; git diff --check.

## Work summary
Added replace-set dependency editing to update_ticket and the web edit path. UpdateTicketInput now carries optional depends_on and parallelizable_with slices; Service.UpdateTicket validates same-project refs, rejects self references, rejects dependency cycles, writes the lists back to ticket.yaml, and refreshes cached BlockedBy. MCP update_ticket now exposes dependency arrays and preserves raw-envelope handling; SPEC documents the new contract.

## Learnings
Mutable dependency editing is best modeled as optional slice pointers on UpdateTicketInput: nil means leave unchanged, while a present empty slice clears the YAML field. UpdateTicket must re-read ticket.yaml before writing so completion/attribution fields are preserved, then update cached DependsOn/ParallelizableWith and recompute BlockedBy. For cycle prevention, checking whether any proposed dependency already reaches the edited ticket is enough because only the edited ticket outgoing edges change.
