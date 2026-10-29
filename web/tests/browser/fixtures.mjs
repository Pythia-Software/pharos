// Default API stubs for the browser harness: one small dataset behind every
// endpoint the page shell and its scripts call, so no page can fail on a
// missing stub. Query Table endpoints use the production transport over
// tests/support/query-server.mjs. The bodies are plausible, not exhaustive:
// they exist so every page renders real content to measure.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import * as core from "@pythia-software/query-table-core";
import { answerQuery, population } from "../support/query-server.mjs";

const repo = fileURLToPath(new URL("../../..", import.meta.url));
const schemas = Object.fromEntries(readdirSync(join(repo, "schemas")).filter(file => file.endsWith(".schema.json"))
  .map(file => [file.replace(".schema.json", ""), core.loadSchema(JSON.parse(readFileSync(join(repo, "schemas", file), "utf8")))]));
const now = Date.now();
export const sourceSetupCandidates = [
  {name:'claude',kind:'claude',path:'/Users/test/.claude/projects',display_path:'~/.claude/projects',count:24,unit:'session',bytes:2400000,status:'found',acceptable:true,recommended:true},
  {name:'claude-work',kind:'claude',path:'/Users/test/.claude-work/projects',display_path:'~/.claude-work/projects',count:8,unit:'session',bytes:800000,status:'found',acceptable:true,recommended:true},
  {name:'codex',kind:'codex',path:'/Users/test/.codex',display_path:'~/.codex',count:16,unit:'session',bytes:1700000,status:'found',acceptable:true,recommended:true},
];
const day = 86400000, iso = offset => new Date(now - offset).toISOString();

// Long values are where layouts break, so some rows carry them.
const longTitle = "Investigate why the incremental indexer re-reads every capture after a laptop sleeps through a scheduled sync window";
const datasets = Object.fromEntries(Object.entries(schemas).map(([name, schema]) => [name, population(schema, { now, count: 48 }).map((row, index) => index % 5 ? row
  : { ...row, title: longTitle, repository_name: "pythia-software/an-unusually-long-repository-name-for-layout", location: "/Users/someone/conductor/workspaces/pharos/a-deeply-nested-worktree-directory/internal/archive" })]));
// Parallelism records are built with the saved counting and gap settings.
for (const row of datasets.parallelism) Object.assign(row, { counting: "workspace", gap_minutes: 60 });
// The service assigns every usage record a carbon factor tier.
datasets.usage.forEach((row, index) => { row.carbon_tier = ["small", "medium", "large"][index % 3]; });
// Usage trends consume the calendar keys of their source records.
for (const row of [...datasets.tools, ...datasets.mcp_calls]) {
  const monday = new Date(`${row.day}T00:00:00Z`);
  monday.setUTCDate(monday.getUTCDate() - (monday.getUTCDay() + 6) % 7);
  row.week = monday.toISOString().slice(0, 10);
  row.month = row.day.slice(0, 7);
}

// The workspace behind /work/<id>: a root conversation with a long
// transcript (long code blocks, a wide table, tool output, an edit, nested
// agents) and two child sessions.
export const workID = "work-long", rootConversation = "conv-root";
const start = now - 2 * 3600000;
const at = seconds => new Date(start + seconds * 1000).toISOString();
let sequence = 0;
function event(kind, text, extra = {}) {
  const id = `m-${++sequence}`;
  return { id, native_id: id, role: kind === "tool_result" ? "tool" : "assistant", kind, text: typeof text === "string" ? text : JSON.stringify(text), created_at: at(sequence * 7), ...extra };
}
const longLine = "const result = await catalog.query({ select: [" + Array.from({ length: 24 }, (_, i) => `\"field_${i}\"`).join(", ") + "] });";
const codeBlock = ["```js", ...Array.from({ length: 30 }, (_, i) => `${"  ".repeat(i % 4)}${i % 3 ? longLine : `// step ${i}: ${"explain the change in one very long comment line ".repeat(4)}`}`), "```"].join("\n");
const wideTable = [`| ${Array.from({ length: 14 }, (_, i) => `Column header ${i}`).join(" | ")} |`, `|${" --- |".repeat(14)}`,
  ...Array.from({ length: 6 }, (_, row) => `| ${Array.from({ length: 14 }, (_, i) => `value-${row}-${i}-${"x".repeat(i)}`).join(" | ")} |`)].join("\n");
const usage = (input, output = 400) => ({ input_tokens: input, output_tokens: output, cache_read_input_tokens: input * 4, cache_creation_input_tokens: 200 });
function transcript() {
  sequence = 0;
  const messages = [];
  for (let turn = 0; turn < 12; turn++) {
    messages.push(event("message", `Turn ${turn}: ${turn % 3 ? "please look at the failing test and fix it" : `${longTitle}. ${"Here is more context that wraps across several lines. ".repeat(8)}`}`, { role: "user" }));
    messages.push(event("message", { message: { id: `r-${turn}`, usage: usage(12000 + turn * 9000), content: [{ type: "text", text: turn % 2 ? `Here is the change:\n\n${codeBlock}` : `Findings so far:\n\n${wideTable}\n\nA path to note: /Users/someone/conductor/workspaces/pharos/a-deeply-nested-worktree-directory/internal/archive/incremental_sync_recovery_test.go` }] } }));
    const call = `call-${turn}`;
    if (turn % 3 === 0) {
      messages.push(event("tool_call", { name: "Bash", input: { command: `go test -run 'TestIncrementalSyncRecovery|TestCaptureAfterSleep' -count=1 -v ./internal/archive/... 2>&1 | tee /tmp/${"very-long-log-name-".repeat(4)}${turn}.log` } }, { call_id: call }));
      messages.push(event("tool_result", { content: Array.from({ length: 40 }, (_, i) => `=== RUN   TestIncrementalSyncRecovery/case_${i}_${"with_a_long_subtest_name_".repeat(3)}`).join("\n"), is_error: turn === 3 }, { call_id: call }));
    } else if (turn % 3 === 1) {
      messages.push(event("tool_call", { name: "Edit", input: { file_path: "/repo/internal/archive/incremental_sync.go", old_string: `func recover() {\n${longLine}\n}`, new_string: `func recover(ctx context.Context) error {\n${longLine}\n${longLine}\n\treturn nil\n}` } }, { call_id: call }));
      messages.push(event("tool_result", { content: "The file /repo/internal/archive/incremental_sync.go has been updated." }, { call_id: call }));
    } else {
      // A delegation with a nested delegation inside it.
      const outer = `agent-${turn}`, inner = `agent-${turn}-inner`;
      messages.push(event("delegation", { name: "Task", input: { description: `Review the change from turn ${turn} and check every caller`, subagent_type: "Reviewer" } }, { call_id: outer }));
      messages.push(event("tool_call", { name: "Read", input: { file_path: "/repo/internal/archive/a/very/deeply/nested/path/to/a/file/with/a/long/name_test.go" } }, { call_id: `${outer}-read`, parent_native_id: outer }));
      messages.push(event("tool_result", { content: codeBlock }, { call_id: `${outer}-read`, parent_native_id: outer }));
      messages.push(event("delegation", { name: "Task", input: { description: "Check the timing assumptions in the nested caller", subagent_type: "Analyst" } }, { call_id: inner, parent_native_id: outer }));
      messages.push(event("message", { message: { id: `inner-${turn}`, usage: usage(5000), content: [{ type: "text", text: `Nested reply with a table:\n\n${wideTable}` }] } }, { parent_native_id: inner }));
      messages.push(event("delegation_result", { content: "Timing checked." }, { call_id: inner }));
      messages.push(event("delegation_result", { content: "Review complete." }, { call_id: outer }));
    }
    messages.push(event("metadata", { type: "token_count", info: { total_token_usage: { total_tokens: 40000 + turn * 15000 }, last_token_usage: { input_tokens: 30000 + turn * 9000, output_tokens: 500 } } }));
  }
  return messages;
}
const childMessages = conversation => [
  { id: `${conversation}-prompt`, native_id: `${conversation}-prompt`, kind: "message", role: "user", text: `Review the change (${conversation})`, created_at: at(10) },
  { id: `${conversation}-reply`, native_id: `${conversation}-reply`, kind: "message", role: "assistant", text: `Reviewed.\n\n${codeBlock}`, created_at: at(20) },
];

const conversationOverview = (id, provider, prompt) => ({ id, provider, native_id: id, started_at: at(0), first_prompt: prompt, message_count: 120, model: "claude-opus-5-5", coverage: "complete" });
const work = {
  id: workID, title: longTitle, repository_name: "pythia-software/pharos", branch: "gbdubs/an-unusually-long-branch-name-for-the-header", activity_at: at(600), location: "/Users/someone/conductor/workspaces/pharos/indianapolis",
  conversations: [conversationOverview(rootConversation, "claude", longTitle), conversationOverview("conv-child", "codex", "Review implementation"), conversationOverview("conv-nested", "claude", "Review timing")],
  changes: [], metrics: [{ name: "input_tokens", value: 190000, unit: "tokens", status: "observed" }], prs: [], identity_links: [],
};

// The delegation explorer's analysis of the workspace's three sessions.
const metricKeys = ["start", "tokens", "cost", "calls", "errors", "files", "messages", "requests", "turns", "clock", "total", "toolTime", "toolAgent", "toolSum", "wait", "modelTime", "backgroundTime", "episodes", "children", "leaves", "depth", "input", "uncached", "cacheRead", "cacheWrite", "output", "reasoning", "remainder"];
const durations = new Set(["clock", "total", "toolTime", "toolAgent", "toolSum", "wait", "modelTime", "backgroundTime"]);
const metricSpecs = metricKeys.map(id => ({ id, label: { start: "First evidence", tokens: "Tokens", cost: "API cost", calls: "Tool calls", errors: "Errors", files: "Files Edited", clock: "Clock span", total: "Session spans", toolTime: "Tool elapsed" }[id] || id,
  unit: id === "cost" ? "usd" : durations.has(id) ? "ms" : id === "start" ? "date" : "count", definition: `Definition for ${id}` }));
const metric = (value, quality = "observed") => ({ value, quality, known: value === null ? 0 : 1, total: 1 });
const sessionEnd = start + 300000;
function session(id, conversation, parent, provider, outline, depth, label, tokens) {
  const self = Object.fromEntries(metricKeys.map(key => [key, metric(0)]));
  Object.assign(self, { tokens: metric(tokens), calls: metric(3), files: metric(depth === 0 ? 1 : 2), cost: metric(tokens / 1000), clock: metric(300000), start: metric(start), wait: metric(null, "unknown"), modelTime: metric(null, "unknown"), backgroundTime: metric(null, "unknown") });
  return { id, workspace_id: workID, conversation_id: conversation, parent_id: parent, provider, outline, depth, label, model: provider === "codex" ? "gpt-test" : "claude-test", effort: "medium", coverage: "complete", usage_status: "reported-reconciled",
    order: depth, child_count: depth < 2 ? 1 : 0, start, end: sessionEnd, subtree_start: start, subtree_end: sessionEnd, self, all: { ...self } };
}
const sessions = [
  session("s-root", rootConversation, "", "claude", "1", 0, longTitle, 100000),
  { ...session("s-child", "conv-child", "s-root", "codex", "1.1", 1, "Review implementation", 60000), message_id: "m-15" },
  { ...session("s-nested", "conv-nested", "s-child", "claude", "1.1.1", 2, "Review timing", 30000), message_id: "conv-child-reply" },
];
function delegation(query) {
  const search = (query.get("search") || "").toLowerCase();
  const rows = sessions.filter(row => !search || row.label.toLowerCase().includes(search) || row.outline === "1");
  return { snapshot: "fixture-v1", rows, ancestors: [], focus: sessions, matched: Object.fromEntries(rows.map(row => [row.id, true])), total: rows.length, session_count: sessions.length, offset: 0, next_cursor: "",
    metrics: metricSpecs, maxima: { self: Object.fromEntries(metricKeys.map(key => [key, key === "tokens" ? 100000 : 300000])), all: Object.fromEntries(metricKeys.map(key => [key, key === "tokens" ? 190000 : 300000])) },
    totals: { ...sessions[0].all, clock: metric(300000), total: metric(900000) }, start, end: sessionEnd, density: Array.from({ length: 120 }, (_, i) => i % 13), warnings: [] };
}
function parallel(query) {
  const from = Number(query.get("start")) || start, to = Number(query.get("end")) || sessionEnd;
  return { snapshot: "fixture-v1", start: from, end: to, inference: "Inferred, not measured", lanes: (query.get("sessions") || "").split(",").filter(Boolean).map(id => ({ session: id,
    model_inferred: [{ start: Math.max(from, start + 1000), end: Math.min(to, sessionEnd - 1000) }], episodes: [{ start, end: sessionEnd }], truncated: false,
    marks: [{ start: start + 1000, end: start + 100000, end_known: true, category: "read", name: "Read", count: 1, tool_id: "tool-read", message_id: "m-1" }, { start: start + 110000, end: start + 125000, end_known: true, category: "run", name: "Test", count: 1, error: true, tool_id: "tool-error", message_id: "m-2" }] })) };
}

// Findings: open, in-cart and watching cards with a detail chart.
const repoTarget = "repo:repo_explo";
const openCard = { id: "drift:repo:repo_explo:abc", state: "open", title: "Codex can't find explo's instructions", explanation: "explo keeps its agent instructions in `CLAUDE.md`, which only Claude reads.",
  impact: { usd: 41.2, tokens: 150e6, minutes: 12, failures: 330, window_days: 28 }, impact_note: "330 searches", rate: "1 in 3", change: "Make one instructions file that both Claude and Codex read.", change_label: "One shared instructions file", attempt: 1,
  detector: "drift", detector_label: "Instruction drift", target: repoTarget, target_label: "explo", scope_label: "explo", repository_id: "repo_explo", first_seen_at: iso(20 * day), last_seen: iso(day), active: true, new: true };
const cartCard = { ...openCard, id: "failure:repo:repo_explo:def", title: `${longTitle} in explo`, explanation: "Agents read gzipped files as text.", impact: { usd: 3, tokens: 1e6, minutes: 1, failures: 36 }, impact_note: "34 failed commands", new: false, cart: { target: repoTarget, target_label: "explo", ticked: true } };
const watchingCard = { ...openCard, id: "drift:repo:repo_tl1:ghi", state: "watching", title: "Codex can't find tl1's instructions", new: false, target: "repo:repo_tl1", target_label: "tl1", scope_label: "tl1", repository_id: "repo_tl1", change_label: "A shared instructions file", change: "A shared instructions file",
  watching: { before: "1 in 3", change: "A shared instructions file", copied_at: iso(12 * day), day: 12, of_days: 28, result_on: new Date(now + 16 * day).toISOString().slice(0, 10), saved_usd: 3.2, target_label: "tl1" } };
const findingCards = [openCard, cartCard, watchingCard];
const findings = {
  status: { built_at: iso(3600000), took_ms: 11000, measured_at: null, conversations_28d: 4482, weekly_conversations: 1120, generation: "g", running: false, phase: null, error: null, current: true },
  settings: { threshold: 0, rank: "usd", handoff: "auto", repository_handoff: {}, seen_at: "" }, threshold: 5, recommended: 5,
  checkpoints: [3, 5, 10, 15, 20].map(threshold => ({ threshold, shown: 60 - threshold * 2, typical_wait_days: 28, clear_share: 0.8 })),
  summary: { saved: { usd: 38.4, tokens: 5.2e6, minutes: 74, failures: 12, added_usd: 1.1, added_tokens: 9e5 }, open: 2, watching: 1, won: 0, dismissed: 0, snoozed: 0, regressed: 0, at_stake_usd: 44.2, new: 1, next_result_days: 16 },
  findings: findingCards, cart_count: 1,
  cart: [{ target: repoTarget, label: "explo", handoff: "pr", handoff_reason: "explo's work ends in a pull request 86% of the time, so Pharos suggests a PR.", ticked: 1, items: [{ id: cartCard.id, title: cartCard.title, ticked: true, change_label: "A line in the instructions", suggested_target: repoTarget, undo: false }] }],
  targets: [{ target: repoTarget, label: "explo" }, { target: "repo:repo_tl1", label: "tl1" }, { target: "global:host:codex", label: "All repositories · Codex on this Mac" }],
  repositories: [{ id: "repo_explo", name: "explo", work: 700, pr_share: 0.86, handoff: "pr", override: null }],
  near: [{ id: "n1", title: "`python`: command not found", affected: 3 }, { id: "n2", title: "A missing AGENTS.md in one repository", affected: 4 }], wins: [],
  detectors: [{ name: "drift", label: "Instruction drift", enabled: true }, { name: "failure", label: "Recurring failures", enabled: false }],
};
const findingWeeks = Array.from({ length: 14 }, (_, index) => ({ start: new Date(now - (14 - index) * 7 * day).toISOString().slice(0, 10), events: index < 12 ? 5 : 1, exposure: index === 3 ? 1 : 12, rate: index < 12 ? 5 / 12 : 1 / 12, hollow: index === 3, after: index >= 12, cost_usd: 1 }));
const findingDetail = card => ({ ...card, chart: { title: "Codex conversations that looked for instructions", kind: "rate", weeks: findingWeeks, anchor: findingWeeks[11].start, copies: [findingWeeks[11].start], baseline: 0.4, baseline_phrase: "2 in 5" },
  evidence: [{ at: iso(day), workspace_id: workID, conversation_id: rootConversation, message_id: "m-1", where: "tl1 · Codex", did: "searched for `AGENTS.md`", happened: "found nothing" }],
  attempts: [{ attempt: 1, change: "A shared instructions file", copied_at: iso(12 * day), status: "watching", target_label: "tl1", savings: { usd: 3.2, added_usd: 0 } }],
  steps: [{ label: "A shared instructions file", change: "" }, { label: "A fallback setting", change: "" }], metric: { kind: "rate", unit: "conversations" }, prompt: `Pharos finding ${card.id} …` });

// Settings, sync and the header's library status.
const hosts = [{ id: "studio", label: "Mac Studio", sources: [{ name: "Claude", captured_at: iso(3 * day), indexed_at: iso(3600000) }] }, { id: "air", label: "MacBook Air with a long machine name", sources: [{ name: "Codex", last_data_at: iso(125 * 60000) }] }, { id: "new", label: "New Mac", sources: [] }];
const sources = { configured: 2, enabled: 2, items: [
  { name: "claude", enabled: true, path: "/Users/someone/.claude/projects", last_success_at: iso(3600000), last_attempt_at: iso(3600000), coverage: "complete", account: "someone@example.com" },
  { name: "codex", enabled: false, path: "/Users/someone/.codex/sessions/a/very/long/path/that/should/wrap/inside/its/card", last_success_at: iso(day), last_attempt_at: iso(day), coverage: "partial", error: "The source directory could not be read." },
] };
const syncHistory = Array.from({ length: 6 }, (_, index) => ({ run_id: `run-${index}`, finished_at: iso((index + 1) * 120000), state: index === 2 ? "failed" : "complete", class: index === 1 ? "no_changes" : "changes", changed_groups: 2, conversations: 12, messages_in_changed_groups: 340,
  seconds: 22.1, cpu_seconds: 8.7, cpu_percent_one_core: 39.4, rss_baseline_bytes: 100 * 1048576, rss_observed_peak_bytes: 129 * 1048576, allocated_bytes: 50 * 1048576, sources: ["codex", "claude"],
  source_results: [{ source: "codex", workspaces: 1, conversations: 8, messages: 130, ...(index === 2 ? { error: "Source unavailable" } : {}) }, { source: "claude", workspaces: 1, conversations: 4, messages: 210 }],
  phases: [{ name: "codex", seconds: 10.7, cpu_seconds: 4.9 }, { name: "claude", seconds: 9, cpu_seconds: 3 }, { name: "audit", seconds: 2.4, cpu_seconds: 0.8 }], audit: { outcome: "passed", discovered_units: 10, deferred_units: 1 } }));
syncHistory[0].phases[0].work = [{ phase: "read", unit: "group", done: 2, total: 2, total_known: true, seconds: 4, features: { conversations: 8, messages: 130, bytes: 4096, bytes_basis: "message_text", since_last_index_seconds: 300, load_one_minute: 1.2 } }];
syncHistory[0].forecasts = [{ reason: "launch", elapsed_seconds: 0, estimate: { point_seconds: 20, low_seconds: 10, high_seconds: 30, predicted_steps: 3, remaining_steps: 3 }, actual_remaining_seconds: 22.1 }];
const syncSettings = { enabled: true, interval_seconds: 300, sources: [], next_at: new Date(now + 180000).toISOString() };
const syncStatus = { settings: syncSettings, busy: false, library_empty: false, has_sources: true, available_sources: [{ name: "codex", preservation_mode: "filesystem-retained-sqlite-gated" }, { name: "claude" }], sources: [{ name: "codex" }, { name: "claude" }], history: syncHistory,
  source_states: [{ source_name: "codex", attempted_at: syncHistory[0].finished_at, succeeded_at: syncHistory[0].finished_at, preservation: {status:"legacy-gaps",legacy_preservation_gaps:2,pending_newer_inputs:true}, pending_preservation: true, last_capture_at: iso(3 * 3600000) }, { source_name: "claude", attempted_at: syncHistory[0].finished_at, succeeded_at: syncHistory[0].finished_at, last_capture_at: iso(3600000) }],
  statistics: { changes: { samples: 10, elapsed_seconds: { median: 22.1, p95: 61.8 }, cpu_seconds: { median: 8.7, p95: 11 }, rss_observed_peak_bytes: { last: 129 * 1048576 }, estimated_cpu_duty_percent_one_core: 2.9 } },
  forecast_accuracy: { 'automatic-sync': { launch: { attempts: 6, predicted: 5, evaluated: 4, covered: 3, median_absolute_error_seconds: 2.1, median_relative_error: 0.1, median_interval_width_seconds: 20 } } },
  forecast_runs: [{ run_id: 'capture-forecast', model: 'capture-index', finished_at: syncHistory[0].finished_at, state: 'complete', forecasts: syncHistory[0].forecasts }],
  coverage: [{ source_name: "codex", outcome: "passed", units: 2, last_verification: syncHistory[0].finished_at }], deferred: [{ name: "tools", changed_at: syncHistory[0].finished_at }], recovery_jobs: [], issues: [], measurement_limits: "CPU is process-wide." };

const carbonFactors = JSON.parse(readFileSync(join(repo, "carbon", "co2_factors.json"), "utf8"));
const carbonTokens = { small: { output: 1000000 }, medium: { uncached_input: 1000000, cache_write: 2000000, cache_read: 20000000, output: 500000 }, large: { output: 250000 } };
const carbon = { factors: carbonFactors, windows: [{ key: "all", tokens: carbonTokens }, { key: "30d", tokens: carbonTokens }],
  series: Array.from({ length: 20 }, (_, index) => ({ day: new Date(now - index * day).toLocaleDateString("en-CA"), value: "", tokens: carbonTokens })), choices: { provider: ["codex", "claude"], repository_name: ["pythia-software/pharos"] },
  default_tier_models: [{ model: "mystery-model", tokens: 500000 }], default_tier_unknown_tokens: 250000, unclassified_tokens: 100000, undated_tokens: 0 };

const mcpCall = { id: 1, called_at: iso(3600000), tool_name: "search_conversations", arguments_json: '{"query":"parser"}', status: "ok", error_text: null, duration_ms: 18, response_bytes: 900, estimated_output_tokens: 300, result_count: 3, truncated: 0 };
const toolsStatus = { conversations: 10, current_conversations: 10, pending_conversations: 0, tool_calls: 400, backfill: { running: false, done: 0, total: 0, error: null } };
const writingCounts = (typed, pasted, harness) => ({
  typed_words: typed, pasted_words: pasted, harness_words: harness, words: typed + pasted + harness,
  typed_chars: typed * 5, pasted_chars: pasted * 8, harness_chars: harness * 6, total_chars: typed * 5 + pasted * 8 + harness * 6,
});

const scheduledCloudPlan={provider:'gcp',region:'us-central1',tier:'gcp-standard',name:'Personal cloud backup',bucket:'pharos-browser-fixture',object:'pharos/library.tar.zst',identity:'pharos-backup-uploader',project:'pharos-example',cadence:7,keep:4,minimumAge:1,retentionMode:'count',retentionDays:30,time:'02:00',weekday:0,timezone:'America/Denver',passwordProtected:false,storage_class:'STANDARD'};
export const cloudScheduledStatus={
  current_host_id:'browser-mac',schedule_catalog:'/tmp/pharos/catalog.sqlite3',library_catalog:'/tmp/pharos/catalog.sqlite3',active_run_id:'',setup_root:'/tmp/pharos/cloud-setup',
  destinations:[{id:'cloud-browser',connected:true,config:scheduledCloudPlan,sample_verified:true,restore_verified:true}],
  schedules:[{destination_id:'cloud-browser',enabled:true,host_id:'browser-mac',host_label:'My Mac',catalog_path:'/tmp/pharos/catalog.sqlite3',cadence:7,time:'02:00',weekday:0,timezone:'America/Denver',anchor:'2026-10-23',next_run_at:iso(-3*day),failures:0}],
  runs:[{id:'cloud-full-latest',destination_id:'cloud-browser',kind:'full',trigger:'scheduled',state:'complete',object:scheduledCloudPlan.object,version:'1792771200000000',total_bytes:42000000000,file_bytes:140000000000,files:42879,started_at:iso(day),verified:false},
    {id:'cloud-full-verified',destination_id:'cloud-browser',kind:'full',trigger:'manual',state:'complete',object:scheduledCloudPlan.object,version:'1792684800000000',total_bytes:40000000000,file_bytes:135000000000,files:40000,started_at:iso(8*day),verified:true,restored_directory:'/tmp/pharos/recovered-library'}]
};

// The status snapshot (docs/background-work.md §7) the unified activity
// panel renders. `eventStream` marks a stub the harness serves as
// server-sent events: each document is one `snapshot` event, and the stream
// stays open.
export const eventStream = documents => ({ __eventStream: [documents].flat() });
const statusLibrary = { id: "library-fixture", name: "Pharos SSD", drive: { name: "Pharos SSD", ejectable: true, location: "external", mounted: true }, portable: true, path: "/Volumes/Pharos SSD/Pharos" };
// The freshness ledger (§5) in the server's shape: rows, and the summary
// groups the Data row shows.
const domain = (id, group, label, state, changes = {}) => ({ domain: id, group, label, as_of: null, behind: { state, count: null, unit: null },
  state_label: { current: "Current", behind: "Behind", unknown: "Unknown" }[state], reason: null, catching_up: null, catching_up_label: null, next: null, ...changes });
const idleLedger = [
  domain("conv:claude-code-with-a-long-source-name", "conversations", "Conversations (claude-code-with-a-long-source-name)", "current", { as_of: iso(120000) }),
  domain("conv:codex", "conversations", "Conversations (codex)", "current", { as_of: iso(180000) }),
  domain("view", "search", "Search view", "current"),
  domain("search.substring", "search", "Substring search", "current"),
  domain("an.identities", "analyses", "Identity links", "current", { as_of: iso(3 * 3600000) }),
  domain("an.tools", "analyses", "Tools rollup", "current", { as_of: iso(3 * 3600000) }),
  domain("findings", "findings", "Findings", "scheduled", { as_of: iso(3 * 3600000), state_label: "Scheduled",
    reason: { code: "changed", text: "New data since today's full pass; the next one runs tomorrow" }, next: "updates with Refresh findings now, or the first index tomorrow" }),
  // A scheduled cloud backup within its schedule: a local one, which has no
  // schedule, would be unknown.
  domain("backup.cloud:archive", "backups", "Cloud backup (Archive bucket)", "current", { as_of: iso(2 * day) }),
];
const idleSummary = { all_current: true, text: "All current", checked_label: "checked", checked_at: iso(180000), groups: [
  { group: "conversations", label: "Conversations", state: "current", short: "", short_age: false, text: "current", as_of: iso(180000), as_of_label: "", catching_up: null, shown: false },
  { group: "analyses", label: "Analyses", state: "current", short: "", short_age: false, text: "current", as_of: iso(3 * 3600000), as_of_label: "", catching_up: null, shown: false },
  { group: "findings", label: "Findings", state: "scheduled", short: "updates tomorrow", short_age: false, text: "scheduled, updates with Refresh findings now, or the first index tomorrow", as_of: iso(3 * 3600000), as_of_label: "", catching_up: null, shown: false, muted: true },
  { group: "backups", label: "Backups", state: "current", short: "", short_age: false, text: "current", as_of: iso(2 * day), as_of_label: "", catching_up: null, shown: false },
] };
const deferred = { reason: { code: "deferred-by-schedule", text: "Scheduled indexing skips analyses" }, next: "updates with the next manual index", behind: { state: "behind", count: null, unit: null } };
const busyLedger = [
  domain("conv:claude-code-with-a-long-source-name", "conversations", "Conversations (claude-code-with-a-long-source-name)", "behind", { as_of: iso(2 * day), reason: { code: "indexing", text: "Indexing now" }, catching_up: "op-index", catching_up_label: "Index changes" }),
  domain("conv:codex", "conversations", "Conversations (codex)", "unknown", { as_of: iso(3600000), reason: { code: "schedule-off", text: "Scheduled indexing is off for it" }, next: "updates with the next manual index" }),
  domain("view", "search", "Search view", "behind", { behind: { state: "behind", count: 38, unit: "workspace" }, reason: { code: "refreshing", text: "Updating now" }, catching_up: "op-view", catching_up_label: "Index the Search view" }),
  domain("an.identities", "analyses", "Identity links", "behind", { as_of: iso(3 * 3600000), ...deferred }),
  domain("an.tools", "analyses", "Tools rollup", "behind", { as_of: iso(3 * 3600000), ...deferred }),
  domain("findings", "findings", "Findings", "behind", { as_of: iso(3 * 3600000), reason: { code: "daily-pass-due", text: "Today's full pass has not run yet" }, next: "updates with the next manual index" }),
  domain("backup.cloud:archive", "backups", "Cloud backup (Archive bucket with a long name)", "behind", { as_of: iso(9 * day), reason: { code: "overdue", text: "Older than its schedule" }, next: "updates with the next scheduled cloud backup" }),
];
const busySummary = { all_current: false, checked_at: null, groups: [
  { group: "conversations", label: "Conversations", state: "behind", short: "updating", short_age: false, text: "catching up", as_of: iso(2 * day), as_of_label: "last current", catching_up: "op-index", tooltip: "Conversations (claude-code-with-a-long-source-name): behind, indexing now", shown: true },
  { group: "analyses", label: "Analyses", state: "behind", short: "waits for manual index", short_age: false, text: "behind, updates with the next manual index", as_of: iso(3 * 3600000), as_of_label: "last current", catching_up: null, tooltip: "Identity links: behind, scheduled indexing skips analyses\nTools rollup: behind, scheduled indexing skips analyses", shown: true },
  { group: "findings", label: "Findings", state: "behind", short: "waits for manual index", short_age: false, text: "behind, updates with the next manual index", as_of: iso(3 * 3600000), as_of_label: "last current", catching_up: null, shown: true },
  { group: "search", label: "Search", state: "behind", short: "updating", short_age: false, text: "catching up", as_of: null, as_of_label: "", catching_up: "op-view", shown: true },
  { group: "backups", label: "Backups", state: "behind", short: "behind", short_age: true, text: "behind, updates with the next scheduled cloud backup", as_of: iso(9 * day), as_of_label: "last current", catching_up: null, shown: false, muted: false },
] };
// Resources as a service reports them (§10): itself and a Terminal command.
const statusResources = (label, processes) => ({ cpu_percent: 3, memory_bytes: 340e6, sampled_at: iso(0), basis: "proc_pid_rusage", approximate: false, label, processes });
const idleResources = statusResources("Pharos is using under 1% of one core · 180 MB", [
  { pid: 4120, role: "service", cpu_percent: 0, memory_bytes: 180e6, sampled_at: iso(0), basis: "proc_pid_rusage", text: "Library service · under 1% of one core · 180 MB" }]);
const busyResources = statusResources("Pharos is using ~45% of one core · 1.2 GB", [
  { pid: 4120, role: "service", cpu_percent: 38, memory_bytes: 1.1e9, sampled_at: iso(0), basis: "proc_pid_rusage", text: "Library service · ~38% of one core · 1.1 GB" },
  { pid: 5150, role: "cli:capture", cpu_percent: 7, memory_bytes: 64e6, sampled_at: iso(0), basis: "proc_pid_rusage", text: "pharos capture (Terminal) · ~7% of one core · 64 MB" }]);
const statusHeadings = { work: "Work", data: "Data" };
// The schedule (docs/background-work.md §11), as /api/status and GET
// /api/background report it.
const backgroundItem = (id, label, cadence, state, changes = {}) => ({
  id, label, verb: `Updating ${label}`, feeds: "Pages it feeds", why: "Why it runs where it does.", sub: changes.sub ?? "Pages it feeds", cadence, cadence_label: cadence, column: cadence, placed: cadence,
  adaptive: false, fixed: false, opt_in: false, override: "", state, state_label: { current: "Updated", running: "Updating now", failed: "Failed", queued: "Up next", unavailable: "Not set up" }[state],
  as_of: iso(41 * 60000), next_at: iso(-19 * 60000), error: null, note: null, cost: { seconds: 40, runs: 7, class: "light", text: "about 40 s" },
  choices: [{ value: "", label: "Automatically" }, { value: "daily", label: "Daily" }, { value: "manual", label: "Only when I ask" }], ...changes,
});
const backgroundView = (items, headline) => ({
  mode: "on", paused_until: null, items, headline,
  cadences: ["frequent", "hourly", "daily"].map(cadence => {
    const members = items.filter(item => item.column === cadence);
    const state = ["failed", "running", "queued"].find(state => members.some(item => item.state === state)) ?? "current";
    return { id: cadence, label: { frequent: "Frequently", hourly: "Hourly", daily: "Daily" }[cadence], description: "When it runs", state, state_label: state === "failed" ? "1 failed" : members[0]?.state_label,
      as_of: iso(41 * 60000), next_at: iso(-19 * 60000), items: members.map(item => item.id) };
  }),
  everything: { seconds: 540, text: "Everything now takes about 9 min, mostly “Your writing”." },
});
const idleBackground = backgroundView([
  backgroundItem("conversations", "New conversations", "frequent", "current", { sub: "Claude Code, Codex, Conductor and Antigravity", fixed: true, choices: null }),
  backgroundItem("repositories", "Repositories & merges", "frequent", "current"),
  backgroundItem("writing", "Your writing", "hourly", "current", { cost: { seconds: 358, runs: 7, class: "heavy", text: "about 6 min" }, note: "Moves to Frequently once a run usually takes under a minute." }),
  backgroundItem("findings", "Findings", "hourly", "current"),
  backgroundItem("preserve", "Preserve originals", "daily", "current"),
  backgroundItem("backup", "Back up to /Volumes/Backup Drive With A Long Name/Pharos Backup", "manual", "unavailable", { column: "daily", cost: null }),
], { tone: "good", title: "Up to date", text: "Everything is on schedule." });
const busyBackground = backgroundView([
  backgroundItem("conversations", "New conversations", "frequent", "current", { fixed: true, choices: null }),
  backgroundItem("repositories", "Repositories & merges", "frequent", "failed", { error: "git rev-list timed out after 5 s in ~/code/a-repository-with-a-very-long-name-that-has-to-wrap" }),
  backgroundItem("writing", "Your writing", "hourly", "running"),
  backgroundItem("findings", "Findings", "hourly", "queued"),
  backgroundItem("preserve", "Preserve originals", "daily", "current"),
], { tone: "busy", title: "Updating your writing", text: "Human Words cards, and Analyze → Writing update when this finishes.", tech: null });
export const backgroundDocument = {
  settings: { mode: "on", paused_until: null, interval_seconds: 300, daily_at: "03:00", hold_when_busy: true, overrides: {} },
  view: idleBackground, rule: { frequent_seconds: 60, hourly_seconds: 600 },
  descriptions: { frequent: "Every 5 min, as soon as there’s new work", hourly: "At most once an hour, after 10 quiet minutes", daily: "Overnight at 3:00 AM, or the first quiet moment after" },
  cost: { window_hours: 24, seconds: 18000, text: "about 5 h", peak_bytes: 9.5e9, cadences: [
    { id: "frequent", label: "Frequently", seconds: 10800, measured: true, text: "197 checks, 54 found nothing new" },
    { id: "hourly", label: "Hourly", seconds: 6840, measured: false, text: "projected: about 8 min × 14, mostly Your writing" },
    { id: "daily", label: "Daily", seconds: 480, measured: false, text: "projected" }] },
};
export const idleStatus = {
  version: 1, revision: 1, generated_at: iso(0), host: { id: "test-mac", label: "This Mac" }, library: statusLibrary,
  eject: { verdict: "safe", eta_seconds: null, checked: "polled", blockers: [], headline: "Safe to eject; checked again on eject", summary: "Nothing Pharos can see is writing to the library. Pharos checks everything again when you eject." },
  operations: [], attention: [], freshness: idleLedger, freshness_summary: idleSummary, resources: idleResources, headings: statusHeadings, features: { unified_activity: true }, journal: { dropped: 0 },
  background: idleBackground,
};
const statusOperation = (id, changes) => ({
  id, kind: "index.changes.manual", variant: null, subject: null, label: "Index changes", verb: "Indexing changes", state: "running", state_label: "Indexing changes",
  detail: null, reason: null, trigger: "user", process: "service", priority: "foreground", started_at: iso(120000), ended_at: null,
  step: null, time: null, stoppable: true, stop: { method: "POST", path: "/api/operations/stop", body: { id } }, holds: [{ resource: "drive", mode: "write" }],
  affects: [], on_eject: "Stops between workspaces; the next index resumes it.", latency: "batch", severity: "none", legacy: null, children: [], children_count: 0, ...changes,
});
export const busyStatus = {
  ...idleStatus, revision: 2,
  eject: { verdict: "safe-in", eta_seconds: null, checked: "polled", headline: "Safe to eject after the current workspace: stops index changes", summary: "Ejecting stops index changes after the current workspace; if that takes more than 7 s, Pharos refuses and you can try again.",
    blockers: [{ operation: "op-index", label: "Index changes", on_eject: "Stops between workspaces; the next index resumes it.", latency: "batch", verdict: "safe-in" }] },
  operations: [
    statusOperation("op-index", { step: { index: 3, count: 6, name: "Indexing a source with a very long name that has to wrap somewhere", source: "Claude Code", unit: "byte", done: 812e6, total: 1980e6, fraction: 0.41 },
      time: { state: "estimating", low_seconds: 180, high_seconds: 360, label: "about 3–6 min" },
      children: [statusOperation("op-chain", { kind: "analysis.chain", label: "Update analyses", state_label: "Updating analyses", stoppable: false, stop: undefined })], children_count: 2 }),
    statusOperation("op-view", { kind: "maintenance.library-view", label: "Index the Search view", state_label: "Indexing the Search view", priority: "background", stop: { method: "POST", path: "/api/operations/stop", body: { id: "op-view" } }, step: { index: 1, count: null, name: "Search view", unit: "workspace", done: 2, total: 40, fraction: 0.05 } }),
    statusOperation("op-backup", { kind: "backup.local", label: "Back up", state: "blocked", state_label: "Waiting", priority: "background", stoppable: true, stop: { method: "POST", path: "/api/operations/stop", body: { id: "op-backup" } }, detail: "Waiting for a capture to finish" }),
    statusOperation("op-old", { kind: "analysis.findings", label: "Update findings", state: "succeeded", state_label: "Done", stoppable: false, stop: undefined, ended_at: iso(60000) }),
  ],
  freshness: busyLedger, freshness_summary: busySummary, resources: busyResources, background: busyBackground,
  attention: [{ id: "backup-failed", rule: 6, severity: "error", title: "The last backup failed", detail: "The destination drive is full: /Volumes/Backup Drive With A Long Name/Pharos Backup", action: { label: "Open Backups", path: "/settings/backups" } }],
};
// The broker's document, with two other libraries. A broker operation
// touching a library makes its verdict busy, as the broker computes it.
const copyBlocker = { operation: "op-copy", kind: "library.transfer", label: "Copy library", on_eject: "Not stoppable; eject of either drive must wait.", latency: "none", verdict: "busy", process: "broker" };
const brokerBusy = eject => ({ ...eject, verdict: "busy", eta_seconds: null, headline: "Eject waits for copy library",
  summary: "Eject waits for copy library, which cannot stop in time.", blockers: [copyBlocker, ...(eject.blockers || [])] });
export const managedStatus = snapshot => ({
  version: 1, revision: 3, generated_at: iso(0),
  libraries: [
    { id: "local", name: "Pending history", selected: true, connected: true, service: { state: "running" }, state_label: "Copying the library", snapshot, eject: brokerBusy(snapshot.eject), error: null },
    { id: "archive", name: "Archive (Backup drive)", selected: false, connected: true, service: { state: "running" }, state_label: "Copying the library", eject: brokerBusy(idleStatus.eject), error: null, snapshot: idleStatus },
    { id: "spare", name: "Spare", selected: false, connected: false, service: { state: "stopped" }, state_label: "Not open", snapshot: null, eject: idleStatus.eject, error: null },
  ],
  // As the broker reports its own work: in app.operations, naming the
  // libraries it touches (a copy from the selected library to Archive).
  app: { operations: [statusOperation("op-copy", { kind: "library.transfer", label: "Copy library", state_label: "Copying the library", process: "broker", stoppable: false, stop: undefined,
    libraries: ["local", "archive"], step: { index: 2, count: 3, name: "Verified copy", unit: "byte", done: 4e9, total: 1e10, fraction: 0.4 } })], update: null },
  attention: snapshot.attention,
  // Every Pharos process on this Mac: the app, both open libraries' services.
  resources: statusResources("Pharos is using ~62% of one core · 1.6 GB", [
    { pid: 4000, role: "broker", cpu_percent: 12, memory_bytes: 90e6, sampled_at: iso(0), basis: "proc_pid_rusage", text: "Pharos app · ~12% of one core · 90 MB" },
    ...snapshot.resources.processes.map(process => ({ ...process, library: "local" })),
    { pid: 4300, role: "service", library: "archive", cpu_percent: 5, memory_bytes: 210e6, sampled_at: iso(0), basis: "proc_pid_rusage", text: "Library service (Archive) · ~5% of one core · 210 MB" }]),
});

export const defaultApi = {
  "GET /api/app/state": {application:"/Applications/Pharos.app",selected:"local",mcp:"external",busy:false,libraries:[{id:"local",name:"Pending history",available:true,config:"/Users/test/Library/Application Support/Pharos/Libraries/Pending/library.toml"},{id:"external",name:"My archive",available:false,config:"/Volumes/Archive/Pharos/library.toml"}]},
  "GET /api/cloud-backup": {destinations:[],runs:[],schedules:[],active_run_id:"",setup_root:"/tmp/pharos/cloud-setup"},
  "POST /api/cloud-backup/schedule": {saved:true},
  "GET /api/ask/status": { settings: { enabled: false, showInvitation: true, selection: null }, providers: [] },
  "POST /api/ask/clear-visits": {ok:true},
  "POST /api/ask/visits": {ok:true},
  "POST /api/ask/remove-visit": {ok:true},

  // Library preferences, saved views and snapshots.
  "GET /api/preferences": ({ state }) => ({ values: state.preferences }),
  "POST /api/preferences": ({ body, state }) => {
    Object.assign(state.preferences, body?.set);
    for (const key of body?.delete ?? []) delete state.preferences[key];
    return { values: state.preferences };
  },
  "GET /api/query-computed-columns": [],
  "GET /api/query-object-references": { references: [] },

  // Query Table, on the production transport.
  "POST /api/query/usage/carbon": carbon,
  "POST /api/query/writing/series": ({ body }) => ({ works: 2, ...(body?.hourly ? { hourly: [] } : {}),
    daily: Array.from({ length: 14 }, (_, index) => ({ day: new Date(now - index * day).toLocaleDateString("en-CA"), typed_messages: 3, messages: 5, ...writingCounts(120 + index * 10, 40, 900) })),
    totals: { messages: 70, typed_messages: 42, first_day: new Date(now - 13 * day).toLocaleDateString("en-CA"), ...writingCounts(2000, 560, 12600) } }),
  "* /api/query/:dataset/:operation": ({ params, body, query }) => schemas[params.dataset] && answerQuery(params.operation, body, datasets[params.dataset], schemas[params.dataset], { now, id: query.get("id") }),
  "POST /api/query/:dataset": ({ params, body }) => schemas[params.dataset] && answerQuery("rows", body, datasets[params.dataset], schemas[params.dataset], { now }),

  // The header, menus and status polling on every page.
  "GET /api/activity": { active: 0, runs: [] },
  "GET /api/library/status": { idle: true, portable: true, library_dir: "/Volumes/Pharos SSD/Pharos", drive: { name: "Pharos SSD", mounted: true, ejectable: true }, activities: [] },
  "GET /api/probe/status": { library: true, needs_onboarding: false, host: { id: "test-mac", label: "This Mac" } },
  "GET /api/probe/tools": {tools:[{kind:'claude',path:'~/.claude',found_by:'data folder'}]},
  "POST /api/probe": ({body})=>({candidates:sourceSetupCandidates.filter(candidate=>body.kinds.includes(candidate.kind)),checked:[]}),
  "GET /api/upgrade": { running: false, needed: false, steps: [], repository_renames: {} },
  "GET /api/pricing": { unpriced_models: [], priced_tokens: 1000, unpriced_tokens: 0, confirmed_changes: 0, proposed_changes: 0 },
  "GET /api/usage/concurrency-prototype": () => ({ ...defaultApi["GET /api/usage/parallelism"], timezone: "UTC" }),
  "GET /api/usage/parallelism": {
    generated_at: iso(0), timezone: "Local", period_start: iso(day).slice(0, 10), period_end: iso(0).slice(0, 10),
    days: [iso(day).slice(0, 10), iso(0).slice(0, 10)],
    threads: [0, 1].map(index => ({
      id: `timing-${index}`, conversation_id: rootConversation, work_id: workID,
      workspace_id: `${workID}-${index}`, workspace_name: `Workspace ${index + 1}`,
      repository: "pharos", title: longTitle, provider: "codex", model: "gpt-5.3-codex", agent_depth: 0,
      turns: [[now - 2 * 3600000 + index * 600000, now - 3600000]],
      model_spans: [[now - 2 * 3600000 + index * 600000, now - 5400000]],
    })),
    coverage: { conversations: 2, mirrors_removed: 0, unexplained_waits_excluded: 0, timing: "Captured timing", catalog: "Test catalog" },
  },
  "GET /api/origins": { machines: [{ id: "machine-fixture", label: "Test Mac", kind: "device" }] },
  "GET /api/usage/history": { oldest_conversation_at: iso(180 * day) },
  "GET /api/usage/summary": { generated_at: iso(0), windows: ["1h", "6h", "today", "24h", "7d", "30d", "all"].map((key, index) => ({ key, human_words: 100 * (index + 1), tokens: 1000 * (index + 1), cost_usd: index + 1, co2e_g: { central: 10 * (index + 1) } })), carbon_factors: carbonFactors },
  "GET /api/search/text/options": { providers: ["codex", "claude"], sources: ["conductor"] },
  "POST /api/search/text/validate": ({ body }) => ({ where: body.where ?? { op: ["literal", "regex", "phrase"].includes(body.mode) ? body.mode : "term", text: body.query } }),
  "GET /api/search/text/capabilities": { fuzzy: true, regex: true, case_sensitive: true },
  "GET /api/tools/status": toolsStatus,
  "GET /api/menu-bar/status": { error: "", indexed_at: iso(60000), library: { library_dir: "/Volumes/Pharos SSD/Pharos", activities: [], drive: { name: "Pharos SSD", ejectable: true } } },
  "GET /api/status": idleStatus,
  "GET /api/status/stream": eventStream(idleStatus),
  "GET /api/app/status": managedStatus(idleStatus),
  "GET /api/app/status/stream": eventStream(managedStatus(idleStatus)),
  "POST /api/operations/stop": { stopping: true },
  "GET /api/menu-bar/usage": { human_words: 3240, tokens: 1200000, cost_usd: 8.42, co2e_g: 18.25 },

  // Settings.
  "GET /api/feedback/diagnostics": { service: "Recent service diagnostics" },
  "POST /api/feedback/submit": ({ body }) => ({ id: body.id, received: true }),
  "GET /api/health/counts": { workspaces: 1964, conversations: 2400, messages: 311120 },
  "GET /api/health/storage": { volume_name: "Pharos SSD", index_bytes: 2_000_000_000, internal_free_bytes: 100_000_000_000, free_bytes: 400_000_000_000, staging_bytes: 0, staging_cap_bytes: 1, path: "/Volumes/Pharos SSD/Pharos", status: "available" },
  "GET /api/health/pricing": { priced_tokens: 90, assumed_tokens: 0, partial_tokens: 0, unpriced_tokens: 10, total_tokens: 2_500_000_000, cost_usd: 1234.5, recent_tokens: 400_000_000, recent_cost_usd: 300, confirmed_changes: 3, proposed_changes: 0, unpriced_models: [] },
  "GET /api/health/carbon": { factors: carbonFactors, windows: [{ key: "all", co2e_g: { central: 123456 } }, { key: "30d", co2e_g: { central: 23456 } }] },
  "GET /api/health/storage/breakdown": {
    status: "measured", measured_at: iso(60000), volume_name: "Pharos SSD", volume_total_bytes: 2_000_000_000_000, volume_free_bytes: 400_000_000_000, total_bytes: 170_000_000_000,
    estimated: "Repository shares, and the catalog's division by Mac and source, are estimated from message counts.",
    parts: [["originals_latest", "Originals: latest copies", 46e9], ["originals_history", "Originals: earlier versions", 15e9], ["originals_other", "Originals: manifests and indexes", 2e8],
      ["catalog_database", "Pharos catalog", 94e9], ["catalog_log", "Catalog write-ahead log", 12e9], ["catalog_sidecar", "Search and analysis caches", 1e9], ["preserved", "Preserved packages", 0], ["other", "App, settings and staging", 1.8e9]]
      .map(([key, label, bytes]) => ({ key, label, bytes })),
    machines: [{ id: "test-mac", label: "A Mac Studio with a rather long name (this Mac)", originals_bytes: 52e9, catalog_bytes: 90e9 }, { id: "air", label: "MacBook Air", originals_bytes: 9e9, catalog_bytes: 17e9 }],
    sources: [{ id: "conductor", label: "conductor", originals_bytes: 30e9, catalog_bytes: 50e9 }, { id: "codex", label: "codex", originals_bytes: 16e9, catalog_bytes: 30e9 }],
    repositories: [{ id: "repo", label: "a-repository-name-without-any-spaces-to-wrap", originals_bytes: 20e9, catalog_bytes: 40e9 }, { id: "", label: "No repository", originals_bytes: 30e9, catalog_bytes: 20e9 }],
  },
  "GET /api/health/freshness": { sources: [], sync_active: false },
  "GET /api/health/drive": { free_bytes: 400_000_000_000, sizes: { library_bytes: 12_000_000_000 } },
  "GET /api/authorship": { built_at: iso(3600000), running: false, stale: false, error: null, totals: { typed_words: 1234, typed_messages: 98, messages: 400 }, last_30_days: { typed_words: 500 } },
  "GET /api/index": { active: false, sync_active: false, hosts },
  "GET /api/capture": { active: false, runs: [] },
  "GET /api/sources": sources,
  "GET /api/sync/status": syncStatus,
  "GET /api/sync/issues": { issues: [] },
  "GET /api/sync/settings": syncSettings,
  "GET /api/sync/history": { history: syncHistory },
  "GET /api/sync/recovery": { jobs: [] },
  "GET /api/background": backgroundDocument,
  "POST /api/background/settings": backgroundDocument,
  "POST /api/background/run": ({ body }) => ({ accepted: true, items: [body?.item].filter(Boolean) }),
  "GET /api/backup": { active: false, run: null, runs: [], last_success: { destination: "/Users/someone/Pharos Backup", finished_at: iso(2 * day) }, record: "/Volumes/Pharos SSD/Pharos/backups.json", suggested_destination: "/Users/someone/Pharos Backup" },

  // Findings and MCP.
  "GET /api/findings": findings,
  "POST /api/findings/seen": { ok: true },
  "POST /api/findings/cart/prompt": ({ body }) => ({ target: body?.target, label: "explo", handoff: body?.handoff ?? "pr", prompt: `Pharos findings for explo:\n${(body?.ids ?? []).join("\n")}`, count: body?.ids?.length ?? 0 }),
  "GET /api/findings/:id": ({ params }) => { const card = findingCards.find(item => item.id === params.id); return card && findingDetail(card); },
  "GET /api/mcp": ({ state }) => ({ enabled: state.mcpEnabled ?? true, transport: "stdio", command: "/Applications/Pharos.app/Contents/MacOS/pharos", args: ["--config", "/Users/someone/Library/Application Support/Pharos/archive.toml", "mcp"], note: "Agent clients launch this local command when they connect.", skill: { name: "search-prior-coding-sessions", text: "Search prior coding sessions with Pharos.", paths: { codex: "~/.codex/skills/search-prior-coding-sessions/SKILL.md" } } }),
  "POST /api/mcp/enabled": ({ body, state }) => { state.mcpEnabled = body.enabled; return { enabled: body.enabled }; },
  "GET /api/mcp/calls": { items: [mcpCall], filtered_total: 1, tool_stats: [{ tool_name: "search_conversations", calls: 1, errors: 0, average_output_tokens: 300, largest_output_tokens: 300, truncated_calls: 0, average_duration_ms: 18 }], stats: { total_calls: 1, failed_calls: 0, average_output_tokens: 300, largest_output_tokens: 300, truncated_calls: 0 } },

  // The workspace reader and delegation explorer.
  "GET /api/work/:id": ({ params }) => params.id === workID ? work : undefined,
  "GET /api/work/:id/conversations/:conversation": ({ params }) => {
    const overview = work.conversations.find(item => item.id === params.conversation);
    return params.id === workID && overview ? { ...overview, repo_root: work.location, messages: params.conversation === rootConversation ? transcript() : childMessages(params.conversation) } : undefined;
  },
  "GET /api/work/:id/delegation": ({ query }) => delegation(query),
  "GET /api/work/:id/parallel": ({ query }) => parallel(query),
  "GET /api/work/:id/find": { conversations: [] },
};
