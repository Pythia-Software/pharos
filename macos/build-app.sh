#!/bin/sh
# Build Pharos.app into OUTDIR (default: dist/) and code-sign it.
#
# Environment:
#   PHAROS_CODESIGN_IDENTITY  Keychain identity to sign with (name or SHA-1).
#                             Unset: ad-hoc signing, whose identity changes
#                             with the code. See macos/create-signing-identity.sh.
#   PHAROS_HARDENED_RUNTIME   Set to 0 to sign without the hardened runtime
#                             (e.g. to attach a debugger). Default: 1.
#   PHAROS_UNIVERSAL          Set to 1 to build arm64 + x86_64. Default: native.
#   PHAROS_VERSION            Numeric major.minor.patch bundle version. Default: 0.2.0.
#   PHAROS_CODESIGN_TIMESTAMP Set to 1 for a secure signing timestamp (required
#                             for Developer ID notarization). Default: 0.
#   PHAROS_ENABLE_UPDATES     Set to 1 only for Developer ID releases. Default: 0.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ "${PHAROS_ENABLE_UPDATES:-0}" = 1 ]; then
    python3 "$ROOT/macos/release_support.py"
fi
OUT=${1:-"$ROOT/dist"}
APP="$OUT/Pharos.app"
BUNDLE_ID=local.pharos
SERVICE_ID=local.pharos.service
MACOS_MIN=14.0
VERSION=${PHAROS_VERSION:-0.2.0}
case "$VERSION" in
    *[!0-9.]*|.*|*.|*..*) echo "PHAROS_VERSION must be major.minor.patch (numeric)." >&2; exit 2 ;;
esac
if ! printf '%s\n' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "PHAROS_VERSION must be major.minor.patch (numeric)." >&2
    exit 2
fi
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
# A local rebuild can reuse dist/Pharos.app from before the CLI and wrapper
# were renamed. Keep the bundle free of obsolete executables.
rm -f "$APP/Contents/MacOS/alexandria" "$APP/Contents/MacOS/AIWorkArchive"
mkdir -p "$ROOT/internal/archive/assets"
# The embedded schema and UI are the source of truth.
for name in schema.sql ui.py; do
    if [ ! -f "$ROOT/internal/archive/assets/$name" ]; then
        echo "internal/archive/assets/$name is missing." >&2
        exit 1
    fi
done
# Reinstall pinned packages and build the ignored frontend before embedding it.
"$ROOT/tools/setup.sh"
SPARKLE=$("$ROOT/macos/fetch-sparkle.sh")
mkdir -p "$APP/Contents/Frameworks"
rm -rf "$APP/Contents/Frameworks/Sparkle.framework"
ditto "$SPARKLE/Sparkle.framework" "$APP/Contents/Frameworks/Sparkle.framework"

if [ "${PHAROS_UNIVERSAL:-0}" = 1 ]; then
    GOARCHES="arm64 amd64"
else
    GOARCHES=$(go env GOARCH)
fi
# Cross-compiling disables cgo by default; keep every slice built the same way.
CGO=$(go env CGO_ENABLED)
ARCHS=
for goarch in $GOARCHES; do
    case "$goarch" in
        arm64) arch=arm64 ;;
        amd64) arch=x86_64 ;;
        *) echo "Unsupported architecture for macOS: $goarch" >&2; exit 1 ;;
    esac
    ARCHS="$ARCHS $arch"
    echo "Building the Go service ($arch)…"
    (cd "$ROOT" && GOOS=darwin GOARCH="$goarch" CGO_ENABLED="$CGO" go build -o "$WORK/pharos-$arch" ./cmd/pharos)
    echo "Building the Swift wrapper ($arch)…"
    swiftc -parse-as-library -target "$arch-apple-macos$MACOS_MIN" -framework SwiftUI -framework WebKit \
        -framework DiskArbitration -framework Security -F "$SPARKLE" -framework Sparkle \
        -Xlinker -rpath -Xlinker '@executable_path/../Frameworks' \
        "$ROOT/macos/PharosApp.swift" "$ROOT/macos/LibraryVolume.swift" "$ROOT/macos/Launch.swift" \
        "$ROOT/macos/MenuBar.swift" \
        "$ROOT/macos/UpdateLifecycle.swift" "$ROOT/macos/UpdateNotice.swift" \
        -o "$WORK/PharosApp-$arch"
done
for name in pharos PharosApp; do
    target="$APP/Contents/MacOS/$name"
    # Replace rather than overwrite in place, so a running copy keeps its
    # mapped executable.
    rm -f "$target"
    if [ "${PHAROS_UNIVERSAL:-0}" = 1 ]; then
        lipo -create "$WORK/$name"-* -output "$target"
    else
        cp "$WORK/$name-$arch" "$target"
    fi
    # Verify slices individually: some lipo versions reject multiple arches
    # as multiple input files despite advertising a variadic -verify_arch.
    for expected_arch in $ARCHS; do
        if ! lipo "$target" -verify_arch "$expected_arch"; then
            echo "$target does not contain: $expected_arch" >&2
            exit 1
        fi
    done
    lipo -info "$target"
done

# Regenerate with macos/make-icon.sh after editing macos/AppIcon.svg.
cp "$ROOT/macos/AppIcon.icns" "$APP/Contents/Resources/AppIcon.icns"
# Keep license terms and the separately licensed Query Table notice with the app.
cp "$ROOT/LICENSE" "$APP/Contents/Resources/LICENSE"
mkdir -p "$APP/Contents/Resources/licenses"
cp "$ROOT/internal/querytable/LICENSE" "$APP/Contents/Resources/licenses/query-table-LICENSE"
cp "$SPARKLE/LICENSE" "$APP/Contents/Resources/licenses/Sparkle-LICENSE"
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>PharosApp</string>
<key>CFBundleIdentifier</key><string>$BUNDLE_ID</string>
<key>CFBundleName</key><string>Pharos</string>
<key>CFBundleDisplayName</key><string>Pharos</string>
<key>CFBundleIconFile</key><string>AppIcon</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>$VERSION</string>
<key>CFBundleVersion</key><string>$VERSION</string>
<key>LSMinimumSystemVersion</key><string>$MACOS_MIN</string>
</dict></plist>
PLIST
python3 - "$APP/Contents/Info.plist" "$ROOT/macos/sparkle.json" "${PHAROS_ENABLE_UPDATES:-0}" <<'PY'
import base64, json, plistlib, sys
path, config_path, enabled = sys.argv[1:]
if enabled not in ('0', '1'):
    raise SystemExit('PHAROS_ENABLE_UPDATES must be 0 or 1')
config = json.load(open(config_path))
public_key = config.get('public_key')
if public_key is not None and len(base64.b64decode(public_key, validate=True)) != 32:
    raise SystemExit('Invalid Sparkle public key')
if enabled == '1' and public_key is None:
    raise SystemExit('Configure the update public key on the deployment Mac first')
with open(path, 'rb') as source:
    info = plistlib.load(source)
info.update(SUFeedURL=config['feed_url'],
            SUVerifyUpdateBeforeExtraction=True, SURequireSignedFeed=True,
            SUAutomaticallyUpdate=False, SUEnableSystemProfiling=False,
            PharosUpdatesEnabled=enabled == '1')
if public_key is not None:
    info['SUPublicEDKey'] = public_key
if enabled == '0':
    info['SUEnableAutomaticChecks'] = False
with open(path, 'wb') as target:
    plistlib.dump(info, target)
PY

# macOS privacy grants (TCC), such as access to files on a removable volume,
# are keyed on the app's designated requirement. With a keychain identity that
# is the bundle identifier plus the certificate, so grants survive rebuilds; an
# ad-hoc signature pins the exact build (its cdhash). The service is launched
# by the wrapper, so macOS normally attributes its file access to Pharos.app.
IDENTITY=${PHAROS_CODESIGN_IDENTITY:-}
if [ -z "$IDENTITY" ]; then
    IDENTITY=-
    echo "Note: PHAROS_CODESIGN_IDENTITY is unset, so Pharos is ad-hoc signed; that signature changes whenever the code does, so macOS may ask for privacy permissions again after a rebuild."
fi
# Positional parameters double as the codesign argument list.
set -- --force --sign "$IDENTITY"
if [ -n "${PHAROS_CODESIGN_KEYCHAIN:-}" ]; then
    set -- "$@" --keychain "$PHAROS_CODESIGN_KEYCHAIN"
fi
case "${PHAROS_CODESIGN_TIMESTAMP:-0}" in
    0) set -- "$@" --timestamp=none ;;
    1) set -- "$@" --timestamp ;;
    *) echo "PHAROS_CODESIGN_TIMESTAMP must be 0 or 1." >&2; exit 2 ;;
esac
# The hardened runtime needs no entitlements here: WebKit's JIT runs in Apple's
# WebContent process, the Go service needs no JIT or unsigned memory, and
# launching it with Process() is unrestricted. It blocks DYLD_* injection and
# debugger attachment, making it harder for other local code to borrow Pharos's
# privacy grants.
if [ "${PHAROS_HARDENED_RUNTIME:-1}" != 0 ]; then
    set -- "$@" --options runtime
fi
# Sign nested code first; the bundle seal records its signature.
FRAMEWORK="$APP/Contents/Frameworks/Sparkle.framework"
for binary in "Versions/B/Sparkle" "Versions/B/Autoupdate" \
    "Versions/B/Updater.app/Contents/MacOS/Updater" \
    "Versions/B/XPCServices/Downloader.xpc/Contents/MacOS/Downloader" \
    "Versions/B/XPCServices/Installer.xpc/Contents/MacOS/Installer"; do
    for expected_arch in $ARCHS; do
        lipo "$FRAMEWORK/$binary" -verify_arch "$expected_arch"
    done
done
for code in "Versions/B/Autoupdate" "Versions/B/Updater.app" \
    "Versions/B/XPCServices/Downloader.xpc" "Versions/B/XPCServices/Installer.xpc"; do
    codesign "$@" --preserve-metadata=entitlements "$FRAMEWORK/$code"
done
codesign "$@" "$FRAMEWORK"
codesign "$@" --identifier "$SERVICE_ID" "$APP/Contents/MacOS/pharos"
if [ "${PHAROS_HARDENED_RUNTIME:-1}" != 0 ] && \
    ! codesign -dvv "$APP/Contents/MacOS/pharos" 2>&1 | grep -Eq '^Authority=(Apple Development|Developer ID Application):'; then
    # Ad hoc and self-signed development builds cannot pass library validation.
    # Developer ID and Apple Development builds retain full library validation.
    cat > "$WORK/debug.entitlements" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>
<key>com.apple.security.cs.disable-library-validation</key><true/>
</dict></plist>
PLIST
    set -- "$@" --entitlements "$WORK/debug.entitlements"
fi
codesign "$@" --identifier "$BUNDLE_ID" "$APP"
if [ "${PHAROS_ENABLE_UPDATES:-0}" = 1 ]; then
    if ! codesign -dvv "$APP" 2>&1 | grep -q 'Authority=Developer ID Application:'; then
        echo "Only Developer ID signed builds may enable production updates." >&2
        exit 1
    fi
fi
if ! codesign --verify --strict --deep --verbose=2 "$APP"; then
    echo "Code signature verification failed for $APP" >&2
    exit 1
fi
echo "Built $APP"
