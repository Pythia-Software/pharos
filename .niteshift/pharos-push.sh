#!/bin/sh
# Installs the pharos command-line program if it is missing, then pushes
# transcripts as they change. Run by the pharos-push service in services.yaml.
set -eu
# A failure (no network, access not granted) waits before the service is
# restarted, so a broken setup retries every few minutes, not in a loop.
trap 'status=$?; [ "$status" -eq 0 ] || sleep 300; exit "$status"' EXIT
here=$(cd "$(dirname "$0")" && pwd)
bin="$HOME/.pharos/bin/pharos"
# To stay on one version, replace latest/download with download/v1.2.3.
release=https://github.com/Pythia-Software/pharos/releases/latest/download

if [ ! -x "$bin" ]; then
  mkdir -p "$(dirname "$bin")"
  name=pharos-linux-$(uname -m)
  curl -fsSL -o "$bin.tmp" "$release/$name"
  expected=$(curl -fsSL "$release/$name.sha256" | cut -d' ' -f1)
  [ "$(sha256sum "$bin.tmp" | cut -d' ' -f1)" = "$expected" ]
  chmod +x "$bin.tmp"
  mv "$bin.tmp" "$bin"
fi

# With no arguments it watches; `sh .niteshift/pharos-push.sh --check` or
# `--status` runs that instead.
if [ $# -eq 0 ]; then
  set -- --watch
fi
"$bin" capture-push --config "$here/pharos-push.json" "$@"
