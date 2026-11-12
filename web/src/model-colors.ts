import { colorDistance, colorLuminance } from "./model-color-math";
import { useSyncExternalStore } from "react";
import catalog from "../../pricing/model_colors.json";
import prices from "../../pricing/cost_changes.json";
import rules from "../../pricing/model_color_rules.json";
import { inferModelProvider, inferModelClass, matchModelClass, modelCatalogDates, modelTimelineDates, modelColorKey } from "./model-color-identity";
export { modelColorKey } from "./model-color-identity";

export type ModelProvider = keyof typeof catalog.providers;
export type ColorKind = "provider" | "source" | "model";
export type ModelPalette = { overrides: Record<string, string>; models: string[]; sources: string[] };
export const modelProviders = Object.keys(catalog.providers) as ModelProvider[];
export const providerBrands = catalog.providers;
export const rankedModelClasses = (provider: ModelProvider): readonly string[] => rules.providers[provider].ranked_classes;
const models = catalog.models as Record<string, { provider: ModelProvider; class: string; color: string }>;
const sources = catalog.sources as Record<string, { provider: ModelProvider; color: string }>;
export function modelProvider(name: string): ModelProvider {
  const key = modelColorKey(name);
  return Object.hasOwn(models, key) ? models[key].provider : (matchModelClass(key, rules.providers)?.provider ?? inferModelProvider(key)) as ModelProvider;
}
export function modelClass(name: string): string | undefined {
  const key = modelColorKey(name);
  return Object.hasOwn(models, key) ? models[key].class : matchModelClass(key, rules.providers)?.class ?? inferModelClass(key);
}
export const sourceProvider = (name: string): ModelProvider => Object.hasOwn(sources, name) ? sources[name].provider : Object.hasOwn(providerBrands, name) ? name as ModelProvider : "other";
export const sourceLabel = (name: string) => ({ claude: "Claude", codex: "Codex", antigravity: "Antigravity", chatgpt: "ChatGPT", tl1: "TL1", canonical: "Canonical export" }[name] ?? name);
const catalogDates = modelCatalogDates(prices.changes), dates = modelTimelineDates(prices.changes, rules.observed_dates);
export const modelTimelineDate = (name: string) => dates.get(modelColorKey(name));
export const modelDateLabel = (name: string) => {
  const key = modelColorKey(name), date = dates.get(key);
  return date ? `${!catalogDates.has(key) && Object.hasOwn(rules.observed_dates, key) ? "First observed" : "Catalog date"} ${date}` : "Date unavailable";
};
// One catalog order drives settings, chart stacks and legends. Unknown
// classes follow the reviewed classes without changing recorded model keys.
export function modelColorOrder(name: string): string {
  const key = modelColorKey(name), provider = modelProvider(key), family = modelClass(key) ?? key.split(/[-.]/)[0];
  const classes: readonly string[] = catalog.class_order[provider], index = classes.indexOf(family);
  return `${modelProviders.indexOf(provider)}:${index < 0 ? classes.length : index}:${family}:${modelTimelineDate(key) ?? "9999"}:${key}`;
}
export const compareModelColors = (a: string, b: string) => modelColorOrder(a).localeCompare(modelColorOrder(b), undefined, { numeric: true });
// Presentation can split shared color classes without changing their policy.
export function modelPaletteGroups(names: readonly string[]): { name: string; models: string[] }[] {
  const groups = new Map<string, { name: string; models: string[] }>();
  for (const model of [...names].sort(compareModelColors)) {
    const family = modelClass(model) ?? "Other models";
    const name = family === "Fable / Mythos" ? modelColorKey(model).startsWith("mythos") ? "Mythos" : "Fable" : family;
    const key = `${modelProvider(model)}:${name}`;
    if (!groups.has(key)) groups.set(key, { name, models: [] });
    groups.get(key)!.models.push(model);
  }
  return [...groups.values()].sort((a, b) => modelProvider(a.models[0]) === modelProvider(b.models[0]) && modelClass(a.models[0]) === modelClass(b.models[0])
    ? a.name.localeCompare(b.name) : compareModelColors(a.models[0], b.models[0]));
}
function hash(name: string) { let h = 2166136261; for (const c of name) h = Math.imul(h ^ c.charCodeAt(0), 16777619); return h >>> 0; }
export function colorHSL(color: string): [number, number, number] {
  const [r, g, b] = [1, 3, 5].map(at => parseInt(color.slice(at, at + 2), 16) / 255);
  const max = Math.max(r, g, b), min = Math.min(r, g, b), delta = max - min, l = (max + min) / 2;
  return [delta === 0 ? 0 : ((max === r ? (g - b) / delta : max === g ? (b - r) / delta + 2 : (r - g) / delta + 4) * 60 + 360) % 360, delta === 0 ? 0 : delta / (1 - Math.abs(2 * l - 1)), l];
}
function hex(h: number, s: number, l: number) {
  const a = s * Math.min(l, 1 - l);
  return "#" + [0, 8, 4].map(offset => { const k = (offset + h / 30) % 12; return Math.round(255 * (l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1)))).toString(16).padStart(2, "0"); }).join("");
}
// Set relative luminance while retaining a shade's hue. Choose the nearest
// RGB rounding to keep fallback shades inside their reviewed class range.
function withLuminance(color: string, target: number): string {
  const linear = (value: number) => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4;
  const channels = [1, 3, 5].map(at => linear(parseInt(color.slice(at, at + 2), 16) / 255));
  const weights = [.2126, .7152, .0722], luminance = channels.reduce((sum, value, i) => sum + value * weights[i], 0);
  const encoded = channels.map(value => {
    const next = luminance > target ? value * target / luminance : value + (1 - value) * (target - luminance) / (1 - luminance);
    return Math.round(255 * (next <= .0031308 ? next * 12.92 : 1.055 * next ** (1 / 2.4) - .055));
  });
  let best = encoded, distance = Infinity;
  for (const r of [-1, 0, 1]) for (const g of [-1, 0, 1]) for (const b of [-1, 0, 1]) {
    const candidate = encoded.map((value, i) => Math.max(0, Math.min(255, value + [r, g, b][i])));
    const delta = Math.abs(candidate.reduce((sum, value, i) => sum + linear(value / 255) * weights[i], 0) - target);
    if (delta < distance) { best = candidate; distance = delta; }
  }
  return "#" + best.map(value => value.toString(16).padStart(2, "0")).join("");
}
// Catalog defaults are generated together when releases/rules change. Runtime
// fallbacks for unreviewed inventory are deterministic and independent of the
// visible/ranked set; dates come from pricing, never a guessed launch date.
function calculateRecommendedColor(name: string, base: string): string {
  const key = modelColorKey(name), provider = modelProvider(key), brand = providerBrands[provider].color;
  const model = Object.hasOwn(models, key) ? models[key] : undefined;
  const family = modelClass(key);
  const relatives = family ? Object.values(models).filter(item => item.provider === provider && item.class === family) : [];
  const reference = model?.color ?? relatives[0]?.color;
  const [baseHue, baseSaturation, baseLightness] = colorHSL(base);
  const [brandHue, brandSaturation, brandLightness] = colorHSL(brand);
  // A provider edit rotates the entire palette together, retaining the
  // distinctions between classes and the individual version shades.
  const rebase = (color: string) => {
    if (base.toLowerCase() === brand.toLowerCase()) return color;
    const [h, s, l] = colorHSL(color);
    return hex((h + baseHue - brandHue + 360) % 360, Math.min(1, s * baseSaturation / brandSaturation), Math.max(.15, Math.min(.85, l + (baseLightness - brandLightness) * .35)));
  };
  if (model) return rebase(model.color);
  const [h, s] = colorHSL(reference ? rebase(reference) : base), seed = hash(key);
  // Keep recognizable classes in a tight hue range, including unlisted
  // older versions. Stay within the catalog class's overall luminance
  // range; historical class ranges can overlap.
  const hueRange = reference ? 3 : 16;
  const relativeColors = relatives.map(item => rebase(item.color)), tones = relativeColors.map(colorHSL);
  const lights = tones.map(tone => tone[2]), saturations = tones.map(tone => tone[1]);
  const low = lights.length ? Math.min(...lights) : .3, high = lights.length ? Math.max(...lights) : .7;
  const lightness = low + ((seed >>> 8) % 1001) / 1000 * (high - low);
  const lowSaturation = saturations.length ? Math.min(...saturations) * .9 : s * .9;
  const highSaturation = saturations.length ? Math.max(...saturations) * 1.1 : s * 1.1;
  const saturation = Math.min(1, lowSaturation + (seed % 1001) / 1000 * (highSaturation - lowSaturation));
  const shade = hex((h + (seed % 16001) / 16000 * hueRange - hueRange / 2 + 360) % 360, saturation, lightness);
  if (!reference) return shade;
  // HSL lightness alone cannot bound perceived depth when versions vary
  // in saturation. Clamp luminance to retain the reviewed class range.
  const levels = relativeColors.map(colorLuminance), level = colorLuminance(shade);
  const target = Math.max(Math.min(...levels), Math.min(Math.max(...levels), level));
  return target === level ? shade : withLuminance(shade, target);
}

const recommendations = new Map<string, string>();
export function recommendedModelColor(name: string, base: string): string {
  const key = `${base}:${modelColorKey(name)}`;
  let color = recommendations.get(key);
  if (!color) { color = calculateRecommendedColor(name, base); recommendations.set(key, color); }
  return color;
}
export function shadeChoices(base: string): string[] {
  const [h, s] = colorHSL(base);
  return [.72, .62, .52, .42, .32].map(l => hex(h, s, l));
}
const empty = (): ModelPalette => ({ overrides: {}, models: [], sources: [] });
let palette = empty(), revision = 0;
const listeners = new Set<() => void>();
const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; };
export const useModelColorRevision = () => useSyncExternalStore(subscribe, () => revision);
export const modelPalette = () => palette;
export function customModelColor(kind: ColorKind, key: string): string | undefined { return Object.hasOwn(palette.overrides, `${kind}:${key}`) ? palette.overrides[`${kind}:${key}`] : undefined; }
// Drafts never install a palette or update mounted chart CSS properties.
export function previewModelColor(name: string, providerColor: string, draft?: string): string {
  const key = modelColorKey(name);
  return draft ?? customModelColor("model", key) ?? recommendedModelColor(key, providerColor);
}
export type ModelPaletteEntry = { name: string; saved: string; preview: string };
export function modelPaletteComparison(names: readonly string[], providerColor: string, drafts: Record<string, string> = {}): { entries: ModelPaletteEntry[]; changed: boolean } {
  const entries = names.map(name => ({name, saved: effectiveModelColor("model", name), preview: previewModelColor(name, providerColor, drafts[`model:${name}`])}));
  return {entries, changed: entries.some(entry => entry.saved !== entry.preview)};
}
export function providerColorWarnings(provider: ModelProvider, base: string, entries: readonly ModelPaletteEntry[]): string[] {
  const warnings: string[] = [], brightness = colorLuminance(base);
  if (brightness > .85) warnings.push("This base color is very light. Some model shades may blend into light backgrounds.");
  if (brightness < .015) warnings.push("This base color is very dark. Some model shades may blend into dark backgrounds.");
  const families = new Map<string, Map<string, ModelPaletteEntry>>();
  for (const entry of [...entries].sort((a, b) => compareModelColors(a.name, b.name))) {
    const date = modelTimelineDate(entry.name), family = modelClass(entry.name);
    if (!date || !family) continue;
    if (!families.has(family)) families.set(family, new Map());
    // Undated aliases and same-date siblings deliberately share nearby tones.
    if (!families.get(family)!.has(date)) families.get(family)!.set(date, entry);
  }
  const pairs: [ModelPaletteEntry, ModelPaletteEntry][] = [];
  for (const releases of families.values()) {
    const versions = [...releases.values()];
    for (let i = 1; i < versions.length; i++) pairs.push([versions[i - 1], versions[i]]);
  }
  const latest = rankedModelClasses(provider).flatMap(family => {
    const releases = families.get(family);
    return releases?.size ? [[...releases.values()].at(-1)!] : [];
  });
  for (let i = 1; i < latest.length; i++) pairs.push([latest[i - 1], latest[i]]);
  if (pairs.some(([a, b]) => colorDistance(a.preview, b.preview) < .025)) warnings.push("Some neighboring model shades are hard to distinguish. Adjust the color for more separation.");
  return warnings;
}

export function effectiveModelColor(kind: ColorKind, key: string): string {
  if (kind === "model") key = modelColorKey(key);
  const custom = customModelColor(kind, key);
  if (custom) return custom;
  if (kind === "provider") return providerBrands[key as ModelProvider]?.color ?? providerBrands.other.color;
  if (kind === "source") {
    const provider = sourceProvider(key), base = effectiveModelColor("provider", provider);
    if (Object.hasOwn(providerBrands, key)) return base;
    if (!Object.hasOwn(sources, key)) return recommendedModelColor(`source:${key}`, base);
    if (sources[key].color === providerBrands[provider].color) return base;
    const [h, s, l] = colorHSL(base), delta = colorHSL(sources[key].color)[2] - colorHSL(providerBrands[provider].color)[2];
    return hex(h, s, Math.max(.15, Math.min(.85, l + delta)));
  }
  return recommendedModelColor(key, effectiveModelColor("provider", modelProvider(key)));
}
export const knownModels = () => [...new Set([...Object.keys(models), ...palette.models, ...Object.keys(palette.overrides).filter(key => key.startsWith("model:")).map(key => key.slice(6))])];
export const knownSources = () => [...new Set([...Object.keys(sources), ...palette.sources, ...Object.keys(palette.overrides).filter(key => key.startsWith("source:")).map(key => key.slice(7))])];
// Bound both var() references and nested color-mix() values for Query Table.
// Hash every UTF-16 code unit in both directions; do not truncate long names or
// lose the low surrogate of a model name containing an astral character.
function property(kind: ColorKind, key: string): string {
  let forward = 0xcbf29ce484222325n, backward = forward;
  for (let i = 0; i < key.length; i++) {
    forward = BigInt.asUintN(64, (forward ^ BigInt(key.charCodeAt(i))) * 0x100000001b3n);
    backward = BigInt.asUintN(64, (backward ^ BigInt(key.charCodeAt(key.length - i - 1))) * 0x100000001b3n);
  }
  return `--model-color-${kind}-${forward.toString(16).padStart(16, "0")}${backward.toString(16).padStart(16, "0")}`;
}
const marks = new Map<string, [ColorKind, string]>();
export function modelSeriesColor(kind: ColorKind, name: string): string {
  const key = kind === "model" ? modelColorKey(name) : name, color = effectiveModelColor(kind, key);
  if (typeof document === "undefined") return color;
  const token = property(kind, key);
  marks.set(token, [kind, key]);
  document.documentElement.style.setProperty(token, color);
  return `var(${token}, ${color})`;
}
export function installModelColors(next: { overrides: Record<string, string>; models?: string[] | null; sources?: string[] | null }) {
  const cleaned = { overrides: Object.fromEntries(Object.entries(next.overrides ?? {}).filter(([key, color]) => /^(provider|source|model):.+/.test(key) && /^#[0-9a-f]{6}$/i.test(color))), models: (next.models ?? palette.models).filter(value => typeof value === "string").map(modelColorKey), sources: (next.sources ?? palette.sources).filter(value => typeof value === "string") };
  if (JSON.stringify(cleaned) === JSON.stringify(palette)) return;
  palette = cleaned;
  if (typeof document !== "undefined") for (const [token, [kind, key]] of marks) document.documentElement.style.setProperty(token, effectiveModelColor(kind, key));
  revision++;
  for (const listener of listeners) listener();
}
declare global { interface Window { pharosModelColors?: ModelPalette } }
if (typeof window !== "undefined") installModelColors(window.pharosModelColors ?? empty());
let pending: Promise<ModelPalette> | undefined, pendingInventory = false;
export function loadModelColors(inventory = false): Promise<ModelPalette> {
  if (pending) return inventory && !pendingInventory ? pending.then(() => loadModelColors(true)) : pending;
  pendingInventory = inventory;
  return pending = (async () => {
    const started = revision, response = await fetch(`/api/model-colors${inventory ? "?inventory=1" : ""}`);
    if (!response.ok) throw new Error("Model and provider colors could not be loaded. Try again.");
    const next = await response.json() as ModelPalette;
    if (revision === started) installModelColors(next);
    else if (inventory) installModelColors({ ...next, overrides: palette.overrides });
    return palette;
  })().finally(() => { pending = undefined; });
}
export async function saveModelColor(kind: ColorKind, key: string, color: string) {
  const response = await fetch("/api/model-colors", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: `${kind}:${key}`, color }) });
  if (!response.ok) throw new Error("Color could not be saved. Try again.");
  installModelColors(await response.json());
}
