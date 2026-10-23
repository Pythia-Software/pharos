#!/bin/sh
# Uses only the public key. Safe on development Macs; never calls sign_update.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mkdir -p "$ROOT/build"
TOOL="$ROOT/build/verify-update"
if [ ! -x "$TOOL" ] || [ "$ROOT/macos/UpdateSignature.swift" -nt "$TOOL" ] || [ "$ROOT/macos/verify-update.swift" -nt "$TOOL" ]; then
    WORK=$(mktemp -d "$ROOT/build/verify-update.XXXXXX")
    trap 'rm -rf "$WORK"' EXIT
    swiftc -parse-as-library "$ROOT/macos/UpdateSignature.swift" "$ROOT/macos/verify-update.swift" -o "$WORK/verify-update"
    mv "$WORK/verify-update" "$TOOL"
fi
exec "$TOOL" "$@"
