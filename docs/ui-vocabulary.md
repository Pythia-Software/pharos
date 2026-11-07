# Pharos user-facing vocabulary

The Data Guide explains the nouns of agent history in terms people recognize
from their agent apps. Its definitions live in
[`data-taxonomy-model.ts`](../web/src/data-taxonomy-model.ts). Query input-row
descriptions are maintained separately in the same module and belong beside
the query builder. Feature explanations belong with their features.

The guide begins inside a conversation, from the conversation and agent
sessions down to turns, messages, tool calls, instructions, context and model
usage. Projects and tasks come next, then apps and computers, then library and
source locations. Work items remain optional task records, outside the
conversation hierarchy. Generic query terminology and Pharos interpretations
such as findings, risky-action notices, message labels and agent events are
not guide topics.

## Relationships

| Term | Use it for |
| --- | --- |
| Library | The current archive, including its sources, settings and saved cards. “Card Library” is the collection of reusable cards. |
| Repository | A Git code project. Several workspaces can belong to one repository; some history has no recorded repository. |
| Workspace | A group of recorded conversations and changes, opened as one Search result. The source determines the grouping: often a checkout/worktree, sometimes one conversation or task. Search table rows and `/work/{work_id}` identify workspaces. |
| Work item | Optional task details explicitly supplied by a source: title, status, attempts or handoffs. It describes the assignment; a workspace groups its history. TL1 imports each task as both a work item and a workspace containing its conversations, so they can share a title. A prompt alone does not create a work item. TL1 groups tasks into **TL1 projects**; a TL1 project need not be a Git repository. |
| Conversation | A recorded exchange in a workspace. A helper may be stored inside it or as a separate linked conversation. |
| Top-level conversation | An independently started conversation. Where an analysis uses this unit, linked helpers and continuations count with the initiating conversation. |
| Agent session | One agent’s recorded execution, main or delegated. It can use several models or effort levels. A delegated run counts one helper execution once across linked recordings and continuations. |
| Message | A prompt, reply or thinking block in an agent session. “User message” describes the recorded role; “your writing” means text classified as typed or dictated by the person. |
| Tool call | One request to a tool, with its result when recorded. Call and result messages do not count as two calls. |
| Turn | A prompt and the following agent activity up to the next prompt. A turn can contain several model requests, tool calls and helpers. |
| Model request (analysis tables) | A recorded model response with token usage and timing where available. It is not a conversation turn. |

This is not a mandatory tree of folders. Repository links and task details are
optional. Delegation and continuations can cross conversation, workspace,
provider and machine boundaries. Label the actual counting unit and explain
when an analysis combines linked activity. Parallelism counts **workspaces** or
**conversations**, not an interchangeable “agent/session/workspace.” Its checkout
counting can combine multiple recorded workspaces at the same location.
Keep optional work-item details separate from the main workspace → conversation
→ agent session → message hierarchy in the guide. Do not present work items as
a universal container between workspaces and conversations.

## Guide illustrations

Illustrate concepts in systems people recognize. All conversation terms use
one persistent chat-style agent app: conversations, sessions, turns, messages,
tool calls, skill instructions, compaction and token/API usage. Model requests
remain an analysis-table term, with their row definition beside the query builder;
legacy guide links open Tokens and API cost.
Keep its transcript, inline skill activity, compaction notice and response usage
present as the selected term changes. Move the yellow highlight to the relevant
part. Highlight the selected conversation in the left chat list. Show the main
agent as one continuous thread across four prompts, with inline descriptions
of its separate delegated helper sessions. Session selection highlights the
main thread and helper descriptions and scrolls a helper into view. Do not
split the main agent into per-turn session fragments or session tabs. Messages
highlight prompts and replies, leaving tool calls for the Tool calls topic.
Keep the model selector in the composer. The
SomeGPT name and original black-and-white sunburst identify the example
provider. The title bar identifies the main agent app; helper descriptions
show their CLI app. Highlight “for Mac” for the desktop starting channel,
which the helpers inherit even though their own app differs. Keep both nouns:
Channel answers how the main conversation began; Agent app answers what runs
this particular session. The origin and runtime fields must not be merged.
Use a continuing conversation with varied turns, repeated tool calls and model
responses, and distinct helper assignments. Avoid a checklist with only one
example of each noun, turn-number headings, or invented compaction details.
Show Git and non-Git workspace examples side by side. Use a GitHub-style project
for the repository and local checkout paths for its workspaces, with an ordinary
folder example for work without Git. Use a Finder-style folder window for the
library, named “My Pharos Library”, and Sources. Keep one window and sidebar:
Applications opens the app grid; External SSD returns to history locations.
Show Sources as a macOS Applications grid, highlighting the apps where agent
history begins among other ordinary apps. Use recognizable app icons and make
clear in the definition/caption that Pharos reads saved history locations or
exports, rather than an application bundle being a source.
One Machines guide entry explains two roles of the same machine entity:
Starting machine is where the main conversation began, inherited by helpers;
Execution machine is where a particular recorded session ran. A comparison
table shows a main session on macbook and its delegated test helper on
build-server, both with macbook as their starting machine. The same two
terminals visibly launch somegpt and contain user prompts. Highlight their
hostnames together. Explain before the example that locations depend on source
records or inference from the computer supplying saved history; unknown or
conflicting placement stays unknown. Do not imply automatic detection of remote
commands or a timeline of a session moving between machines. Keep old
execution_machines guide URLs as aliases to Machines, while retaining stored
query fields and the two independent attributions.
For optional work items, list recognizable tracked assignments: issues, tickets
and tasks supplied by agent apps. Do not imply that these examples are all
supported integrations. A task describes the assignment; a workspace groups its
recorded history. TL1 can supply both for one task.
These are original synthetic illustrations, not screenshots of source apps.
Keep them visually distinct from Pharos. No source screenshot of skill or
compaction activity was available for this revision; their simple inline
notices illustrate the event without claiming an exact desktop-app rendering.
Do not imply that conversations are folders inside a checkout, or that every
source stores helpers the same way. Keep classifications, findings and generic
query help out of the guide's illustrations.

Use the shared yellow attention border to point to the region each definition
describes. Highlight the conversation’s sidebar entry, the main thread and its
delegated session descriptions,
individual messages or tool calls, and the relevant folder, task, file or data
view. Instructional highlights use `.pharos-guide-highlight` from the shared
attention stylesheet and remain separate from keyboard focus.

## Context and derived information

- **Source** identifies the history location/export imported into Pharos.
  **Provider** identifies the recorded agent product/service. **Agent app** is
  the software running the agent (the source may call it a harness).
- **Channel** is how the initiating conversation started. **Machine** is where
  it started; **Execution machine** is where the particular agent ran. Helpers
  inherit the initiating channel/machine, even when their own runtime differs.
- **Model** and **effort** are recorded settings. “Top-level” labels refer to
  the initiating conversation’s settings; helpers may use different settings.
- **Dataset** is the table a query reads. Name each input row before describing
  its grouping: individual calls and daily summaries have different meanings.
  **Evidence** links open supporting messages/history. “Text source” describes
  where text within a user message came from.
- **API cost** is the estimated list-price equivalent, not subscription spend.
  **CO₂e** is estimated carbon-dioxide-equivalent emissions. Preserve missing
  prices, incomplete coverage and estimation qualifications.
- **Skills**, **instructions** and **context compactions** are agent-history
  nouns in the guide. A load is not proof of instruction compliance.
  **Message labels**, **agent events**, **findings** and **risky action notices**
  are features Pharos adds to the history; explain them on their own pages.
  A classification or notice is not proof that its interpretation is correct.

## Copy rules

Use concrete nouns in headings, columns, empty states, tooltips, accessibility
labels, card defaults and help. Prefer “Workspace” to the countable “Work,”
“Top-level conversation” to “root task/thread,” “Agent app” to “harness,” “group”
to “cohort,” “tool index” to “tool ledger,” and “daily tool summaries” to “rollup.”
Use “Canonical export” for the import format, rather than an unexplained
“Canonical.” Prefer “parent links” when explaining incomplete delegation.

“Work” remains useful ordinary English (“Find past work,” “background work,”
“work ending in a pull request”). “Task” remains useful for an assignment or a
source’s work item. Neither is a replacement entity name. Session can mean a
login or app-window lifetime in those contexts; in agent-history headings use
“agent session.” Shorthand inside a clearly labeled agent-session view is fine.

Keep original transcripts, commands, file paths, model names, user-authored
card titles, external terminology, raw event/JSON fields, stored classification rule names, stored query keys,
API/MCP names and database identifiers intact. For example, `work_id` still
identifies the workspace route, `harness` still stores the agent app, and the
Parallelism setting `counting=agent` still selects conversation counting.
Display labels must not become a migration of stored data or URLs.

## Repository audit (November 2026)

The audit discovered potential text in all 344 first-party frontend/model,
classic-script, HTML and schema files: 251 contained candidate copy, including
roughly 12,800 literals, template fragments, JSX/HTML text and accessible labels.
The inventory intentionally included code-like strings, not just rendered copy.
The review followed noun usage through each source family below and through
server-supplied labels, descriptions, prompts and operation messages (328 named
copy entries, plus the delegation metric definitions).

| Surface | Review and changes |
| --- | --- |
| Shell, header, Search and workspace reader | Workspace labels and empty states; conversation outlines versus agent-session hierarchy; evidence links; saved view settings versus agent sessions; direct Data Guide entry. |
| Analyze and chart help | Shared catalog labels, row meanings, main/sub-agent roles, top-level conversation counting, Source versus Provider/Agent app, Machine versus Execution machine. |
| Card Library, Home and saved-card defaults | Recipe titles, descriptions, actions, sample names, default headings and shared metadata; workspace/conversation identities preserved; ordinary “work” kept where appropriate. |
| Schemas and query presets | Field labels, enum labels and descriptions across all schemas; explicit TL1 work items/projects and attempts; internal keys preserved. |
| Optimize and risky actions | Repository/instruction/app-setting terminology; existing evidence and uncertainty qualifications preserved. |
| Settings, sources, onboarding and library management | Source vocabulary, tool-index/summary progress, saved view settings, capture and index status. Source-native folder names remain literal. |
| Ask Pharos and agent-facing site descriptions | Workspace route names, previews and navigation context; generated suggestions and site map rebuilt from their sources. |
| Backend UI metadata | Delegation metric names/definitions and operation/upgrade labels; request/response shapes and counting algorithms preserved. |

The checked-in artifacts are regenerated from their sources, including the
recipe catalog, site map, Ask suggestions and view-control review ledger.
Third-party Query Table controls retain their published vocabulary: generic
query terms such as row, group, filter and column are appropriate. Dependency
changes must follow the upstream-only policy in AGENTS.md.

For subsequent audits, search the source families above, including accessible
labels and server metadata, rather than editing generated bundles or only
looking at page headings. Check source-specific meanings before replacing a
word. Extend the existing state/aggregation tests when behavior changes; do not
freeze paragraphs of editorial copy in new tests.

## Guide access and navigation

Use the app’s shared NavigationItems tree for the guide menu and DashboardSelect
for its mobile selector. Menu expansion uses nav_expanded under the stable
data-guide section; selected nouns still use guide_entity. Finder locations
also select their corresponding noun through that same field.

The guide’s section labels are Conversations, Projects, Apps & machines and
Libraries. Keep headings short enough to fit on one line. Data Guide is also
an optional header tool in Settings → Layout → Header; it starts in the Toolbox
and uses the existing saved header layout, ordering and overflow behavior.

Expose the guide from Settings → Sources and the shared source setup flow
(including first-run setup and adding sources). Setup loads only the standalone
read-only guide entry point, without mounting library pages or their queries.
The provider registry and capability UI added in #449/#451 already cover the
new source kinds; do not duplicate the support matrix in the guide. Explain
that the available history depends on the source and saved format: an absent
token count or tool result may be unrecorded, rather than zero.
