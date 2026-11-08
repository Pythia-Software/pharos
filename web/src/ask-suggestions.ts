// The Ask Pharos suggestion catalog (docs/ask-suggestions.md). An area is a
// part of the user's work Pharos can notice something in; Go's signals
// (internal/archive/ask_suggestions.go) score each area against the library.
// A family is one generic way to invite a conversation about an area. Its
// text never names what a signal found: only an optional repository scope
// ({scope}) and period ({period}) come from the library. Every site map page
// is covered by an area or excluded here with a reason, so a new page needs
// a question or a decision. generate-site-map.mjs writes the result to
// internal/archive/assets/ask-suggestions.json.

export type AskForm = "understand" | "compare" | "change" | "find" | "improve" | "check" | "learn";
export type AskArea = { id: string; label: string; pages: string[]; signals: string[] };
// signals narrows a family to some of its area's signals; prior is its
// starting weight before outcomes adjust it.
export type AskFamily = { id: string; area: string; form: AskForm; prior: number; variants: string[]; signals?: string[] };

export const askForms: AskForm[] = ["understand", "compare", "change", "find", "improve", "check", "learn"];

export const askAreas: AskArea[] = [
  {id:"message-labels",label:"Conversation functions",pages:["analyze/labels","analyze/query/message_labels"],signals:["labels.coverage"]},
  { id: "verification", label: "Tests and builds", pages: ["analyze/calls", "analyze/time"], signals: ["verification.failures", "verification.time"] },
  { id: "tool-failures", label: "Failing tool calls", pages: ["analyze/calls", "analyze/tools", "analyze/query/tool_calls", "analyze/session_shape", "analyze/query/session_shape"], signals: ["tools.failures"] },
  { id: "machine-time", label: "Where agent time goes", pages: ["analyze/time", "analyze/tools"], signals: ["tools.time"] },
  { id: "thinking", label: "Reasoning and waiting", pages: ["analyze/thinking-overview", "analyze/thinking-detail", "analyze/query/model_requests"], signals: ["thinking.reasoning"] },
  { id: "context", label: "Context pressure", pages: ["analyze/compaction", "analyze/query/compactions", "analyze/tools", "analyze/thinking-detail", "analyze/query/model_requests"], signals: ["context.compaction", "context.output"] },
  { id: "spend", label: "Tokens and cost", pages: ["analyze/tokens", "analyze/query/usage", "cards"], signals: ["spend.tokens", "spend.change"] },
  { id: "models", label: "Model choice", pages: ["analyze/tokens", "analyze/query/usage"], signals: ["models.mix"] },
  { id: "caching", label: "Cache reuse", pages: ["analyze/tokens", "analyze/query/usage"], signals: ["spend.cache"] },
  { id: "delegation", label: "Sub-agents and delegation", pages: ["work", "analyze/tokens"], signals: ["delegation.agents"] },
  { id: "parallelism", label: "Agents side by side", pages: ["analyze/parallelism", "analyze/query/parallelism"], signals: ["activity.parallel"] },
  { id: "rhythm", label: "Working patterns", pages: ["analyze/activity", "analyze/query/conversation_activity", "analyze/habits", "analyze/query/rhythm"], signals: ["activity.days"] },
  { id: "repositories", label: "Repositories and activity", pages: ["home", "library", "analyze/activity"], signals: ["activity.repos"] },
  { id: "writing", label: "My writing and steering", pages: ["analyze/human", "analyze/query/messages", "analyze/session_shape"], signals: ["writing.volume", "writing.back_and_forth"] },
  { id: "supplied-context", label: "Context I supply", pages: ["analyze/human", "analyze/query/messages"], signals: ["writing.supplied"] },
  { id: "agent-experience", label: "What agents run into", pages: ["analyze/agent_experience", "analyze/agent_event_rates", "analyze/query/agent_events", "analyze/query/agent_event_messages", "analyze/query/agent_event_rates", "findings/fixes", "findings/fixes/table", "findings/fixes/settings"], signals: ["agent.friction", "agent.reactions"] },
  { id: "web", label: "Sites agents visit", pages: ["analyze/tools", "analyze/query/tool_calls"], signals: ["web.hosts", "web.new_hosts"] },
  { id: "risk", label: "Risky actions", pages: ["findings/safety", "findings/safety/table", "findings/safety/settings"], signals: ["risk.safety"] },
  { id: "findings", label: "Recommended changes", pages: ["findings", "findings/table", "findings/settings"], signals: ["improve.findings", "improve.watching"] },
  { id: "skills", label: "Skills and instructions", pages: ["analyze/skills", "analyze/query/skill_usages"], signals: ["skills.usage"] },
  { id: "mcp", label: "MCP servers", pages: ["analyze/mcp", "analyze/query/mcp_calls", "settings/mcp"], signals: ["mcp.calls"] },
  { id: "carbon", label: "Carbon impact", pages: ["analyze/carbon"], signals: ["carbon.usage"] },
  { id: "recall", label: "Past work", pages: ["library", "work"], signals: ["recall.history"] },
  { id: "ask-history", label: "Earlier Ask chats", pages: ["settings/ask"], signals: ["pharos.ask_history"] },
  { id: "library-health", label: "Library health", pages: ["settings", "settings/sync", "settings/sources", "settings/logs", "settings/libraries"], signals: ["pharos.freshness"] },
  { id: "backups", label: "Backups", pages: ["settings/backups"], signals: ["pharos.backups"] },
  // Scored when suggestions are chosen, from the page visits on this Mac.
  { id: "discovery", label: "Pharos views", pages: ["analyze", "analyze/query"], signals: ["pharos.unseen"] },
];

// Pages no question is about, and why.
export const askExcludedPages: Record<string, string> = {
  "ask": "Ask Pharos full screen: the chat itself, not a subject of its questions.",
  "settings/layout": "Header and navigation layout preferences.",
  "settings/preferences": "Display preferences, including agent event markers and arcs in fingerprints.",
  "analyze/query/tl1_attempts": "TL1 is mothballed; its attempts stay queryable without being promoted.",
};

export const askFamilies: AskFamily[] = [
  {id:"message-label-patterns",area:"message-labels",form:"understand",prior:1,variants:["How do the functions of my conversations change over time{scope}?","Which message labels show up most often{scope}?"]},
  { id: "verification-latency", area: "verification", form: "understand", prior: 1, signals: ["verification.time"], variants: ["Find the causes of testing latency{scope}.", "Where do tests and builds slow my agents down{scope}?"] },
  { id: "verification-reliability", area: "verification", form: "understand", prior: 1, signals: ["verification.failures"], variants: ["What makes my tests and builds unreliable{scope}?", "Why do my agents struggle to get tests passing{scope}?"] },
  { id: "verification-improve", area: "verification", form: "improve", prior: 0.9, variants: ["How could my agents spend less time verifying their work{scope}?"] },
  { id: "failures-causes", area: "tool-failures", form: "understand", prior: 1, variants: ["What keeps going wrong when my agents run commands{scope}?", "Why do my agents' tool calls fail{scope}?"] },
  { id: "failures-change", area: "tool-failures", form: "change", prior: 0.8, variants: ["How have my agents' errors changed {period}?"] },
  { id: "time-where", area: "machine-time", form: "understand", prior: 0.9, variants: ["Where does my agents' time go{scope}?", "Which kinds of work take my agents the longest{scope}?"] },
  { id: "time-improve", area: "machine-time", form: "improve", prior: 0.8, variants: ["What could make my agents finish work faster{scope}?"] },
  { id: "thinking-payoff", area: "thinking", form: "understand", prior: 0.8, variants: ["How much do my agents reason, and when does it pay off?", "Which work makes my models think the longest?"] },
  { id: "context-crowding", area: "context", form: "understand", prior: 1, variants: ["What's crowding my agents' context{scope}?", "Why do my long conversations run out of room{scope}?"] },
  { id: "context-improve", area: "context", form: "improve", prior: 0.9, signals: ["context.compaction"], variants: ["How could I keep long conversations from compacting{scope}?"] },
  { id: "spend-where", area: "spend", form: "understand", prior: 0.9, signals: ["spend.tokens"], variants: ["Where do my tokens go{scope}?", "What's driving my API cost equivalent{scope}?"] },
  { id: "spend-change", area: "spend", form: "change", prior: 1, signals: ["spend.change"], variants: ["How has my token use changed {period}?", "What changed in my agent spending {period}?"] },
  { id: "models-fit", area: "models", form: "compare", prior: 1, variants: ["Am I using the right models for the right work?", "How do the models I use compare on cost and results?"] },
  { id: "caching-reuse", area: "caching", form: "improve", prior: 0.8, variants: ["How well do my agents reuse cached context?", "Where could better caching save me tokens?"] },
  { id: "delegation-value", area: "delegation", form: "understand", prior: 0.9, variants: ["How much of my work do sub-agents do, and does it help?", "When do my agents delegate, and what does it cost?"] },
  { id: "parallel-pattern", area: "parallelism", form: "understand", prior: 0.9, variants: ["How many agents do I usually run at once?", "What happens when I run several agents at the same time?"] },
  { id: "rhythm-week", area: "rhythm", form: "understand", prior: 0.7, variants: ["When during the week do I get the most done with agents?", "What does a typical day of agent work look like for me?"] },
  { id: "repositories-recent", area: "repositories", form: "change", prior: 0.9, variants: ["What have I been working on {period}?", "What stood out in my work {period}?"] },
  { id: "repositories-spread", area: "repositories", form: "compare", prior: 0.7, variants: ["How is my agent work spread across repositories?"] },
  { id: "writing-steering", area: "writing", form: "understand", prior: 0.9, signals: ["writing.volume"], variants: ["How do I usually steer my agents?", "Which phrases do I lean on most when I write to agents?"] },
  { id: "writing-improve", area: "writing", form: "improve", prior: 1, signals: ["writing.back_and_forth"], variants: ["How could I steer my agents with less back-and-forth{scope}?", "Where do I end up repeating myself to agents{scope}?"] },
  { id: "supplied-context", area: "supplied-context", form: "understand", prior: 0.7, variants: ["What context do I hand my agents, and how?", "How much of what I send agents is pasted or attached?"] },
  { id: "experience-friction", area: "agent-experience", form: "understand", prior: 1, signals: ["agent.friction"], variants: ["Where do my agents struggle to understand the project{scope}?", "What do my agents keep running into{scope}?"] },
  { id: "experience-reactions", area: "agent-experience", form: "understand", prior: 0.8, signals: ["agent.reactions"], variants: ["What surprises my agents most often?", "What have my agents learned the hard way{scope}?"] },
  { id: "web-reliance", area: "web", form: "understand", prior: 0.7, signals: ["web.hosts"], variants: ["Which sites do my agents rely on?", "What do my agents look up on the web?"] },
  { id: "web-new", area: "web", form: "check", prior: 1, signals: ["web.new_hosts"], variants: ["Have my agents been visiting any new or unusual sites {period}?"] },
  { id: "risk-review", area: "risk", form: "check", prior: 1.1, variants: ["Is there anything risky in what my agents have been doing lately?", "Which agent actions should I double-check?"] },
  { id: "findings-best", area: "findings", form: "improve", prior: 1.1, signals: ["improve.findings"], variants: ["What's the single best change I could make to how I work with agents?", "Which of Pharos's recommendations would help me most?"] },
  { id: "findings-payoff", area: "findings", form: "check", prior: 1, signals: ["improve.watching"], variants: ["Are the changes I've made to my workflow paying off?"] },
  { id: "skills-use", area: "skills", form: "understand", prior: 0.8, variants: ["Which skills do my agents actually use?", "Are my skills and instructions pulling their weight?"] },
  { id: "mcp-use", area: "mcp", form: "understand", prior: 0.7, variants: ["How do my agents use MCP servers?", "Which MCP tools do my agents depend on?"] },
  { id: "carbon-footprint", area: "carbon", form: "understand", prior: 0.6, variants: ["What's the carbon footprint of my agent work?", "How could I reduce the carbon impact of my agent work?"] },
  { id: "recall-similar", area: "recall", form: "find", prior: 0.9, variants: ["Help me find a past conversation where we solved something similar.", "Find earlier work I could build on{scope}."] },
  { id: "recall-forgotten", area: "recall", form: "find", prior: 0.7, variants: ["What did I work on last month that I might have forgotten?"] },
  { id: "ask-history", area: "ask-history", form: "find", prior: 0.5, variants: ["What have I asked Pharos before, and what did I learn?"] },
  { id: "library-current", area: "library-health", form: "check", prior: 0.9, variants: ["Is my library up to date, and is anything holding it back?"] },
  { id: "backups-safety", area: "backups", form: "check", prior: 0.8, variants: ["How is my library backed up, and what would I lose without a backup?"] },
  { id: "discovery-unseen", area: "discovery", form: "learn", prior: 0.9, variants: ["What could Pharos show me that I haven't looked at yet?", "Which Pharos views would be most useful for how I work?"] },
];

const placeholder = /\{([^}]*)\}/g;

// The JSON Go reads. It throws when a page is neither covered nor excluded,
// or when a question could name more than its scope and period.
export function buildAskSuggestions(pages: string[]) {
  const problems: string[] = [];
  const areas = new Map(askAreas.map(area => [area.id, area]));
  const known = new Set(pages);
  if (areas.size !== askAreas.length) problems.push("area ids repeat");
  if (new Set(askFamilies.map(family => family.id)).size !== askFamilies.length) problems.push("family ids repeat");
  const covered = new Set(askAreas.flatMap(area => area.pages));
  for (const area of askAreas) {
    for (const page of area.pages) if (!known.has(page)) problems.push(`area ${area.id} names unknown page ${page}`);
    if (!askFamilies.some(family => family.area === area.id)) problems.push(`area ${area.id} has no questions`);
  }
  for (const page of pages) {
    if (!covered.has(page) && !askExcludedPages[page]) problems.push(`page ${page} has no Ask suggestion area; add it to an area or to askExcludedPages`);
    if (covered.has(page) && askExcludedPages[page]) problems.push(`page ${page} is both covered and excluded`);
  }
  for (const page of Object.keys(askExcludedPages)) if (!known.has(page)) problems.push(`excluded page ${page} is unknown`);
  for (const family of askFamilies) {
    const area = areas.get(family.area);
    if (!area) { problems.push(`family ${family.id} names unknown area ${family.area}`); continue; }
    for (const signal of family.signals ?? []) if (!area.signals.includes(signal)) problems.push(`family ${family.id} names signal ${signal} outside its area`);
    if (!family.variants.length) problems.push(`family ${family.id} has no text`);
    if (!(family.prior > 0 && family.prior <= 2)) problems.push(`family ${family.id} prior must be in (0, 2]`);
    for (const text of family.variants) {
      for (const [, name] of text.matchAll(placeholder)) if (name !== "scope" && name !== "period") problems.push(`family ${family.id} uses {${name}}`);
      // Questions stay generic: no counts, quoted names or code.
      if (/[0-9`"“”]/.test(text)) problems.push(`family ${family.id} text "${text}" names specifics`);
      if (!/[?.]$/.test(text) || text.length > 100) problems.push(`family ${family.id} text "${text}" must be one short sentence`);
    }
  }
  if (problems.length) throw new Error("Ask suggestions:\n" + problems.join("\n"));
  return {
    version: 1,
    forms: askForms,
    areas: askAreas.map(({ id, label, pages, signals }) => ({ id, label, pages, signals })),
    families: askFamilies.map(({ id, area, form, prior, variants, signals }) => ({ id, area, form, prior, variants, ...signals ? { signals } : {} })),
    excluded: askExcludedPages,
  };
}
