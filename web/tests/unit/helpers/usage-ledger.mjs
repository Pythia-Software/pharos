// A small usage ledger for dashboard analysis tests: twelve top-level
// conversations over five days, each with a root session and some delegated
// subagent runs. Every model family belongs to one provider, as in practice,
// so a card keyed by provider and model and a dashboard split by model
// describe the same groups.
export const now = Date.parse("2026-10-16T18:00:00Z");
const models = [["claude", "opus"], ["claude", "sonnet"], ["codex", "gpt-5"]];
// Six Opus, four Sonnet and two GPT-5 conversations: a five-conversation
// threshold keeps only Opus.
const roots = [0, 0, 1, 2, 0, 1, 0, 1, 0, 2, 1, 0];

export function usageRows() {
  const rows = [];
  roots.forEach((model, conversation) => {
    const [provider, family] = models[model], rootID = `conversation-${conversation}`;
    const sessions = [{ id: `${rootID}/root`, kind: "root", depth: 0, model }, ...Array.from({ length: conversation % 3 }, (_, index) => ({ id: `${rootID}/sub-${index}`, kind: "subagent", depth: 1 + index % 2, model: (model + 1 + index) % 3 }))];
    for (const session of sessions) for (let day = 0; day < 1 + (conversation + session.depth) % 2; day++) {
      const at = new Date(now - ((conversation % 5) + day + .5) * 86400000 - (conversation % 7) * 3600000);
      const [sessionProvider, sessionFamily] = models[session.model], size = 1000 * (conversation + 1) + 250 * session.depth + 100 * day;
      const status = conversation === 3 && session.kind === "subagent" ? "assumed" : conversation === 9 && day === 1 ? "unpriced" : "priced";
      const input = size * 3, reads = Math.round(input * (.2 + .1 * session.model)), output = size;
      rows.push({
        id: `${session.id}|${at.toISOString().slice(0, 10)}`, day: at.toISOString().slice(0, 10), hour: `${at.toISOString().slice(0, 13)}:00:00.000Z`,
        first_usage_at: at.toISOString(), last_usage_at: new Date(at.getTime() + 1800000).toISOString(),
        provider: sessionProvider, model_family: sessionFamily, model: sessionFamily, effort: conversation % 2 ? "high" : "medium", repository_name: `repo-${conversation % 3}`,
        root_provider: provider, root_model_family: family, root_effort: conversation % 2 ? "high" : "medium", root_repository_name: `repo-${conversation % 3}`,
        root_conversation_id: rootID, conversation_id: session.kind === "root" ? rootID : session.id, agent_session_id: session.id, session_kind: session.kind,
        delegated_session_id: session.kind === "root" ? null : session.id, delegated_run_depth: session.depth,
        total_tokens: input + output, input_tokens: input, cache_read_input_tokens: reads, uncached_input_tokens: input - reads, output_tokens: output,
        cost_usd: status === "unpriced" ? null : (input + output) * (session.model === 0 ? 15 : session.model === 1 ? 3 : 2) / 1e6, price_status: status,
      });
    }
  });
  return rows;
}
