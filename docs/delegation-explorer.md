# Delegation explorer

The workspace route `/work/{workspace}` opens an overview without fetching a
transcript. Selecting a session opens `/work/{workspace}/conversation/{conversation}`
and animates the overview into an inbox sidebar alongside the existing reader.
The panes scroll independently. Their divider supports dragging, keyboard
resizing, and double-click reset; its width is remembered. Below 720 pixels of
sidebar width, the usage table becomes a compact hierarchy with fixed Self API
cost and Time to run columns, plus a Timeline button for each session. Time to run is the session's earliest-to-latest
evidence span; its tooltip explains the evidence limits. Branch collapse and
pagination remain available. Comparison controls and metric definitions are
omitted. Widening the sidebar restores the full table and its saved ranking. Timeline replaces Parallel activity
throughout the workspace explorer. Beside a reader it shows a collapsed, static
overview with Session and clock columns; session names switch the reader, while
interacting with the clock returns to the editable full-width view. Events stay
available in the full-width view. Saved `explorer=parallel` links
open Timeline and retain their time range, session and hierarchy state.

Full-width usage rows show the age of their first retained evidence on the same
line as provider, model, and effort. Compact rows keep the session's outline
number, provider and model beneath its title. Turn headers show the age of the user message (or first event
for session setup). Ages refresh each minute and expose
the exact local timestamp on hover. Missing timestamps omit the age.

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

A workspace with one conversation that has no parent conversation skips that
split: `/work/{workspace}` opens its conversation directly, with the workspace
heading, totals, controls, and Archive details leading the transcript in a single
scrolling pane. There is no sidebar, sessions table, or overview to return to.
Sessions load afterwards; if they form a hierarchy after all (more than one
session, or a parent or child in another workspace), the page reverts to the
sidebar beside the already-open conversation.

Assignment labels use the launch's `task_name`, then `description`, `prompt`,
Antigravity's `Prompt`, `task`, or plaintext `message`. Only nonempty text fields
are eligible; opaque encrypted messages and unrecognized JSON payloads are omitted. Extraction happens
before the 180-character display bound, so a task name following a long message
is still available. Embedded sessions without an assignment use their execution
ID instead of borrowing the parent conversation's nickname or request. Labels
resolve from retained launch messages without reindexing. Every view
(hierarchy, inbox, Timeline lane, reader heading, parent context) shows the
resolver's one label (`execution.Label`). Local context goes in `subtitle` on
the metadata line, never in place of the label: the work title of a
user-started session, a context row's conversation title, or otherwise the
execution's own opening request.
Separately archived children keep that same assignment when opened. Spawn calls
copied from the parent into a fork's history do not create additional hierarchy
rows unless the copied session has independently reported usage.

A **Workspace Sessions** toolbar selects the transcript, replacing the
conversation dropdown. A single button beside the sliding
Hierarchy & usage / Timeline switch shows or hides the session content.
The toolbar remains visible while collapsed, and choosing a view opens it.
The views sit directly on the workspace without an enclosing disclosure panel. The selected row and conversation header
share a session outline number and assignment label. Both session views outline
the current row. Titles of other sessions use link styling and underline on hover;
the current session's title has no navigation action.
Workspace totals sit alongside the workspace title and remain visible when the
explorer is collapsed. The native workspace header omits its old Usage disclosure.
The conversation header shows self usage and a child-session picker. Parent
sessions and Timeline are reached through the workspace navigator.
Cross-workspace parent references retain their direct navigation link. Delegation entries gain
links to child transcripts when their turns are materialized. Child transcripts
open in the existing reader, with its search, context rail, and lazy tool output.

The explorer has a hierarchy/usage table and a shared-axis activity view. Both
use the same session identities, ownership tree branches and outline numbering. The table
uses the full overview width and grows with its rows, scrolling naturally with the overview. The inbox sidebar width is adjusted with
its divider.
The two orders are hierarchy and metric ranking. Clicking a metric header cycles
through descending, ascending, and hierarchy; the Execution session header
returns to hierarchy. Headers are the sole sorting controls. The explorer has no
session search or matching-only toggle; legacy URL filters are cleared on load.
First evidence ranking provides chronological order. A scope selector shows either Self or Self + children
for all visible metrics and their rankings; the default is Self + children. A column picker exposes the metrics, with drag handles (or arrow keys) to
reorder them. Order and visibility are saved independently. Messages and Agent events appear beside Tool calls;
Files read counts distinct successful read paths and excludes directory listings and searches.
Each row's Timeline button opens that session's own evidence span with its immediate event groups visible and deeper groups closed.
Large absolute values use K/M/B with three significant digits; metric tooltips
retain exact values. A global tint control replaces per-cell percentages. Tint uses a
softened logarithmic curve and each column's fixed, whole-workspace maximum.
Pagination and collapsed branches do not change those maxima or spend totals.
Files Edited counts distinct paths from successful edit/write ledger calls;
inclusive values deduplicate shared paths across agents. Missing paths/results
and first-path-only patch evidence show partial or unknown coverage. Shell edits
without recorded file paths are outside this metric. Pages show up to 40 sessions;
range text and navigation appear only when pagination is needed.
Self tokens show usage attributed to that agent session. A harness result without
request envelopes shows a reported minimum (`≥`), with its exact count in the
tooltip. Hierarchy & usage cells append an asterisk (`≥116.8K*`), with a
visible footnote explaining the minimum. Compact rows also show the marked minimum
beside the session metadata. The reader header explains the coverage and links to the retained result. Missing
input/cache/output categories leave API cost unknown. For sessions with delegates, aggregate remainders are
shown as unallocated within the branch, excluded from Self tokens and Self API
cost, and included once in Self + children. Leaf sessions retain their aggregate
usage and API cost as Self. Inclusive token totals reconcile
observations against ancestor remainders only within their own branch; the
priced ledger remains unchanged. The API exposes `token_attribution` with direct,
accounted, unallocated and observed-minimum tokens and source-message references.

The read API still supports filtering and matching-only accounting for analysis.

The timeline retains a full-workspace density overview while detail is zoomed.
Zoom keeps the current page of session rows visible, with empty lanes where no
evidence falls in the window. Hierarchy & usage also keeps those rows; cells
for sessions (or branches in Self + children scope) known to be outside the
window are blank. Other usage values still cover whole sessions, not just the
selected window, and unknown bounds do not remove usage.
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
catalog flattened it. Codex acknowledgements can identify a child by native
agent/thread ID or by a task path scoped to its recorded parent, provider, and
account. Ambiguous handles are left unmerged. Failed spawn acknowledgements
without independent execution evidence remain in the transcript and tool ledger,
but do not create execution rows. Unresolved launches do not acquire an Open
child transcript button. Cyclic ownership is excluded with a visible warning.

Incoming parents and linked child transcripts in another workspace retain
navigation references. Opening a subagent's own workspace includes its incoming
ancestor chain in the hierarchy, so its session outline and reader heading remain
nested. Incoming ownership uses exact retained delegation handles before
transcript parents, and follows inferred command launches when no parent was
recorded, at every level of the chain. Ancestor rows are marked **Parent context**,
open their own transcripts,
and show unknown self usage. They do not contribute to workspace totals, coverage,
or activity bounds; their inclusive values cover only the retained subtree in
this view. Siblings in other workspaces are not loaded by this incoming context.
If optional ancestor-workspace reconciliation exceeds its analysis limit, the
view retains recorded ancestry instead of rejecting the child workspace.
The hierarchy follows recorded subagent parent edges and inferred command-launch
links recursively across workspace boundaries, including mixed-provider families.
Codex stores each rollout as a separate workspace, which must not sever the execution family.
Recorded transcript parents take precedence over inferred command launches.
Launch placeholders link
to those references; children without an acknowledgement still appear beneath
their recorded parent. External references retain the child's conversation and
workspace identity, show an outgoing arrow, and open its actual transcript.
Workspace totals stay scoped to that workspace; unqueried outside-child usage
and event metrics remain unknown, making inclusive totals partial. Mirrored
workspaces are not merged by this workspace-local view. Extra causal edges do not create a second ownership total.
Effort uses the execution's recorded value. A main stream can fall back to its
conversation's effort; embedded children never inherit their parent's value.
Provider, model, and effort share one dot-separated metadata line, with missing
effort shown as unknown.

**Related work.** Certain related work (`work_peer_links`, see
[peer conversations](peer-conversations-design.md#what-was-built)), such as a
review that read this workspace's diff, joins the hierarchy and timeline as
further top-level trees: `workGroupSnapshot` copies each related workspace's
cached snapshot into one and renumbers the outline. Its nodes carry `peer`
(workspace, title and role), and their sub-agents stay nested under them.
Workspace `totals` stay this workspace's own; `group_totals` counts every
session shown. The merged snapshot is cached under its own `delegation:` key
and obeys the same limits. Uncertain related work stays out of the hierarchy.

Episodes currently cluster tool/request timestamps and message-envelope
endpoints separated by more than 30 minutes. Intermediate message timestamps
are not sampled, so these gaps do not establish inactivity. These are neither
persistent run objects nor proof of completion. Source lifecycle boundaries would be needed for measured run,
model-processing, or background-process time.

## Read API and evidence

`GET /api/work/{workspace}/delegation` accepts `mode` (`hierarchy` or `rank`), `sort`, `scope` (`self`/`all`), `direction`, `search`, `provider`,
`collapsed`, `relatives`, `only_matching`, `conversation`, `limit`, and an opaque `cursor`.
It returns a bounded page, separate ancestor context, conversation navigation
context, metric definitions, workspace totals/maxima (and `group_totals` with
related work), and full-range density.
Incoming ancestor rows carry `context_only` and a `navigation` reference.
`session_count` excludes them; `total` counts the displayed hierarchy rows.
Cursor continuations are bound to both the content snapshot and query; changes
return HTTP 409 rather than silently reordering a later page.

Rows come from the shared execution resolution
([execution-resolver-design.md](execution-resolver-design.md)). Each row has
`parent_certainty` (`native`, `derived` or `legacy`); a legacy or inferred
parent is shown as not verified, never as confirmed ancestry. It also has
`segments` (the transcripts of one execution, such as a Codex continuation in
another workspace, each with its workspace) and, for a launching row,
`attempts`. `coverage` counts unresolved and unverified parents, identity
conflicts, sessions whose other-workspace activity is not counted
(`unqueried`; their metrics are partial), and missing evidence capabilities.
`resolve=` returns `aliases`, which maps a requested ID to its canonical row
(itself for a current row), or to null when no row answers it. Session IDs can
contain colons, so a saved disclosure key moves with the longest prefix that
resolves, and a current session whose ID extends a moved one keeps its key. The reader then says the selection is
unresolved rather than showing another session. Timeline lanes leave out copied
history and report `inherited_messages` (with `inherited_owners`, the rows that
own the originals), `repeated_messages` (copies of the execution's own earlier
segment), and `occurrence_partial` with `occurrence_reasons` when copies could
not be told apart. A whole conversation is partial when it was indexed before
its provenance was typed (`untyped-ancestry/v1`), when its retained history
declaration was not read again (`retained-history-undeclared/v1`), when its
provider read does not declare whether it copied history (`history-undeclared/v1`),
or when the conversation it declares it copied is not retained
(`declared-copy-source-missing/v1`). The row then carries the same reason as
`occurrence_partial`, and its own message, event, count and clock metrics are
partial, matching quick stats. A lane reads each segment of the execution in
this work, with the agent events of each, and lists segments retained in other
work as `omitted_segments`. Copies are excluded before the 2,400-message cap:
the lane scans in bounded batches and sets `messages_truncated` whenever its
own messages may continue past what it read.

The execution family around this work's sessions is loaded nearest first, up to
`delegationFamilyLimit` related rows: ancestor chains always, then parents,
children and detached members breadth first. `family_complete` is false when
relatives were left out, and each row reports `children_omitted`. Its
**+N more** control adds the row to the `delegation_relatives` view field,
sent as `relatives`, which loads that row's children and detached members
within the 10,000-row analysis limit. Opening a related session reads its own
nearest relatives, so every member stays reachable. Lane requests must send the
same `relatives` so they name the same snapshot.

`ancestry.depth` counts resolved parent links. `ancestry.certainty`,
`depth_quality`, `root_quality` and `conflicted` are the resolver's ancestry
quality (`executionAncestry`), never an explorer policy. The depth metric is
`observed` only when every link is native or derived, `inferred` through an
inferred command launch, and `unverified` through a legacy link (`?` in the
tables). Indentation still follows the resolved tree. `session_count_uncertain`
counts sessions in an unresolved identity conflict, their own or an
ancestor's (the scope usage marks `run_identity` uncertain), and the header,
inbox and pagination show the count as approximate (≈).

Agent events that the Agent Experience datasets withhold (built on superseded
execution evidence, `agentEventsWithheld`), or that await a rebuild, are not
counted. Those rows' events metric is partial and carries `events_pending`, and
Timeline lanes leave the events out and report `events_pending`. The delegation
response also returns `events_pending_sessions` across the full snapshot,
including rows omitted by collapse or pagination. The Timeline and its compact
reader preview show a summary even when affected sessions have no drawn lane.
Conversation
and workspace ribbons (`/api/labels/...`) leave the same events out and report
`events_pending` and `events_pending_conversations`, apart from label
`pending`.

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
in-flight work, which may temporarily exceed the completed-snapshot limit.
A snapshot is also keyed by the execution source generation, so typed
evidence written without other catalog changes (a backfill, late claims)
produces a new snapshot. With `labels=1`, lane messages, their occurrence
ownership and the resolution are read in one transaction; if that resolution
is not the snapshot's, the request returns HTTP 409 `snapshot_changed` rather
than pairing an older graph with newer rows, and the client refreshes. Analysis currently
limits a workspace to 10,000 sessions/conversations (including its linked
conversation family) and 500,000 tool/request
observations; pages, ancestor context, and lane requests are capped at 100;
omitted context is reported. A lane reports mark
truncation above 2,400 marks and requests a narrower window. Conversation
navigation context reports truncation above 100 references.

Metric values carry quality and known/total retained coverage. `—` means unknown;
`+` indicates partial coverage, `?` an unverified or inferred value, and API cost is an estimate using the existing
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
