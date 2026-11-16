#!/bin/sh
# Build the pharos command-line program for Linux, for machines and sandboxes
# that push their transcripts with `pharos capture-push`.
# Usage: macos/build-linux-cli.sh 0.3.0 OUTPUT_DIRECTORY
#
# Writes pharos-linux-x86_64 and pharos-linux-aarch64, each with a .sha256
# beside it. The names carry no version and match `uname -m`, so one link
# always fetches the newest release:
#   https://github.com/Pythia-Software/pharos/releases/latest/download/pharos-linux-$(uname -m)
#
# Go embeds the frontend, so build it first (tools/setup.sh or
# macos/build-app.sh). The program is built without cgo and runs without any
# shared library. Only capture-push is supported on Linux.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VERSION=${1:-}
OUT=${2:-}
if [ "$#" -ne 2 ] || ! printf '%s\n' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "Usage: $0 major.minor.patch OUTPUT_DIRECTORY" >&2
    exit 2
fi
mkdir -p "$OUT"
OUT=$(CDPATH= cd -- "$OUT" && pwd)
LDFLAGS="-s -w -X github.com/gbdubs/pharos/internal/archive.pushVersion=$VERSION"
for pair in amd64:x86_64 arm64:aarch64; do
    goarch=${pair%%:*}
    name=pharos-linux-${pair##*:}
    echo "Building the Linux command-line program ($goarch)…"
    (cd "$ROOT" && GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 \
        go build -trimpath -ldflags="$LDFLAGS" -o "$OUT/$name" ./cmd/pharos)
    # The first bytes are the ELF magic and class; a wrong target would
    # otherwise be found only inside a sandbox.
    if [ "$(head -c 4 "$OUT/$name" | od -An -c | tr -d ' ')" != '177ELF' ]; then
        echo "$name is not a Linux executable." >&2
        exit 1
    fi
    (cd "$OUT" && shasum -a 256 "$name" > "$name.sha256")
done
