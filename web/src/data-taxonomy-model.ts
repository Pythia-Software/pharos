// Row grains describe Pharos's input datasets, before a query groups them.
// Keep these separate from Query Table: this is help about the host's archive.
export const dataEntities = {
  workspaces: { label: "Workspaces", singular: "Workspace", description: "A workspace groups related conversations and code changes. Each conversation contains the agent sessions that did the work." },
  conversations: { label: "Conversations", singular: "Conversation", description: "A conversation belongs to a workspace and can contain multiple agent sessions, including the main agent and delegated helpers." },
  agents: { label: "Agent sessions", singular: "Agent session", description: "An agent session belongs to a conversation and contains that agent’s messages and tool calls. Delegated helpers have their own sessions." },
  messages: { label: "Messages", singular: "Message", description: "A message is always within an agent session, which belongs to a conversation. It can be a prompt, reply, thought, or tool event." },
  tool_calls: { label: "Tool calls", singular: "Tool call", description: "A tool call is recorded in an agent session’s messages, alongside its result when retained. One session can make many tool calls." },
} as const;
export type DataEntity = keyof typeof dataEntities;

export const dataSources = {
  library: { label: "Workspaces", entity: "workspaces", grain: "One workspace" },
  usage: { label: "Agent Session Token Usage by Day, Model & Effort", entity: "agents", grain: "Daily tokens for one agent session, with a separate row for each model and effort level" },
  conversation_activity: { label: "Root Conversation Input & Usage Events", entity: "conversations", grain: "One human-input message or one daily agent-session usage record, attributed to a root conversation" },
  writing: { label: "Workspace Human-Word Totals", entity: "workspaces", grain: "Authorship totals for one workspace, including folded sub-agent and linked launched work" },
  writing_messages: { label: "Individual User Messages", entity: "messages", grain: "One message recorded with the user role" },
  tool_calls: { label: "Individual Tool Calls", entity: "tool_calls", grain: "One tool invocation" },
  tools: { label: "Tool Call Totals by Day & Tool/Command", entity: "tool_calls", grain: "Daily call totals for a tool and command, split by repository, source, provider, model, effort, and agent kind" },
  mcp_calls: { label: "Individual Calls to Pharos MCP", entity: "tool_calls", grain: "One request to Pharos’s MCP server" },
  skill_usages: { label: "Skill Invocations & Loads in Messages", entity: "messages", grain: "One piece of message evidence for a named skill invocation or load" },
  parallelism: { label: "Workspace Parallel Activity by Hour", entity: "workspaces", grain: "Active time for one workspace in one local hour, split when machine-wide or repository concurrency changes" },
  findings: { label: "Findings from Archived Work", entity: "workspaces", grain: "One finding" },
  tl1_attempts: { label: "Individual Agent Attempts (TL1)", entity: "agents", grain: "One TL1 attempt" },
  agent_events: { label: "Events in Agent Replies & Thinking", entity: "messages", grain: "One sentence judged to express one kind of event (experimental)" },
  agent_event_messages: { label: "Agent Messages Read for Events", entity: "messages", grain: "One agent reply or thinking block the event pass read" },
} as const satisfies Record<string, { label: string; entity: DataEntity; grain: string }>;
export type DataSource = keyof typeof dataSources;

// Parallelism's saved counting setting changes both the row object and its
// destination in the guide. "agent" counts user-managed root conversations.
export function describeDataSource(dataset: DataSource, counting: "workspace" | "agent" = "workspace") {
  if (dataset === "parallelism" && counting === "agent") return {
    ...dataSources.parallelism,
    label: "Conversation Parallel Activity by Hour",
    entity: "conversations" as const,
    grain: "Active time for one root conversation in one local hour, split when machine-wide or repository concurrency changes",
  };
  return dataSources[dataset];
}
