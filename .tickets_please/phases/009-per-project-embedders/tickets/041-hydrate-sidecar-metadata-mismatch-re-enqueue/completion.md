## Testing evidence
make build clean; go test -race all packages green. hydrate_test.go covers wrong-provider, wrong-model, legacy flat-array, cold-clone.

## Work summary
staleSidecar predicate, dropStaleSidecar helper, three-branch switch over read err. ProjectMount.EmbedModel already existed from W2-T1.

## Learnings
ProjectMount.EmbedModel already exists from W2-T1; spec hint was stale. Hand-seeded sidecars in tests must match mount.EmbedModel or get evicted as stale on hydrate. Decoder-error fallthrough cleanly absorbs legacy flat-array sidecars without back-compat code.
