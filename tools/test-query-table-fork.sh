#!/bin/sh
# Run upstream Query Table's Go tests, from the release Pharos forked, against
# the patched backend in third_party/query-table-go.
#
# go test ./... never reaches the fork: it is a nested module. And the
# published module has no V3 tests; those live in upstream's
# backends/sqlite-tests. This script is the guard for the local patches listed
# in third_party/query-table-go/README.pharos.md. Run it after changing the
# fork.
#
# It reads the release with git archive into a temporary directory:
#   backends/go           the fork's sources, with upstream's package tests;
#   backends/sqlite-tests upstream's suite, whose replace of backends/go
#                         points at ../go, which is now the fork;
#   schema                fixtures that tests in both read.
# Neither checkout changes.
#
# Usage: tools/test-query-table-fork.sh [go test flags, e.g. -run TestV3 -count=1]
#
# Environment:
#   QUERY_TABLE_UPSTREAM  Upstream checkout. Default: ~/gbdubs/query-table
#   QUERY_TABLE_TAG       Release the fork is based on. Default: v0.8.0
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
FORK="$ROOT/third_party/query-table-go"
UPSTREAM=${QUERY_TABLE_UPSTREAM:-$HOME/gbdubs/query-table}
TAG=${QUERY_TABLE_TAG:-v0.8.0}
MODULE=github.com/Pythia-Software/query-table/backends/go

fail() {
	echo "test-query-table-fork: $*" >&2
	exit 2
}

git -C "$UPSTREAM" rev-parse --git-dir >/dev/null 2>&1 ||
	fail "no Query Table checkout at $UPSTREAM; clone it there or set QUERY_TABLE_UPSTREAM"
git -C "$UPSTREAM" rev-parse -q --verify "refs/tags/$TAG" >/dev/null ||
	fail "$UPSTREAM has no tag $TAG; run git -C '$UPSTREAM' fetch --tags, or set QUERY_TABLE_TAG"

WORK=$(mktemp -d "${TMPDIR:-/tmp}/pharos-query-table-fork.XXXXXX")
trap 'rm -rf "$WORK"' EXIT INT TERM
git -C "$UPSTREAM" archive "$TAG" backends/go backends/sqlite-tests schema | tar -x -C "$WORK"

# Swap upstream's sources for the fork's, keeping upstream's tests.
find "$WORK/backends/go" -maxdepth 1 -type f ! -name '*_test.go' -exec rm {} +
for file in "$FORK"/*.go "$FORK/go.mod"; do
	case $file in
	*_test.go) ;;
	*) cp "$file" "$WORK/backends/go/" ;;
	esac
done
ls "$WORK"/backends/go/*_test.go >/dev/null 2>&1 || fail "$TAG has no tests in backends/go"
(cd "$WORK/backends/sqlite-tests" && go mod edit -replace "$MODULE=../go")

echo "Query Table $TAG tests against $FORK"
status=0
for suite in go sqlite-tests; do
	echo "== backends/$suite"
	(cd "$WORK/backends/$suite" && GOWORK=off go test "$@" ./...) || status=1
done
if [ "$status" -ne 0 ]; then
	echo "test-query-table-fork: upstream $TAG tests fail against the fork" >&2
fi
exit "$status"
