#!/bin/bash
# Cross-build the executable package's test and run it without timezone files.
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
cd "$ROOT"
TEST_GOARCH="${GOARCH:-amd64}"
BUILD_DIR=$(mktemp -d "${TMPDIR:-/tmp}/pharos-timezones.XXXXXX")
IMAGE="pharos-timezones-test:$$"
IMAGE_BUILT=0
cleanup() {
    rm -rf "$BUILD_DIR"
    if [ "$IMAGE_BUILT" -eq 1 ]; then
        docker image rm "$IMAGE" >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT

CGO_ENABLED=0 GOOS=linux GOARCH="$TEST_GOARCH" go test -c -o "$BUILD_DIR/pharos.test" ./cmd/pharos
echo "Cross-built cmd/pharos test binary for linux/$TEST_GOARCH (CGO_ENABLED=0)."
if ! command -v docker >/dev/null || ! docker info >/dev/null 2>&1; then
    echo "Isolated timezone verification pending: a running Linux Docker daemon is required." >&2
    exit 1
fi

cp cmd/pharos/testdata/Dockerfile "$BUILD_DIR/Dockerfile"
docker build --network none --platform "linux/$TEST_GOARCH" -t "$IMAGE" "$BUILD_DIR"
IMAGE_BUILT=1
docker run --rm --network none --read-only --platform "linux/$TEST_GOARCH" "$IMAGE"
