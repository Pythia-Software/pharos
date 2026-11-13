// Local experiment only. Production recommendations and saved preferences are
// deliberately read through the existing resolver, never installed or changed.
import catalog from "../../../pricing/model_colors.json";
import rules from "../../../pricing/model_color_rules.json";
import prices from "../../../pricing/cost_changes.json";
import { modelTimelineDates } from "../../src/model-color-identity";
import { perceptualColor } from "../../src/model-color-math";
import { recommendedModelColor } from "../../src/model-colors";

export type Provider = keyof typeof catalog.providers;
export type Algorithm = "current" | "chroma" | "fan" | "fresh";
export type Options = { algorithm: Algorithm; vividness: number; tierWeight: number; spread: number; lightness: number; specialistHues: boolean; specialistMode: "catalog" | "expanded"; specialistSpread: number };
export const defaults: Options = { algorithm: "fan", vividness: 1, tierWeight: .5, spread: 30, lightness: .67, specialistHues: true, specialistMode: "expanded", specialistSpread: 75 };
export const algorithms: { id: Algorithm; name: string; description: string }[] = [
  { id: "current", name: "Current palette", description: "The exact production resolver. Newer and stronger models get deeper shades." },
  { id: "chroma", name: "Chroma ladder", description: "Keep the provider hue. Give stronger tiers and newer releases more color, at a useful middle lightness." },
  { id: "fan", name: "Hue fan", description: "Spread tiers over nearby hues, while newer releases become more vivid. More separation, less uniformity." },
  { id: "fresh", name: "Freshness first", description: "Make the latest release in every class vivid. Use nearby hues to separate tiers, without ranking their saturation." },
];
export const providers = Object.keys(catalog.providers) as Provider[];
export const brands = catalog.providers;
export const roots = () => Object.fromEntries(providers.map(provider => [provider, brands[provider].color])) as Record<Provider, string>;
export const dates = modelTimelineDates(prices.changes, rules.observed_dates);
export const models = Object.entries(catalog.models).map(([name, value]) => ({ name, ...value, provider: value.provider as Provider, date: dates.get(name) }));
export type Model = typeof models[number];
export const providerModels = (provider: Provider) => models.filter(model => model.provider === provider);
export const classes = (provider: Provider): string[] => catalog.class_order[provider];
const clamp = (value: number, low = 0, high = 1) => Math.max(low, Math.min(high, value));
const angle = (value: number) => ((value + 180) % 360 + 360) % 360 - 180;
// A developer with no ranked core classes still owns its provider root. It
// isn't a specialist merely because its catalog has no capability hierarchy.
export const specialistModels = (provider?: Provider) => models.filter(model => {
  const ranked = rules.providers[model.provider].ranked_classes as string[];
  return (!provider || model.provider === provider) && ranked.length > 0 && !ranked.includes(model.class);
});
export function specialistHueOffset(model: Model, options: Options): number {
  if (!options.specialistHues || position(model).ranked) return 0;
  const definition = rules.providers[model.provider];
  const classRules = definition.classes as Record<string, { hue?: number }>;
  const original = angle((classRules[model.class]?.hue ?? definition.hue) - definition.hue);
  if (options.specialistMode === "catalog" || !definition.ranked_classes.length) return original;
  const names = classes(model.provider).filter(name => specialistModels(model.provider).some(item => item.class === name));
  const offsets = names.map(name => angle((classRules[name]?.hue ?? definition.hue) - definition.hue));
  const span = Math.max(...offsets) - Math.min(...offsets);
  // Scale the authored hue relationships around the same provider root.
  // Unlike tier spread, these are category offsets, not capability ranks.
  if (options.specialistSpread === 0) return 0;
  if (span > 0) return original * options.specialistSpread / span;
  // A lone specialist without an authored offset (e.g. Nano Banana) needs a
  // separate lane too. Spread zero intentionally returns it to the root.
  return names.length < 2 ? options.specialistSpread / 2 : 180 + (names.indexOf(model.class) / (names.length - 1) - .5) * options.specialistSpread;
}

export function lch(color: string): [number, number, number] {
  const [l, a, b] = perceptualColor(color);
  return [l, Math.hypot(a, b), (Math.atan2(b, a) * 180 / Math.PI + 360) % 360];
}
function rgb(l: number, c: number, h: number): number[] {
  const a = c * Math.cos(h * Math.PI / 180), b = c * Math.sin(h * Math.PI / 180);
  const x = (l + .3963377774 * a + .2158037573 * b) ** 3;
  const y = (l - .1055613458 * a - .0638541728 * b) ** 3;
  const z = (l - .0894841775 * a - 1.2914855480 * b) ** 3;
  return [4.0767416621 * x - 3.3077115913 * y + .2309699292 * z, -1.2684380046 * x + 2.6097574011 * y - .3413193965 * z, -.0041960863 * x - .7034186147 * y + 1.7076147010 * z];
}
// Reduce chroma at fixed lightness/hue until the color is inside sRGB, rather
// than clipping RGB channels and silently changing the requested hue.
export function gamutChroma(l: number, h: number): number {
  let low = 0, high = .4;
  for (let i = 0; i < 24; i++) {
    const middle = (low + high) / 2;
    if (rgb(l, middle, h).every(value => value >= 0 && value <= 1)) low = middle;
    else high = middle;
  }
  return low;
}
export function fromLCH(l: number, c: number, h: number): string {
  return "#" + rgb(l, Math.min(c, gamutChroma(l, h)), h).map(value => {
    const encoded = value <= .0031308 ? value * 12.92 : 1.055 * value ** (1 / 2.4) - .055;
    return Math.round(255 * clamp(encoded)).toString(16).padStart(2, "0");
  }).join("");
}
export function position(model: Model) {
  const order = rules.providers[model.provider].ranked_classes as string[];
  const index = order.indexOf(model.class);
  const tier = index < 0 || order.length < 2 ? .5 : index / (order.length - 1);
  const releases = [...new Set(providerModels(model.provider).filter(item => item.class === model.class && item.date).map(item => item.date!))].sort();
  // Dates are catalog/observed metadata, never parsed from a model name. An
  // undated label gets a neutral midpoint; same-date siblings have equal rank.
  const recency = !model.date ? .5 : releases.length < 2 ? 1 : releases.indexOf(model.date) / (releases.length - 1);
  return { tier, recency, ranked: index >= 0, latest: Boolean(model.date && model.date === releases.at(-1)) };
}
export function experimentColor(model: Model, base: string, options: Options): string {
  if (options.algorithm === "current") return recommendedModelColor(model.name, base);
  const [baseL, baseC, baseH] = lch(base), { tier, recency, ranked } = position(model);
  // An achromatic root has no meaningful hue. Preserve its neutral intent.
  const neutral = baseC < .008;
  const hue = neutral ? brands[model.provider].hue : baseH;
  const specialistOffset = specialistHueOffset(model, options);
  const fan = options.algorithm === "fan" || options.algorithm === "fresh";
  const siblings = providerModels(model.provider).filter(item => item.class === model.class && item.date === model.date).map(item => item.name).sort();
  // A symmetric, identity-stable hue offset separates same-date siblings
  // without inventing a capability or release ordering between them.
  const siblingOffset = siblings.length < 2 ? 0 : (siblings.indexOf(model.name) / (siblings.length - 1) - .5) * 8;
  const h = hue + specialistOffset + siblingOffset + (fan && ranked ? (tier - .5) * options.spread : 0);
  const score = options.algorithm === "fresh" || !ranked ? recency : options.tierWeight * tier + (1 - options.tierWeight) * recency;
  const anchorL = clamp(options.lightness + (baseL - .65) * .28, .46, .79);
  const l = clamp(anchorL + (1 - score) * .16, .44, .89);
  const anchorC = neutral ? 0 : clamp(baseC / .16, .18, 1.2) * .22 * options.vividness;
  // Budget at the frontier lightness; don't let the small gamut of a pale
  // historical tint desaturate the entire class. Each output is gamut-mapped
  // separately, and the inspector exposes any resulting crowded neighbors.
  const c = Math.min(anchorC, gamutChroma(anchorL, h) * .98) * (.2 + .8 * score);
  return fromLCH(l, c, h);
}
export function palette(base: Record<Provider, string>, options: Options): Map<string, string> {
  return new Map(models.map(model => [model.name, experimentColor(model, base[model.provider], options)]));
}

export type Workload = "frontier" | "balanced" | "history" | "specialists";
// The workload is explicitly synthetic, fixed across algorithms and never
// derived from real library usage. Keep its models/weights color-independent.
export function workload(kind: Workload, provider?: Provider): { model: Model; weight: number }[] {
  if (kind === "specialists") {
    const latest = new Map<string, Model>();
    for (const model of [...specialistModels(provider)].sort((a, b) => (a.date ?? '').localeCompare(b.date ?? '') || a.name.localeCompare(b.name))) latest.set(`${model.provider}:${model.class}`, model);
    return [...latest.values()].slice(0, 8).map(model => ({ model, weight: 1 }));
  }
  const core = models.filter(model => (!provider || model.provider === provider) && rules.providers[model.provider].ranked_classes.includes(model.class as never));
  const candidates = core.length ? core : models.filter(model => !provider || model.provider === provider);
  if (kind === "balanced") {
    // Sample across providers and their full dated histories, rather than
    // taking eight alphabetical ties and dropping later provider families.
    const buckets = providers.map(key => candidates.filter(item => item.provider === key).sort((a, b) => classes(key).indexOf(a.class) - classes(key).indexOf(b.class) || (a.date ?? '9999').localeCompare(b.date ?? '9999') || a.name.localeCompare(b.name))).filter(bucket => bucket.length);
    const chosen: Model[] = [];
    const rounds = Math.max(1, Math.ceil(8 / (buckets.length || 1)));
    for (let round = 0; round < rounds; round++) for (const bucket of buckets) {
      const item = bucket[Math.round(round / Math.max(1, rounds - 1) * (bucket.length - 1))];
      if (!chosen.includes(item)) chosen.push(item);
    }
    return chosen.slice(0, 8).map(model => ({ model, weight: 1 }));
  }
  return candidates.map(model => {
    const { tier, recency } = position(model);
    const weight = kind === "frontier" ? (.08 + tier ** 3) * (.025 + recency ** 5)
      : (.3 + tier) * (.15 + (1 - recency) ** 2);
    return { model, weight };
  }).sort((a, b) => b.weight - a.weight || a.model.name.localeCompare(b.model.name)).slice(0, 8);
}
