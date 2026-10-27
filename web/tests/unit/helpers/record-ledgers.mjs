// Small record ledgers for the Tools, Pharos MCP, Agent Time and Skills
// dashboards: tool calls, the daily tools rollup the service derives from
// them, Pharos MCP requests and skill evidence. Calls spread over 40 days, so
// trailing windows and norms have history; days and weeks are local dates of
// each timestamp, as the service records them.
export const now = Date.parse("2026-10-16T18:00:00Z");
const pad = value => String(value).padStart(2, "0");
const localDay = date => `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
function calendar(at) {
  const date = new Date(at), monday = new Date(date);
  monday.setDate(monday.getDate() - (monday.getDay() + 6) % 7);
  return { day: localDay(date), week: localDay(monday), month: localDay(date).slice(0, 7) };
}
// Hours before now, away from local midnight so a day's calls stay whole.
const hoursAgo = (index, spread) => { const at = new Date(now - (index % spread + 1) * 86400000); at.setHours(9 + index % 7, 15, 0, 0); return at.getTime(); };

const tools = [
  // name, category, activity, command, operation
  ["Bash", "command", "command:test", "go test", "go test"],
  ["Bash", "command", "command:build", "go build", "go build"],
  ["Read", "read", "tool:Read", null, "Read"],
  ["Edit", "edit", "tool:Edit", null, "Edit"],
  ["WebSearch", "web", "tool:WebSearch", null, "WebSearch"],
];
const repositories = ["pharos", "query-table", null];
export function toolCalls() {
  return Array.from({ length: 360 }, (_, index) => {
    // A rare failing tool and a rare command, so minimums hide groups.
    const [tool_name, tool_category, activity, command_name, operation_name] = index % 30 === 3 ? ["Grep", "read", "tool:Grep", null, "Grep"] : index % 45 === 8 ? ["Bash", "command", "command:vcs", "git status", "git status"] : tools[index % 5 === 4 ? 4 : index % 7 % 4];
    const at = hoursAgo(index * 7 + Math.floor(index / 5), 40), conversation = `conversation-${index % 13}`;
    const failed = (index % 9 === 0 || tool_name === "Edit" && index % 3 === 0 || tool_name === "Grep") && tool_name !== "Read";
    return {
      id: `call-${index}`, started_at: new Date(at).toISOString(), ended_at: new Date(at + 1000).toISOString(), ...calendar(at),
      tool_name, tool_category, activity, command_name, operation_name, program: command_name?.split(" ")[0] ?? null, command_category: command_name ? command_name.split(" ")[1] : null,
      repository_name: repositories[index % 3], provider: index % 4 ? "claude" : "codex", conversation_id: index % 17 === 0 ? null : tool_name === "Grep" ? "conversation-grep" : conversation, title: `Work ${index % 13}`,
      // Some calls have no timing, some only timestamps; result sizes vary
      // by orders of magnitude and are measured or estimated.
      duration_ms: index % 11 === 0 ? null : (index % 6 + 1) * 10 ** (1 + index % 4), duration_source: index % 5 ? "reported" : "timestamps",
      result_tokens: index % 8 === 0 ? 0 : (index % 9 + 1) * 10 ** (index % 3 + 1), result_tokens_source: index % 2 ? "measured" : "estimated",
      error_count: failed ? 1 : 0, call_count: 1, status: failed ? "error" : "ok", truncated: index % 13 === 0,
      search_query: tool_name === "WebSearch" ? `query ${index % 4}` : null,
    };
  });
}

// The rollup sums each day's calls of one tool group.
const rollupKeys = ["day", "week", "month", "tool_name", "tool_category", "command_name", "command_category", "program", "repository_name", "provider"];
export function toolsRollup(calls = toolCalls()) {
  const groups = new Map();
  for (const call of calls) {
    const key = JSON.stringify(rollupKeys.map(field => call[field]));
    const row = groups.get(key) ?? { id: `summary-${groups.size}`, ...Object.fromEntries(rollupKeys.map(field => [field, call[field]])), call_count: 0, error_count: 0, timed_count: 0, total_duration_ms: 0, result_tokens: 0, truncated_count: 0 };
    row.call_count += 1; row.error_count += call.error_count; row.truncated_count += call.truncated ? 1 : 0; row.result_tokens += call.result_tokens;
    if (call.duration_ms !== null) { row.timed_count += 1; row.total_duration_ms += call.duration_ms; }
    groups.set(key, row);
  }
  return [...groups.values()];
}

const mcpTools = ["search_conversations", "get_conversation_messages", "search_text", "list_findings"];
export function mcpCalls() {
  return Array.from({ length: 160 }, (_, index) => {
    const at = hoursAgo(index * 3, 40), failed = index % 10 === 0;
    return {
      id: index + 1, called_at: new Date(at).toISOString(), ...calendar(at), tool_name: mcpTools[index % 4], status: failed ? "error" : "ok",
      query_source: index % 3 === 0 ? null : index % 3 === 1 ? "codex" : "claude",
      duration_ms: index % 12 === 0 ? null : (index % 5 + 1) * 10 ** (1 + index % 3), response_bytes: (index % 7 + 1) * 10 ** (2 + index % 3), estimated_output_tokens: index % 9 === 0 ? null : (index % 7 + 1) * 25 * 10 ** (index % 3),
      result_count: index % 4 === 2 ? 0 : index % 6, call_count: 1, error_count: failed ? 1 : 0, truncated: index % 15 === 0, truncated_count: index % 15 === 0 ? 1 : 0,
    };
  });
}

const skills = [["review", "/skills/review/SKILL.md"], ["deploy", "/skills/deploy/SKILL.md"], ["triage", "/skills/triage/SKILL.md"]];
export function skillUsages() {
  return Array.from({ length: 150 }, (_, index) => {
    const [skill_name, skill_path] = index % 37 === 5 ? ["rare", "/skills/rare/SKILL.md"] : skills[index % 3], at = hoursAgo(index * 5, 40);
    const evidence_type = ["file_load", "explicit_invocation", "harness_load"][index % 7 % 3];
    return {
      id: `evidence-${index}`, created_at: new Date(at).toISOString(), ...calendar(at), skill_name: index % 23 === 0 ? null : skill_name, skill_path, evidence_type,
      status: index % 19 === 0 ? "error" : "ok", content_bytes: index % 4 === 0 ? null : index % 10 === 1 ? -1 : (index % 6 + 1) * 10 ** (2 + index % 2),
      conversation_id: index % 13 === 0 ? null : `conversation-${index % 11}`, tool_call_id: evidence_type === "file_load" ? `load-${Math.floor(index / 2)}` : null,
      provider: index % 2 ? "claude" : "codex", repository_name: repositories[index % 3],
    };
  });
}
