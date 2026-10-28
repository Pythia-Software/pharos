// Small ledgers for the Human Words, Carbon and Parallelism dashboards:
// classified user messages, hour-by-agent parallelism records, and the usage
// ledger with the carbon estimates the service adds under some assumptions.
// Records spread over 40 days, so trailing windows and norms have history;
// days, weeks, months and local hours are local to each timestamp, as the
// service records them.
import { now, usageLedger } from "./usage-ledger.mjs";

export { now };
const pad = value => String(value).padStart(2, "0");
const localDay = date => `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
function calendar(at) {
  const date = new Date(at), monday = new Date(date);
  monday.setDate(monday.getDate() - (monday.getDay() + 6) % 7);
  return { day: localDay(date), week: localDay(monday), month: localDay(date).slice(0, 7) };
}
const repositories = ["pharos", "query-table", null];

// One message every few hours over 40 days. A conversation belongs to one
// work, and a work to one repository; some messages hold no typed words.
export function writingMessages() {
  return Array.from({ length: 320 }, (_, index) => {
    const at = new Date(now - (index % 40 + 1) * 86400000); at.setHours(7 + index * 7 % 15, index % 60, 0, 0);
    const conversation = index % 17, work = conversation % 7;
    const typed = index % 6 === 0 ? 0 : (index % 9 + 1) * (index % 4 + 2), pasted = index % 5 === 0 ? 40 + index % 30 : 0, copied = index % 7 === 0 ? 25 : 0, prompt = index % 11 === 0 ? 12 : 0, machine = index % 13 === 0 ? 80 : 0;
    // Message 1 was classified on a Mac in another zone: its recorded day is
    // the day after its local date here.
    const recorded = index === 1 ? calendar(at.getTime() + 86400000) : calendar(at);
    return {
      id: `message-${index}`, sent_at: at.toISOString(), ...recorded,
      typed_words: typed, pasted_words: pasted, copied_words: copied, prompt_words: prompt, machine_words: machine, words: typed + pasted + copied + prompt + machine,
      message_count: 1, conversation_id: `conversation-${conversation}`, work_id: `work-${work}`, workspace_id: `work-${work}`, repository_name: repositories[work % 3],
      provider: conversation % 2 ? "claude" : "codex", source_kind: conversation % 2 ? "claude" : "codex", channel: conversation % 3 ? "desktop" : "cli",
      correction_cue: typed > 0 && index % 4 === 1, positive_cue: typed > 0 && index % 8 === 3, has_source: copied > 0, main_category: typed ? "typed" : pasted ? "pasted" : "harness",
    };
  });
}

// Hour-by-agent records: each local hour of an agent at one level of agents
// active at once, built with the saved settings (workspace counting, a
// 60-minute gap), the only records the service serves. The last starts in
// the hour that is in progress at now.
export function parallelismRecords() {
  const rows = [];
  const cells = [];
  for (let day = 0; day < 40; day++) for (let slot = 0; slot < 3 + day % 4; slot++) {
    const at = new Date(now - (day + 1) * 86400000); at.setHours(8 + slot * 2 + day % 3, 0, 0, 0);
    cells.push([day, slot, at]);
  }
  const current = new Date(now); current.setMinutes(0, 0, 0);
  cells.push([40, 1, current]);
  for (const [day, slot, at] of cells) {
    const level = 1 + (day + slot) % 4, unit = (day * 3 + slot) % 6;
    for (const [counting, gap] of [["workspace", 60]]) {
      const unitMs = 1800000 + 600000 * (slot % 3), elapsedMs = unitMs / level;
      rows.push({
        id: `cell-${day}-${slot}`, started_at: at.toISOString(), ...calendar(at), hour_of_day: pad(at.getHours()), counting, gap_minutes: gap,
        unit_id: `unit-${unit}`, unit_label: `Workspace ${unit}`, workspace_name: `Workspace ${unit}`, repository_name: repositories[unit % 3], provider: unit % 2 ? "claude" : "codex",
        level, level_band: level >= 4 ? "4+" : String(level), unit_ms: unitMs, elapsed_ms: elapsedMs, unit_hours: unitMs / 3600000, elapsed_hours: elapsedMs / 3600000,
        parallel_elapsed_hours: level > 1 ? elapsedMs / 3600000 : 0,
      });
    }
  }
  return rows;
}

// The usage ledger with each record's carbon estimate, as the service
// computes it (withCarbon) under the assumptions estimate applies: a tier by
// model name, then the record's token categories through the calculator.
const tiers = { opus: "large", sonnet: "medium", "gpt-5": "medium" };
export function carbonUsage(estimate, factors) {
  const { rows, hourly } = usageLedger(), carbon = row => {
    const tier = tiers[row.model] ?? factors.default_tier;
    const result = estimate(factors, { tokens: { [tier]: { uncached_input: row.uncached_input_tokens + (row.unclassified_tokens ?? 0), cache_read: row.cache_read_input_tokens, cache_write: row.cache_creation_input_tokens, output: row.output_tokens } } });
    return { ...row, carbon_tier: tier, unclassified_tokens: row.unclassified_tokens ?? 0, energy_wh: result.facilityWh, co2e_g: result.co2, energy_kwh: result.facilityWh / 1000, co2e_kg: result.co2 / 1000 };
  };
  return { rows: rows.map(carbon), hourly: hourly.map(carbon) };
}
