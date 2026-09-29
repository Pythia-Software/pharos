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
for command in hdiutil tiffutil swift uvx; do
    command -v "$command" >/dev/null 2>&1 || { echo "$command is required." >&2; exit 2; }
done

VOLUME="v$VERSION Pharos"
# dmgbuild records the background by its mounted path; a second volume with the
# same name would be mounted as "$VOLUME 1".
if [ -e "/Volumes/$VOLUME" ]; then
    echo "A volume named $VOLUME is already mounted. Eject it before building this disk image." >&2
    exit 1
fi

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
swift "$ROOT/macos/make-update-background.swift" "$WORK/background" "$VERSION"
tiffutil -cathidpicheck "$WORK/background.png" "$WORK/background@2x.png" \
    -out "$WORK/background.tiff"

# dmgbuild writes the .DS_Store directly instead of scripting Finder, so the
# layout does not pick up the release Mac's Finder preferences, such as tabs.
# Pinned: 1.6.7 writes a background alias that macOS 14 and later resolve.
uvx --quiet --from dmgbuild==1.6.7 dmgbuild -s "$ROOT/macos/dmg-settings.py" \
    -D app="$APP" -D background="$WORK/background.tiff" "$VOLUME" "$OUTPUT"
hdiutil verify -quiet "$OUTPUT"
