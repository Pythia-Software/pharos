import type { ChartBucket, ChartSeries } from "./chart-series";

// What a chart popover lists for one period. Dashboard charts render it
// directly; card covers carry it on their marks (data-chart-popover) to the
// shared hover bubble, so both read the same rows, order and total.
export type ChartPopoverEntry = { label: string; value: string; color?: string; href?: string; parts?: Array<[string, string]>; more?: number };
export type ChartPopoverContent = { heading: string; notes?: string[]; rows: ChartPopoverEntry[]; totals?: Array<[string, string]>; detail?: string; empty?: string };

const partLimit = 4;

// A ratio of zero is a value; an amount of zero is nothing to list. Rows are
// largest first. Several amounts add up to a total; ratios never do.
export function periodRows(bucket: ChartBucket, series: ChartSeries[], additive: boolean): { rows: ChartSeries[]; total?: number } {
  const rows = series.filter(item => additive ? (bucket.values[item.key] ?? 0) > 0 : bucket.values[item.key] !== undefined)
    .sort((left, right) => bucket.values[right.key] - bucket.values[left.key]);
  return additive && rows.length > 1 ? { rows, total: series.reduce((sum, item) => sum + (bucket.values[item.key] ?? 0), 0) } : { rows };
}

export function periodPopover(bucket: ChartBucket, series: ChartSeries[], { heading, additive, format, suffix = "", notes, empty, references = [], href }: {
  heading: string; additive: boolean; format: (value: number) => string; suffix?: string; notes?: string[]; empty: string;
  references?: Array<{ label: string; value: number }>; href?: (item: ChartSeries) => string | undefined;
}): ChartPopoverContent {
  const { rows, total } = periodRows(bucket, series, additive);
  return {
    heading,
    ...notes?.length ? { notes } : {},
    rows: rows.map(item => {
      const parts = [...(bucket.parts?.[item.key] ?? [])].sort((left, right) => right[1] - left[1]), link = href?.(item);
      return { label: item.label, value: format(bucket.values[item.key]), color: item.color, ...link ? { href: link } : {},
        ...parts.length ? { parts: parts.slice(0, partLimit).map(([name, value]) => [name, format(value)] as [string, string]) } : {},
        ...parts.length > partLimit ? { more: parts.length - partLimit } : {} };
    }),
    ...total !== undefined || references.length ? { totals: [...total !== undefined ? [["Total", `${format(total)}${suffix}`] as [string, string]] : [], ...references.map(item => [item.label, `${format(item.value)}${suffix}`] as [string, string])] } : {},
    ...bucket.detail ? { detail: bucket.detail } : {},
    ...rows.length ? {} : { empty },
  };
}

const literalColor = /^(?:#[a-f0-9]{3,8}|rgba?\([\d.,% ]+\)|white|black|transparent|currentcolor)$/i;
const property = /^var\(\s*--[a-z0-9_-]+\s*(?:,(.*))?\)$/is;
const mixture = /^color-mix\(\s*in\s+[a-z-]+(?:\s+(?:shorter|longer|increasing|decreasing)\s+hue)?\s*,(.*)\)$/is;
const share = /^(?:\d+(?:\.\d+)?%\s+)?(.*?)(?:\s+\d+(?:\.\d+)?%)?$/s;
// Split a function's arguments at top-level commas only.
function argumentsOf(list: string): string[] | null {
  const parts = [""];
  let depth = 0;
  for (const character of list) {
    if (character === "(") depth++;
    if (character === ")" && --depth < 0) return null;
    if (character === "," && !depth) parts.push("");
    else parts[parts.length - 1] += character;
  }
  return depth ? null : parts;
}
// Series colors are literals, custom properties with a fallback, and
// color-mix() blends of those (model + effort shades, event kinds, provisional
// estimates). Marks carry them as data, so accept exactly that grammar, never
// arbitrary CSS.
export function chartColor(value: unknown, depth = 0): value is string {
  if (typeof value !== "string") return false;
  value = value.trim();
  if (literalColor.test(value as string)) return true;
  if (depth > 3) return false;
  const reference = property.exec(value as string);
  if (reference) return reference[1] === undefined || chartColor(reference[1], depth + 1);
  const blend = mixture.exec(value as string), parts = blend && argumentsOf(blend[1]);
  return Boolean(parts?.length === 2 && parts.every(part => chartColor(share.exec(part.trim())![1], depth + 1)));
}

// Preserve custom-property references so portalled swatches track saved color
// edits too.
export function chartTooltipColors(serialized: string | null): (string | null)[] {
  try {
    const raw = JSON.parse(serialized ?? "[]");
    return Array.isArray(raw) ? raw.map(value => chartColor(value) ? value : null) : [];
  } catch { return []; }
}

// Read a mark's serialized popover, keeping only well-formed text and colors.
// Links stay with pinned dashboard popovers; a hover bubble never has them.
export function readChartPopover(serialized: string | null): ChartPopoverContent | null {
  const text = (value: unknown): value is string => typeof value === "string";
  const pairs = (value: unknown) => Array.isArray(value) ? value.filter((pair): pair is [string, string] => Array.isArray(pair) && pair.length === 2 && pair.every(text)) : [];
  try {
    const raw = JSON.parse(serialized ?? "null");
    if (!raw || typeof raw !== "object" || !text(raw.heading) || !Array.isArray(raw.rows)) return null;
    const rows = raw.rows.filter((row: unknown) => row && typeof row === "object" && text((row as ChartPopoverEntry).label) && text((row as ChartPopoverEntry).value)).map((row: ChartPopoverEntry): ChartPopoverEntry => {
      const parts = pairs(row.parts);
      return { label: row.label, value: row.value, ...chartColor(row.color) ? { color: row.color } : {}, ...parts.length ? { parts } : {}, ...Number.isInteger(row.more) && row.more! > 0 ? { more: row.more } : {} };
    });
    const notes = Array.isArray(raw.notes) ? raw.notes.filter(text) : [], totals = pairs(raw.totals);
    return { heading: raw.heading, ...notes.length ? { notes } : {}, rows, ...totals.length ? { totals } : {}, ...text(raw.detail) && raw.detail ? { detail: raw.detail } : {}, ...text(raw.empty) && raw.empty ? { empty: raw.empty } : {} };
  } catch { return null; }
}
