#!/usr/bin/env bash
# Spin up a fresh `tickets_please serve` for the e2e suite. We point
# DATA_DIR at a per-run tempdir so the tests don't disturb the real
# data root, and listen on the dedicated test port.
#
# Critical: we change cwd to the tempdir before exec — the server
# eager-mounts a `.tickets_please/` directory in cwd if one is present,
# and the repo root has one (the dogfood project), which would surface
# in the e2e UI as a pre-existing project and break the "from empty"
# walkthrough.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"

# Build a fresh binary at the repo's known location (matches the systemd
# unit so we don't ship a stale e2e binary into the repo).
( cd "$REPO" && go build -o ./tickets_please ./cmd/tickets_please )

TMPDIR="$(mktemp -d -t tickets_please_e2e_XXXXXX)"
echo "e2e: data root = $TMPDIR" >&2

cd "$TMPDIR"
exec "$REPO/tickets_please" serve --addr 127.0.0.1:18900 --data-root "$TMPDIR"
