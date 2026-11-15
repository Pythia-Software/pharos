import type { CellDisplay, MetricValueFormat } from "@pythia-software/query-table-core";

// A currency amount's metric format as a table cell's display: the cell keeps
// its currency, decimals and notation.
export const currencyDisplay = (format: MetricValueFormat): CellDisplay => ({ kind: "currency", currency: format.currency ?? "USD",
  ...format.decimals !== undefined ? { decimals: format.decimals } : {}, ...format.notation ? { notation: format.notation } : {} });
