import type { WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisMeasure, RatioPart } from "./dashboard-analysis-model";
import { archetypes, bandBreakdowns } from "./session-shape-bands";

// Session shape over the session_shape dataset: one record is one agent
// session (one conversation; a delegated run is its own session), with its
// shape, bands and forensic signals (internal/archive/session_shape.go,
// docs/session-shape.md). A signal without enough evidence is empty, never
// zero, so each rate's population is the sessions whose coverage flag says
// the evidence was there.

const count = { kind: "number", decimals: 0 } as const, mean = { kind: "number", decimals: 1 } as const, percent = { kind: "percent", decimals: 1 } as const;
const hours = { kind: "number", decimals: 1 } as const, seconds = { kind: "duration", sourceUnit: "seconds" } as const;
const covered = (flag: string): WhereTerm[] => [{ field: flag, op: "=", value: "true" }];
export const toolData = covered("has_tool_data"), authorship = covered("has_authorship"), contextWindow = covered("has_context_window");
const counted = (field: string, label: string): RatioPart => ({ op: "count", field, label });
const signal = (label: string, field: string, note: string, where?: WhereTerm[]): AnalysisMeasure =>
  ({ label, unit: "events", format: count, op: "sum", field, value: field, additive: true, ...where ? { where } : {}, note });

const measures: Record<string, AnalysisMeasure> = {
  sessions: { label: "Sessions", unit: "sessions", format: count, op: "count", additive: true, note: "Agent sessions: each conversation once, a delegated run as its own session. Mirrors and aliases of one session count once." },
  active_hours: { label: "Active hours", unit: "hours", format: hours, op: "sum", field: "active_hours", value: "active_hours", additive: true, note: "Time between consecutive messages, counting gaps of 300 seconds or less; a longer gap counts as zero." },
  active_time: { label: "Active time per session", unit: "seconds", format: seconds, op: "sum", ratio: ["active_seconds", counted("active_seconds", "sessions with timestamps")], value: "active_seconds", additive: false,
    note: "Active time divided by sessions with timestamps, pooled. Per session it is the value the distribution draws." },
  messages: { label: "Messages", unit: "messages", format: count, op: "sum", field: "messages", value: "messages", additive: true, note: "Every message of the session's own stream; an embedded sub-agent's messages are its own." },
  typed_messages: { label: "Typed human messages", unit: "messages", format: count, op: "sum", field: "typed_human_messages", value: "typed_human_messages", additive: true, where: authorship,
    note: "User messages holding text you typed or dictated. Pasted, quoted, re-sent, template, harness and automated text does not count. Only sessions whose prompts are all classified." },
  tool_calls: { label: "Tool calls", unit: "calls", format: count, op: "sum", field: "tool_calls", value: "tool_calls", additive: true, where: toolData, note: "The session's own tool calls, in sessions the tool ledger covers." },
  // How much the agent did per thing you wrote: per typed message, and per
  // 100 typed words. The id stays "autonomy" for links made before.
  autonomy: { label: "Tool calls per typed message", unit: "calls per typed message", format: mean, op: "sum", ratio: ["tool_calls", "autonomy_prompts"], value: "autonomy", additive: false, where: [...toolData, ...authorship],
    note: "Tool calls per message holding text you typed (a session with none counts one), pooled: summed calls over summed messages. Pasted or quoted text alone does not count as a message." },
  calls_per_words: { label: "Tool calls per 100 typed words", unit: "calls per 100 words", format: mean, op: "sum", ratio: [{ op: "sum", expression: "[tool_calls]*100", label: "Tool calls × 100" }, "typed_words"], additive: false,
    where: [...toolData, ...authorship, { field: "typed_words", op: ">", value: "0" }],
    note: "Tool calls per 100 words you typed, pooled: summed calls over summed typed words. Only sessions where you typed something; pasted and quoted text is not typed." },
  turn_cycle: { label: "Turn cycle (session median)", unit: "seconds", format: seconds, op: "sum", ratio: ["turn_cycle_p50_seconds", counted("turn_cycle_p50_seconds", "sessions with a turn cycle")], value: "turn_cycle_p50_seconds", additive: false,
    note: "Each session's median gap from a typed human message to the agent's next message, leaving out gaps over 1,800 seconds. Pooled over groups it is the mean of those medians; the distribution draws them." },
  active_share: { label: "Active share of wall time", unit: "percent", format: percent, op: "sum", ratio: ["active_seconds", "wall_seconds"], additive: false, note: "Active time divided by first-to-last message time, pooled over sessions." },
  context_peak: { label: "Peak context share", unit: "percent", format: percent, op: "sum", ratio: ["context_peak_ratio", counted("context_peak_ratio", "sessions with a known window")], value: "context_peak_ratio", additive: false, where: contextWindow,
    note: "Each session's largest request input divided by its model's context window, the figure the reader's context rail uses; only sessions whose window is known. Pooled over groups it is the mean of the sessions' peaks." },
  one_shot_rate: { label: "One-shot edit turns", unit: "percent", format: percent, op: "sum", ratio: ["one_shot_edit_turns", "edit_turns"], additive: false, where: [...toolData, ...authorship],
    note: "Human turns with an edit and no edit–verify–edit retry on one file, divided by turns with an edit, pooled." },
  steering_load: { label: "Steering load", unit: "percent", format: percent, op: "sum", ratio: ["corrections", "typed_human_messages"], additive: false, where: authorship,
    note: "Typed messages with a correction cue in the typed text, divided by typed messages, pooled. Pasted text is never a correction." },
  verification: { label: "Verified handbacks", unit: "percent", format: percent, op: "sum", ratio: ["verified_handbacks", "edit_handbacks"], additive: false, where: [...toolData, ...authorship],
    note: "Handbacks after an edit in which a test or build command ran after the last edit, divided by handbacks after an edit, pooled." },
  failure_rate: { label: "Failure rate", unit: "percent", format: percent, op: "sum", ratio: ["failed_calls", "tool_calls"], additive: false, where: toolData,
    note: "Failed calls divided by tool calls, pooled. Rejections, interrupts and hook blocks are not failures." },
  failed_calls: signal("Failed calls", "failed_calls", "Calls that failed, other than your rejections, interrupts and hook blocks.", toolData),
  rejections: signal("Rejections", "rejections", "Calls you rejected.", toolData),
  interrupts: signal("Interrupts", "interrupts", "Calls interrupted before they finished.", toolData),
  hook_blocks: signal("Hook blocks", "hook_blocks", "Calls a hook blocked.", toolData),
  identical_retries: signal("Identical retries", "identical_retries", "Runs of three or more consecutive calls with the same tool and byte-identical input; each run adds its length less one.", toolData),
  thrash_retries: signal("Edit–verify–edit retries", "thrash_retries", "Within one human turn, an edit to a file, then a test or build command, then another edit to that file. Read-only commands are not verification.", [...toolData, ...authorship]),
  churned_files: signal("Churned files", "churned_files", "Files edited three or more times within fewer than 10 messages.", toolData),
  mid_task_compactions: signal("Mid-task compactions", "mid_task_compactions", "Compactions with at least two tool names both among the 10 calls before and the 5 after.", toolData),
  context_pressure: signal("Sessions over 90% of context", "context_pressure", "Sessions whose peak context exceeded 90% of the model's window; only sessions whose window is known.", contextWindow),
  corrections: signal("Corrections", "corrections", "Typed messages with a correction cue in the typed text.", authorship),
  long_unprompted_runs: signal("Long unprompted runs", "long_unprompted_runs", "Runs of more than 150 tool calls with no typed human message between them.", [...toolData, ...authorship]),
  ended_on_failure: signal("Ended on a failed call", "ended_on_failure", "Experimental: the session's last tool call failed and nothing you typed follows. It does not say the work was abandoned.", [...toolData, ...authorship]),
  forensic_events: { label: "Forensic events", unit: "events", format: count, op: "sum", field: "forensic_events", value: "forensic_events", additive: true, note: "Every signal's count added up: a way to find sessions to read, not a score." },
};

// Bands and archetypes in their order (session-shape-bands.ts).
export { bandBreakdowns, archetypes, type BandBreakdown } from "./session-shape-bands";
const notEnough = "Not enough evidence";

const sessionBreakdowns: AnalysisCatalog["breakdowns"] = {
  ...Object.fromEntries(Object.entries(bandBreakdowns).map(([id, band]) => [id, { label: `${band.label} band`, fields: [id], order: [...band.order], values: band.values, empty: notEnough }])),
  archetype: { label: "Archetype", fields: ["archetype"], order: [...archetypes.order], values: archetypes.values, empty: notEnough },
  provider: { label: "Provider", fields: ["provider"], order: ["claude", "antigravity", "codex", "chatgpt", "tl1", "canonical"] },
  harness: { label: "Harness", fields: ["harness"], empty: "No harness" },
  model_family: { label: "Model", fields: ["model_family"], empty: "Unknown model" },
  effort: { label: "Effort", fields: ["effort"], empty: "No effort" },
  repository_name: { label: "Repository", fields: ["repository_name"], empty: "No repository" },
  agent_kind: { label: "Agent depth", fields: ["agent_kind"], order: ["root", "subagent"], values: { root: "Root agent", subagent: "Sub-agent" } },
  channel: { label: "Channel", fields: ["channel"] },
  machine_id: { label: "Machine", fields: ["machine_id"] },
  execution_machine_id: { label: "Execution machine", fields: ["execution_machine_id"] },
};

export const sessionShapeAnalysisCatalog: AnalysisCatalog = {
  dataset: "session_shape", time: { first: "started_at", last: "started_at" }, measures, breakdowns: sessionBreakdowns,
  records: { label: "Session", plural: "Sessions", note: "A record is one agent session: a conversation, with a delegated run as its own session." },
  units: {
    // Every breakdown is a property of the session itself.
    session: { label: "Agent session", plural: "Agent sessions", key: "conversation_id", where: [{ field: "conversation_id", op: "is_not_null", value: "" }],
      breakdowns: Object.fromEntries(Object.entries(sessionBreakdowns).map(([id, breakdown]) => [id, breakdown.fields])),
      note: "An agent session is one conversation; a delegated run is its own session." },
    // A root and its delegated runs can differ in provider, model, repository
    // and every band, so no breakdown is constant within one.
    conversation: { label: "Root conversation", plural: "Root conversations", key: "root_conversation_id", where: [{ field: "root_conversation_id", op: "is_not_null", value: "" }],
      breakdowns: {}, note: "A root conversation is a top-level session with its delegated runs; their providers, repositories and bands can differ, so it has no breakdown." },
  },
  cycle: { hour: { kind: "timestamp", field: "started_at" }, weekday: { kind: "timestamp", field: "started_at" } },
  // A session can also fold by when it was last worked in; the range still
  // picks sessions by their start.
  cycleOn: { label: "Session start", options: { last_message: { label: "Last message", cycle: { hour: { kind: "timestamp", field: "last_message_at" }, weekday: { kind: "timestamp", field: "last_message_at" } } } } },
  periods: { day: "day", week: "week", month: "month" }, timestamp: "started_at",
  trend: { metric: "sessions", split: "length_band" },
  scatter: ["active_time", "tool_calls"],
};
