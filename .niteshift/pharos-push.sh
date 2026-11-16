#!/bin/sh
# Installs the pharos CLI if it is missing, then pushes transcripts as they
# change. Run by the pharos-push service in services.yaml.
set -eu
# A failure (no network, access not granted) waits before the service is
# restarted, so a broken setup retries every few minutes, not in a loop.
trap 'status=$?; [ "$status" -eq 0 ] || sleep 300; exit "$status"' EXIT
here=$(cd "$(dirname "$0")" && pwd)
bucket=pythia-pharos-push-test
audience=$(jq -r '.destination.identity_token.command[-1]' "$here/pharos-push.json")
bin="$HOME/.pharos/bin/pharos"

if [ ! -x "$bin" ]; then
  mkdir -p "$(dirname "$bin")"
  token=$(ns oidc-token --audience "$audience")
  access=$(curl -fsS https://sts.googleapis.com/v1/token \
    --data-urlencode grant_type=urn:ietf:params:oauth:grant-type:token-exchange \
    --data-urlencode requested_token_type=urn:ietf:params:oauth:token-type:access_token \
    --data-urlencode subject_token_type=urn:ietf:params:oauth:token-type:jwt \
    --data-urlencode scope=https://www.googleapis.com/auth/devstorage.read_only \
    --data-urlencode audience="$audience" \
    --data-urlencode subject_token="$token" | jq -r .access_token)
  curl -fsS -H "Authorization: Bearer $access" -o "$bin.tmp" \
    "https://storage.googleapis.com/storage/v1/b/$bucket/o/bin%2Fpharos-linux-$(uname -m)?alt=media"
  chmod +x "$bin.tmp"
  mv "$bin.tmp" "$bin"
fi

# With no arguments it watches; `sh .niteshift/pharos-push.sh --check` or
# `--status` runs that instead.
if [ $# -eq 0 ]; then
  set -- --watch
fi
"$bin" capture-push --config "$here/pharos-push.json" "$@"
