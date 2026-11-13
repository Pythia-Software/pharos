import { colorDistance } from "./model-color-math";
import { modelColorPositions, policyColor, type PalettePolicy } from "./model-color-policy";
export { colorLuminance as generatedColorLuminance } from "./model-color-math";
import { inferModelClass, matchModelClass, modelTimelineDates, modelColorKey, type ColorPrice } from "./model-color-identity";
export { modelTimelineDates } from "./model-color-identity";

export type ColorCatalog = {
  version: number;
  providers: Record<string, { label: string; color: string; hue: number }>;
  class_order: Record<string, string[]>;
  models: Record<string, { provider: string; class: string; color: string }>;
  sources: Record<string, { provider: string; color: string }>;
};
type ClassRule = { hue?: number; match?: string };
export type ColorRules = {
  version: number;
  palette: PalettePolicy;
  observed_dates?: Record<string, string>;
  providers: Record<string, { hue: number; pricing_provider?: string; ranked_classes: string[]; classes: Record<string, ClassRule> }>;
};
export function generateModelColors(input: ColorCatalog, changes: readonly ColorPrice[], rules: ColorRules): { catalog: ColorCatalog; warnings: string[] } {
  if (rules.version !== 2 || input.version !== 1) throw new Error("Unsupported model color rules/catalog version");
  const p = rules.palette;
  if (!p || ![p.vividness, p.tierWeight, p.lightness, p.spread, p.specialistSpread].every(Number.isFinite)
    || p.vividness < 0 || p.vividness > 1.2 || p.tierWeight < 0 || p.tierWeight > 1 || p.lightness < .5 || p.lightness > .78
    || p.spread < 0 || p.spread > 100 || p.specialistSpread < 0 || p.specialistSpread > 300) throw new Error("Invalid model color policy");
  const catalog: ColorCatalog = JSON.parse(JSON.stringify(input)), dates = modelTimelineDates(changes, rules.observed_dates), warnings: string[] = [];
  // Research supplies dates/provider identities; generation never invents a
  // release date or guesses where a genuinely new capability class belongs.
  for (const price of changes) {
    if (price.status === "rejected") continue;
    const key = modelColorKey(price.model);
    if (!key || !/^\d{4}-\d{2}-\d{2}$/.test(price.effective_from) || !Number.isFinite(Date.parse(price.effective_from)) || new Date(price.effective_from).toISOString().slice(0, 10) !== price.effective_from) throw new Error(`Invalid catalog date/model: ${price.model}`);
    if (!Object.hasOwn(catalog.models, key)) {
      const match = matchModelClass(key, rules.providers, price.provider), provider = match?.provider ?? price.provider;
      const family = match?.class ?? inferModelClass(key);
      if (!family || !Object.hasOwn(rules.providers, provider) || !Object.hasOwn(rules.providers[provider].classes, family)) throw new Error(`Define a model color class/rule for ${price.provider}: ${price.model}`);
      catalog.models[key] = { provider, class: family, color: "" };
    } else if (catalog.models[key].provider !== price.provider && rules.providers[catalog.models[key].provider]?.pricing_provider !== price.provider) throw new Error(`Conflicting provider for ${key}`);
  }
  for (const [key, model] of Object.entries(catalog.models)) {
    if (key !== modelColorKey(key) || !Object.hasOwn(catalog.providers, model.provider) || !Object.hasOwn(rules.providers, model.provider) || !Object.hasOwn(rules.providers[model.provider].classes, model.class)) throw new Error(`Define a normalized model color class/rule for ${key}`);
  }
  for (const [provider, providerRule] of Object.entries(rules.providers)) {
    if (!Object.hasOwn(catalog.providers, provider) || !(providerRule.hue >= 0 && providerRule.hue < 360)) throw new Error(`Invalid provider color rule: ${provider}`);
    const classNames = Object.keys(providerRule.classes);
    if (new Set(providerRule.ranked_classes).size !== providerRule.ranked_classes.length || providerRule.ranked_classes.some(name => !Object.hasOwn(providerRule.classes, name))) throw new Error(`Invalid capability order for ${provider}`);
    catalog.class_order[provider] = [...providerRule.ranked_classes, ...classNames.filter(name => !providerRule.ranked_classes.includes(name))];
    for (const [name, rule] of Object.entries(providerRule.classes)) {
      if (rule.match !== undefined) {
        try { if (!rule.match) throw new Error(); new RegExp(rule.match); }
        catch { throw new Error(`Invalid model name matcher for ${provider} ${name}`); }
      }
      if (rule.hue !== undefined && !(rule.hue >= 0 && rule.hue < 360)) throw new Error(`Invalid hue for ${provider} ${name}`);
    }
  }
  const positions = modelColorPositions(catalog, dates, rules);
  for (const [name, model] of Object.entries(catalog.models)) model.color = policyColor(catalog.providers[model.provider].color, catalog.providers[model.provider].hue, positions.get(name)!, p);
  // Finite gamut can crowd a dense history. Report it rather than darkening
  // frontier colors or claiming a minimum separation that the palette lacks.
  for (const [provider, rule] of Object.entries(rules.providers)) for (const family of Object.keys(rule.classes)) {
    const releases = new Map<string, string>();
    for (const name of Object.keys(catalog.models).sort()) {
      const model = catalog.models[name], date = dates.get(name);
      if (model.provider === provider && model.class === family && date && !releases.has(date)) releases.set(date, model.color);
    }
    const colors = [...releases].sort(([a], [b]) => a.localeCompare(b)).map(([, color]) => color);
    if (colors.some((color, index) => index > 0 && colorDistance(color, colors[index - 1]) < .025)) warnings.push(`${provider} ${family}: neighboring releases are close in the available color gamut`);
  }
  return { catalog, warnings };
}
