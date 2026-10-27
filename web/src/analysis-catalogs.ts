import { normalizeAnalysis, type AnalysisCatalog, type AnalysisState } from "./dashboard-analysis-model";
import { tokenAnalysisCatalog } from "./token-analysis-catalog";
import { toolsAnalysisCatalog } from "./tools-analysis-catalog";
import { mcpAnalysisCatalog } from "./mcp-analysis-catalog";
import { agentTimeAnalysisCatalog, skillsAnalysisCatalog } from "./record-analysis-catalogs";

// The engine dashboards, by Analyze topic.
export const analysisCatalogs: Partial<Record<string, AnalysisCatalog>> = {
  tokens: tokenAnalysisCatalog, tools: toolsAnalysisCatalog, mcp: mcpAnalysisCatalog, time: agentTimeAnalysisCatalog, skills: skillsAnalysisCatalog,
};

// A dashboard chart as it draws, whatever version or vocabulary wrote it, so
// two links to the same view compare equal. Other dashboards' charts compare
// as written.
export function normalizedDashboardChart(topic: string | null, chart: string | null): string {
  const catalog = topic ? analysisCatalogs[topic] : undefined;
  if (!catalog || !chart) return chart ?? "";
  try {
    const { v: _, ...state } = normalizeAnalysis(catalog, JSON.parse(chart) as AnalysisState);
    return JSON.stringify(state, Object.keys(state).sort());
  } catch { return chart; }
}
