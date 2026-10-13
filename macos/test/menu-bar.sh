#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
swiftc -parse-as-library -framework AppKit -framework WebKit \
    "$ROOT/macos/MenuBar.swift" "$ROOT/macos/test/MenuBarHarness.swift" -o "$WORK/MenuBarHarness"
"$WORK/MenuBarHarness" "$@"
