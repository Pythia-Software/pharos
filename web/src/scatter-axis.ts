import type { MetricAxisScale, MetricValueFormat } from "@pythia-software/query-table-core";
import { formatMetricOutput } from "@pythia-software/query-table-ui";

const tidy = (value: number) => Number(value.toPrecision(12));

function linearTicks(min: number, max: number, limit: number): number[] {
  const rough = (max - min) / (limit - 1), power = 10 ** Math.floor(Math.log10(rough));
  let step = [1, 2, 5, 10].find(n => n * power >= rough)! * power;
  if (!Number.isFinite(step) || step <= 0) return [];
  if (Math.floor(max / step + 1e-10) - Math.ceil(min / step - 1e-10) < 1) step = [10, 5, 2, 1].find(n => n * power <= rough)! * power;
  const ticks: number[] = [], first = Math.ceil(min / step - 1e-10);
  for (let i = 0; i < limit; i++) {
    const tick = tidy((first + i) * step);
    if (tick > max + step * 1e-9) break;
    if (tick >= min) ticks.push(Object.is(tick, -0) ? 0 : tick);
  }
  return ticks;
}

// Tick positions are independent of the extent: keep the data in view without
// turning arbitrary observations (or their geometric mean) into axis labels.
export function scatterAxis(values: number[], options: MetricAxisScale = {}, limit = 4) {
  const log = options.mode === "log", accepts = (value: number) => Number.isFinite(value) && (!log || value > 0);
  const valid = values.filter(accepts);
  let min = valid.length ? Math.min(...valid) : log ? 1 : 0;
  let max = valid.length ? Math.max(...valid) : log ? 10 : 1;
  if (min === max) {
    if (log) { min /= Math.sqrt(10); max *= Math.sqrt(10); }
    else { const pad = Math.abs(min) * .05 || 1; min -= pad; max += pad; }
  }
  min = options.min ?? min;
  max = options.max ?? max;
  if (options.min !== undefined && options.max === undefined && max <= min) max = log ? min * 10 : min + (Math.abs(min) * .05 || 1);
  if (options.max !== undefined && options.min === undefined && min >= max) min = log ? max / 10 : max - (Math.abs(max) * .05 || 1);
  const error = !Number.isFinite(min) || !Number.isFinite(max) || min >= max || log && min <= 0
    ? "Axis bounds must be finite, increasing, and positive for logarithmic scales." : undefined;
  const fraction = (value: number) => log ? (Math.log10(value) - Math.log10(min)) / (Math.log10(max) - Math.log10(min)) : (value - min) / (max - min);
  let ticks: number[] = [];
  if (!error && log) {
    const start = Math.ceil(Math.log10(min) - 1e-10), end = Math.floor(Math.log10(max) + 1e-10);
    const stride = Math.max(1, Math.ceil((end - start) / (limit - 1)));
    for (let exponent = start; exponent <= end; exponent += stride) ticks.push(10 ** exponent);
    if (ticks.length < 2) {
      ticks = [];
      for (let exponent = Math.floor(Math.log10(min)); exponent <= Math.ceil(Math.log10(max)); exponent++) {
        for (const multiple of [1, 2, 5]) {
          const tick = tidy(multiple * 10 ** exponent);
          if (tick >= min && tick <= max) ticks.push(tick);
        }
      }
      if (ticks.length < 2) ticks = linearTicks(min, max, limit);
      // On short ranges, 1–2–5 ticks may be too close in screen space.
      ticks = ticks.filter((tick, i, all) => i === 0 || fraction(tick) - fraction(all[i - 1]) >= .18);
      if (ticks.length > limit) {
        const candidates = ticks;
        ticks = Array.from({ length: limit }, (_, i) => candidates[Math.round(i * (candidates.length - 1) / (limit - 1))]);
      }
    }
  } else if (!error) ticks = linearTicks(min, max, limit);
  return { min, max, ticks, fraction, accepts, error };
}

export function scatterTick(value: number, format?: MetricValueFormat, ticks: number[] = []): string {
  if (format?.kind === "duration" || format?.kind === "date" || format?.kind === "time" || format?.kind === "datetime") return formatMetricOutput(value, format).text;
  const number = format?.kind === "percent" ? value * 100 : value;
  const spacing = Math.min(...ticks.slice(1).map((tick, index) => Math.abs(tick - ticks[index])).filter(gap => gap > 0));
  const digits = value && Number.isFinite(spacing) ? Math.max(3, Math.floor(Math.log10(Math.abs(value))) - Math.floor(Math.log10(spacing)) + 1) : 3;
  return number.toLocaleString(undefined, { notation: "compact", maximumSignificantDigits: Math.min(15, digits) }) + (format?.kind === "percent" ? "%" : "");
}
