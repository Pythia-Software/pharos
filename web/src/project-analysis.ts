import type { AnalysisBreakdown, AnalysisMeasure, AnalysisState } from "./dashboard-analysis-model";

// Every dashboard groups by project: a session's repository, else its working
// folder (home shown as ~), else none, for a chat export without a folder
// (docs/projects-and-outcomes.md). A folder is a project as much as a
// repository is, so a library without Git groups as completely as one with it.
export const projectBreakdown = (): AnalysisBreakdown => ({ label: "Project", fields: ["project_name"], empty: "No project" });

// A count of the projects reached, for a catalog's records.
export const projectsReached = (subject: string): AnalysisMeasure => ({ label: `Projects ${subject}`, unit: "projects", format: { kind: "number", decimals: 0 }, op: "count_distinct", field: "project_name", additive: false,
  note: `Distinct projects (repositories or folders) ${subject === "reached" ? "with matching records" : subject}; a project can appear in several groups.` });

// Links and saved states from before projects named repository breakdowns,
// units and measures; each reads as its project counterpart, which holds the
// same groups for repositories and adds the folders that had none.
const legacyIds: Record<string, string> = {
  repository_name: "project_name", root_repository_name: "root_project_name", repository_provider_model: "project_provider_model",
  repository_error: "project_error", repository_command: "project_command", family_repository: "family_project",
  repository: "project", repositories: "projects",
};

// projectLegacy reads those older ids, after the catalog's own legacy mapping
// when it has one.
export function projectLegacy(legacy?: (state: AnalysisState) => AnalysisState): (state: AnalysisState) => AnalysisState {
  return state => {
    const read = legacy ? legacy(state) : state;
    const change: Partial<AnalysisState> = {};
    for (const key of ["split", "unit", "metric", "metricY"] as const) {
      const value = read[key];
      if (value !== undefined && Object.hasOwn(legacyIds, value)) change[key] = legacyIds[value];
    }
    return Object.keys(change).length ? { ...read, ...change } : read;
  };
}
