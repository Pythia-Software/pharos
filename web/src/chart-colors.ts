import catalog from "../../pricing/model_colors.json";

type Provider = keyof typeof catalog.providers;
type ModelStyle = { provider: Provider; class: string; color: string };
type SourceStyle = { provider: Provider; color: string };
export type SeriesStyle = { color: string; group?: string; order?: string };
const models = catalog.models as Record<string, ModelStyle>;
const sources = catalog.sources as Record<string, SourceStyle>;
const providerOrder: Provider[] = ["anthropic", "google", "openai", "other"];
export const otherSeriesColor = "#85909b";
const allocated = new Map<string, string>();
const reserved = new Set([otherSeriesColor, ...Object.values(models).map(model => model.color), ...Object.values(sources).map(source => source.color), ...Object.values(catalog.providers).map(provider => provider.color)]);

export function modelColorKey(name: string): string {
  return name.trim().toLowerCase().replace(/^claude-/, "").replace(/(-1m|\[1m\])$/, "").replace(/-\d{8}$/, "");
}

function modelProvider(name: string): Provider {
  if (/^(opus|sonnet|haiku|fable)(-|$)/.test(name)) return "anthropic";
  if (/^(gemini|gemma)(-|$)/.test(name)) return "google";
  if (/^(gpt|chatgpt|codex|o[134])(-|$)/.test(name)) return "openai";
  return "other";
}

function hashName(name: string): number {
  let hash = 2166136261;
  for (const character of name) hash = Math.imul(hash ^ character.charCodeAt(0), 16777619);
  return hash >>> 0;
}

function hexColor(hue: number, saturation: number, lightness: number): string {
  const amplitude = saturation * Math.min(lightness, 1 - lightness);
  const channel = (offset: number) => {
    const position = (offset + hue / 30) % 12;
    const value = lightness - amplitude * Math.max(-1, Math.min(position - 3, 9 - position, 1));
    return Math.round(255 * value).toString(16).padStart(2, "0");
  };
  return `#${channel(0)}${channel(8)}${channel(4)}`;
}

function generatedColor(key: string, provider?: Provider): string {
  const existing = allocated.get(key);
  if (existing) return existing;
  for (let attempt = 0; ; attempt++) {
    const hash = hashName(`${key}:${attempt}`);
    const hue = provider ? (catalog.providers[provider].hue + (hash % 17) - 8 + 360) % 360 : hash % 360;
    const color = hexColor(hue, (55 + ((hash >>> 8) % 30)) / 100, (35 + ((hash >>> 16) % 31)) / 100);
    if (reserved.has(color)) continue;
    reserved.add(color);
    allocated.set(key, color);
    return color;
  }
}

export function chartSeriesStyle(scope: string, name: string): SeriesStyle {
  if (scope === "model_family") {
    const key = modelColorKey(name), model = Object.hasOwn(models, key) ? models[key] : undefined, provider = model?.provider ?? modelProvider(key);
    const modelClass = model?.class ?? (/codex/.test(key) ? "Codex" : /gemini.*flash/.test(key) ? "Flash" : /gemini.*pro/.test(key) ? "Pro" : key.split(/[-.]/)[0]);
    return {
      color: model?.color ?? generatedColor(`${scope}:${key}`, provider),
      group: catalog.providers[provider].label,
      order: `${providerOrder.indexOf(provider)}:${modelClass}:${key}`,
    };
  }
  if (scope === "provider") {
    const source = Object.hasOwn(sources, name) ? sources[name] : undefined;
    const provider = source?.provider ?? (Object.hasOwn(catalog.providers, name) ? name as Provider : "other");
    return {
      color: source?.color ?? generatedColor(`${scope}:${name}`, provider),
      group: catalog.providers[provider].label,
      order: `${providerOrder.indexOf(provider)}:${name}`,
    };
  }
  return { color: generatedColor(`${scope}:${name}`) };
}
