import { isWhereConjunction, isWhereGroup, type WhereBranch, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";

export const activities = [
  { id: "test", label: "Tests & checks", color: "var(--heat-2)" },
  { id: "build", label: "Builds", color: "var(--gold)" },
  { id: "read", label: "Reading & search", color: "var(--sea)" },
  { id: "edit", label: "Editing", color: "var(--heat-0)" },
  { id: "setup", label: "Dependencies", color: "var(--heat-3)" },
  { id: "git", label: "Git", color: "var(--heat-1)" },
  { id: "delegate", label: "Delegation", color: "var(--heat-3)" },
  { id: "plan", label: "Planning", color: "var(--heat-1)" },
  { id: "user", label: "Human input", color: "var(--gold)" },
  { id: "discovery", label: "Tool discovery", color: "var(--heat-0)" },
  { id: "network", label: "Network operations", color: "var(--heat-1)" },
  { id: "process", label: "Process management", color: "var(--sea)" },
] as const;
export type Activity = string;
export type Metric = { id: string; buckets: { keys: unknown[]; value: unknown; count?: number }[] };

export function machineFilterKey(term: WhereTerm): string {
  const clause = (value: WhereClause) => JSON.stringify([value.field, value.op, value.value, Boolean(value.negated)]);
  const branch = (member: WhereBranch) => isWhereConjunction(member) ? JSON.stringify(member.all.map(clause).sort()) : clause(member);
  return isWhereGroup(term) ? JSON.stringify(term.any.map(branch).sort()) : clause(term);
}

export function activityPresentation(id: string) {
  const known = activities.find(activity => activity.id === id);
  if (known) return known;
  const label = id.replace(/^(tool|command):/, "") || "(Unidentified tool)";
  const hash = [...id].reduce((value, letter) => value * 31 + letter.charCodeAt(0) | 0, 0);
  return { id, label, color: ["var(--sea)", "var(--gold)", "var(--heat-0)", "var(--heat-1)", "var(--heat-2)", "var(--heat-3)"][Math.abs(hash) % 6] };
}

