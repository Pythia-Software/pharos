import { linear, perceptualColor } from "./model-color-math";
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
type ClassRule = { luminance: number[]; single_luminance?: number; hue?: number; saturation?: number; match?: string };
export type ColorRules = {
  version: number;
  lightness_bounds: number[];
  minimum_version_step: number;
  contemporary_step: number;
  observed_dates?: Record<string, string>;
  providers: Record<string, { hue: number; saturation: number; pricing_provider?: string; ranked_classes: string[]; classes: Record<string, ClassRule> }>;
};
function colorAtLevel(hue: number, saturation: number, level: number): string {
  const channels = (lightness: number) => {
    const a = saturation * Math.min(lightness, 1 - lightness);
    return [0, 8, 4].map(offset => { const k = (offset + hue / 30) % 12; return lightness - a * Math.max(-1, Math.min(k - 3, 9 - k, 1)); });
  };
  let low = 0, high = 1;
  for (let i = 0; i < 48; i++) {
    const middle = (low + high) / 2;
    const luminance = channels(middle).reduce((sum, value, index) => sum + linear(value) * [.2126, .7152, .0722][index], 0);
    if (luminance < level ** 3) low = middle; else high = middle;
  }
  return "#" + channels((low + high) / 2).map(value => Math.round(value * 255).toString(16).padStart(2, "0")).join("");
}

function rampColor(first: string, last: string, position: number): string {
  if (position === 0) return first;
  if (position === 1) return last;
  const start = perceptualColor(first), end = perceptualColor(last);
  const [L, a, b] = start.map((value, index) => value * (1 - position) + end[index] * position);
  const l = (L + .3963377774 * a + .2158037573 * b) ** 3;
  const m = (L - .1055613458 * a - .0638541728 * b) ** 3;
  const s = (L - .0894841775 * a - 1.2914855480 * b) ** 3;
  return "#" + [4.0767416621 * l - 3.3077115913 * m + .2309699292 * s, -1.2684380046 * l + 2.6097574011 * m - .3413193965 * s, -.0041960863 * l - .7034186147 * m + 1.7076147010 * s]
    .map(value => Math.round(255 * Math.max(0, Math.min(1, value <= .0031308 ? value * 12.92 : 1.055 * value ** (1 / 2.4) - .055)))).map(value => value.toString(16).padStart(2, "0")).join("");
}
type Constraint = { terms: [number, number][]; minimum: number };
// Project the preferred class endpoints onto linear ordering/spacing constraints.
// Dykstra's corrections avoid depending on the order constraints are visited.
// The variables are cube-root luminance: equal steps approximate equal visual
// depth. The endpoints seed one straight perceptual ramp for each class.
function fitEndpoints(preferred: number[], constraints: Constraint[]): number[] | undefined {
  const values = [...preferred], corrections = constraints.map(() => 0);
  for (let iteration = 0; iteration < 12000; iteration++) {
    const previous = [...values];
    for (let i = 0; i < constraints.length; i++) {
      const { terms, minimum } = constraints[i];
      for (const [index, weight] of terms) values[index] += corrections[i] * weight;
      const distance = minimum - terms.reduce((sum, [index, weight]) => sum + values[index] * weight, 0);
      const shift = Math.max(0, distance) / terms.reduce((sum, [, weight]) => sum + weight * weight, 0);
      for (const [index, weight] of terms) values[index] += shift * weight;
      corrections[i] = -shift;
    }
    if (values.every((value, index) => Math.abs(value - previous[index]) < 1e-9)
      && constraints.every(({ terms, minimum }) => terms.reduce((sum, [index, weight]) => sum + values[index] * weight, 0) >= minimum - 1e-8)) return values;
  }
  return undefined;
}

export function generateModelColors(input: ColorCatalog, changes: readonly ColorPrice[], rules: ColorRules): { catalog: ColorCatalog; warnings: string[]; versionSteps: Record<string, number> } {
  if (rules.version !== 1 || input.version !== 1) throw new Error("Unsupported model color rules/catalog version");
  const [floor, ceiling] = rules.lightness_bounds;
  if (!(floor > 0 && ceiling < 1 && floor < ceiling && rules.lightness_bounds.length === 2 && Number.isFinite(rules.minimum_version_step) && rules.minimum_version_step > 0 && Number.isFinite(rules.contemporary_step) && rules.contemporary_step > 0)) throw new Error("Invalid model color spacing/bounds");
  const catalog: ColorCatalog = JSON.parse(JSON.stringify(input)), dates = modelTimelineDates(changes, rules.observed_dates), warnings: string[] = [], versionSteps: Record<string, number> = {};
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
  const used = new Set<string>();
  for (const [provider, providerRule] of Object.entries(rules.providers)) {
    if (!Object.hasOwn(catalog.providers, provider) || !(providerRule.hue >= 0 && providerRule.hue < 360 && providerRule.saturation > 0 && providerRule.saturation < 1)) throw new Error(`Invalid provider color rule: ${provider}`);
    const classNames = Object.keys(providerRule.classes);
    if (new Set(providerRule.ranked_classes).size !== providerRule.ranked_classes.length || providerRule.ranked_classes.some(name => !Object.hasOwn(providerRule.classes, name))) throw new Error(`Invalid capability order for ${provider}`);
    catalog.class_order[provider] = [...providerRule.ranked_classes, ...classNames.filter(name => !providerRule.ranked_classes.includes(name))];
    const classes = classNames.map((name, index) => {
      const rule = providerRule.classes[name];
      if (rule.match !== undefined) {
        try { if (!rule.match) throw new Error(); new RegExp(rule.match); }
        catch { throw new Error(`Invalid model name matcher for ${provider} ${name}`); }
      }
      if (!(rule.luminance.length === 2 && rule.luminance[0] >= rule.luminance[1] && rule.luminance.every(value => value > 0 && value < 1)
        && (rule.single_luminance === undefined || rule.single_luminance > 0 && rule.single_luminance < 1)
        && (rule.hue === undefined || rule.hue >= 0 && rule.hue < 360)
        && (rule.saturation === undefined || rule.saturation > 0 && rule.saturation < 1))) throw new Error(`Invalid ramp for ${provider} ${name}`);
      const names = Object.keys(catalog.models).filter(key => catalog.models[key].provider === provider && catalog.models[key].class === name).sort();
      const releases = [...new Set(names.flatMap(key => dates.has(key) ? [dates.get(key)!] : []))].sort();
      return { name, index, rule, names, releases };
    });
    const preferred = classes.flatMap(({ rule, releases }) => releases.length > 1 ? rule.luminance.map(Math.cbrt) : [0, 1].map(() => Math.cbrt(rule.single_luminance ?? (rule.luminance[0] + rule.luminance[1]) / 2)));
    const point = (entry: typeof classes[number], rank: number): [number, number][] => {
      const position = entry.releases.length > 1 ? rank / (entry.releases.length - 1) : 0;
      return [[entry.index * 2, 1 - position], [entry.index * 2 + 1, position]].filter(([, weight]) => weight !== 0) as [number, number][];
    };
    const ranked = providerRule.ranked_classes.map(name => classes.find(entry => entry.name === name)!);
    const eraDates = [...new Set(ranked.flatMap(entry => entry.releases))].sort();
    let fitted: number[] | undefined, spacing = rules.minimum_version_step;
    // A finite color gamut cannot separate arbitrarily many releases. Prefer
    // the requested spacing, relax it only when infeasible, and report that
    // crowding explicitly rather than emitting invalid colors or zigzags.
    for (let attempt = 0; attempt < 12 && !fitted; attempt++, spacing *= .9) {
      const constraints: Constraint[] = [];
      for (const entry of classes) {
        for (const endpoint of [entry.index * 2, entry.index * 2 + 1]) {
          constraints.push({ terms: [[endpoint, 1]], minimum: floor }, { terms: [[endpoint, -1]], minimum: -ceiling });
        }
        const difference: [number, number][] = [[entry.index * 2, 1], [entry.index * 2 + 1, -1]];
        constraints.push({ terms: difference, minimum: spacing * Math.max(0, entry.releases.length - 1) });
        if (entry.releases.length < 2) constraints.push({ terms: difference.map(([index, weight]) => [index, -weight]), minimum: 0 });
      }
      for (const date of eraDates) {
        const latest = ranked.map(entry => ({ entry, rank: entry.releases.filter(release => release <= date).length - 1 })).filter(item => item.rank >= 0);
        for (let i = 1; i < latest.length; i++) constraints.push({ terms: [...point(latest[i - 1].entry, latest[i - 1].rank), ...point(latest[i].entry, latest[i].rank).map(([index, weight]) => [index, -weight] as [number, number])], minimum: rules.contemporary_step });
      }
      fitted = fitEndpoints(preferred, constraints);
      if (fitted) versionSteps[provider] = spacing;
      if (fitted && attempt) warnings.push(`${provider}: dense release history; version spacing reduced to ${(spacing).toFixed(3)} (requested ${rules.minimum_version_step})`);
    }
    if (!fitted) throw new Error(`Cannot fit ${provider} capability ordering inside the configured color range`);
    for (const entry of classes) {
      const [oldest, newest] = fitted.slice(entry.index * 2, entry.index * 2 + 2);
      const hue = entry.rule.hue ?? providerRule.hue, saturation = entry.rule.saturation ?? providerRule.saturation;
      for (const key of entry.names.sort((a, b) => (dates.get(a) ?? "9999").localeCompare(dates.get(b) ?? "9999") || a.localeCompare(b))) {
        const rank = entry.releases.indexOf(dates.get(key) ?? ""), position = rank < 0 ? .5 : entry.releases.length > 1 ? rank / (entry.releases.length - 1) : 0;
        const level = oldest * (1 - position) + newest * position;
        // Interpolate in OKLab rather than HSL: even a wide green ramp must
        // not bend back toward saturation in its middle versions.
        let color = rampColor(colorAtLevel(hue, saturation, oldest), colorAtLevel(hue, saturation, newest), position);
        // Same-date siblings and undated aliases share a class/date position.
        // Small deterministic offsets distinguish them without changing the
        // chronological ramp or consulting their previous assigned colors.
        for (let offset = 1; used.has(color) && offset < 200; offset++) {
          const delta = .002 * Math.ceil(offset / 2) * (offset % 2 ? 1 : -1);
          color = colorAtLevel(hue, saturation, Math.max(floor, Math.min(ceiling, level + delta)));
        }
        if (used.has(color)) throw new Error(`Too many indistinguishable model colors near ${key}`);
        catalog.models[key].color = color;
        used.add(color);
      }
    }
  }
  return { catalog, warnings, versionSteps };
}
