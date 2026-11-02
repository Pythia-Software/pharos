#!/bin/sh
# Download the pinned ONNX Runtime release that cmd/pharos-labeler-ort loads at
# run time (lib/libonnxruntime.dylib). Prints the extracted directory. Never
# commit runtime binaries; build/ is ignored.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VERSION=$(plutil -extract version raw "$ROOT/macos/onnxruntime.json")
URL=$(plutil -extract url raw "$ROOT/macos/onnxruntime.json")
SHA=$(plutil -extract sha256 raw "$ROOT/macos/onnxruntime.json")
CACHE="$ROOT/build/onnxruntime-$VERSION"
if [ -f "$CACHE/.verified-$SHA" ]; then
    echo "$CACHE"
    exit 0
fi
mkdir -p "$ROOT/build"
WORK=$(mktemp -d "$ROOT/build/onnxruntime.XXXXXX")
trap 'rm -rf "$WORK"' EXIT
curl --fail --location --silent --show-error --retry 3 "$URL" -o "$WORK/onnxruntime.tgz"
ACTUAL=$(shasum -a 256 "$WORK/onnxruntime.tgz" | awk '{print $1}')
if [ "$ACTUAL" != "$SHA" ]; then
    echo "ONNX Runtime $VERSION checksum mismatch." >&2
    exit 1
fi
# The archive holds one top-level directory (onnxruntime-osx-arm64-VERSION);
# strip it so the library is at $CACHE/lib.
mkdir "$WORK/distribution"
tar -xzf "$WORK/onnxruntime.tgz" -C "$WORK/distribution" --strip-components 1
if [ ! -f "$WORK/distribution/include/onnxruntime_c_api.h" ]; then
    echo "ONNX Runtime $VERSION archive has an unexpected layout." >&2
    exit 1
fi
touch "$WORK/distribution/.verified-$SHA"
rm -rf "$CACHE"
mv "$WORK/distribution" "$CACHE"
echo "$CACHE"
