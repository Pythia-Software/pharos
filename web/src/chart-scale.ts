export const chartScales = ["linear", "logarithmic", "half"] as const;
export type ChartScale = typeof chartScales[number];
export const defaultChartScale: ChartScale = "half";
export const validChartScale = (value: unknown): value is ChartScale => chartScales.includes(value as ChartScale);

// An axis's display unit: a value divided by per reads as shown (hours of
// milliseconds: 3,600,000). A clock axis shows seconds as durations, so its
// gridlines fall on round times (15 min, 1 h, 6 h) rather than round numbers
// of seconds (10,000 s reads as 2 h 47 min).
export type AxisUnit = { per: number; clock?: boolean };
const clockSteps = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400, 172800, 604800];
const clockMarks = [1, 60, 3600, 86400, 604800];

// niceCeil rounds up to 1, 2, or 5 times a power of ten.
function niceCeil(value: number): number {
  const magnitude = 10 ** Math.floor(Math.log10(value)), normal = value / magnitude;
  return (normal <= 1 ? 1 : normal <= 2 ? 2 : normal <= 5 ? 5 : 10) * magnitude;
}

// niceAtLeast is the smallest round value, in the axis's unit, at or above value.
function niceAtLeast(value: number, unit?: AxisUnit): number {
  const per = unit?.per ?? 1, shown = value / per;
  if (!unit?.clock || shown < 1) return niceCeil(shown) * per;
  return (clockSteps.find(step => step >= shown) ?? niceCeil(shown / 86400) * 86400) * per;
}

// Round values between low and high, and among them the ones a reader
// counts by: powers of ten, or a minute, hour, day and week.
function roundValues(low: number, high: number, unit?: AxisUnit): { candidates: number[]; marks: number[] } {
  const per = unit?.per ?? 1, candidates: number[] = [], marks: number[] = [];
  const add = (shown: number, mark: boolean) => {
    const value = shown * per;
    if (value < low || value > high || candidates.includes(value)) return;
    candidates.push(value);
    if (mark) marks.push(value);
  };
  if (unit?.clock) for (const step of clockSteps) add(step, clockMarks.includes(step));
  for (let power = Math.floor(Math.log10(low / per)); power <= Math.ceil(Math.log10(high / per)); power++)
    for (const step of [1, 2, 5]) if (!unit?.clock || step * 10 ** power < 1 || step * 10 ** power > 604800) add(step * 10 ** power, !unit?.clock && step === 1);
  candidates.sort((left, right) => left - right);
  return { candidates, marks: marks.sort((left, right) => left - right) };
}

// niceTicks places two or three gridlines at round values up to peak.
function niceTicks(peak: number, unit?: AxisUnit): number[] {
  if (!(peak > 0)) return [];
  const step = niceAtLeast(peak / 2.5, unit), ticks: number[] = [];
  for (let index = 1; index * step <= peak * 1.0001 && index < 10; index++) ticks.push(index * step);
  return ticks;
}

// A column scale maps a value to a height, in percent of the plot.
export type ColumnScale = { y: (value: number) => number; ticks: number[]; split?: number };

export function columnScale(totals: number[], mode: ChartScale, unit?: AxisUnit): ColumnScale {
  const peak = Math.max(0, ...totals), filled = totals.filter(value => value > 0).sort((left, right) => left - right);
  // Ignore idle buckets and isolated tiny values. Require enough nonzero
  // observations and more than ten times their 80th
  // percentile before compressing peaks in the default linear + log mode.
  const typical = filled.length >= 5 ? filled[Math.ceil(filled.length * 0.8) - 1] : 0;
  if (mode === "linear" || !peak || mode === "half" && (!typical || peak <= 10 * typical)) {
    const scale = peak || 1;
    return { y: value => 100 * value / scale, ticks: niceTicks(peak, unit) };
  }
  if (mode === "logarithmic") {
    // log1p keeps empty columns at zero and supports costs below one dollar.
    const y = (value: number) => 100 * Math.log1p(Math.max(0, value)) / Math.log1p(peak);
    const per = unit?.per ?? 1, { candidates } = roundValues(per * 10 ** Math.floor(Math.log10(Math.min(1, filled[0] / per))), peak, unit);
    const ticks: number[] = [];
    for (const value of candidates) if (y(value) - (ticks.length ? y(ticks[ticks.length - 1]) : 0) >= 10) ticks.push(value);
    return { y, ticks };
  }
  const split = niceAtLeast(typical, unit);
  const span = Math.log(peak / split);
  const y = (value: number) => value <= split ? 50 * value / split : 50 + 50 * Math.log(value / split) / span;
  // Round marks above the break (powers of ten, or an hour and a day), or
  // every round value when fewer than two fit; labels closer than a tenth of
  // the plot to the break or each other are dropped.
  const { candidates, marks } = roundValues(split, peak, unit), above = (values: number[]) => values.filter(value => value > split);
  const upper: number[] = [];
  for (const value of above(marks).length >= 2 ? above(marks) : above(candidates))
    if (y(value) - Math.max(50, ...upper.map(y)) >= 10) upper.push(value);
  return { y, ticks: [...niceTicks(split, unit).filter(value => y(value) <= 40), ...upper], split };
}
