# Handoff: confirm Machine Time's all-time CPU

## Assignment

In #157's last two probe runs, Machine Time's all-time view took about twice
the CPU of the legacy `/aggregations` route, with about the same wall time.
Earlier runs measured 1.0–1.3×, so this is probably machine load. Confirm it,
or find and fix the cause.

## Evidence

- **The probe.** `TestUsagePerformanceMachineTime` in `internal/archive/usage_performance_test.go` logs the warm median latency and process CPU of each Machine Time period, all time included. It needs a `tools/dev.py` snapshot: see the file's header.
- **The baseline.** The legacy route was deleted in #157. To compare, run the same request on a checkout from before #157 (`77d8d34`), on the same snapshot and an idle machine.
- **Not yet run since.** The Usage filtered-metrics work (QT-16) left this open: no dev snapshot was available on the machine that did it.

## Done when

- The PR records both CPU readings for all time, taken back to back on an idle machine.
- If the current path takes more than about 1.3× the legacy CPU, it is fixed, or the PR records why that cost is inherent.
