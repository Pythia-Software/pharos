#!/bin/bash
# Prepare a fresh checkout for Go builds/tests and UI development.
set -eo pipefail
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"
case "${1:-}" in
    "") VERIFY=0 ;;
    --check) VERIFY=1 ;;
    *) echo "Usage: tools/setup.sh [--check]" >&2; exit 2 ;;
esac

# Conductor runs setup in a non-interactive shell. Use an installed nvm when
# PATH still selects an older Node, without downloading a toolchain implicitly.
node_ready() { command -v node >/dev/null && node -e 'process.exit(Number(process.versions.node.split(".")[0]) >= 24 ? 0 : 1)'; }
if ! node_ready; then
    NVM_SCRIPT="${NVM_DIR:-$HOME/.nvm}/nvm.sh"
    if [ -s "$NVM_SCRIPT" ]; then
        . "$NVM_SCRIPT" --no-use
        nvm use --silent "$(cat .nvmrc)" || true
    fi
fi
if ! node_ready || ! command -v npm >/dev/null; then
    echo "Pharos source builds require Node 24 LTS and npm. Install the version in .nvmrc, then rerun tools/setup.sh." >&2
    exit 1
fi

# Always reinstall: old worktree links or modified packages must not leak into
# a release or branch update. npm ci verifies the lockfile's integrity hashes.
npm --prefix web ci --no-audit --no-fund
npm --prefix web run check-schemas
npm --prefix web run build
if [ "$VERIFY" -eq 1 ]; then
    npm --prefix web run check
    GOWORK=off go test -short ./...
fi
