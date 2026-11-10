// Fine bins of one numeric value, for a heatmap over a continuous axis
// (dashboard-heatmap-model.ts). The service stores each record's bin as the
// bin's lower edge (internal/archive/value_bins.go), so a query groups by it
// and the chart lays the edges on a numeric axis. Both sides compute an edge
// by parsing the same decimal text, so the doubles agree exactly
// (testdata/value-bins.json).
//
// - log: ten bins a decade at 1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6 and 8 times a
//   power of ten. Zero and below is one bin at 0.
// - count: whole numbers. 0 to 9 are a bin each, then log bins from 10.
// - linear: steps of 1/per from 0. Below 0 is clamped to 0.
export type BinScheme = { scale: "log" } | { scale: "count" } | { scale: "linear"; per: number };

export const logMantissas = [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8] as const;
const edge = (mantissa: number, decade: number) => Number(`${mantissa}e${decade}`);

// The decade whose 1× edge is at or below a positive value, exactly.
function decadeOf(value: number) {
  let decade = Math.floor(Math.log10(value));
  while (edge(1, decade) > value) decade--;
  while (edge(1, decade + 1) <= value) decade++;
  return decade;
}
function logBin(value: number) {
  const decade = decadeOf(value);
  let found = edge(1, decade);
  for (const mantissa of logMantissas) if (edge(mantissa, decade) <= value) found = edge(mantissa, decade);
  return found;
}

// The lower edge of the bin holding value; null for no value.
export function binOf(value: number | null | undefined, scheme: BinScheme): number | null {
  if (value === null || value === undefined || !Number.isFinite(value)) return null;
  if (scheme.scale === "linear") {
    let index = Math.max(0, Math.floor(value * scheme.per));
    while (index > 0 && index / scheme.per > value) index--;
    while ((index + 1) / scheme.per <= value) index++;
    return index / scheme.per;
  }
  if (value <= 0) return 0;
  if (scheme.scale === "count" && value < 10) return Math.floor(value);
  return logBin(value);
}

// The upper edge of the bin starting at lower.
export function nextEdge(lower: number, scheme: BinScheme): number {
  if (scheme.scale === "linear") return (Math.round(lower * scheme.per) + 1) / scheme.per;
  if (scheme.scale === "count" && lower < 10) return lower + 1;
  if (lower <= 0) return scheme.scale === "count" ? 1 : NaN;
  const decade = decadeOf(lower), index = logMantissas.findIndex(mantissa => edge(mantissa, decade) === lower);
  return index < 0 || index === logMantissas.length - 1 ? edge(1, decade + 1) : edge(logMantissas[index + 1], decade);
}

// Every bin from the lowest observed to the highest, in order, so empty bins
// between observed ones take their place on the axis. A log scheme's zero
// bin, which no axis position holds, is first when observed.
export function binsSpanning(observed: readonly number[], scheme: BinScheme, limit = 400): number[] {
  const lows = observed.filter(Number.isFinite), zero = scheme.scale === "log" && lows.includes(0);
  const walked = scheme.scale === "log" ? lows.filter(value => value > 0) : lows;
  const bins: number[] = zero ? [0] : [];
  if (!walked.length) return bins;
  const last = Math.max(...walked);
  for (let at = Math.min(...walked); at <= last && bins.length < limit; at = nextEdge(at, scheme)) bins.push(at);
  return bins;
}
