// A small usage ledger for dashboard analysis tests: fourteen top-level
// conversations over five days, each with a root session and some delegated
// subagent runs. One model family (opus) is served by two providers, so
// provider + model groups differ from model groups. Input includes cache
// reads and cache writes. Each daily record has two hourly rows, keyed as the
// service keys them, so hour metrics read hours.
export const now = Date.parse("2026-10-16T18:00:00Z");
const models = [["claude", "opus"], ["claude", "sonnet"], ["codex", "gpt-5"], ["antigravity", "opus"]];
// Six Claude Opus, four Sonnet, two GPT-5 and two Antigravity Opus
// conversations: a five-conversation threshold keeps only Claude Opus.
const roots = [0, 0, 1, 2, 0, 1, 0, 1, 0, 2, 1, 0, 3, 3];

export function usageLedger() {
  const rows = [], hourly = [];
  roots.forEach((model, conversation) => {
    const [provider, family] = models[model], rootID = `conversation-${conversation}`;
    const sessions = [{ id: `${rootID}/root`, kind: "root", depth: 0, model }, ...Array.from({ length: conversation % 3 }, (_, index) => ({ id: `${rootID}/sub-${index}`, kind: "subagent", depth: 1 + index % 2, model: (model + 1 + index) % models.length }))];
    for (const session of sessions) for (let day = 0; day < 1 + (conversation + session.depth) % 2; day++) {
      const at = new Date(now - ((conversation % 5) + day + .5) * 86400000 - (conversation % 7) * 3600000);
      at.setUTCMinutes(0, 0, 0);
      const [sessionProvider, sessionFamily] = models[session.model], size = 1000 * (conversation + 1) + 250 * session.depth + 100 * day;
      const status = conversation === 3 && session.kind === "subagent" ? "assumed" : conversation === 9 && day === 1 ? "unpriced" : "priced";
      const reads = size * (1 + session.model), writes = size, uncached = size, input = reads + writes + uncached, output = size;
      const effort = conversation % 2 ? "high" : "medium", date = at.toISOString().slice(0, 10);
      const row = {
        id: `${session.id}|${date}|${sessionFamily}|${effort}`, day: date,
        first_usage_at: at.toISOString(), last_usage_at: new Date(at.getTime() + 2 * 3600000 - 1).toISOString(),
        provider: sessionProvider, model_family: sessionFamily, model: sessionFamily, effort, repository_name: `repo-${conversation % 3}`,
        root_provider: provider, root_model_family: family, root_effort: effort, root_repository_name: `repo-${conversation % 3}`,
        root_conversation_id: rootID, conversation_id: session.kind === "root" ? rootID : session.id, agent_session_id: session.id, session_kind: session.kind,
        delegated_session_id: session.kind === "root" ? null : session.id, delegated_run_depth: session.depth,
        total_tokens: input + output, input_tokens: input, cache_read_input_tokens: reads, cache_creation_input_tokens: writes, uncached_input_tokens: uncached, output_tokens: output,
        cost_usd: status === "unpriced" ? null : (input + output) * (session.model === 0 ? 15 : session.model === 1 ? 3 : 2) / 1e6, price_status: status,
      };
      rows.push(row);
      // The first hour holds a quarter of the day's usage, the next the rest.
      for (const [offset, part] of [[0, .25], [1, .75]]) {
        const start = new Date(at.getTime() + offset * 3600000).toISOString(), scaled = {};
        for (const key of ["total_tokens", "input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "uncached_input_tokens", "output_tokens", "cost_usd"]) scaled[key] = row[key] === null ? null : row[key] * part;
        hourly.push({ ...row, ...scaled, id: `${row.id}|${start}`, hour: start.replace(".000Z", "Z"), first_usage_at: start, last_usage_at: start });
      }
    }
  });
  return { rows, hourly };
}
export const usageRows = () => usageLedger().rows;
