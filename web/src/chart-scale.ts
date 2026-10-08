export type ChartScale = "linear" | "logarithmic" | "half";
export const defaultChartScale: ChartScale = "half";
export const validChartScale = (value: unknown): value is ChartScale => value === "linear" || value === "logarithmic" || value === "half";

// niceTicks places two or three gridlines at round values up to peak.
function niceTicks(peak: number): number[] {
  if (!(peak > 0)) return [];
  const raw = peak / 2.5, magnitude = 10 ** Math.floor(Math.log10(raw)), normal = raw / magnitude;
  const step = (normal <= 1 ? 1 : normal <= 2 ? 2 : normal <= 5 ? 5 : 10) * magnitude, ticks: number[] = [];
  for (let index = 1; index * step <= peak * 1.0001 && index < 10; index++) ticks.push(index * step);
  return ticks;
}

// niceCeil rounds up to 1, 2, or 5 times a power of ten.
function niceCeil(value: number): number {
  const magnitude = 10 ** Math.floor(Math.log10(value)), normal = value / magnitude;
  return (normal <= 1 ? 1 : normal <= 2 ? 2 : normal <= 5 ? 5 : 10) * magnitude;
}

// A column scale maps a value to a height, in percent of the plot.
export type ColumnScale = { y: (value: number) => number; ticks: number[]; split?: number };

export function columnScale(totals: number[], mode: ChartScale): ColumnScale {
  const peak = Math.max(0, ...totals), filled = totals.filter(value => value > 0).sort((left, right) => left - right);
  const typical = filled.length >= 5 ? niceCeil(filled[Math.ceil(filled.length * 0.8) - 1]) : 0;
  if (mode === "linear" || !peak) {
    const scale = peak || 1;
    return { y: value => 100 * value / scale, ticks: niceTicks(peak) };
  }
  if (mode === "logarithmic") {
    // log1p keeps empty columns at zero and supports costs below one dollar.
    const y = (value: number) => 100 * Math.log1p(Math.max(0, value)) / Math.log1p(peak);
    const candidates: number[] = [];
    for (let power = Math.floor(Math.log10(Math.min(1, filled[0]))); power <= Math.ceil(Math.log10(peak)); power++)
      for (const step of [1, 2, 5]) {
        const value = step * 10 ** power;
        if (value <= peak) candidates.push(value);
      }
    const ticks: number[] = [];
    for (const value of candidates) if (y(value) - (ticks.length ? y(ticks[ticks.length - 1]) : 0) >= 10) ticks.push(value);
    return { y, ticks };
  }
  // Keep the existing outlier break when useful; otherwise reserve the lower
  // half for values up to a round number near one tenth of the peak.
  const split = typical > 0 && peak > 2 * typical ? typical : niceCeil(peak / 10);
  const span = Math.log(peak / split);
  const y = (value: number) => value <= split ? 50 * value / split : 50 + 50 * Math.log(value / split) / span;
  // Powers of ten above the break, or 1-2-5 steps when fewer than two fit;
  // labels closer than a tenth of the plot to the break or each other are dropped.
  const candidates: number[] = [], powers: number[] = [];
  for (let power = Math.floor(Math.log10(split)); power <= Math.ceil(Math.log10(peak)); power++)
    for (const step of [1, 2, 5]) {
      const value = step * 10 ** power;
      if (value <= split || value > peak) continue;
      candidates.push(value);
      if (step === 1) powers.push(value);
    }
  const upper: number[] = [];
  for (const value of powers.length >= 2 ? powers : candidates)
    if (y(value) - Math.max(50, ...upper.map(y)) >= 10) upper.push(value);
  return { y, ticks: [...niceTicks(split).filter(value => y(value) <= 40), ...upper], split };
}

