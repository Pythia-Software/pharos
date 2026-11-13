import { fromLCH, gamutChroma, lch } from "./model-color-math";
import type { ColorCatalog, ColorRules } from "./model-color-generation";

export type PalettePolicy = { vividness: number; tierWeight: number; lightness: number; spread: number; specialistSpread: number };
export type ColorPosition = { tier: number; recency: number; ranked: boolean; hueOffset: number; siblingOffset: number; chromaScale?: number };
const clamp = (value: number, low: number, high: number) => Math.max(low, Math.min(high, value));
const angle = (value: number) => ((value + 180) % 360 + 360) % 360 - 180;

// Rank only reviewed capability classes and distinct catalog/observed dates.
// Undated catalog labels use the midpoint; usage never rebalances colors.
export function modelColorPositions(catalog: ColorCatalog, dates: Map<string, string>, rules: ColorRules): Map<string, ColorPosition> {
  const positions = new Map<string, ColorPosition>();
  for (const [provider, definition] of Object.entries(rules.providers)) {
    const entries = Object.entries(catalog.models).filter(([, model]) => model.provider === provider);
    const specialists = Object.keys(definition.classes).filter(name => !definition.ranked_classes.includes(name) && entries.some(([, model]) => model.class === name));
    const offsets = specialists.map(name => angle((definition.classes[name].hue ?? definition.hue) - definition.hue));
    const span = Math.max(...offsets) - Math.min(...offsets);
    for (const [family, rule] of Object.entries(definition.classes)) {
      const index = definition.ranked_classes.indexOf(family), ranked = index >= 0;
      const tier = !ranked || definition.ranked_classes.length < 2 ? .5 : index / (definition.ranked_classes.length - 1);
      const names = entries.filter(([, model]) => model.class === family).map(([name]) => name).sort();
      const releases = [...new Set(names.flatMap(name => dates.has(name) ? [dates.get(name)!] : []))].sort();
      const original = angle((rule.hue ?? definition.hue) - definition.hue);
      const hueOffset = ranked || !definition.ranked_classes.length ? original : span > 0 ? original * rules.palette.specialistSpread / span
        : specialists.length < 2 ? rules.palette.specialistSpread / 2 : 180 + (specialists.indexOf(family) / (specialists.length - 1) - .5) * rules.palette.specialistSpread;
      for (const name of names) {
        const date = dates.get(name), siblings = names.filter(other => dates.get(other) === date);
        positions.set(name, {
          tier, ranked, hueOffset,
          recency: !date ? .5 : releases.length < 2 ? 1 : releases.indexOf(date) / (releases.length - 1),
          siblingOffset: siblings.length < 2 ? 0 : (siblings.indexOf(name) / (siblings.length - 1) - .5) * 8,
        });
      }
    }
  }
  return positions;
}

// The reviewed Hue fan formula is shared by generation, provider customization
// and the playground. More capability/freshness means more chroma at a useful
// middle lightness, rather than an ever darker endpoint. Map into sRGB at fixed
// hue/lightness so clipping cannot silently change the intended family.
export function policyColor(base: string, defaultHue: number, position: ColorPosition, policy: PalettePolicy): string {
  const [baseL, baseC, baseH] = lch(base), neutral = baseC < .008;
  const h = (neutral ? defaultHue : baseH) + position.hueOffset + position.siblingOffset + (position.ranked ? (position.tier - .5) * policy.spread : 0);
  const score = position.ranked ? policy.tierWeight * position.tier + (1 - policy.tierWeight) * position.recency : position.recency;
  const anchorL = clamp(policy.lightness + (baseL - .65) * .28, .46, .79);
  const l = clamp(anchorL + (1 - score) * .16, .44, .89);
  const anchorC = neutral ? 0 : clamp(baseC / .16, .18, 1.2) * .22 * policy.vividness;
  const c = Math.min(anchorC, gamutChroma(anchorL, h) * .98) * (.2 + .8 * score) * (position.chromaScale ?? 1);
  return fromLCH(l, c, h);
}
