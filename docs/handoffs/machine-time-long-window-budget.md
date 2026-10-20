# Handoff: make Machine Time work over 90 days on a large library

## Assignment

Machine Time's 90-day view asks for more groups than its 50,000-group budget
allows on a large library, and shows an error instead of data. First find out
how often that happens on real data. Then make every Machine Time period (7,
14, 30 and 90 days and all time) render on a library at least as large as the
user's.

## Evidence

- **The probe.** Before #157 removed the legacy `/aggregations` route, it was compared with `/metrics` on a synthetic catalog of 300,000 tool calls over 120 days with many repositories, operations and MCP servers. The 90-day fact cube had more than 50,000 groups and returned the budget error on **both** routes. So this is not a regression from #157; the legacy route failed the same way.
- **Other periods are fine.** 7 days (about 4,900 groups), 30 days (about 21,400) and all time (about 18,200) fit the budget. All time fits because it groups by a coarser time unit.
- **Unknown for real data.** The synthetic distribution may be more varied than real data. Real group counts have not been measured.
- **No fallback.**
  - `web/src/machine-time.tsx` retries on `/query-v3` only when `/metrics` reports the generic budget. Machine Time's own budget message is worded differently (`web/src/metric-request.ts`, `groupBudget`).
  - `/query-v3` caps groups at 10,000 anyway.

  The user therefore sees the error.

## Where to look

- **Server.** `internal/archive/machine_time_queries.go` has the `/metrics` accelerator (`machineTimeMetricsV2`), `machineTimeMaxGroups = 50_000`, and its budget error. It reads the tool-call projection in `tool_cube.go`.
- **Requests.** `web/src/machine-activity-data.ts` builds the period fact cubes: 8 keys, for the current and previous period. `web/src/machine-time.tsx` sends them.
- **Probe.** `internal/archive/usage_performance_test.go` (`-tags probe`) has the Machine Time probe for measuring a real catalog. Use an isolated dev snapshot (`tools/dev.sh`, see `docs/technical-overview.md`), never the production library.

## Approach

Measure first. Run the probe on a dev snapshot of the user's library and record the group count and latency for every period.

If 90 days exceeds the budget, choose with the owner among:
- a coarser time grain for long windows, as all time already uses;
- fewer key dimensions in the cube, with drill-down for the rest;
- a larger budget, if memory and latency stay acceptable.

Report the trade-off rather than picking silently, because it changes what the page shows.

## Done when

- Every Machine Time period renders on the dev snapshot within a latency you report: before and after, warm median and CPU.
- A Go test lowers `machineTimeMaxGroups` with `override` to cover the long-window path, with no large fixture.
- If the response changes shape, the unit tests in `web/tests/unit/activity/` cover the UI's handling.
- `go test ./...`, `npm --prefix web run check` and `npm --prefix web run test:browser` pass.
