#!/bin/sh
# Refresh /Volumes/euclid/pharos-dev from production, then run this checkout.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec python3 "$ROOT/tools/dev.py" "$@"
