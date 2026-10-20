# Handoff: run the Go tests in parallel

## Assignment

The Go suite runs strictly one test at a time, so its wall time is the sum of
688 mostly small tests. Make tests able to run in parallel, safely. Then
measure how far that takes `go test ./...` and `go test -short ./...`. Treat
the targets below as goals to validate, not promises.

## Evidence

Measured at the top of the test stack (#157), 2026-10-19:

| | Quiet machine (load ≈10) | Loaded machine (load ≈60) |
|---|---|---|
| `go test ./...` | 71 s | 86 s |
| `go test -short ./...` | 51 s | 83 s |
| Compiling the test binary after an edit | about 6–7 s | |

- **The shape of the suite.** Of 688 tests, 403 take under 50 ms. The 15 slowest take 40% of the time and the 50 slowest take 60%.
- **The time is a long serial tail.** With tests in parallel, wall time could approach compile time plus the slowest tests.
- **Why there is no parallelism today.**
  - No test calls `t.Parallel()`.
  - 42 test files mutate package-level state:
    - host detection (`useHost` / `currentHost`);
    - `parseHook`, `syncAdapter`, `captureFault`;
    - `driveTool`, `githubCLI`, `probeShellEnv`;
    - `catalogDurability`, `time.Local`;
    - limits lowered with `override(t, &limit, value)`.
  - Packages do run in parallel, but almost everything is in `internal/archive`.

## Approach

Choose with evidence, and say why:

1. **Seams per catalog or server, then `t.Parallel()`.**
   - Move the seams tests mutate onto the `Catalog`/`Server`, or behind an injectable value, so a test sets them for itself.
   - Mark tests that touch no process-wide state with `t.Parallel()`. `harness_test.go`'s helpers (`testCatalog`, `testServer`, `override`) are the natural place to enforce this. For example, `override` could refuse to run in a parallel test.
   - `t.Setenv` and `t.Chdir` already refuse parallel tests.
2. **Split the package.** The cheapest first leaf is the parsers and record types: about 16k lines that never touch the catalog, with roughly 175 tests that need none. Separate packages run in parallel and recompile less after an edit. `docs/testing-survey/go-archive-a-m.md` §4 and `go-archive-n-z.md` §4 map the clusters and the import-cycle risks: 80 files define `*Catalog` methods, and `Initialize` calls into findings and TL1.

Option 1 is likely the smaller change for the larger win. Option 2 also shortens the edit–compile loop.

## Constraints

- **Keep the guarantees.** Keep every data-safety test, and keep `TestMain`'s guarantees: no `ioreg`/`scutil`/`diskutil`, no user git configuration, no drive flushes.
- **No new flakes.** Run the suite many times with `-count` and with `-race` on the parallel subset, and fix any shared-state races you find rather than serialising them away.
- **Update `AGENTS.md`** with the rule new tests must follow.

## Done when

- Before and after wall times for both tiers are recorded on the same machine.
- `go test ./...` and `go test -short ./...` pass reliably, for example 10 consecutive runs.
- `go test -race` passes for the parallel tests.
