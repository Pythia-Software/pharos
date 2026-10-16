import type { AggregationClause, WhereClause, WhereTerm } from "@pythia-software/query-table-core";
import type { ConcurrencyData, ConcurrencyThread, Interval } from "./concurrency-data";

export type MachineScope = { days: number; repository: string; provider: string; model: string };
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
export type TimeFact = TimeTotal & { repository: string; activity: Activity; day: string; operation: string; name: string; tool: string; server: string };
export type Metric = { id: string; buckets: { keys: unknown[]; value: unknown; count?: number }[] };
export type TimeMetric = "average" | "total" | "calls";
export const noRepository = "(No repository)";

export function machineFilterKey(term: WhereTerm): string {
  const clause = (value: WhereClause) => JSON.stringify([value.field, value.op, value.value, Boolean(value.negated)]);
  return "any" in term ? JSON.stringify(term.any.map(clause).sort()) : clause(term);
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
    const clauses = "any" in term ? term.any : [term];
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
  const groupBy = ["repository_name", "activity", period, "status", "operation_key", "operation_name", "tool_name", "mcp_server"];
  return [{ id: "time", op: "sum", field: "duration_ms", groupBy }, { id: "timed", op: "count", field: "duration_ms", groupBy }, { id: "population", op: "count", groupBy: [] }];
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
  return [...where.filter(term => !bounds.includes(term as Exclude<WhereTerm, { any: unknown }>)), { any: days }];
}
export function timeFacts(metrics: Metric[]): TimeFact[] {
  const times = metrics.find(metric => metric.id === "time")?.buckets ?? [];
  const measured = metrics.find(metric => metric.id === "timed")?.buckets ?? [];
  const population = metrics.find(metric => metric.id === "population")?.buckets[0]?.value;
  // The legacy aggregation API bounds each metric independently. Never turn
  // differently truncated sums and counts into a seemingly exact average.
  if (population === undefined || times.reduce((sum, bucket) => sum + (bucket.count ?? 0), 0) !== Number(population) || measured.reduce((sum, bucket) => sum + (bucket.count ?? 0), 0) !== Number(population))
    throw new Error("Operation groups are incomplete. Choose a shorter period or a single repository to inspect the full selection.");
  const timed = new Map(measured.map(bucket => [JSON.stringify(bucket.keys), Number(bucket.value) || 0]));
  return times.map(bucket => {
    const [repository, activity, day, status, operation, name, tool, server] = bucket.keys.map(value => String(value ?? ""));
    const duration = Math.max(0, Number(bucket.value) || 0);
    return { repository: repository || noRepository, activity, day, operation, name, tool, server, duration, calls: bucket.count ?? 0, timed: timed.get(JSON.stringify(bucket.keys)) ?? 0, failedDuration: status === "error" ? duration : 0 };
  });
}
export function factsFromCalls(rows: MachineCall[], period: "day" | "month"): TimeFact[] {
  const groups = new Map<string, TimeFact>();
  for (const row of rows) {
    const day = String(row.day).slice(0, period === "month" ? 7 : 10);
    const key = JSON.stringify([row.repository_name, row.activity, day, row.operation_key, row.tool_name, row.mcp_server]);
    const fact = groups.get(key) ?? { repository: row.repository_name || noRepository, activity: row.activity, day, operation: row.operation_key, name: row.operation_name, tool: row.tool_name, server: row.mcp_server ?? "", duration: 0, calls: 0, timed: 0, failedDuration: 0 };
    const duration = row.duration_ms == null ? 0 : Number(row.duration_ms);
    groups.set(key, { ...fact, calls: fact.calls + 1, timed: fact.timed + (row.duration_ms == null ? 0 : 1), duration: fact.duration + duration, failedDuration: fact.failedDuration + (row.status === "error" ? duration : 0) });
  }
  return [...groups.values()];
}
export function totalTime(facts: TimeTotal[]): TimeTotal {
  return facts.reduce((sum, fact) => ({ duration: sum.duration + fact.duration, calls: sum.calls + fact.calls, timed: sum.timed + fact.timed, failedDuration: sum.failedDuration + fact.failedDuration }), { duration: 0, calls: 0, timed: 0, failedDuration: 0 });
}
export function groupTime<T extends TimeTotal>(facts: T[], key: (fact: T) => string): Array<TimeTotal & { key: string; facts: T[] }> {
  const groups = new Map<string, T[]>();
  for (const fact of facts) { const name = key(fact); groups.set(name, [...groups.get(name) ?? [], fact]); }
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
  const shift = (clause: Exclude<WhereTerm, { any: unknown }>) => {
    if (!["started_at", "ended_at", "day", "week", "month"].includes(clause.field) || clause.op === "is_null" || clause.op === "is_not_null") return clause;
    if (clause.field === "month" || clause.field === "week") { valid = false; return clause; }
    if (clause.field === "day" && /^\d{4}-\d{2}-\d{2}$/.test(clause.value)) return { ...clause, value: shiftMachineDay(clause.value, -days) };
    const date = new Date(clause.value);
    if (!Number.isFinite(date.getTime())) { valid = false; return clause; }
    date.setDate(date.getDate() - days); return { ...clause, value: date.toISOString() };
  };
  const shifted = where.map(term => "any" in term ? { ...term, any: term.any.map(shift) } : shift(term));
  return valid ? shifted : null;
}

const examples = [
  { repository: "pharos", provider: "codex", model: "gpt-5.3-codex" },
  { repository: "query-table", provider: "claude", model: "claude-sonnet-4-5" },
  { repository: "api-server", provider: "codex", model: "gpt-5.3-codex" },
  { repository: "docs-site", provider: "claude", model: "claude-sonnet-4-5" },
];
export const exampleRepositories = examples.map(example => example.repository);
export const exampleModels = [...new Set(examples.map(example => example.model))];
const exampleOperations = [
  { tool: "exec_command", program: "npm", subcommand: "run test:unit", category: "test", seconds: 70, invocation: "npm run test:unit -- --runInBand" },
  { tool: "Bash", program: "go", subcommand: "test", category: "test", seconds: 120, invocation: "go test ./internal/... -count=1" },
  { tool: "exec_command", program: "npm", subcommand: "run build", category: "build", seconds: 160, invocation: "npm run build -- --mode production" },
  { tool: "exec_command", program: "go", subcommand: "build", category: "build", seconds: 90, invocation: "go build ./cmd/..." },
  { tool: "exec_command", program: "rg", subcommand: "", category: "search", seconds: 6, invocation: "rg -n 'workspace' src" },
  { tool: "Read", toolCategory: "read", seconds: 3, invocation: "src/index.tsx" },
  { tool: "apply_patch", toolCategory: "edit", seconds: 7, invocation: "Update src/index.tsx" },
  { tool: "exec_command", program: "npm", subcommand: "ci", category: "package", seconds: 110, invocation: "npm ci --no-audit" },
  { tool: "exec_command", program: "git", subcommand: "diff", category: "vcs", seconds: 4, invocation: "git diff -- src" },
  { tool: "spawn_agent", toolCategory: "agent", seconds: 250, invocation: "Review repository changes" },
  { tool: "search_issues", server: "github", seconds: 8, invocation: '{"query":"repo:example/project is:open"}' },
  { tool: "query_statistics", server: "pharos", seconds: 5, invocation: '{"dataset":"tool_calls"}' },
  { tool: "update_plan", toolCategory: "plan", seconds: 2, invocation: "Update implementation plan" },
];
// Examples are actual synthetic call rows. Every displayed sum, mean, and raw
// table is calculated from these same rows rather than invented independently.
export function exampleTimeCalls(scope: MachineScope, window: MachineWindow, previous = false): MachineCall[] {
  const days = scope.days || 14, end = machineDay(previous ? window.start : window.end), rows: MachineCall[] = [];
  for (const [repoIndex, repository] of examples.entries()) {
    if ((scope.repository && scope.repository !== repository.repository) || (scope.provider && scope.provider !== repository.provider) || (scope.model && scope.model !== repository.model)) continue;
    for (let index = 0; index < days; index++) for (const [operationIndex, operation] of exampleOperations.entries()) {
      const op = operation as typeof exampleOperations[number] & { program?: string; subcommand?: string; category?: string; server?: string; toolCategory?: string };
      const day = shiftMachineDay(end, index - days);
      const calls = 4 + operationIndex % 4 + repoIndex * 2 + (!previous && repoIndex === 1 && op.category === "test" ? 5 : 0);
      for (let run = 0; run < calls; run++) {
        const at = new Date(`${day}T09:00:00`); at.setMinutes(operationIndex * 25 + run * 2);
        const growth = repoIndex === 0 && ["test", "build"].includes(op.category ?? "") ? previous ? .75 : 1 + index / Math.max(1, days - 1) * .9 : 1;
        const duration = (index + run + operationIndex) % 19 === 0 ? null : Math.round(op.seconds * 1000 * growth * (.9 + (run % 3) / 10));
        const id = `example-${day}-${repoIndex}-${operationIndex}-${run}`;
        rows.push({ id, repository_name: repository.repository, provider: repository.provider, model: repository.model, day, month: day.slice(0, 7), started_at: at.toISOString(), ended_at: new Date(at.getTime() + (duration ?? 0)).toISOString(), duration_ms: duration, duration_source: "reported", status: run === 3 && operationIndex < 4 ? "error" : "ok", tool_name: op.tool, mcp_server: op.server ?? "", program: op.program ?? "", subcommand: op.subcommand ?? "", tool_category: op.toolCategory ?? (op.program ? "command" : "mcp"), command_category: op.category ?? "", command_name: [op.program, op.subcommand].filter(Boolean).join(" ") || null, command: op.program ? `${run % 2 ? "CI=1 timeout 600 " : ""}${op.invocation}` : null,
          activity: activityFor(op.toolCategory ?? (op.program ? "command" : "mcp"), op.category ?? "", op.tool, op.server ?? "", op.program ?? ""), operation_key: operationKey(op.tool, op.server ?? "", op.program ?? "", op.subcommand ?? ""), operation_name: operationName(op.tool, op.server ?? "", op.program ?? "", op.subcommand ?? ""), title: `Example: ${repository.repository} verification`, workspace_id: `example-${repoIndex}`, conversation_id: `example-conversation-${repoIndex}-${index}`, input_text: op.invocation, result_text: "Synthetic example result; no library source was accessed." });
      }
    }
  }
  return rows;
}
export function exampleTimeFacts(scope: MachineScope, window: MachineWindow, previous = false): TimeFact[] {
  return factsFromCalls(exampleTimeCalls(scope, window, previous), scope.days ? "day" : "month");
}

export function exampleConcurrency(window: MachineWindow, days = 14): ConcurrencyData {
  const end = machineDay(window.end);
  const threads: ConcurrencyThread[] = [];
  for (let day = 0; day < days; day++) {
    for (const [index, example] of examples.entries()) {
      for (let session = 0; session < 3; session++) {
        const at = new Date(`${shiftMachineDay(end, day - days)}T00:00:00`);
        at.setHours(9 + session * 2, index * 15);
        const start = at.getTime();
        const span: Interval = [start, start + (90 + ((day + index * 3) % 7) * 10) * 60000];
        const id = `example-${day}-${index}-${session}`;
        threads.push({ id, conversation_id: id, work_id: id, workspace_id: `example-${index}-${session}`, workspace_name: `${example.repository} / session ${session + 1}`,
          repository: example.repository, title: ["Implement repository changes", "Run tests and review changes", "Refine and verify"][session], provider: example.provider, model: example.model, agent_depth: 0,
          turns: [span], model_spans: [[start, start + 20 * 60000], [start + 45 * 60000, span[1]]] });
      }
    }
  }
  const startDay = shiftMachineDay(end, -days);
  const endDay = shiftMachineDay(end, -1);
  return { generated_at: window.end, timezone: Intl.DateTimeFormat().resolvedOptions().timeZone, period_start: startDay, period_end: endDay,
    days: Array.from({ length: days }, (_, index) => shiftMachineDay(end, index - days)), threads,
    coverage: { conversations: threads.length, mirrors_removed: 0, unexplained_waits_excluded: 0, timing: "Synthetic example", catalog: "Example data" } };
}
