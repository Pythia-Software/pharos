import { isWhereConjunction, isWhereGroup, predicatesOf, type AggregationClause, type WhereBranch, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";
import { mapTermPredicates } from "./where-terms";

export type MachineScope = { channel?: string; machine_id?: string; execution_machine_id?: string; days: number; repository: string; provider: string; model: string };
export function machineScopeFromParams(params: URLSearchParams, saved: MachineScope): MachineScope {
  // Explicit links carry their own population, including older links written
  // before origin filters existed. Only an unscoped visit restores preferences.
  const fields = ["channel", "machine_id", "execution_machine_id"] as const;
  const explicit = params.has("q_usage") || ["days", "repo", "provider", "model", ...fields].some(field => params.has(`activity_${field}`));
  const fallback = explicit ? { days: 0, repository: "", provider: "", model: "" } : saved;
  const days = Number(params.get("activity_days") ?? fallback.days);
  return {
    days: [0, 7, 14, 30, 90].includes(days) ? days : 0,
    repository: params.get("activity_repo") ?? fallback.repository ?? "",
    provider: params.get("activity_provider") ?? fallback.provider ?? "",
    model: params.get("activity_model") ?? fallback.model ?? "",
    ...Object.fromEntries(fields.map(field => [field, params.get(`activity_${field}`) ?? (fallback as MachineScope)[field] ?? ""])),
  };
}
export type MachineWindow = { start: string; end: string; previous: string };
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
export type TimeTotal = { duration: number; calls: number; timed: number; failedDuration: number };
export type TimeFact = TimeTotal & { repository: string; activity: Activity; operation: string; name: string; tool: string; server: string };
export type DayTime = TimeTotal & { day: string };
// Machine time in a selection: its breakdown by repository, activity and
// operation, and its trend by day or month. Nothing on the page crosses the
// two, so they are separate cubes whose groups do not multiply as the period
// lengthens.
export type MachineTime = { facts: TimeFact[]; days: DayTime[] };
export type Metric = { id: string; buckets: { keys: unknown[]; value: unknown; count?: number }[] };
export type TimeMetric = "average" | "total" | "calls";
export const noRepository = "(No repository)";

export function machineFilterKey(term: WhereTerm): string {
  const clause = (value: WhereClause) => JSON.stringify([value.field, value.op, value.value, Boolean(value.negated)]);
  const branch = (member: WhereBranch) => isWhereConjunction(member) ? JSON.stringify(member.all.map(clause).sort()) : clause(member);
  return isWhereGroup(term) ? JSON.stringify(term.any.map(branch).sort()) : clause(term);
}

function repositoryWhere(repository: string): WhereTerm[] {
  if (repository === noRepository) return [{ any: [{ field: "repository_name", op: "is_null", value: "" }, { field: "repository_name", op: "=", value: "" }] }];
  return repository ? [{ field: "repository_name", op: "=", value: repository }] : [];
}

export function selectedMachineRepository(where: WhereTerm[], fallback: string): string {
  const missing = machineFilterKey(repositoryWhere(noRepository)[0]);
  if (where.some(term => machineFilterKey(term) === missing)) return noRepository;
  const selected = where.find(term => !("any" in term) && term.field === "repository_name" && term.op === "=" && !term.negated) as WhereClause | undefined;
  return selected ? selected.value || noRepository : fallback;
}

export function machineDrilldownWhere(where: WhereTerm[], terms: WhereTerm[], toggle = false): WhereTerm[] {
  // A homogeneous OR is one field's selection, such as null-or-empty repository.
  // Mixed-field ORs must stay intact when replacing a chart selection.
  const fieldOf = (term: WhereTerm) => {
    const clauses = predicatesOf(term);
    return clauses.length && clauses.every(clause => clause.field === clauses[0].field) ? clauses[0].field : undefined;
  };
  const fields = new Set(terms.map(fieldOf).filter((field): field is string => field !== undefined));
  if (fields.has("activity")) for (const field of ["operation_key", "tool_name", "mcp_server"]) fields.add(field);
  const keys = new Set(terms.map(machineFilterKey));
  const already = terms.every(term => where.some(existing => machineFilterKey(existing) === machineFilterKey(term)));
  return [...where.filter(term => !fields.has(fieldOf(term) ?? "") && !keys.has(machineFilterKey(term))), ...(toggle && already ? [] : terms)];
}
export type MachineCall = Record<string, any>;

export function activityPresentation(id: string) {
  const known = activities.find(activity => activity.id === id);
  if (known) return known;
  const label = id.replace(/^(tool|command):/, "") || "(Unidentified tool)";
  const hash = [...id].reduce((value, letter) => value * 31 + letter.charCodeAt(0) | 0, 0);
  return { id, label, color: ["var(--sea)", "var(--gold)", "var(--heat-0)", "var(--heat-1)", "var(--heat-2)", "var(--heat-3)"][Math.abs(hash) % 6] };
}
export function machineDay(value: string | Date | number): string {
  const date = new Date(value);
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}
export function shiftMachineDay(value: string, days: number): string {
  const date = new Date(`${value}T12:00:00`); date.setDate(date.getDate() + days); return machineDay(date);
}
export function machineWindow(days: number, now = new Date()): MachineWindow {
  const end = new Date(now); end.setHours(0, 0, 0, 0);
  const shift = (offset: number) => { const date = new Date(end); date.setDate(date.getDate() - offset); return date.toISOString(); };
  return { start: shift(days), end: shift(0), previous: shift(days * 2) };
}
export function machineWhere(scope: MachineScope, window: MachineWindow, timeField = "started_at", previous = false): WhereTerm[] {
  const where: WhereTerm[] = [];
  where.push(...repositoryWhere(scope.repository));
  for (const field of ["channel", "machine_id", "execution_machine_id"] as const) if (scope[field]) where.push({ field, op: "=", value: scope[field]! });
  if (scope.provider) where.push({ field: "provider", op: "=", value: scope.provider });
  if (scope.model) where.push({ field: "model", op: "=", value: scope.model });
  if (scope.days) where.push({ field: timeField, op: ">=", value: previous ? window.previous : window.start }, { field: timeField, op: "<", value: previous ? window.start : window.end });
  return where;
}

// These rules mirror machine_activity.go. Preserve unknown named tools rather
// than treating missing classification as evidence that their work is alike.
export function activityFor(tool: string, command: string, name = "", server = "", program = ""): Activity {
  const commands: Record<string, string> = { test: "test", build: "build", package: "setup", vcs: "git", read: "read", search: "read", "file-write": "edit", network: "network", process: "process" };
  if (Object.hasOwn(commands, command)) return commands[command];
  const tools: Record<string, string> = { read: "read", search: "read", web: "read", edit: "edit", agent: "delegate", plan: "plan", user: "user", meta: "discovery" };
  return Object.hasOwn(tools, tool) ? tools[tool] : program ? `command:${program}` : `tool:${operationName(name, server, "", "")}`;
}
export function operationKey(tool: string, server: string, program: string, subcommand: string): string {
  return JSON.stringify(program ? ["command", program, subcommand] : ["tool", server, tool]);
}
export function operationName(tool: string, server: string, program: string, subcommand: string): string {
  return program ? [program, subcommand].filter(Boolean).join(" ") : !tool ? "(Unidentified tool)" : server ? `${server} / ${tool}` : tool;
}
export function timeAggregations(period: "day" | "month"): AggregationClause[] {
  const groupBy = ["repository_name", "activity", "status", "operation_key", "operation_name", "tool_name", "mcp_server"], trend = [period, "status"];
  return [
    { id: "time", op: "sum", field: "duration_ms", groupBy }, { id: "timed", op: "count", field: "duration_ms", groupBy },
    { id: "trend_time", op: "sum", field: "duration_ms", groupBy: trend }, { id: "trend_timed", op: "count", field: "duration_ms", groupBy: trend },
    { id: "population", op: "count", groupBy: [] },
  ];
}
// A bounded set of full local days selects the same calls through the day
// column and lets SQL use its cube. Other timestamps retain exact predicates.
export function timeAggregationWhere(where: WhereTerm[]): WhereTerm[] {
  const bounds = where.filter((term): term is Exclude<WhereTerm, { any: unknown }> => {
    if ("any" in term || term.field !== "started_at" || term.negated || ![">=", "<"].includes(term.op)) return false;
    const date = new Date(term.value);
    return Number.isFinite(date.getTime()) && !date.getHours() && !date.getMinutes() && !date.getSeconds() && !date.getMilliseconds();
  });
  const lower = bounds.filter(term => term.op === ">=").map(term => machineDay(term.value)).sort().at(-1);
  const upper = bounds.filter(term => term.op === "<").map(term => machineDay(term.value)).sort()[0];
  if (!lower || !upper || lower >= upper) return where;
  const days: Exclude<WhereTerm, { any: unknown }>[] = [];
  for (let day = lower; day < upper; day = shiftMachineDay(day, 1)) {
    // Preserve the backend's predicate budget for longer or custom queries.
    if (days.length >= 30) return where;
    days.push({ field: "day", op: "=", value: day });
  }
  // A one-day group is a plain predicate: metric requests refuse a one-clause OR.
  return [...where.filter(term => !bounds.includes(term as Exclude<WhereTerm, { any: unknown }>)), days.length === 1 ? days[0] : { any: days }];
}
export function timeFacts(metrics: Metric[]): MachineTime {
  const buckets = (id: string) => metrics.find(metric => metric.id === id)?.buckets ?? [];
  const population = metrics.find(metric => metric.id === "population")?.buckets[0]?.value;
  // Every call in the population belongs to one group of each metric. Never
  // turn sums and counts that disagree into a seemingly exact average.
  if (population === undefined || ["time", "timed", "trend_time", "trend_timed"].some(id => buckets(id).reduce((sum, bucket) => sum + (bucket.count ?? 0), 0) !== Number(population)))
    throw new Error("Operation groups are incomplete. Choose a shorter period or a single repository to inspect the full selection.");
  // status is the index of a group's status among its keys.
  const totals = <T>(time: string, timed: string, identity: (keys: string[]) => T, status: number): Array<T & TimeTotal> => {
    const counts = new Map(buckets(timed).map(bucket => [JSON.stringify(bucket.keys), Number(bucket.value) || 0]));
    return buckets(time).map(bucket => {
      const keys = bucket.keys.map(value => String(value ?? "")), duration = Math.max(0, Number(bucket.value) || 0);
      return { ...identity(keys), duration, calls: bucket.count ?? 0, timed: counts.get(JSON.stringify(bucket.keys)) ?? 0, failedDuration: keys[status] === "error" ? duration : 0 };
    });
  };
  return {
    facts: totals("time", "timed", ([repository, activity, , operation, name, tool, server]) => ({ repository: repository || noRepository, activity, operation, name, tool, server }), 2),
    days: totals("trend_time", "trend_timed", ([day]) => ({ day }), 1),
  };
}
export function factsFromCalls(rows: MachineCall[], period: "day" | "month"): MachineTime {
  const facts = new Map<string, TimeFact>(), days = new Map<string, DayTime>();
  const add = <T extends TimeTotal>(groups: Map<string, T>, key: string, empty: T, row: MachineCall) => {
    const total = groups.get(key) ?? empty, duration = row.duration_ms == null ? 0 : Number(row.duration_ms);
    groups.set(key, { ...total, calls: total.calls + 1, timed: total.timed + (row.duration_ms == null ? 0 : 1), duration: total.duration + duration, failedDuration: total.failedDuration + (row.status === "error" ? duration : 0) });
  };
  const zero = { duration: 0, calls: 0, timed: 0, failedDuration: 0 };
  for (const row of rows) {
    const day = String(row.day).slice(0, period === "month" ? 7 : 10);
    add(facts, JSON.stringify([row.repository_name, row.activity, row.operation_key, row.tool_name, row.mcp_server]), { repository: row.repository_name || noRepository, activity: row.activity, operation: row.operation_key, name: row.operation_name, tool: row.tool_name, server: row.mcp_server ?? "", ...zero }, row);
    add(days, day, { day, ...zero }, row);
  }
  return { facts: [...facts.values()], days: [...days.values()] };
}
export function totalTime(facts: TimeTotal[]): TimeTotal {
  return facts.reduce((sum, fact) => ({ duration: sum.duration + fact.duration, calls: sum.calls + fact.calls, timed: sum.timed + fact.timed, failedDuration: sum.failedDuration + fact.failedDuration }), { duration: 0, calls: 0, timed: 0, failedDuration: 0 });
}
export function groupTime<T extends TimeTotal>(facts: T[], key: (fact: T) => string): Array<TimeTotal & { key: string; facts: T[] }> {
  const groups = new Map<string, T[]>();
  for (const fact of facts) {
    const name = key(fact);
    let group = groups.get(name);
    if (!group) { group = []; groups.set(name, group); }
    group.push(fact);
  }
  return [...groups].map(([key, facts]) => ({ key, facts, ...totalTime(facts) })).sort((a, b) => b.duration - a.duration || a.key.localeCompare(b.key));
}
export function timeValue(total: TimeTotal, metric: TimeMetric): number | null {
  return metric === "calls" ? total.calls : metric === "total" ? total.duration : total.timed ? total.duration / total.timed : null;
}
export function timeLabel(ms: number): string {
  if (ms === 0) return "0s"; if (ms < 1000) return "<1s"; if (ms < 60000) return `${Math.round(ms / 1000)}s`; if (ms < 3600000) return `${Math.round(ms / 60000)}m`;
  return `${(ms / 3600000).toLocaleString(undefined, { maximumFractionDigits: 1 })}h`;
}
export function relativeChange(current: number, previous: number): string {
  if (!previous) return current ? "New" : "—";
  const percent = (current - previous) / previous * 100;
  return Math.abs(percent) < .5 ? "0%" : `${percent > 0 ? "+" : "−"}${Math.abs(percent).toFixed(0)}%`;
}
export function previousTimeWhere(where: WhereTerm[], days: number): WhereTerm[] | null {
  let valid = true;
  const shift = (clause: WhereClause) => {
    if (!["started_at", "ended_at", "day", "week", "month"].includes(clause.field) || clause.op === "is_null" || clause.op === "is_not_null") return clause;
    if (clause.field === "month" || clause.field === "week") { valid = false; return clause; }
    if (clause.field === "day" && /^\d{4}-\d{2}-\d{2}$/.test(clause.value)) return { ...clause, value: shiftMachineDay(clause.value, -days) };
    const date = new Date(clause.value);
    if (!Number.isFinite(date.getTime())) { valid = false; return clause; }
    date.setDate(date.getDate() - days); return { ...clause, value: date.toISOString() };
  };
  const shifted = where.map(term => mapTermPredicates(term, shift)!);
  return valid ? shifted : null;
}
