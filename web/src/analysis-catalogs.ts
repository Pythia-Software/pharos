import { normalizeAnalysis, type AnalysisCatalog, type AnalysisState } from "./dashboard-analysis-model";
import { conversationAnalysisCatalog } from "./conversation-analysis-catalog";
import { tokenAnalysisCatalog } from "./token-analysis-catalog";
import { toolsAnalysisCatalog } from "./tools-analysis-catalog";
import { mcpAnalysisCatalog } from "./mcp-analysis-catalog";
import { agentTimeAnalysisCatalog, skillsAnalysisCatalog } from "./record-analysis-catalogs";
import { humanAnalysisCatalog } from "./human-analysis-catalog";
import { carbonAnalysisCatalog, carbonCatalogShape, type CarbonAssumptions } from "./carbon-analysis-catalog";
import { parallelismAnalysisCatalog, parallelismCatalogShape, type ParallelismCounting } from "./parallelism-analysis-catalog";

// The engine dashboards, by Analyze topic. Carbon and Parallelism build
// their catalogs with the user's settings; these are their shapes, for
// reading and comparing links.
export const analysisCatalogs: Partial<Record<string, AnalysisCatalog>> = {
  calls: agentTimeAnalysisCatalog, activity: conversationAnalysisCatalog, tokens: tokenAnalysisCatalog, tools: toolsAnalysisCatalog, mcp: mcpAnalysisCatalog, time: agentTimeAnalysisCatalog, skills: skillsAnalysisCatalog,
  human: humanAnalysisCatalog, carbon: carbonCatalogShape, parallelism: parallelismCatalogShape,
};

// The complete catalog a topic draws with, given the user's settings: Carbon
// needs the current assumptions and factors, Parallelism the saved counting.
// Without them, Carbon and Parallelism give their shapes, which read links
// but cannot draw (useAnalysisCatalog supplies them in the page).
export type CatalogSettings = { carbon?: CarbonAssumptions; parallelism?: ParallelismCounting };
export function analysisCatalogFor(topic: string, settings: CatalogSettings = {}): AnalysisCatalog | undefined {
  if (topic === "carbon") return settings.carbon ? carbonAnalysisCatalog(settings.carbon) : undefined;
  if (topic === "parallelism") return settings.parallelism ? parallelismAnalysisCatalog(settings.parallelism) : undefined;
  return analysisCatalogs[topic];
}

// A dashboard chart as it draws, whatever version or vocabulary wrote it, so
// two links to the same view compare equal: defaults are filled in (an
// absent scale is linear, an empty selection is none), and keys are sorted
// at every depth. Other dashboards' charts compare as written.
export function normalizedDashboardChart(topic: string | null, chart: string | null): string {
  const catalog = topic ? analysisCatalogs[topic] : undefined;
  if (!catalog || !chart) return chart ?? "";
  try {
    const { v: _, ...state } = normalizeAnalysis(catalog, JSON.parse(chart) as AnalysisState);
    const drawn: Record<string, unknown> = { ...state, scale: state.scale ?? "linear" };
    if (!state.selection?.values.length) delete drawn.selection;
    return canonical(drawn);
  } catch { return chart; }
}
const canonical = (value: unknown): string => Array.isArray(value) ? `[${value.map(canonical).join(",")}]`
  : value && typeof value === "object" ? `{${Object.keys(value).sort().filter(key => (value as Record<string, unknown>)[key] !== undefined).map(key => `${JSON.stringify(key)}:${canonical((value as Record<string, unknown>)[key])}`).join(",")}}`
  : JSON.stringify(value);
