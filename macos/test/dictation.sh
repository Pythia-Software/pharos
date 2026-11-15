#!/bin/sh
# Manual native dictation checks: session lifecycle with fake recorders, then
# a spoken file (from `say`) transcribed on this Mac through the microphone's
# conversion path. No microphone, app build or service. macOS 26 transcribes;
# older systems run only the lifecycle checks. The first run may download the
# speech model for the current language.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

/bin/sh "$ROOT/macos/swift-toolchain.sh" compile -parse-as-library \
    "$ROOT/macos/Dictation.swift" "$ROOT/macos/test/DictationHarness.swift" -o "$WORK/DictationHarness"
say -o "$WORK/speech.aiff" "The lighthouse keeper walked down to the harbor before sunrise."
"$WORK/DictationHarness" "$WORK/speech.aiff"
