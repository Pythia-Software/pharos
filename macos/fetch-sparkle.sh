#!/bin/sh
# Download the pinned upstream distribution; never commit framework binaries.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VERSION=$(plutil -extract version raw "$ROOT/macos/sparkle.json")
SHA=$(plutil -extract sha256 raw "$ROOT/macos/sparkle.json")
CACHE="$ROOT/build/Sparkle-$VERSION"
if [ -f "$CACHE/.verified-$SHA" ]; then
    echo "$CACHE"
    exit 0
fi
mkdir -p "$ROOT/build"
WORK=$(mktemp -d "$ROOT/build/sparkle.XXXXXX")
trap 'rm -rf "$WORK"' EXIT
curl --fail --location --silent --show-error --retry 3 \
    "https://github.com/sparkle-project/Sparkle/releases/download/$VERSION/Sparkle-$VERSION.tar.xz" \
    -o "$WORK/sparkle.tar.xz"
ACTUAL=$(shasum -a 256 "$WORK/sparkle.tar.xz" | awk '{print $1}')
if [ "$ACTUAL" != "$SHA" ]; then
    echo "Sparkle $VERSION checksum mismatch." >&2
    exit 1
fi
mkdir "$WORK/distribution"
tar -xJf "$WORK/sparkle.tar.xz" -C "$WORK/distribution"
touch "$WORK/distribution/.verified-$SHA"
rm -rf "$CACHE"
mv "$WORK/distribution" "$CACHE"
echo "$CACHE"
