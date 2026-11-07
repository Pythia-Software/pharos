import React from "react";
import { dataEntities, type DataEntity } from "./data-taxonomy-model";
import { Icon } from "./icons";

type ExampleProps = {
  entity: DataEntity;
  onSelect: (entity: DataEntity) => void;
  conversation: string;
  onConversationChange: (conversation: "implementation" | "review") => void;
};

function highlight(entity: DataEntity, term: DataEntity) {
  return entity === term ? " pharos-guide-highlight" : "";
}

function ExampleTerm({ term, entity, onSelect, children, className = "", highlightSelection = true, label }: Pick<ExampleProps, "entity" | "onSelect"> & {
  term: DataEntity; children: React.ReactNode; className?: string; highlightSelection?: boolean; label?: string;
}) {
  return <button data-view-field="guide_entity" type="button" className={`guide-example-term ${className}${highlightSelection ? highlight(entity, term) : ""}`} aria-label={label ?? `Explain ${dataEntities[term].label.toLowerCase()}`} aria-pressed={entity === term} onClick={() => onSelect(term)}>{children}</button>;
}

function WindowLights() {
  return <span className="guide-window-lights" aria-hidden="true"><i /><i /><i /></span>;
}
function WindowBar({ title }: { title: string }) {
  return <div className="guide-window-bar"><WindowLights /><strong>{title}</strong><span className="guide-window-tools" aria-hidden="true"><Icon name="sidebar" /><Icon name="more" /></span></div>;
}

// Original, synthetic identity for the example app, not a source-app logo.
function Sunburst() {
  return <svg className="guide-sunburst" viewBox="0 0 40 40" aria-hidden="true"><circle cx="20" cy="20" r="8" fill="currentColor" />{Array.from({ length: 12 }, (_, i) => <path key={i} d="M18.5 2h3l-1 8h-1Z" fill="currentColor" transform={`rotate(${i * 30} 20 20)`} />)}<circle cx="20" cy="20" r="3" fill="white" /></svg>;
}
function Folder({ file = false }: { file?: boolean }) {
  return file ? <svg className="guide-file-icon" viewBox="0 0 32 32" aria-hidden="true"><path d="M8 3h11l6 6v20H8Z" /><path d="M19 3v7h6M12 16h9M12 20h9M12 24h6" /></svg>
    : <svg className="guide-folder-icon" viewBox="0 0 36 30" aria-hidden="true"><path d="M2 7V4a2 2 0 0 1 2-2h10l4 4h14a2 2 0 0 1 2 2v17a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2Z" /><path d="M2 10h32v15a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2Z" /></svg>;
}

function ChatBubble() {
  return <svg className="app-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true"><path d="M5 4h14a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H9l-6 3V6a2 2 0 0 1 2-2Z" /></svg>;
}

function ConversationChoices({ entity, conversation, onConversationChange }: Pick<ExampleProps, "entity" | "conversation" | "onConversationChange">) {
  return <div className="guide-chat-choices" role="group" aria-label="Example conversations">
    {(["implementation", "review"] as const).map(key => <button key={key} data-view-field="guide_example" type="button" aria-label={`Conversation ${key === "review" ? "Review" : "Implementation"}`} className={conversation === key ? highlight(entity, "conversations").trim() : ""} aria-pressed={conversation === key} onClick={() => onConversationChange(key)}><ChatBubble /><span>{key === "review" ? "Review" : "Implementation"}</span></button>)}
  </div>;
}

function ProjectExample(props: ExampleProps) {
  return <div className="guide-project-columns">
    <section><h4>With Git</h4><div className="guide-native-window guide-git-project">
      <div className="guide-browser-address">github.com/maya/lighthouse-app</div>
      <ExampleTerm {...props} term="repositories" className="guide-git-repository"><Icon name="tree" /><span>maya / <strong>lighthouse-app</strong><small>One repository · shared Git history</small></span></ExampleTerm>
      <div className="guide-git-files"><span><Folder />src</span><span><Folder />tests</span><span><Folder file />README.md</span></div>
      <div className="guide-project-checkouts"><small>Local checkouts of this repository</small>
        <ExampleTerm {...props} term="workspaces" className="guide-project-folder"><Folder /><span><strong>search-feature</strong><code>~/Workspaces/search-feature</code><small><Icon name="tree" /> feature/search</small></span></ExampleTerm>
        <ExampleTerm {...props} term="workspaces" className="guide-project-folder"><Folder /><span><strong>fix-navigation</strong><code>~/Workspaces/fix-navigation</code><small><Icon name="tree" /> fix/navigation</small></span></ExampleTerm>
      </div>
    </div><p>Each checkout is a workspace with its own conversations. Both belong to the same repository.</p></section>
    <section><h4>Without Git</h4><div className="guide-native-window guide-folder-project"><WindowBar title="Documents" />
      <ExampleTerm {...props} term="workspaces" className="guide-project-folder"><Folder /><span><strong>Launch notes</strong><code>~/Documents/Launch notes</code><small>Project folder · no Git repository</small></span></ExampleTerm>
      <div className="guide-project-documents"><span><Folder file />brief.md</span><span><Folder file />launch-plan.md</span></div>
      <div className="guide-project-chats"><small>Conversations in this workspace</small><span><ChatBubble />Draft the launch plan</span><span><ChatBubble />Review the announcement</span></div>
    </div><p>A workspace can group work on ordinary files. Some sources group by conversation or task instead.</p></section>
    <div className="guide-example-connections"><span>Open a conversation from search-feature</span><ConversationChoices {...props} /></div>
  </div>;
}

function FinderExample(props: ExampleProps) {
  const applications = props.entity === "sources";
  const rows: { term: DataEntity; name: string; location: string }[] = [
    { term: "sources", name: "sessions", location: "~/.codex" },
    { term: "sources", name: "chat-export.zip", location: "~/Downloads" },
    { term: "libraries", name: "My Pharos Library", location: "External SSD" },
  ];
  return <div className="guide-desktop-stage"><div className="guide-native-window guide-finder" aria-label="Finder window"><WindowBar title={applications ? "Applications" : "History locations"} /><div className="guide-finder-body">
    <div className="guide-finder-sidebar"><small>Favorites</small><button type="button" data-view-field="guide_entity" aria-label="Show Applications" aria-pressed={applications} className={applications ? "is-current" : ""} onClick={() => props.onSelect("sources")}><Icon name="compass" />Applications</button><span><Folder />Downloads</span><small>Locations</small><button type="button" data-view-field="guide_entity" aria-label="Show Pharos library" aria-pressed={!applications} className={!applications ? "is-current" : ""} onClick={() => props.onSelect("libraries")}><Icon name="read" />External SSD</button></div>
    {applications ? <ApplicationsContents {...props} /> : <div className="guide-finder-files"><div className="guide-finder-columns"><span>Name</span><span>Location</span></div>{rows.map(row => <ExampleTerm {...props} term={row.term} className="guide-finder-row" key={row.name}><span><Folder file={row.name.endsWith(".zip")} />{row.name}</span><span>{row.location}</span></ExampleTerm>)}</div>}
  </div><div className="guide-finder-path"><Folder /><span>{applications ? "Macintosh HD › Applications" : "Search results · 3 history locations"}</span>{applications ? <span>{sourceApps.length} items</span> : null}</div></div></div>;
}

function TaskExample(props: ExampleProps) {
  return <section className="guide-task-examples"><h4>A recorded task might be…</h4><ul className="pharos-guide-highlight">
    <li><Icon name="check" /><span><strong>A GitHub issue</strong><small>“Add search” · issue #142 · Open</small></span></li>
    <li><Icon name="check" /><span><strong>A ticket in a work tracker</strong><small>“Fix keyboard navigation” · assigned to Maya</small></span></li>
    <li><Icon name="check" /><span><strong>A task created in an agent app</strong><small>A TL1 task, with its title, status and attempts</small></span></li>
  </ul><p>These are examples of tracked assignments. Pharos shows a work item when the imported source includes task details; the examples do not imply an issue-tracker integration.</p><p>The <ExampleTerm {...props} term="workspaces" className="guide-inline-term">workspace</ExampleTerm> groups the conversations and changes involved in doing the task.</p></section>;
}

function ChatPrompt({ sender = "You", children, ...props }: ExampleProps & { sender?: string; children: React.ReactNode }) {
  return <ExampleTerm {...props} term="messages" className="guide-user-message"><small>{sender}</small><span>{children}</span></ExampleTerm>;
}
function ChatReply({ speaker = "SomeGPT", request, input, output, cost, children, ...props }: ExampleProps & {
  speaker?: string; request: number; input: string; output: string; cost: string; children: React.ReactNode;
}) {
  return <div data-guide-request={request} className="guide-model-response"><div className="guide-assistant-reply"><span className="guide-agent-avatar" aria-hidden="true"><Sunburst /></span><div>
    <ExampleTerm {...props} term="messages" className="guide-assistant-message"><small>{speaker}</small><span>{children}</span></ExampleTerm>
    <div className="guide-response-meta"><ExampleTerm {...props} term="tokens" className="guide-token-counts"><span>{input} input · {output} output tokens</span><span>≈ {cost} API cost</span></ExampleTerm></div>
  </div></div></div>;
}
function ChatTool({ kind, input, result, duration, ...props }: ExampleProps & {
  kind: "Read file" | "Edit file" | "Run command"; input: string; result: string; duration: string;
}) {
  return <ExampleTerm {...props} term="tool_calls" className={`guide-terminal${kind === "Run command" ? "" : " guide-file-tool"}`}>
    <span className="guide-terminal-heading"><Icon name={kind === "Read file" ? "read" : kind === "Edit file" ? "edit" : "command"} />{kind}<span>{duration}</span></span>
    <code>{kind === "Run command" ? <span className="guide-terminal-prompt">$ </span> : null}{input}</code>
    <span className="guide-terminal-result"><Icon name="check" />{result}</span>
  </ExampleTerm>;
}
function ChatTurn({ number, children, ...props }: ExampleProps & { number: number; children: React.ReactNode }) {
  return <section data-guide-turn={number} className={`guide-chat-turn${highlight(props.entity, "turns")}`} aria-label={`Turn ${number}`}>{children}</section>;
}
function Muscle() {
  return <span className="guide-skill-muscle" aria-hidden="true">💪</span>;
}
function SessionDescription({ name, assignment, result, ...props }: ExampleProps & { name: string; assignment: string; result: string }) {
  return <div className={`guide-helper-record${highlight(props.entity, "agents")}`}><ExampleTerm {...props} term="agents" className="guide-helper-description" highlightSelection={false}><Icon name="tree" /><span><strong>{name}</strong><span>{assignment}</span><small><Icon name="check" />{result}</small></span></ExampleTerm><ExampleTerm {...props} term="agent_apps" className="guide-helper-runtime"><Icon name="command" />SomeGPT CLI</ExampleTerm></div>;
}
function MainSession(props: ExampleProps) {
  const review = props.conversation === "review";
  return <>
    <ChatTurn {...props} number={1}><ChatPrompt {...props}>{review ? "Review the search change before we merge." : "Add search to the library."}</ChatPrompt><ChatReply {...props} request={1} input="920" output="42" cost="$0.002">{review ? "Should I check matching behavior, keyboard support, or both?" : "Should search match conversation titles, message text, or both?"}</ChatReply></ChatTurn>
    <ChatTurn {...props} number={2}>
      <ChatPrompt {...props}>Both. Keep the existing filters working, too.</ChatPrompt>
      <ExampleTerm {...props} term="skills" className="guide-chat-activity"><Muscle /><span>Using <strong>search-checks</strong></span><code>…/search-checks/SKILL.md</code></ExampleTerm>
      <ChatReply {...props} request={2} input="1,480" output="76" cost="$0.004">{review ? "I’ll read how search combines with filters, then add checks for the edge cases." : "I’ll use the existing search control and combine its results with the current filters."}</ChatReply>
      <ChatTool {...props} kind="Read file" input="src/search.ts" result="Read the matching and filter logic" duration="0.1s" />
      <ChatReply {...props} request={3} input="2,160" output="130" cost="$0.006">{review ? "Title and message matches share the same filter path. I’ll add tests for empty queries and combined filters." : "Titles already use the shared filter path. I’ll add message matches there so the filters keep applying."}</ChatReply>
      <ChatTool {...props} kind="Edit file" input={review ? "tests/search.test.ts" : "src/search.ts"} result={review ? "Added empty-query and combined-filter tests" : "Added message matching; kept the shared filters"} duration="0.2s" />
      <ChatReply {...props} request={4} input="2,540" output="85" cost="$0.007">{review ? "The checks are in place. I’m ready to run them and review keyboard behavior." : "Search now matches titles and messages. An empty query keeps the full filtered list."}</ChatReply>
    </ChatTurn>
    <ChatTurn {...props} number={3}><ChatPrompt {...props}>Check empty searches and keyboard navigation before we ship.</ChatPrompt><ChatReply {...props} request={5} input="3,010" output="61" cost="$0.008">I’ll ask one helper to run the search tests and another to check keyboard behavior.</ChatReply>
      <SessionDescription {...props} name="Test helper" assignment="Check empty queries and filter combinations." result="Completed · 12 tests passed" /><SessionDescription {...props} name="Review helper" assignment="Check Escape and keyboard focus in search." result="Completed · no keyboard issues found" />
      <ChatReply {...props} request={8} input="3,720" output="93" cost="$0.010">Both checks passed. Search matches titles and messages, keeps filters applied, and works with the keyboard.</ChatReply>
    </ChatTurn>
    <ChatTurn {...props} number={4}><ChatPrompt {...props}>Write a short changelog entry for this.</ChatPrompt><ExampleTerm {...props} term="compactions" className="guide-compaction-notice"><span /><Icon name="refresh" />Context compacted<span /></ExampleTerm><ChatReply {...props} request={9} input="1,640" output="96" cost="$0.005">Added search across conversation titles and message text. Existing filters still apply; an empty search shows the full list. Escape clears the query without moving focus.</ChatReply></ChatTurn>
  </>;
}
function ChatExample(props: ExampleProps) {
  return <div className="guide-chat-window" aria-label="SomeGPT desktop window">
    <div className="guide-chat-titlebar"><WindowLights /><span className={`guide-chat-app-title${highlight(props.entity, "agent_apps")}`}><ExampleTerm {...props} term="agent_apps" className="guide-chat-app-name" highlightSelection={false}>SomeGPT</ExampleTerm><ExampleTerm {...props} term="channels" className="guide-chat-platform">for Mac</ExampleTerm></span><Icon name="sidebar" /></div>
    <div className="guide-chat-sidebar"><ExampleTerm {...props} term="providers" className="guide-chat-brand"><Sunburst /><strong>SomeGPT</strong></ExampleTerm><span className="guide-chat-new" aria-hidden="true"><Icon name="edit" />New chat</span><small>Conversations</small><ConversationChoices {...props} /><span className="guide-chat-account" aria-hidden="true"><i>M</i>Maya</span></div>
    <div className="guide-chat-main"><div className="guide-chat-heading"><strong>{props.conversation === "review" ? "Review" : "Implementation"}</strong><Icon name="more" /></div>
      <div className={`guide-main-thread${highlight(props.entity, "agents")}`} role="group" aria-label="Main agent session"><div className="guide-main-thread-heading"><Sunburst /><span>Main agent</span></div><div className="guide-chat-history"><MainSession {...props} /></div></div>
      <div className="guide-chat-composer"><span aria-hidden="true">Message SomeGPT</span><div><Icon name="plus" /><ExampleTerm {...props} term="models">Example model · High<Icon name="chevron-down" /></ExampleTerm><span className="guide-composer-send" aria-hidden="true"><Icon name="arrow-up" /></span></div></div>
    </div>
  </div>;
}

function MachinePrompt({ machine, path, command, ...props }: ExampleProps & { machine: string; path: string; command: string }) {
  return <div className="guide-machine-command"><span>maya@<ExampleTerm {...props} term="machines" className="guide-machine-host">{machine}</ExampleTerm>:{path}$ </span><code>{command}</code></div>;
}
function MachinesExample(props: ExampleProps) {
  return <div className="guide-machine-example"><table className="guide-machine-roles"><caption>Example when both locations are known</caption><thead><tr><th scope="col">Agent session</th><th scope="col">Starting machine<small>Where the main conversation began</small></th><th scope="col">Execution machine<small>Where this session ran</small></th></tr></thead><tbody><tr><th scope="row">Main session</th><td>macbook</td><td>macbook</td></tr><tr><th scope="row">Test helper</th><td>macbook</td><td>build-server</td></tr></tbody></table><p className="guide-machine-inheritance">Helpers keep the main conversation’s starting machine. When a session runs on that same computer, both values match.</p><div className="guide-machine-scenarios"><div className="guide-native-window"><WindowBar title="Terminal — Main session" /><div className="guide-machine-terminal">
    <MachinePrompt {...props} machine="macbook" path="~/search-feature" command="somegpt" /><span>SomeGPT · Main session started</span><span className="guide-machine-user-prompt">› Review the changes here. Ask a helper on build-server to run the tests.</span>
    <span>I’ll review this checkout and delegate the tests to that machine.</span>
    <MachinePrompt {...props} machine="macbook" path="~/search-feature" command="git diff --stat" /><span>src/search.ts | 18 lines changed</span>
  </div></div><div className="guide-native-window"><WindowBar title="Terminal — Remote helper" /><div className="guide-machine-terminal">
    <MachinePrompt {...props} machine="build-server" path="~/search-feature" command="somegpt" /><span>SomeGPT · Test helper · delegated from macbook</span><span className="guide-machine-user-prompt">› Run the search tests for the main session.</span><MachinePrompt {...props} machine="build-server" path="~/search-feature" command="npm test -- search" /><small>12 tests passed</small>
  </div></div></div></div>;
}

const sourceApps = [
  { id: "calculator", name: "Calculator", history: false },
  { id: "chatgpt", name: "ChatGPT", history: true },
  { id: "claude", name: "Claude", history: true },
  { id: "codex", name: "Codex", history: true },
  { id: "conductor", name: "Conductor", history: true },
  { id: "cursor", name: "Cursor", history: true },
  { id: "notes", name: "Notes", history: false },
  { id: "safari", name: "Safari", history: false },
  { id: "vscode", name: "VS Code", history: true },
] as const;
function ApplicationsContents(props: ExampleProps) {
  return <div className="guide-applications-grid">{sourceApps.map(app => {
    const content = <><img src={`/assets/data-guide-apps/${app.id}.png`} alt="" width={64} height={64} /><span>{app.name}</span></>;
    return app.history ? <ExampleTerm {...props} term="sources" label={`Explain sources from ${app.name}`} className="guide-application" key={app.id}>{content}</ExampleTerm> : <div className="guide-application" key={app.id}>{content}</div>;
  })}</div>;
}

const scenes = {
  libraries: "finder", repositories: "project", workspaces: "project", sources: "finder", work_items: "task",
  conversations: "chat", agents: "chat", messages: "chat", tool_calls: "chat", turns: "chat", compactions: "chat", providers: "chat", agent_apps: "chat", channels: "chat", models: "chat", tokens: "chat", skills: "chat", machines: "machines",
} as const satisfies Record<DataEntity, string>;
export function guideSceneForEntity(entity: DataEntity) { return scenes[entity]; }
const captions: Record<Exclude<DataEntity, "machines">, string> = {
  libraries: "My Pharos Library is the library folder. It brings together recordings imported from source folders and exports, separately from the files you work on.",
  repositories: "lighthouse-app is a Git repository. search-feature and fix-navigation are separate checkouts of it. The Launch notes workspace has no repository.",
  workspaces: "A workspace groups conversations and changes in one working context. With Git, that is often a checkout or worktree; without Git, it can be a folder or another grouping supplied by the source.",
  work_items: "One task can involve several conversations. The task details and its conversation history describe different parts of the work.",
  conversations: "Implementation and Review are separate conversations. The selected entry in the left panel identifies the conversation you are reading.",
  agents: "The main thread is one agent session spanning four turns. Test helper and Review helper each describe a separate delegated session within the same conversation.",
  messages: "Your prompts and the agent’s replies are individual messages. Tool calls have their own entries between those messages.",
  tool_calls: "Reading files, editing code and running commands are tool calls. Several calls can happen during one turn, before the agent finishes its reply.",
  turns: "Each prompt starts a turn, continuing through the following agent activity up to the next prompt. The main session contains four turns, with different amounts of work in each.",
  compactions: "The inline notice marks a compaction. The agent continues in the same session with a shorter working context; the recorded conversation remains available.",
  sources: "The highlighted apps are places you work with agents. A source tells Pharos where to read their saved history: a history folder, database or export.",
  providers: "SomeGPT is the example provider. Its name and sunburst appear at the top of the app. The model selector names the model used for responses.",
  agent_apps: "SomeGPT for Mac is the desktop app running the agent. Its helpers use SomeGPT CLI, shown on their descriptions. They still share the Desktop starting channel of the main conversation.",
  channels: "The Mac window shows that this conversation started in a desktop app. The helpers use a CLI app but keep Desktop as their starting channel. A separately started terminal or browser conversation would have its own channel.",
  models: "The composer’s model selector sets the model and reasoning effort. These settings can change during a session, and helpers may use different settings.",
  tokens: "Each response has input and output usage. Input can include earlier messages, instructions and tool results. The costs illustrate API usage, separately from a subscription bill.",
  skills: "“Using search-checks” marks a skill invocation. Its reusable instructions guide how the agent checks search behavior. An invocation alone does not prove the instructions were followed.",
};
export function DataGuideExample(props: ExampleProps) {
  const scene = guideSceneForEntity(props.entity);
  const caption = props.entity === "machines" ? null : captions[props.entity];
  return <figure className="data-guide-example" aria-label={`Illustration of ${dataEntities[props.entity].label.toLowerCase()}`}><div className="guide-example-scene">{scene === "finder" ? <FinderExample {...props} /> : scene === "project" ? <ProjectExample {...props} /> : scene === "task" ? <TaskExample {...props} /> : scene === "machines" ? <MachinesExample {...props} /> : <ChatExample {...props} />}</div>{caption ? <figcaption>{caption}</figcaption> : null}</figure>;
}
