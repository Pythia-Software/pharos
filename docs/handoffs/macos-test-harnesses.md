# Handoff: repair or retire the manual macOS harnesses

## Assignment

The manual harnesses in `macos/test/` are not run by any test tier, and some
have drifted. Make each one either work and be documented, or delete it. They
stay manual and opt-in: they build the app, compile Swift and mount disk
images.

## Evidence

| Harness | Problem |
|---|---|
| `adopt-library.sh` | Broken by default. `REF=${1:-euclid/baseline}`, but no `euclid/baseline` ref exists in this repository, and it builds `./cmd/alexandria`, which exists only before the rename (≤ `e8cd2f2`). It tests adopting a pre-release per-user install. |
| `menu-bar.sh` + `MenuBarHarness.swift` | Possibly stale. Last changed 2026-10-13 (`b010730`), but `macos/MenuBar.swift` changed on 2026-10-15 (`55169ef`, #124/#127). |
| `UpdateCheckHarness.swift` | No script; it runs only by following its comment header (`swiftc … && /tmp/pharos-update-check`). |
| `swift-harness.sh` + `Harness.swift` | Runs `macos/build-app.sh`, which runs `npm ci` in `web/` and replaces `web/node_modules` underneath any JS tests running at the same time. It also creates and attaches a 300 MB disk image. |
| `eject-release.sh` | Works as documented. Check it still does. |

## Approach

- **`adopt-library.sh`.** Either point it at a real pre-host-schema commit and build `./cmd/pharos` at that commit, or delete it. Check with the owner whether pre-release per-user installs still need adopting. The Go adopt tests in `internal/archive/adopt_test.go` cover the adoption logic.
- **The light harnesses.** Run `menu-bar.sh`, fix what drifted, and add a script for the update check. A single `macos/test/run-light.sh` that runs both, with no disk images, would make them easy to run when touching `MenuBar.swift` or `UpdateChecker.swift`.
- **`swift-harness.sh`.** Make it stop disturbing `web/node_modules`, for example by building from a temporary copy, or by skipping `npm ci` when the lockfile and installed tree already match.
- **Docs.** List the harnesses in `docs/configuration.md` and `AGENTS.md`'s "Manual only" line: what each covers, when to run it, and what it needs (macOS, disk images, Xcode Command Line Tools).

## Done when

- Every remaining harness runs from its documented command on a current checkout, and each deleted one is gone with its references.
- Running the Swift harness no longer breaks concurrent `npm --prefix web test` runs.
