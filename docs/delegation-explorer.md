# Delegation explorer

The workspace route `/work/{workspace}` opens an overview without fetching a
transcript. Selecting a session opens `/work/{workspace}/conversation/{conversation}`
and animates the overview into an inbox sidebar alongside the existing reader.
The panes scroll independently. Their divider supports dragging, keyboard
resizing, and double-click reset; its width is remembered. Below 720 pixels of
sidebar width, the usage table becomes a compact hierarchy with one comparison
metric selectable from every Self and Self + children column. Hierarchy boxes,
selection highlighting, sorting, branch collapse, and pagination remain available.
Widening the sidebar restores the full table. Parallel activity stays available
in the sidebar with its full-workspace overview, range selection, explicit detail
expansion, and tool-call popovers; event grouping adapts to its narrower axis.

The reader toolbar shows the selected session number, assignment, provider,
model, and effort above a borderless transcript. It returns to the overview
and toggles visibility of workspace sessions. Browser Back/Forward retains analysis state and the per-conversation reading position
and expanded top-level turns for the mounted workspace. A newly opened
single-turn session expands its turn by default, including long sessions;
returning to a session preserves its saved open/closed state. Direct conversation and
tool-message links open the reader, including legacy conversation query links.
On screens below 960 pixels, the conversation fills the screen and Workspace
Sessions opens a modal navigator sheet with a backdrop, focus trap, and Escape
to dismiss. Selecting a session dismisses the sheet. Reduced-motion settings
disable layout animations. Standalone Share exports keep their original reader.

Assignment labels use the launch's `task_name`, then `description`, `prompt`,
Antigravity's `Prompt`, `task`, or plaintext `message`. Only nonempty text fields
are eligible; opaque encrypted messages and unrecognized JSON payloads are omitted. Extraction happens
before the 180-character display bound, so a task name following a long message
is still available. Embedded sessions without an assignment use their execution
ID instead of borrowing the parent conversation's nickname or request. Labels
resolve from retained launch messages without reindexing.

A **Workspace Sessions** toolbar selects the transcript, replacing the
conversation dropdown. A single button beside the sliding
Hierarchy & usage / Parallel activity switch shows or hides the session content.
The toolbar remains visible while collapsed, and choosing a view opens it.
The views sit directly on the workspace without an enclosing disclosure panel. The selected row and conversation header
share a background highlight, session outline number, and assignment label.
Workspace totals sit alongside the workspace title and remain visible when the
explorer is collapsed. The native workspace header omits its old Usage disclosure.
The conversation header shows self usage and a child-session picker. Parent
sessions and parallel activity are reached through the workspace navigator.
Cross-workspace parent references retain their direct navigation link. Delegation entries gain
links to child transcripts when their turns are materialized. Child transcripts
open in the existing reader, with its search, context rail, and lazy tool output.

The explorer has a hierarchy/usage table and a shared-axis activity view. Nested
boxes and level badges frame each visible subtree. Parallel heavy borders retain
every enclosing level on both sides, including during horizontal scrolling.
A lighter divider beneath each parent separates it from its children. The table
uses the full overview width and grows with its rows, scrolling naturally with the overview. The inbox sidebar width is adjusted with
its divider.
The two orders are hierarchy and metric ranking. Clicking a metric header cycles
through descending, ascending, and hierarchy; the Execution session header
returns to hierarchy. Headers are the sole sorting controls. The explorer has no
session search or matching-only toggle; legacy URL filters are cleared on load.
First evidence ranking provides chronological order. Every metric supports Self and Self + children,
with separate column families. A column picker exposes all 28 metrics, with drag handles (or arrow keys) to
reorder them in both families. Order and visibility are saved independently.
Large absolute values use K/M/B with three significant digits; metric tooltips
retain exact values. A global tint control replaces per-cell percentages. Tint uses a
softened logarithmic curve and each column's fixed, whole-workspace maximum.
Pagination and collapsed branches do not change those maxima or spend totals.
Files Edited counts distinct paths from successful edit/write ledger calls;
inclusive values deduplicate shared paths across agents. Missing paths/results
and first-path-only patch evidence show partial or unknown coverage. Shell edits
without recorded file paths are outside this metric. Pages show up to 40 sessions;
range text and navigation appear only when pagination is needed.
The read API still supports filtering and matching-only accounting for analysis.

The timeline retains a full-workspace density overview while detail is zoomed.
Drag across the timeline lanes or axis to zoom into a range, with a shared
selection preview in every lane and the overview. Tool events remain clickable.
Drag the overview window to pan without changing its duration, drag its edges
to resize, or drag outside the window to select a range. Arrow keys and Home/End operate the selection and handles. Double-click
or choose Whole workspace to reset. Zoom/pan buttons and UTC input fields
are omitted in favor of direct manipulation. Breadcrumbs, local-time bounds (in the browser's timezone), visible
span, archive offset, and zoom factor describe the viewport. Short neighboring
calls are grouped at the current resolution; errors keep independent references.
Clicking a session label or lane background opens its conversation in the reader,
matching the hierarchy table. Each lane has an independent Activity button to
expand or collapse its tool categories; labels occupy a separate band above the
spans. The displayed conversation is highlighted independently of expansion.
Tool snippets use solid colors without borders. Clicking a snippet opens an
anchored popover with a prominent Jump to tool call action, elapsed-time evidence,
and local timestamps. Grouped snippets link to their first retained call. Escape
returns focus to the snippet; outside clicks dismiss it. The popover follows its
span while scrolling and closes when that span leaves the viewport.
The model-side background remains solid dark violet.

## Objects and ownership

The existing persistent objects remain the source of truth:

- `workspaces`: the catalog's unit of work/source grouping.
- `conversations`: persistent provider/account/native transcript identities.
  `parent_id` can link conversations; it does not require matching providers.
- `agent_sessions`: execution/accounting streams within conversations. A
  conversation can contain a main stream and several embedded child streams.
  `parent_id` links session streams; `delegation_message_id` links their launch.
- `child_conversation_id`: links a delegation handle to its separate child
  transcript. `in-child-conversation` identifies usage accounted in that child.
- `agent_session_messages`: session membership of retained transcript events.
  User turns are derived from user message starts rather than stored turn rows.
- `model_requests`, `tool_calls`, `agent_session_usage`: request evidence,
  joined call/result evidence, and reconciled hourly accounting respectively.
- `conversation_launches`: inferred command-based launch relationships, including
  a headless agent of a different provider or in another workspace.

`delegationSnapshot` is a cached read model, not a new ingestion identity. Its
nodes retain session/conversation/workspace IDs, source provider, model and effort,
ownership evidence, launch-message reference, Self and inclusive metrics,
outline, coverage, and self/subtree evidence envelopes. An alias map reconciles
zero-usage delegation handles with their child's main stream. Exact retained
async agent handles can recover a nested relationship when the conversation
catalog flattened it. Ambiguous handles are left unmerged. Cyclic ownership is
excluded with a visible warning.

Incoming parents and linked child transcripts in another workspace retain
navigation references. Workspace totals stay scoped to that workspace; unqueried
outside-child usage remains unknown. Mirrored workspaces are not merged by this
workspace-local view. Extra causal edges do not create a second ownership total.
Effort uses the execution's recorded value. A main stream can fall back to its
conversation's effort; embedded children never inherit their parent's value.
Provider, model, and effort share one dot-separated metadata line, with missing
effort shown as unknown.

Episodes currently cluster tool/request timestamps and message-envelope
endpoints separated by more than 30 minutes. Intermediate message timestamps
are not sampled, so these gaps do not establish inactivity. These are neither
persistent run objects nor proof of completion. Source lifecycle boundaries would be needed for measured run,
model-processing, or background-process time.

## Read API and evidence

`GET /api/work/{workspace}/delegation` accepts `mode` (`hierarchy` or `rank`), `sort`, `scope` (`self`/`all`), `direction`, `search`, `provider`,
`collapsed`, `only_matching`, `conversation`, `limit`, and an opaque `cursor`.
It returns a bounded page, separate ancestor context, conversation navigation
context, metric definitions, workspace totals/maxima, and full-range density.
Cursor continuations are bound to both the content snapshot and query; changes
return HTTP 409 rather than silently reordering a later page.

`GET /api/work/{workspace}/parallel` accepts that `snapshot`, a bounded list of
`sessions`, absolute millisecond `start`/`end`, and viewport `width`. It returns
windowed/grouped tool marks with explicit end-evidence status, independent error
references, inferred background intervals, and evidence clusters. Neither endpoint sends tool outputs or full
transcripts. Compact ledgers are streamed into a cached snapshot inside one
read transaction. A separate retention policy keeps at most four completed
workspace snapshots, evicts the least recently requested snapshot, and expires
snapshots after five idle minutes during requests or cache maintenance. Catalog
changes drop outdated snapshots; delegation snapshots rebuild only on demand
and are excluded from background cache warming. Concurrent requests share
in-flight work, which may temporarily exceed the completed-snapshot limit. Analysis currently
limits a workspace to 10,000 sessions/conversations and 500,000 tool/request
observations; pages, ancestor context, and lane requests are capped at 100;
omitted context is reported. A lane reports mark
truncation above 2,400 marks and requests a narrower window. Conversation
navigation context reports truncation above 100 references.

Metric values carry quality and known/total retained coverage. `—` means unknown;
`+` indicates partial coverage, and API cost is an estimate using the existing
historical pricing book. Clock span, summed session spans, global tool union,
summed per-session tool unions, and summed recorded durations remain distinct.
Cache and reasoning components are subsets, not additional total tokens.
Unallocated session usage stays at the session level.

Request timestamps are observations, not model-processing intervals or token
rates. The inferred background bridges nearby evidence at most 90 seconds apart;
it contributes no measured model time. A wait tool is distinguished from
polling, but its name alone does not establish a blocking boundary. Model,
blocking-wait, and process-lifetime metrics stay unknown without such evidence.

Analysis order, selected execution, collapsed branches, and viewport
bounds are retained in the page URL. Transcript navigation retains conversation
and launch/call-message IDs. Standalone Share exports keep their existing reader;
the service-backed explorer is currently omitted from those exports.
