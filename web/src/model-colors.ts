import { useSyncExternalStore } from "react";
import catalog from "../../pricing/model_colors.json";
import prices from "../../pricing/cost_changes.json";

export type ModelProvider = keyof typeof catalog.providers;
export type ColorKind = "provider" | "source" | "model";
export type ModelPalette = { overrides: Record<string, string>; models: string[]; sources: string[] };
export const modelProviders = Object.keys(catalog.providers) as ModelProvider[];
export const providerBrands = catalog.providers;
const models = catalog.models as Record<string, { provider: ModelProvider; class: string; color: string }>;
const sources = catalog.sources as Record<string, { provider: ModelProvider; color: string }>;
export const modelColorKey = (name: string) => name.trim().toLowerCase().replace(/^claude-/, "").replace(/(-1m|\[1m\])$/, "").replace(/-\d{8}$/, "");
export function modelProvider(name: string): ModelProvider {
  const key = modelColorKey(name);
  if (Object.hasOwn(models, key)) return models[key].provider;
  if (/^(opus|sonnet|haiku|fable|mythos)(-|$)|^\d-\d-haiku/.test(key)) return "anthropic";
  if (/^(gemini|gemma)(-|$)/.test(key)) return "google";
  if (/^(gpt|chatgpt|codex|o[134])(-|$)/.test(key)) return "openai";
  return "other";
}
export const sourceProvider = (name: string): ModelProvider => Object.hasOwn(sources, name) ? sources[name].provider : Object.hasOwn(providerBrands, name) ? name as ModelProvider : "other";
export const sourceLabel = (name: string) => ({ claude: "Claude", codex: "Codex", antigravity: "Antigravity", chatgpt: "ChatGPT", tl1: "TL1", canonical: "Canonical export" }[name] ?? name);
const dates = new Map<string, string>();
for (const price of prices.changes) {
  const key = modelColorKey(price.model), previous = dates.get(key);
  if (!previous || price.effective_from < previous) dates.set(key, price.effective_from);
}
export const modelCatalogDate = (name: string) => dates.get(modelColorKey(name));
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
// RGB rounding so small hue variations do not reverse chronological darkness.
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
// Fixed date scale, never today's date or the visible/ranked set. Adding a
// model cannot recolor an existing one. These are first pricing-catalog dates,
// not asserted launch dates. Undated models get a deterministic middle shade.
function calculateRecommendedColor(name: string, base: string): string {
  const key = modelColorKey(name), date = dates.get(key), [h, s] = colorHSL(base), seed = hash(key);
  if (!date) return hex((h + (seed % 16001) / 1000 - 8 + 360) % 360, Math.min(1, s * (.7 + (seed % 401) / 1000)), .3 + ((seed >>> 8) % 1001) / 2500);
  const age = (Date.parse(date) - Date.UTC(2024, 0, 1)) / 86400000;
  const target = Math.max(.13, Math.min(.46, .46 - age * .00025));
  const shade = hex((h + seed % 7 - 3 + 360) % 360, Math.min(1, s * (.8 + ((seed >>> 8) % 301) / 1000)), .5);
  return withLuminance(shade, target);
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
