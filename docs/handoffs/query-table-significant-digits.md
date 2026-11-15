# Query Table: significant digits for number and currency formats

Status: blocked on an upstream published release. Pharos pins the four Query
Table npm packages and the Go module to 1.0.9. Pharos shows token counts with
three significant figures ([Amounts](../ui-vocabulary.md#amounts)), and its
own surfaces already do. Query Table's native cards, charts and table cells
cannot, and Pharos must not patch around that locally.

## Evidence

- `MetricValueFormat` and the `number`/`currency` `CellDisplay` offer only
  `decimals` and `notation: "standard" | "compact"` (`query-table-core`
  `src/cellDisplay.ts` and the metric format type in `index.d.ts`).
- With compact notation, `metricNumberOptions` (`query-table-ui`
  `src/metricFormat.ts`) sets `maximumFractionDigits = decimals ?? 1`, so the
  digit count depends on the mantissa: `1.2K`, `12.3K` and `123.4K`, which is
  two to four significant figures.
- The host has no hook here. `MetricCard` formats values, tooltips and
  histogram or heatmap labels through `formatMetricOutput(value,
  display.format)`, and `formatMetricValue` consults host renderers only for
  `duration_ms` and `byte_size`.
- Pharos now gives every token measure
  `{ kind: "number", notation: "compact", decimals: 1 }` (`tokenMetricFormat`
  in `web/src/token-count.ts`). That is the closest format available, and it
  reads differently from Pharos's own `1.23K` / `12.3K` / `123K`.

## Requested behavior

1. Add `significantDigits?: number` (1–21) to `MetricValueFormat` and to the
   `number` and `currency` `CellDisplay` kinds. When it is set, format with
   `maximumSignificantDigits` and no fraction-digit bounds, in either notation.
   `decimals` keeps its meaning when `significantDigits` is absent. Choose and
   document a precedence if both are set, or reject that combination.
2. Round before choosing the compact unit, as `Intl.NumberFormat` already
   does, so that 999,500 at three digits reads `1M`, not `1,000K`.
3. Accept the option everywhere a format travels: `validateMetricFormat`,
   compact ticks (`src/compactMetricChart.ts`, `compactMetricTick`, which may
   keep its own tick-spacing digits), the summary format control, and the Go
   link normalizer (`backends/go/query_links_normalize.go`, the
   `metricFormat` options near line 546 and the display options near line
   844). Without the Go change, saved links drop the key.

## Done when

A Query Table release has this option. Then Pharos pins the release everywhere
(npm packages and Go module) and changes `tokenMetricFormat` to
`{ kind: "number", notation: "compact", significantDigits: 3 }`. It also
updates the Query Table note in `docs/ui-vocabulary.md#amounts` and the
assertion in `web/tests/unit/page/token-count.test.mjs`.
