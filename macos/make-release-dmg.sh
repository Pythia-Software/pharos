#!/bin/sh
# Package a signed Pharos.app in a Finder disk image with manual update steps.
# Usage: macos/make-release-dmg.sh APP OUTPUT.dmg X.Y.Z
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
APP=${1:-}
OUTPUT=${2:-}
VERSION=${3:-}
if [ "$#" -ne 3 ] || [ ! -d "$APP" ] || [ -e "$OUTPUT" ]; then
    echo "Usage: $0 APP OUTPUT.dmg X.Y.Z (OUTPUT must not exist)" >&2
    exit 2
fi
for command in hdiutil tiffutil python3 swiftc xcrun; do
    command -v "$command" >/dev/null 2>&1 || { echo "$command is required." >&2; exit 2; }
done
python3 "$ROOT/macos/build_dmg.py" --check

VOLUME="v$VERSION Pharos"
# Finder records the background by its mounted path; a second volume with the
# same name would be mounted as "$VOLUME 1".
if [ -e "/Volumes/$VOLUME" ]; then
    echo "A volume named $VOLUME is already mounted. Eject it before building this disk image." >&2
    exit 1
fi

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
/bin/sh "$ROOT/macos/swift-toolchain.sh" compile "$ROOT/macos/make-update-background.swift" -o "$WORK/make-update-background"
"$WORK/make-update-background" "$WORK/background" "$VERSION"
tiffutil -cathidpicheck "$WORK/background.png" "$WORK/background@2x.png" \
    -out "$WORK/background.tiff"

# Write the .DS_Store directly instead of scripting Finder, so the
# layout does not pick up the release Mac's Finder preferences, such as tabs.
python3 "$ROOT/macos/build_dmg.py" "$APP" "$OUTPUT" "$VERSION" "$WORK/background.tiff"
