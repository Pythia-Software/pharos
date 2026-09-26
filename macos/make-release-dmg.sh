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
for command in hdiutil ditto osascript swift; do
    command -v "$command" >/dev/null 2>&1 || { echo "$command is required." >&2; exit 2; }
done

WORK=$(mktemp -d)
RW="$WORK/layout.dmg"
STAGE="$WORK/stage"
VOLUME="Pharos v$VERSION"
mounted=0
MOUNT=
cleanup() {
    if [ "$mounted" -eq 1 ]; then
        hdiutil detach "$MOUNT" -quiet || hdiutil detach -force "$MOUNT" -quiet || true
    fi
    rm -rf "$WORK"
}
trap cleanup EXIT
mkdir -p "$STAGE/.background"
ditto "$APP" "$STAGE/Pharos.app"
swift "$ROOT/macos/make-update-background.swift" "$STAGE/.background/Update Guide.png" "$VERSION"
hdiutil create -quiet -srcfolder "$STAGE" -volname "$VOLUME" -format UDRW "$RW"
MOUNT=$(hdiutil attach -readwrite -noautoopen "$RW" | awk -F '\t' '/\/Volumes\// { print $NF; exit }')
[ -n "$MOUNT" ] || { echo "Could not find the disk image's mount point." >&2; exit 1; }
mounted=1

# Finder writes the view options and icon position into the image's .DS_Store.
# The background stays hidden as a normal file inside the mounted image.
osascript - "$VOLUME" "$MOUNT/.background/Update Guide.png" <<'APPLESCRIPT'
on run argv
    set volumeName to item 1 of argv
    set backgroundPath to item 2 of argv
    tell application "Finder"
        set volumeDisk to disk volumeName
        open volumeDisk
        set imageWindow to container window of volumeDisk
        set current view of imageWindow to icon view
        set bounds of imageWindow to {120, 120, 1020, 680}
        set toolbar visible of imageWindow to false
        set statusbar visible of imageWindow to false
        set viewOptions to icon view options of imageWindow
        set arrangement of viewOptions to not arranged
        set icon size of viewOptions to 112
        set background picture of viewOptions to (POSIX file backgroundPath) as alias
        set position of item "Pharos.app" of imageWindow to {160, 295}
        close imageWindow
    end tell
end run
APPLESCRIPT

sync
hdiutil detach "$MOUNT" -quiet
mounted=0
hdiutil convert -quiet "$RW" -format UDZO -imagekey zlib-level=9 -o "$OUTPUT"
hdiutil verify -quiet "$OUTPUT"
