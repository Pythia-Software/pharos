#!/bin/sh
# Tests the app's DiskArbitration handling and the Eject button's
# release, stop, and eject, without launching Pharos: macos/test/Harness.swift
# is built with macos/LibraryVolume.swift and macos/Launch.swift.
#
#   macos/test/swift-harness.sh [PORT]
#
# It builds Pharos.app into a temporary directory (never launched), creates
# and attaches its own disk image (/Volumes/PharosTest-*), serves a test
# library on PORT (default 18768), and uses a temporary support directory in
# place of ~/Library/Application Support/Pharos.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
PORT=${1:-18768}
case "$PORT" in 8765|8766) echo "Use a port other than 8765/8766." >&2; exit 2 ;; esac
WORK=$(mktemp -d)
# Every pharos run below (and the service the harness starts) writes
# per-Mac files here, never in ~/Library/Application Support/Pharos.
export PHAROS_SUPPORT_DIR="$WORK/service-support"
NAME="PharosTest-$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')"
DEV=
FAKE=
cleanup() {
    if [ -n "$DEV" ]; then hdiutil detach -force "$DEV" >/dev/null 2>&1 || true; fi
    if [ -n "$FAKE" ]; then { kill "$FAKE"; wait "$FAKE"; } 2>/dev/null || true; fi
    rm -rf "$WORK"
}
trap cleanup EXIT

echo "Building Pharos.app (not launched)…"
"$ROOT/macos/build-app.sh" "$WORK/real" >"$WORK/build.log" 2>&1 || { cat "$WORK/build.log" >&2; exit 1; }
REAL="$WORK/real/Pharos.app"

echo "Building the harness…"
swiftc -parse-as-library -framework DiskArbitration -framework Security \
    "$ROOT/macos/test/Harness.swift" "$ROOT/macos/LibraryVolume.swift" "$ROOT/macos/Launch.swift" \
    -o "$WORK/harness"

hdiutil create -quiet -size 300m -fs APFS -volname "$NAME" "$WORK/$NAME.dmg"
attached=$(hdiutil attach -nobrowse "$WORK/$NAME.dmg")
DEV=$(echo "$attached" | awk 'NR == 1 {print $1}')
MOUNT=$(echo "$attached" | awk -F'\t' '/\/Volumes\//{print $NF}')
case "$MOUNT" in "/Volumes/$NAME"*) ;; *) echo "unexpected mount point '$MOUNT'" >&2; exit 1 ;; esac
CLI="$REAL/Contents/MacOS/pharos"
"$CLI" init-library "$MOUNT/Pharos" >/dev/null
CONFIG="$MOUNT/Pharos/library.toml"
sed -i '' "s/^port = 8766$/port = $PORT/" "$CONFIG"
TOKEN=$(sed -n 's/^api_token = "\(.*\)"$/\1/p' "$CONFIG")
UUID=$(diskutil info -plist "$MOUNT" | plutil -extract VolumeUUID raw -)
VOLUME_DEV=$(diskutil info -plist "$MOUNT" | plutil -extract DeviceIdentifier raw -)

status=0
echo "== service"
# A service still finishing a write answers a release with 503.
python3 - "$WORK/stopping-port" <<'EOF' &
import http.server, os, sys
class Stopping(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = b'{"released":false,"error":"Pharos is finishing a write; try again in a moment"}'
        self.send_response(503)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *args): pass
server = http.server.HTTPServer(("127.0.0.1", 0), Stopping)
open(sys.argv[1] + ".tmp", "w").write(str(server.server_address[1]))
os.rename(sys.argv[1] + ".tmp", sys.argv[1])
server.serve_forever()
EOF
FAKE=$!
tries=0
until [ -s "$WORK/stopping-port" ]; do tries=$((tries + 1)); [ "$tries" -lt 100 ] || { echo "fake service did not start" >&2; exit 1; }; sleep 0.1; done
"$WORK/harness" service "http://127.0.0.1:$(cat "$WORK/stopping-port")/" || status=1
echo "== library volume"
"$WORK/harness" volume "$UUID" "$MOUNT" "$VOLUME_DEV" "$CLI" "$CONFIG" "$PORT" "$TOKEN" "$DEV" || status=1
echo "== eject button"
# The volume tests end by force-detaching the image; attach it again.
hdiutil detach -force "$DEV" >/dev/null 2>&1 || true
attached=$(hdiutil attach -nobrowse "$WORK/$NAME.dmg")
DEV=$(echo "$attached" | awk 'NR == 1 {print $1}')
MOUNT=$(echo "$attached" | awk -F'\t' '/\/Volumes\//{print $NF}')
case "$MOUNT" in "/Volumes/$NAME"*) ;; *) echo "unexpected mount point '$MOUNT'" >&2; exit 1 ;; esac
# The library's app, on the drive as users install it.
cp -R "$REAL" "$MOUNT/Pharos/"
if "$WORK/harness" eject "$MOUNT" "$MOUNT/Pharos/Pharos.app" "$MOUNT/Pharos/library.toml" "$PORT" "$TOKEN"; then
    DEV= # ejected
else
    status=1
fi
exit $status
