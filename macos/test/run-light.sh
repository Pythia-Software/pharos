#!/bin/sh
# Manual native menu and update checks; no app build, service or disk image.
# Requires macOS and Xcode Command Line Tools; the menu needs a GUI session.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

"$ROOT/macos/test/menu-bar.sh"
swiftc -parse-as-library "$ROOT/macos/UpdateChecker.swift" \
    "$ROOT/macos/test/UpdateCheckHarness.swift" -o "$WORK/UpdateCheckHarness"
"$WORK/UpdateCheckHarness"
