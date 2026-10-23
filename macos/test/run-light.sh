#!/bin/sh
# Manual native menu/update checks; uses disposable children and stubbed HTTP.
# No app build, production service, release keys or disk image.
# Requires macOS and Xcode Command Line Tools; the menu needs a GUI session.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

"$ROOT/macos/test/menu-bar.sh"
SPARKLE=$("$ROOT/macos/fetch-sparkle.sh")
swiftc -parse-as-library -D PHAROS_NATIVE_TEST -F "$SPARKLE" -framework Sparkle \
    -Xlinker -rpath -Xlinker "$SPARKLE" -framework DiskArbitration -framework WebKit \
    "$ROOT/macos/PharosApp.swift" "$ROOT/macos/MenuBar.swift" "$ROOT/macos/UpdateNotice.swift" \
    "$ROOT/macos/LibraryVolume.swift" "$ROOT/macos/UpdateLifecycle.swift" "$ROOT/macos/Launch.swift" "$ROOT/macos/UpdateSignature.swift" \
    "$ROOT/macos/test/UpdateCheckHarness.swift" -o "$WORK/UpdateCheckHarness"
"$WORK/UpdateCheckHarness" "$ROOT/testdata/sparkle-signed-appcast.xml"
