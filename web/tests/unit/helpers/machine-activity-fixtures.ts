// Synthetic rows for aggregation tests, never imported by the application.
import { activityFor, factsFromCalls, machineDay, operationKey, operationName, shiftMachineDay, type MachineCall, type MachineScope, type MachineTime, type MachineWindow } from "../../../src/machine-activity-data";
import type { ConcurrencyData, ConcurrencyThread, Interval } from "../../../src/concurrency-data";

const examples = [
  { repository: "pharos", provider: "codex", model: "gpt-5.3-codex" },
  { repository: "query-table", provider: "claude", model: "claude-sonnet-4-5" },
  { repository: "api-server", provider: "codex", model: "gpt-5.3-codex" },
  { repository: "docs-site", provider: "claude", model: "claude-sonnet-4-5" },
];
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
export function exampleTimeFacts(scope: MachineScope, window: MachineWindow, previous = false): MachineTime {
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
