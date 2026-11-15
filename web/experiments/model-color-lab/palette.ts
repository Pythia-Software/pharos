// Local experiment only. Production recommendations and saved preferences are
// deliberately read through the existing resolver, never installed or changed.
import catalog from "../../../pricing/model_colors.json";
import rules from "../../../pricing/model_color_rules.json";
import prices from "../../../pricing/cost_changes.json";
import { modelTimelineDates } from "../../src/model-color-identity";
import { policyColor } from "../../src/model-color-policy";
export { lch, fromLCH } from "../../src/model-color-math";
import { recommendedModelColor } from "../../src/model-colors";

export type Provider = keyof typeof catalog.providers;
export type Algorithm = "current" | "chroma" | "fan" | "fresh";
export type Options = { algorithm: Algorithm; vividness: number; tierWeight: number; spread: number; lightness: number; specialistHues: boolean; specialistMode: "catalog" | "expanded"; specialistSpread: number };
export const defaults: Options = { algorithm: "fan", ...rules.palette, specialistHues: true, specialistMode: "expanded" };
export const algorithms: { id: Algorithm; name: string; description: string }[] = [
  { id: "current", name: "Current palette", description: "The exact production resolver, using the selected Hue fan preset." },
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
  const cohort = position(model);
  const siblings = providerModels(model.provider).filter(item => item.class === model.class && item.date === model.date).map(item => item.name).sort();
  const siblingOffset = siblings.length < 2 ? 0 : (siblings.indexOf(model.name) / (siblings.length - 1) - .5) * 8;
  return policyColor(base, brands[model.provider].hue, {
    ...cohort, hueOffset: specialistHueOffset(model, options), siblingOffset,
  }, { ...options, spread: options.algorithm === "chroma" ? 0 : options.spread, tierWeight: options.algorithm === "fresh" ? 0 : options.tierWeight });
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
