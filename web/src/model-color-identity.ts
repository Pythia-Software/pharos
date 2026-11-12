// Shared by the runtime palette and catalog generator. Identity normalization
// never rewrites the source-recorded model string.
export const modelColorKey = (name: string) => name.trim().toLowerCase().replace(/^claude-/, "").replace(/(-1m|\[1m\])$/, "").replace(/-\d{8}$/, "");
export function inferModelProvider(name: string): string {
  const key = modelColorKey(name);
  if (/^(opus|sonnet|haiku|fable|mythos)(-|$)|^\d+(?:[-.]\d+)*-(opus|sonnet|haiku)(-|$)/.test(key)) return "anthropic";
  if (/^(gemini|gemma)(-|$)/.test(key)) return "google";
  if (/^(gpt|chatgpt|codex|o[134])(-|$)/.test(key)) return "openai";
  return "other";
}
export function inferModelClass(name: string): string | undefined {
  const key = modelColorKey(name);
  const claude = key.match(/^(opus|sonnet|haiku|fable|mythos)(?:-|$)|^\d+(?:[-.]\d+)*-(opus|sonnet|haiku)(?:-|$)/);
  if (claude) { const value = claude[1] ?? claude[2]; return value === "fable" || value === "mythos" ? "Fable / Mythos" : value[0].toUpperCase() + value.slice(1); }
  if (/^o[134](?:-|$)/.test(key)) return "Reasoning";
  if (/^(gpt|codex|chatgpt)(-|$)/.test(key)) {
    if (/codex/.test(key)) return "Codex";
    if (/rosalind/.test(key)) return "Rosalind";
    if (/^gpt-image(?:-|$)/.test(key)) return "Image";
    if (/cyber/.test(key)) return "Cyber";
    const tier = key.match(/-(luna|terra|sol|astra)(?:-|$)/)?.[1];
    return tier ? tier[0].toUpperCase() + tier.slice(1) : "GPT";
  }
  if (/^gemini(?:-|$)/.test(key)) {
    if (/flash/.test(key)) return "Flash";
    if (/(?:^|-)pro(?:-|$)/.test(key)) return "Pro";
    return /nano-banana/.test(key) ? "Nano Banana" : "Gemini";
  }
  if (/^(?:deepseek|reflection\/beam|big-pickle|mai-code)/.test(key)) return /^deepseek/.test(key) ? "DeepSeek" : /^reflection/.test(key) ? "Beam" : /^big-pickle/.test(key) ? "Big Pickle" : "MAI Code";
  return undefined;
}
type ClassMatchers = Record<string, { classes: Record<string, { luminance: number[]; match?: string }> }>;
// New classes can add a normalized-name matcher in data rather than editing
// the built-in recognizers. Ambiguous matches require an explicit correction.
export function matchModelClass(name: string, providers: ClassMatchers, provider?: string): { provider: string; class: string } | undefined {
  const key = modelColorKey(name), matches: {provider: string; class: string}[] = [];
  for (const [owner, rule] of Object.entries(providers)) {
    if (provider && provider !== owner) continue;
    for (const [family, definition] of Object.entries(rule.classes)) {
      if (definition.match && new RegExp(definition.match).test(key)) matches.push({provider: owner, class: family});
    }
  }
  if (matches.length > 1) throw new Error(`Ambiguous model color class rules for ${name}`);
  return matches[0];
}
export type ColorPrice = { model: string; provider: string; effective_from: string; status?: string };
export function modelCatalogDates(changes: readonly ColorPrice[]): Map<string, string> {
  const dates = new Map<string, string>();
  for (const price of changes) {
    if (price.status === "rejected") continue;
    const key = modelColorKey(price.model), previous = dates.get(key);
    if (!previous || price.effective_from < previous) dates.set(key, price.effective_from);
  }
  return dates;
}
