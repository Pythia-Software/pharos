import type { QueryRecipe } from "./curiosity-data";

// A grouped card can name a summary column that counts rows whose result is
// not established exactly: for example delegated runs whose depth or root
// was reached through an inferred or unverified link, or whose execution
// identity is in an unresolved conflict. A nonzero count qualifies the card's
// own summary (≈) and adds a note; it never hides or drops the rows.
export type CardQualifier = { field: string; label: string };

/**
 * ≈ and a note when any group's qualifier column is positive. Groups are
 * counted, not summed sessions: one session can appear in several groups
 * (a run at two efforts), so a summed session count would overstate it.
 */
export function cardQualification(rows: readonly Record<string, unknown>[] | undefined, qualifier: CardQualifier | undefined, complete: boolean): { mark: string; note: string } | null {
  if (!qualifier || !rows?.length) return null;
  const groups = rows.filter(row => typeof row[qualifier.field] === "number" && Number(row[qualifier.field]) > 0).length;
  if (!groups) return null;
  return { mark: "≈", note: `${complete ? "" : "At least "}${groups.toLocaleString()} ${groups === 1 ? "group includes" : "groups include"} ${qualifier.label}` };
}

// Usage cards over lineage fields (delegation depth, run role, delegated runs
// and every root cohort) read the shared ancestry resolution. A session whose
// root or depth was reached through an inferred or unverified link, or whose
// run identity is in an unresolved conflict, is still counted but qualifies
// the card: delegated_run_depth_quality, root_quality and run_identity are
// product inputs here, not metadata.
const lineageFields = /^(root_[a-z_]+|run_role|delegated_run_depth|delegated_session_id|delegated_parent_session_id)$/;
const lineageReference = /\[(root_[a-z_]+|run_role|delegated_run_depth|delegated_session_id|delegated_parent_session_id)\]/;
// One identity per qualified session: its agent session, or its conversation
// when the usage row has no session.
const usageIdentity = 'IF([agent_session_id]<>"",[agent_session_id],[conversation_id])';
const inexact = (condition: string[], identity: string) => condition.reduceRight((rest, test) => `IF(${test},${identity},${rest})`, "NULL");
export const lineageQualified = { id: "lineage_qualified", op: "formula" as const, label: "Sessions with unverified ancestry or identity",
  expression: `COUNT_DISTINCT(${inexact(['[run_identity]="uncertain"', '[root_quality]="unverified"', '[root_quality]="inferred"', '[delegated_run_depth_quality]="unverified"', '[delegated_run_depth_quality]="inferred"'], usageIdentity)})` };
export const lineageQualifier: CardQualifier = { field: lineageQualified.id, label: "sessions counted through an inferred or unverified link, or with unresolved identity" };
// Conversation activity carries root_quality for its root cohort fields
// (it has no run identity column).
export const activityRootQualified = { id: "lineage_qualified", op: "formula" as const, label: "Conversations under an inferred or unverified top-level conversation",
  expression: `COUNT_DISTINCT(${inexact(['[root_quality]="unverified"', '[root_quality]="inferred"'], "[conversation_id]")})` };
export const activityRootQualifier: CardQualifier = { field: activityRootQualified.id, label: "conversations placed under a top-level conversation through an inferred or unverified link" };
const activityRootFields = /^root_[a-z_]+$/;
const activityRootReference = /\[root_[a-z_]+\]/;

export function usesLineage(recipe: QueryRecipe): boolean {
  const e = recipe.exploration;
  if (!e || !["usage", "conversation_activity"].includes(recipe.dataset)) return false;
  const [named, referenced] = recipe.dataset === "usage" ? [lineageFields, lineageReference] : [activityRootFields, activityRootReference];
  const fields = [...e.keys, ...(e.labelFields ?? []), ...(e.coverMetric?.groupBy ?? []), ...(recipe.where ?? []).map(term => "field" in term ? term.field : ""), ...e.summaries.map(column => column.field ?? "")];
  return fields.some(field => named.test(field)) || e.summaries.some(column => referenced.test(column.expression ?? ""));
}

export function qualifyLineage(recipe: QueryRecipe): QueryRecipe {
  if (!usesLineage(recipe) || recipe.exploration!.qualifier) return recipe;
  const e = recipe.exploration!, [column, qualifier] = recipe.dataset === "usage" ? [lineageQualified, lineageQualifier] : [activityRootQualified, activityRootQualifier];
  return { ...recipe, exploration: { ...e, summaries: [...e.summaries, column], qualifier } };
}
