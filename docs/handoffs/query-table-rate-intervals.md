# Query Table: intervals for rates and shares

For the Query Table team. This is a self-contained request against the
published **1.0.0** release (Go module `backends/go` v1.0.0 and the npm core,
react, ui and codegen packages at 1.0.0), dated **2026-11-06**. Pharos will
consume it through a published release; it does not patch Query Table locally.

## Context

Pharos is a Go service with a React frontend over an SQLite catalog of
coding-agent conversations. Its dashboards are Query Table metrics: the Go
backend answers `/query-v3`, and the UI's metric charts draw the buckets.

The new **Agent Events** dashboard compares how often agents report kinds of
events (an obstacle, a workaround, an assumption…) across breakdowns. Its
measures are ratios of distinct counts, for example:

- events per 1,000 messages read: `SUM([events])/(COUNT_DISTINCT([message_id])/1000)`;
- the share of turns with an event:
  `COUNT_DISTINCT(IF([events]>0,[turn_id],NULL))/COUNT_DISTINCT([turn_id])`.

Groups differ in size by two orders of magnitude: one model has 19,500
messages read, another 594; one prompt-length bucket has 3,000 messages and 128
turns, another 113 messages and 15 turns. Pharos hides groups under a minimum
(`minCount: { min, distinct: "message_id" }`) and pools the rest into Other,
but a bar of 15 turns at 40% and a bar of 128 turns at 4.7% look equally
certain. The dashboard should show how far each value could move.

## What is missing

A metric bucket carries `value`, `count` and, for paired axes, `yValue`. There
is no way to ask for, return or draw an interval around a value:

1. **Request.** No metric option asks for an interval, and formulas have no
   function that returns one.
2. **Result.** `MetricBucket` has no lower and upper bound, so a host cannot
   compute one outside the backend without re-reading the parts of every
   formula (which it cannot do for Other or for hidden groups).
3. **Display.** The UI's bar and line charts draw no error bars or bands, and
   the hover card has no place to show a range.

## Request

Add an optional interval to metrics, computed by the backend from the same
parts it already pools, and drawn by the UI.

- **Clause.** `interval?: { kind: "proportion" | "rate"; level?: number }` on an
  aggregation clause whose value is a ratio. `level` defaults to 0.95.
  - `proportion`: a share of units with a property (numerator ≤ denominator),
    for example turns with an event over turns. Wilson score interval.
  - `rate`: a count over an exposure, for example events per 1,000 messages.
    An exact Poisson (or Garwood) interval on the numerator, divided by the
    exposure.
  The backend needs the numerator and denominator separately. Either accept the
  two parts explicitly (`interval: { kind, numerator: expression, denominator:
  expression }`), or derive them from a top-level `A/B` formula; the explicit
  form is enough for Pharos.
- **Result.** `lower?: number; upper?: number` on each bucket, including Other
  and the ungrouped total, in the same units as `value` (after any constant
  scaling such as `/1000`). Omit them when the denominator is zero.
- **Display.** Bar charts draw a whisker per bar and line charts a band per
  series when the buckets have bounds; the hover card shows "value (lower to
  upper)". A display option turns them off.
- **Capabilities.** Advertise support in `queryCapabilities` (for example
  `metricIntervals: ["proportion", "rate"]`) so a host can fall back.

## Evidence

On a 26,443-message development library (2026-10-27 to 11-02):

| Group | Messages read | Turns | Turns with "unclear instructions" |
|---|---:|---:|---:|
| Person prompts, 31–100 words | 3,002 | 128 | 4.7% |
| Person prompts, 101–300 words | 1,274 | 52 | 15.4% |
| Person prompts, over 300 words | 113 | 15 | 40.0% |

Wilson 95% intervals for those shares are roughly 2–10%, 8–28% and 20–64%:
the over-300 bucket's interval overlaps the next one, which the bars do not
show.

## Done when

- `/query-v3` returns `lower`/`upper` for a clause with `interval`, on grouped,
  Other and total buckets, through the SQLite backend and the in-memory
  evaluator alike, with tests against hand-computed Wilson and Poisson bounds.
- The UI's bar and line charts draw them, and the hover card names them.
- The capability is advertised and documented in the integration guide.

Pharos will then add `interval` to the Agent Events catalog's ratio measures
(`web/src/agent-event-rates-catalog.ts`) and pin the release.
