## Testing evidence
make build clean. go test race green. Four new tests cover wipe/rebuild, UpdateProject auto-trigger, two-mount different dims, rebuild at new dim.

## Work summary
ReembedProject mirrors DeleteProject prelude. rebuildMountEmbedAssets atomically swaps the bundle on yaml drift. Worker Flush before sidecar wipe walk. UpdateProject extracts updateProjectLocked then calls ReembedProject after the lock release.

## Learnings
UpdateProject inline ReembedProject call deadlocks because ReembedProject re-acquires Cache.Get RLock under the existing write lock. Solution: extract updateProjectLocked and call ReembedProject after the lock is released. mountsMu held only briefly during the atomic bundle swap. Flush-before-tree-removal generalizes once more to the sidecar wipe walk inside ReembedProject.
