import type { AnalysisCatalog, AnalysisMeasure, AnalysisState, DerivedTotals } from "./dashboard-analysis-model";
import { tokenAnalysisCatalog } from "./token-analysis-catalog";

// Carbon over the usage ledger: one record is one agent session's usage of
// one model and effort on one recorded day, as in Tokens and Cost. Its
// energy_wh and co2e_g are estimated by the service from the record's token
// counts and model tier under the Carbon assumptions a request names
// (carbon_settings), with the arithmetic of the Carbon page's calculator
// (carbon.go withCarbon, carbon.js calculate). The catalog sends the user's
// current assumptions with every request, so the stored estimates are the
// user's, not the defaults.

export type CarbonCounts = Record<string, Record<string, number>>;
export type CarbonFactors = { tiers: Array<{ key: string; label: string }> };
export type CarbonEstimate = { pue: number; byCategory: Record<string, { wh: number }>; grams: (wh: number) => number };
// The page's calculator (window.pharosCarbon) and its current settings.
export type CarbonAssumptions = { settings: Record<string, unknown>; factors: CarbonFactors; estimate: (factors: CarbonFactors, period: { tokens: CarbonCounts }) => CarbonEstimate };

// The calculator's token categories. Unclassified tokens are charged as
// uncached input, the costlier input kind, as carbon.go does.
export const carbonCategories = [
  { key: "uncached_input", label: "Uncached input", color: "var(--heat-3)", fields: ["uncached_input_tokens", "unclassified_tokens"] },
  { key: "cache_read", label: "Cache reads", color: "var(--heat-1)", fields: ["cache_read_input_tokens"] },
  { key: "cache_write", label: "Cache writes", color: "var(--gold)", fields: ["cache_creation_input_tokens"] },
  { key: "output", label: "Output", color: "var(--heat-2)", fields: ["output_tokens"] },
];
const inputs = carbonCategories.flatMap(category => category.fields);

// Each period's token totals by model tier, estimated with the user's
// factors: CO₂e in grams, or facility electricity (IT energy × PUE) in Wh.
export function deriveCarbon(assumptions: CarbonAssumptions | undefined, totals: DerivedTotals, metric: string): Record<string, number> {
  if (!assumptions) return {};
  const tokens: CarbonCounts = {};
  for (const { keys, values } of totals) {
    const tier = (tokens[String(keys[0] ?? "")] ??= {});
    for (const category of carbonCategories) tier[category.key] = (tier[category.key] ?? 0) + category.fields.reduce((sum, field) => sum + (values[field] ?? 0), 0);
  }
  const result = assumptions.estimate(assumptions.factors, { tokens });
  return Object.fromEntries(carbonCategories.map(category => {
    const wh = result.byCategory[category.key]?.wh ?? 0;
    return [category.key, metric === "energy" ? wh * result.pue : result.grams(wh)];
  }));
}

const grams = { kind: "number", decimals: 1 } as const, tokens = { kind: "number", decimals: 0 } as const;
const assumptionsNote = "Estimated with your Carbon Impact assumptions (estimate range, facility overhead, grid and input sensitivity); token counts are reported, energy and CO₂e are modeled. Models are assigned to size tiers by name; unmatched models use the default tier.";
const measures: Record<string, AnalysisMeasure> = {
  co2: { label: "Estimated CO₂e (g)", unit: "g CO₂e", format: grams, op: "sum", field: "co2e_g", value: "co2e_g", additive: true, note: assumptionsNote },
  energy: { label: "Estimated electricity (Wh)", unit: "Wh", format: grams, op: "sum", field: "energy_wh", value: "energy_wh", additive: true, note: `Facility electricity: IT energy times facility overhead (PUE). ${assumptionsNote}` },
  tokens: { label: "Tokens", unit: "tokens", format: tokens, op: "sum", field: "total_tokens", value: "total_tokens", additive: true, note: "Reported tokens behind the estimates, including cached input and reported reasoning output." },
};

// The old chart's model tier split.
const legacy = (state: AnalysisState): AnalysisState => state.split === "tier" ? { ...state, split: "carbon_tier" } : state;

// The catalog under the user's current assumptions. Without them (link
// validation, chart comparison) it has the same measures and breakdowns and
// sends no assumptions; it is never drawn.
export function carbonAnalysisCatalog(assumptions?: CarbonAssumptions): AnalysisCatalog {
  const { breakdowns } = tokenAnalysisCatalog;
  return {
    ...tokenAnalysisCatalog, measures, legacy, legacySlices: false,
    breakdowns: {
      carbon_tier: { label: "Model tier", fields: ["carbon_tier"], order: assumptions?.factors.tiers.map(tier => tier.key) },
      provider: breakdowns.provider, repository_name: breakdowns.repository_name, model_family: breakdowns.model_family, provider_model: breakdowns.provider_model, effort: breakdowns.effort, model_effort: breakdowns.model_effort,
      channel: breakdowns.channel, machine_id: breakdowns.machine_id, execution_machine_id: breakdowns.execution_machine_id, session_kind: breakdowns.session_kind,
    },
    components: { type: { label: "Token type", measures: ["co2", "energy"], fallback: "carbon_tier",
      parts: carbonCategories.map(({ key, label, color, fields }) => ({ key, label, color, fields })),
      derived: { inputs: { fields: inputs, groupBy: ["carbon_tier"] }, derive: (totals, metric) => deriveCarbon(assumptions, totals, metric) } } },
    request: assumptions ? { params: { carbon_settings: JSON.stringify(assumptions.settings) } } : undefined,
    trend: { metric: "co2", split: "type" },
    scatter: ["tokens", "co2"],
  };
}
// For validation and comparison only.
export const carbonCatalogShape = carbonAnalysisCatalog();
