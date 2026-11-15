#!/bin/sh
# Apple's /usr/bin/python3 launcher supplies SDKROOT=MacOSX.sdk when unset.
# Resolve the intended SDK in the shell before Python can inject that default.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ -z "${SDKROOT:-}" ]; then
    SDKROOT=$(xcrun --sdk macosx26.5 --show-sdk-path)
fi
export SDKROOT
exec python3 "$ROOT/macos/swift_toolchain.py" "$@"
