// The guide explains agent-history nouns. Query input-row descriptions below
// belong to the query builder, rather than to the guide's glossary.
export const dataEntities = {
  libraries: { label: "Libraries", singular: "Library", description: "An archive of your agent history. One library can bring together conversations from several apps, repositories and computers.", detail: "A library keeps imported recordings together. The original history can come from several sources, such as an app’s history folder or an exported archive." },
  repositories: { label: "Repositories", singular: "Repository", description: "A code project tracked in Git. A repository can have several workspaces, such as separate checkouts for different changes.", detail: "Conversations without a recorded repository stay in the library. A repository filter groups recorded history; it does not scan your code." },
  workspaces: { label: "Workspaces", singular: "Workspace", description: "The group of conversations and changes you open as one result in Pharos Search. In a coding app, a workspace often corresponds to a checkout or worktree. Other sources group history by conversation or task.", detail: "A workspace groups the recorded history. A work item holds task details, when the source supplies them. For example, Pharos groups a TL1 task’s conversations into one workspace and also keeps its title, status and attempts as a work item. Several workspaces can belong to the same repository." },
  work_items: { label: "Work items", singular: "Work item", description: "Task details explicitly recorded by a source: the assignment’s title, status and any attempts or handoffs. A workspace groups the history of doing that work; a work item describes the task itself.", detail: "Work items are optional. Asking an agent to do something does not automatically create one. TL1 records tasks, so Pharos keeps those task details and groups each task’s conversations into a workspace. The work item and workspace can share the same title. Sources without task records still have workspaces and conversations." },
  conversations: { label: "Conversations", singular: "Conversation", description: "An exchange with an agent, like a chat in your agent app. It contains messages, turns and tool calls, with a main agent session and sometimes delegated helpers.", detail: "A top-level conversation is one you start independently. A helper can appear inside that chat or as a separate linked conversation, depending on how the app records it." },
  agents: { label: "Agent sessions", singular: "Agent session", description: "A sequence of turns carried out by one agent: the main agent or a delegated helper. Its messages, tool calls and model usage belong to the same session.", detail: "A session continues when you send another prompt; a turn is just one prompt and the activity that follows it. A session can use several models or effort levels. A helper has its own session, even when the app shows it inside the main conversation. The agent that delegates is its parent." },
  messages: { label: "Messages", singular: "Message", description: "One prompt, reply or thinking block in an agent session. A chat app usually shows prompts and replies as separate messages in order.", detail: "Apps display thinking and tool events differently. A message recorded with the user role can also contain instructions supplied by the app, a script or another agent." },
  tool_calls: { label: "Tool calls", singular: "Tool call", description: "An agent asking a tool to do something, such as read a file, run a command or search. A call can have a recorded result, error and duration.", detail: "The call and its result may be stored as separate messages. Tool call totals count calls, rather than counting both messages. MCP is a protocol for connecting agents to tools; Pharos MCP requests are calls to this library’s read-only tools." },
  turns: { label: "Turns", singular: "Turn", description: "A prompt and the agent activity that follows it, up to the next prompt. A turn can contain replies, thinking, tool calls and delegated work.", detail: "The agent can think, use tools and reply several times within one turn. Sending another prompt starts the next turn." },
  compactions: { label: "Context compactions", singular: "Context compaction", description: "When an agent replaces earlier conversation content in its working context with a shorter summary, so it has room to continue.", detail: "The app may do this automatically or in response to a command. The summary changes what the agent carries forward; the earlier recorded messages remain part of the conversation’s history." },
  sources: { label: "Sources", singular: "Source", description: "A history location or export that Pharos reads, such as a Codex history folder, Conductor database or ChatGPT export.", detail: "A source can hold local coding history or an exported web chat. Different apps and formats record different details; missing token usage, tool results or timestamps can mean the source did not record them. One library can read several profiles or computers." },
  providers: { label: "Providers", singular: "Provider", description: "The agent service or product recorded for a conversation or session, such as Codex or Claude. Model identifies the particular model it used.", detail: "The main agent and a helper can use different providers while working in the same conversation." },
  agent_apps: { label: "Agent apps", singular: "Agent app", description: "The program running a particular agent session, such as a desktop client or CLI. A helper can use a different app from the one where you started the conversation.", detail: "Start in a desktop app, then launch a CLI helper: the main session’s app is the desktop client and the helper’s app is the CLI. Both keep the Desktop starting channel. Source records may call the app the harness." },
  channels: { label: "Channels", singular: "Channel", description: "Where you started the main conversation: a desktop app, terminal, IDE, web app or another recorded entry point. This starting channel stays with the conversation and its helpers.", detail: "Channel describes the start; Agent app describes what runs a particular session. A CLI helper launched from a desktop conversation keeps Channel = Desktop while its Agent app is the CLI. Unknown means the source did not record the entry point." },
  machines: { label: "Machines", singular: "Machine", description: "A computer or environment associated with your agent history. Pharos distinguishes where the main conversation started from where each agent session ran.", detail: "Locations may be reported by a source or inferred from the computer that supplied its saved history. Missing or conflicting evidence stays unknown. A remote command alone does not locate an agent session, and these fields do not track a session moving between computers." },
  models: { label: "Models and effort", singular: "Model and effort", description: "Model names the system generating a response. Effort is the recorded reasoning-effort setting, such as low or high.", detail: "A session can change model or effort, and a helper can use different settings from its parent. Unknown effort means the app did not record the setting." },
  tokens: { label: "Tokens and API cost", singular: "Tokens and API cost", description: "Tokens measure the input a model reads and the output it generates. Each model response can report its token usage; API prices put a cost on that usage.", detail: "Input can include earlier messages, instructions and tool results. Cached input and reasoning output count when reported. Pharos estimates the API list-price equivalent where pricing is known; this is not your subscription bill." },
  skills: { label: "Skills and instructions", singular: "Skills and instructions", description: "Instructions guide an agent’s behavior. A skill is a named, reusable set of instructions that an agent can invoke or load.", detail: "A recorded invocation or file load shows evidence that a skill was requested or read. It does not prove the instructions were followed or remained in the model’s context." },
} as const;
export type DataEntity = keyof typeof dataEntities;
export const dataEntityKeys = Object.keys(dataEntities) as DataEntity[];
// Previously shared guide URLs still open the related history noun. These
// compatibility values are not glossary entries or navigation choices.
export const guideEntityAliases = { datasets: "conversations", labels: "messages", findings: "conversations", model_requests: "tokens", execution_machines: "machines" } as const satisfies Record<string, DataEntity>;
export type DataGuideSelection = DataEntity | keyof typeof guideEntityAliases;
export const dataGuideSelectionKeys = [...dataEntityKeys, ...Object.keys(guideEntityAliases)];
export function resolveGuideEntity(selection: DataGuideSelection): DataEntity {
  return Object.hasOwn(guideEntityAliases, selection) ? guideEntityAliases[selection as keyof typeof guideEntityAliases] : selection as DataEntity;
}
export const dataEntityGroups = [
  { id: "conversation", label: "Conversations", entities: ["conversations", "agents", "turns", "messages", "tool_calls", "skills", "compactions", "tokens"] },
  { id: "projects", label: "Projects", entities: ["repositories", "workspaces", "work_items"] },
  { id: "apps", label: "Apps & machines", entities: ["agent_apps", "providers", "models", "channels", "machines"] },
  { id: "archive", label: "Libraries", entities: ["libraries", "sources"] },
] as const satisfies readonly { id: string; label: string; entities: readonly DataEntity[] }[];

// Each definition describes an input row, before filters and grouping.
export const dataSources = {
  message_labels: { label: "Message labels", entity: "messages", grain: "One accepted specific label on a saved message, or one record of labeling coverage. A message can have several labels" },
  compactions: { label: "Context compactions", entity: "compactions", grain: "One recorded compaction, with a human, agent-app or unknown trigger" },
  model_requests: { label: "Model requests", entity: "tokens", grain: "One recorded response with token usage, with timing after tool results when available" },
  library: { label: "Workspaces", entity: "workspaces", grain: "One workspace" },
  usage: { label: "Daily usage by agent session", entity: "agents", grain: "Tokens for one agent session on one day, split by model and effort level" },
  conversation_activity: { label: "Conversation activity", entity: "conversations", grain: "One human-input message or daily agent-session usage record, counted under its top-level conversation" },
  writing: { label: "Writing by workspace", entity: "workspaces", grain: "Word totals for one workspace, including linked sub-agent activity" },
  writing_messages: { label: "User messages", entity: "messages", grain: "One message recorded with the user role" },
  tool_calls: { label: "Tool calls", entity: "tool_calls", grain: "One tool call" },
  tools: { label: "Daily tool summaries", entity: "tool_calls", grain: "One day’s tool-call totals for a tool and command, split by repository, source, provider, model, effort and agent kind" },
  mcp_calls: { label: "Pharos MCP requests", entity: "tool_calls", grain: "One request to this library’s Pharos MCP server" },
  skill_usages: { label: "Skill invocations and loads", entity: "skills", grain: "One piece of message evidence that a named skill was invoked or loaded" },
  parallelism: { label: "Workspace activity by hour", entity: "workspaces", grain: "Active time for one checkout, or one workspace when no checkout is known, in one local hour. Rows split when the number active on the machine or in the repository changes" },
  rhythm: { label: "Activity by hour", entity: "turns", grain: "One local hour of activity in a top-level conversation, split between you and agents working with you, unattended or automatically" },
  safety: { label: "Risky action notices", entity: "tool_calls", grain: "One risky action notice about recorded tool calls" },
  findings: { label: "Findings", entity: "conversations", grain: "One finding" },
  tl1_attempts: { label: "TL1 attempts", entity: "work_items", grain: "One recorded attempt at a TL1 work item" },
  agent_events: { label: "Agent events", entity: "messages", grain: "One sentence classified as one kind of event (experimental)" },
  agent_event_messages: { label: "Messages checked for agent events", entity: "messages", grain: "One agent reply or thinking block checked for events" },
} as const satisfies Record<string, { label: string; entity: DataEntity; grain: string }>;
export type DataSource = keyof typeof dataSources;

// Saved keys remain stable; "agent" counts independently started conversations.
export function describeDataSource(dataset: DataSource, counting: "workspace" | "agent" = "workspace") {
  if (dataset === "parallelism" && counting === "agent") return {
    ...dataSources.parallelism,
    label: "Conversation activity by hour",
    entity: "conversations" as const,
    grain: "Active time for one top-level conversation in one local hour, split when the number active on the machine or in the repository changes",
  };
  return dataSources[dataset];
}
