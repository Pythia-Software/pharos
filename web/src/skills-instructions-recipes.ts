import type { AggregationClause, SummaryColumn, WhereTerm } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";

const nonempty = (field: string): WhereTerm[] => [{ field, op: "is_not_null", value: "" }, { field, op: "!=", value: "" }];
const ok: WhereTerm = { field: "status", op: "=", value: "ok" };
const named = nonempty("skill_name");
const records: SummaryColumn = { id: "si_records", op: "count", label: "Evidence records" };
const conversations: SummaryColumn = { id: "si_conversations", op: "count_distinct", field: "conversation_id", label: "Recorded conversations" };
const fileLoads: SummaryColumn = { id: "si_loads", op: "count_distinct", field: "tool_call_id", label: "Distinct successful file-load calls" };
const bodyWhere: WhereTerm[] = [ok, ...named, { field: "content_bytes", op: ">", value: "0" }];
const loadWhere: WhereTerm[] = [ok, ...named, { field: "evidence_type", op: "=", value: "file_load" }, ...nonempty("tool_call_id"), ...nonempty("skill_path"), ...nonempty("conversation_id")];
const loadKeys = ["skill_name", "skill_path", "conversation_id"];
const evidenceNote = "Evidence records distinguish explicit invocations, file-load attempts, and harness-reported loads. A listing is not use, a recorded load does not prove the instructions were followed, and multiple evidence types can describe related activity. Names can combine versions or skills with the same name. Catalog mirror exclusion applies; remaining source capture can differ between agents.";
const sizeNote = "content_bytes measures a retained skill-body observation. Many successful file loads have no body size; missing values are not zero. Summing known sizes does not measure all instruction exposure, unique instruction text, model tokens, context carriage, or compliance. Evidence types remain separate; repeated observed bodies contribute again.";
const repeatNote = "Count distinct successful file-load tool_call_id values per recorded conversation and skill path. Duplicate evidence for one parent call counts once within that path. Harness snapshots and explicit invocations are excluded. A conversation can span multiple agent streams; paths and bodies may change. Counts include the first load. Repetition does not establish wasted work, identical bodies, or a compaction cause.";

function card(id: string, title: string, description: string, keys: string[], summaries: SummaryColumn[], action: string, unit = "records", extra: Partial<QueryRecipe> = {}): QueryRecipe {
  return { id, title, description, category: "skills", idea: "skills-01", dataset: "skill_usages", field: "created_at", days: [7, 30], unit, measures: [], action, where: named, note: evidenceNote,
    exploration: { keys, summaries, valueField: summaries[0].id, unit, subjects: "skill groups", action, labelFields: keys },
    cover: { mode: "result", metricId: summaries[0].id, kind: "bar-horizontal", maxGroups: 6, showOther: false }, ...extra };
}
const bodyBox: AggregationClause = { id: "si_body_box", op: "count", field: "content_bytes", groupBy: ["skill_name", "evidence_type"], groupLimit: 10000, sort: [{ key: "samples", dir: "desc" }], distribution: { kind: "box", input: "[content_bytes]", whiskers: "tukey" }, label: "Retained skill body sizes", display: { kind: "box", xLabel: "Retained body size (bytes)", xScale: { mode: "log" } } };
const coverage: SummaryColumn = { id: "si_size_coverage", op: "formula", expression: "SUM(IF([content_bytes]>=0,1,0))/NULLIF(COUNT(),0)", label: "Recorded body-size coverage" };
const loadSummary: SummaryColumn[] = [fileLoads, { id: "si_title", op: "row", field: "title", label: "Work context" }, { id: "si_repo", op: "row", field: "repository_name", label: "Repository" }, { id: "si_call", op: "row", field: "tool_call_id", label: "Latest observed load call" }];
const latest = { orderBy: [{ field: "created_at", dir: "desc" as const, nulls: "last" as const }, { field: "id", dir: "asc" as const, nulls: "last" as const }] };

export const skillsInstructionsRecipes: QueryRecipe[] = [
  card("skill-evidence-mix", "Skill evidence mix", "Recorded skill evidence over the last {days} days, split into invocations, file-load attempts, and harness loads.", ["evidence_type", "status"], [records], "Inspect skill evidence types", "records", {
    exploration: { keys: ["evidence_type", "status"], summaries: [records], valueField: records.id, unit: "records", subjects: "evidence/status groups", action: "Inspect skill evidence types", labelFields: ["evidence_type", "status"],
      coverMetric: { id: records.id, op: "sum", field: records.id, groupBy: ["evidence_type"], groupLimit: 10000, label: "Recorded skill evidence", display: { kind: "pie" } } },
    cover: { mode: "result", metricId: records.id, kind: "pie", maxGroups: 3, showOther: true, showLegend: true, showHeadline: false },
    note: `${evidenceNote} Include successful, error, and no-result observations. Each retained evidence record counts once; this is an evidence-record composition, not a share of unique uses or of all agent conversations. The destination separates status within evidence type.`,
  }),
  card("skill-body-sizes", "Skill body sizes", "Positive retained skill-body sizes over the last {days} days, separated by skill and evidence type; three-record minimum.", ["skill_name", "evidence_type"], [{ id: "si_body_median", op: "formula", expression: "MEDIAN([content_bytes])", label: "Median retained body bytes" }, records, { id: "si_body_max", op: "max", field: "content_bytes", label: "Largest retained body bytes" }], "Explore retained skill body sizes", "bytes", {
    idea: "context-07", days: [30, 90], where: bodyWhere, measures: [bodyBox], cover: { mode: "source", metricId: bodyBox.id, kind: "box", maxGroups: 6, minSamples: 3, density: "compact", sampleUnit: "body records" },
    note: `${sizeNote} One successful, positive-size body observation is one sample. Require three observations per skill/evidence cohort before selecting six cohorts by sample count. Compact boxes share one log-byte axis. All cohorts, including sparse ones, remain inspectable.`,
  }),
  card("skill-size-coverage", "Skill body size coverage", "Share of successful skill evidence with recorded body sizes over the last {days} days, by evidence type.", ["evidence_type"], [coverage, { id: "si_sized", op: "formula", expression: "SUM(IF([content_bytes]>=0,1,0))", label: "Records with known body sizes" }, records], "Inspect body-size recording coverage", "percent", {
    idea: "context-07", where: [ok, ...named], note: `${sizeNote} Divide successful evidence records with nonnegative content_bytes by all successful evidence records within each evidence type. Zero-byte observations count as measured; negative and missing sizes do not. No sample cutoff hides sparse evidence types. This measures recording coverage within observed evidence, not the percentage of all agent activity covered by skills.`,
  }),
  card("skill-load-distribution", "Skill loads per conversation", "Distinct successful file loads per conversation and skill path over the last {days} days; three-sample minimum.", loadKeys, loadSummary, "Explore skill loading across conversations", "loads", {
    idea: "skills-05", where: loadWhere, note: `${repeatNote} Each observed conversation/path pair is one sample. Single-load pairs remain in the distribution; zero-load conversations are absent. Group pairs by recorded skill name, require three pairs before choosing six cohorts by sample count. Multiple paths with the same skill name remain separate samples and are pooled in the cover. All samples remain in the destination. Boxes share a logarithmic count axis.`,
    exploration: { keys: loadKeys, summaries: loadSummary, valueField: fileLoads.id, unit: "loads", subjects: "conversation/path groups", action: "Explore skill loading across conversations", labelFields: ["skill_name", "si_title", "skill_path"], representative: latest,
      coverMetric: { id: fileLoads.id, op: "count", field: fileLoads.id, expression: `MEDIAN([${fileLoads.id}])`, groupBy: ["skill_name"], groupLimit: 10000, sort: [{ key: "samples", dir: "desc" }], distribution: { kind: "box", input: `[${fileLoads.id}]`, whiskers: "tukey" }, label: "Successful skill file loads", display: { kind: "box", xLabel: "Loads / conversation-path", xScale: { mode: "log" } } } },
    cover: { mode: "result", metricId: fileLoads.id, kind: "box", maxGroups: 6, minSamples: 3, density: "compact", sampleUnit: "conversation/path groups" },
  }),
  card("largest-skill-load-count", "Most skill file loads", "Largest successful file-load count for one conversation and skill path over the last {days} days.", loadKeys, loadSummary, "Find the heaviest skill loading", "loads", {
    idea: "skills-05", where: loadWhere, note: `${repeatNote} Display the maximum across observed conversation/path counts, without a sample cutoff. The destination ranks all pairs, retaining work context, repository, path, and a latest-load call ID. This is a count of file-load calls, not the number of reloads after the first or a measure of bytes.`,
    exploration: { keys: loadKeys, summaries: loadSummary, valueField: fileLoads.id, unit: "loads", subjects: "conversation/path groups", action: "Find the heaviest skill loading", labelFields: ["skill_name", "si_title", "skill_path"], representative: latest,
      coverMetric: { id: fileLoads.id, op: "max", field: fileLoads.id, groupBy: [], label: "file-load calls", display: { kind: "auto", format: { kind: "number", decimals: 0 } } } },
    cover: { mode: "result", metricId: fileLoads.id, kind: "auto", height: 200 },
  }),
];

export function refineSkillsInstructionsRecipe(recipe: QueryRecipe): QueryRecipe {
  if (["skill-evidence-mix", "instruction-bytes", "skill-size-coverage", "skill-reloads", "largest-skill-load-count"].includes(recipe.id)) recipe = { ...recipe, retired: true };
  if (!["skill-evidence", "skill-reach", "instruction-bytes", "skill-reloads"].includes(recipe.id)) return recipe;
  const view = recipe.exploration!;
  const next = { ...recipe, days: [7, 30], cover: { mode: "result" as const, metricId: view.valueField, kind: "bar-horizontal" as const, maxGroups: 6, showOther: false } };
  if (recipe.id === "skill-evidence") return { ...next, title: "Skills across conversations", description: "Distinct recorded conversations with successful skill evidence over the last {days} days, ranked by skill name.", where: [ok, ...named, ...nonempty("conversation_id")], unit: "conversations", action: "Inspect conversations using each skill", note: `${evidenceNote} Count each nonempty recorded conversation ID once per skill across successful evidence of any type. Repeated loads do not increase reach. The denominator excludes conversations with no recorded skill evidence; these are counts, not an adoption rate.`,
    exploration: { ...view, summaries: [view.summaries.find(s => s.field === "conversation_id")!, ...view.summaries.filter(s => s.field !== "conversation_id")], valueField: "card_conversation_id", unit: "conversations", action: "Inspect conversations using each skill", context: [{ field: "card_count", label: "successful records" }] }, cover: { ...next.cover, metricId: "card_conversation_id" } };
  if (recipe.id === "skill-reach") return { ...next, title: "Skills across repositories", description: "Named repositories with successful skill evidence over the last {days} days; skills recorded in at least two.", where: [ok, ...named, ...nonempty("repository_name")], action: "Inspect skills across repositories", emptyLabel: "No skills recorded across at least two named repositories", note: `${evidenceNote} Count distinct nonempty repository names per skill among successful evidence. Require two named repositories before ranking six skills; retain conversation reach and evidence counts in the destination. This is repository breadth, not an adoption rate or proof of usefulness.`, exploration: { ...view, action: "Inspect skills across repositories" } };
  if (recipe.id === "instruction-bytes") return { ...next, title: "Recorded skill body volume", description: "Sum of known positive skill-body bytes over the last {days} days, separated by skill and evidence type.", days: [30, 90], where: bodyWhere, action: "Inspect recorded instruction volume", note: sizeNote, exploration: { ...view, action: "Inspect recorded instruction volume" } };
  return { ...next, title: "Repeated skill file loads", description: "Conversation/path pairs with at least two successful skill file-load calls over the last {days} days.", where: loadWhere, action: "Inspect repeated skill file loads", note: repeatNote,
    exploration: { ...view, summaries: [...view.summaries, { id: "si_skill", op: "row", field: "skill_name", label: "Recorded skill name" }], labelFields: ["si_skill", "card_title", "repository_name"], action: "Inspect repeated skill file loads" } };
}
