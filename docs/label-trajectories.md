# Conversation shapes lab

Revision 10 centers captured parallelism in a violin-shaped envelope filled with
spaced diagonal hatching, keeping colors visible at the ribbon edges and between
stripes. Fingerprints have square intrinsic corners; surrounding cards may clip
them. Hover trees are denser and bounded to the viewport, with singular captions
for a one-message column (for example, Message 8 / 9).

Open **Analyze → Conversation shapes · lab** in an opted-in dev UI, or
`/analyze?label_lab=flow`.

| View | Purpose |
| --- | --- |
| 00 Workspace timeline | Reuses `ParallelActivity`, with separate session/detail columns, compact borderless detail rows, elapsed label attribution and recorded tool spans |
| 01 Fingerprints | Compressed run, volume and thread ribbons; the same compact component appears in matching conversation rows |
| 02 Phase flow | Additive taxonomy table, also grouped by actor kind, generation model, provider or nesting depth |
| 05 State path | Compact Sender/Receiver duet, timed agent ensemble and nesting-depth choir |
| 06 Recurring motifs | Three complete observed function states, with supporting branches |
| 09 Label detail | Expandable label tracks, brief definitions, original scores, and an optional message-kind, actor or generation-model metadata lane |
| Message Labels dashboard | Promoted from study 18; normal Analyze dashboard and native query pages |
| 13 Branching paths | Compact columns of inclusive tag predicates at successive captured states, with parent context and matching conversations |

03 and 10–12/14 remain in the earlier-studies disclosure. Exchanges (04),
Recurrence (07), Shape map (08), and the artistic studies 15–17 are retired.
Their URL values remain recognized: 04 opens State path, 07/08 open Machine map,
and 15–17 open Fingerprints with a visible retirement notice.

## Message Labels dashboard and query pages

**Analyze → Message Labels** (`/analyze?analyze_topic=labels`) is a standard
catalog dashboard. Over time and Compare groups reuse shared chart controls,
range brushing, legend selection, pinned keys and
supporting-query links. Filters include repository, generation model, text
source and coverage. Every chart uses Sender / Receiver, General Label or Specific
Label as its primary breakdown. Nested outlines in chart, timeline and reader keys
make roles and label ancestry explicit. Human input and agent thinking have separate Home/Card
Library recipes that open this dashboard with their source filters. Old
`label_lab=distribution` links migrate to the dashboard, preserving their scope.

The cards beneath the chart open **Query → Message labels** with the matching
source filters. The default query groups by conversation and supports Cards or
Table presentation. Both reuse the approved Thread Ribbon fingerprint. Table
Records drilldown exposes the underlying label observations. Presentation,
filtering, grouping, sorting and pagination belong to the native query definition,
so saves, sharing and browser history retain them. Dashboard URLs use the normal
`analyze_chart`, `analyze_filters` and range contract; query URLs use
`q_message_labels` and its registered `label_results` host parameter. The site
map exposes `analyze/labels` and `analyze/query/message_labels` to `browse_site`
and `site_link`, including native query schemas and chart choices.

The `message_labels` dataset contains one observation per most-specific accepted
label per selected retained message. Accepted ancestors are suppressed when a
child is present. Messages with no accepted label contribute one explicit
coverage observation (Not labeled or No accepted match). Missing, unfinished or
dirty ledgers count as Not labeled. Opening either page reads stored results;
it never starts labeling or initializes its optional ledger.

Daily, weekly and monthly trends use the library’s local calendar fields,
matching the other native message dashboards. Hourly views use timestamp
bucketing. The Messages metric counts **distinct messages**. In trend shares, the denominator
is all distinct matching messages in that interval; hiding a legend series does
not change it. Labels overlap, so their shares may sum above 100%. This differs
from Phase flow's additive allocations described below. The Label matches metric
counts observations instead. Generation model and labeling encoder remain
separate fields. Fingerprints are loaded only for visible rows/cards and use a
bounded sample for long conversations, explicitly marked on hover.

## Stored label query rows

Message Labels dashboards and Query Tables read a versioned, rebuildable query
projection. `message_label_facts` stores small facts once per eligible message;
`message_label_memberships` stores integer message/label pairs, and
`message_label_names` stores taxonomy names, functions and depths once per label.
Text, titles and repository metadata remain in their source tables and are joined
only when consumed. This retains individual-message joins and filters without
requiring hourly summaries or copying transcript bodies/probability arrays.

`messageLabelFactsAlgorithm` versions this projection independently of the
classifier's inference contract. The local calendar is included in its build
version. Opening a writable catalog installs transactional invalidation triggers;
the service's `maintenance.labels.query` daemon backfills and refreshes a bounded
number of conversations per transaction. Restarts resume the queue. Changes to
messages, authorship, accepted labels, coverage markers or model taxonomies queue
affected conversations. Edits to titles/repositories are read directly at source.

A changed conversation is excluded from the stored branch immediately. Until its
replacement commits, a live projection supplies that conversation's observations
in the same query snapshot. Unchanged conversations keep using stored rows. The
initial build or an algorithm/calendar upgrade retains the complete live query
path until publication. Reads—including CLI and read-only catalogs—never start
maintenance or inference. Publication/invalidation advances the shared query
cache generation. The tables are disposable; the labeling ledger remains the
authority.

Hierarchy selection keeps the standard query predicates: a subtree is the exact
parent **or** a label starting with the parent's dot-separated prefix; an exact
parent-only selection stays exact. Matching multiple children still counts a
message once with `count_distinct(message_id)`. Both parent and leaf observations
use the same small taxonomy dictionary, avoiding per-message JSON/bitmask decoding
once facts have been stored.

## Color and identity

Sender aliases taxonomy root `P` (Pitcher), Receiver root `C` (Catcher).
These are functions, independent of human/agent identity; a message can have
both. Sender colors span mint, cyan, blue and violet. Receiver colors span
amber, orange and red. Difficulty is red; Challenge and Reject move toward red.
Report uses vibrant gold, yellow and orange; Finding and other specific Report
labels retain their own hues. Deliberation uses coral, orange and magenta rather
than pastels. Empty coverage stays neutral gray. Unknown descendants inherit their
nearest known parent color. Related sublabels share hue relationships; no score
or confidence is encoded by color. Names and hover definitions are brief;
canonical taxonomy IDs remain intact in the source inspector.

## Additive accounting

The table root is **All messages**, with **Sender**, **Receiver** and
**Uncategorized** as siblings. Uncategorized expands into Not labeled and
Labeled/no accepted match. Every taxonomy parent with children also has an
**Other** child for a label accepted at that parent without an accepted deeper
label (including root-only Generic). Empty categories stay visible.

Each retained message contributes exactly one unit. A message with both roots
allocates half to each root. Within a root, its most-specific accepted labels
share that root's weight equally. Ancestors receive the sum of those allocations.
Thus every displayed parent's children sum to its value before rounding, without
counting a multi-label message twice. These are **message shares**, not ordinary
overlapping per-label frequencies or confidence scores. Percentages use the
full retained-event count in their column. Columns partition events; the phase
view uses five normalized local-branch message positions. It no longer averages
branches equally. Printed values round to one decimal place.

Choosing a cell or row filters the **actual matching events** below; matching
conversation counts are never fractional. Other uses exact parent-only matches.
Repository, provider, workflow population, thinking scope and branch controls
also constrain the population. Role breakdown means recorded user role, main
agent, subagent, tool and other role, not inferred authorship. Generation model
comes from message model metadata, with recorded session-model fallback only
for agent messages. Missing metadata has its own Not recorded column. The
labeling encoder identity is never used as the generation model.

## Conversation chooser and compact identity

Phase flow, Branching paths and Fingerprints use a paginated list instead of
an inline transcript sidebar. A row provides title, repository, provider,
first-to-last captured timestamp span, first matching branch turn/message
position, count of matching events, and a small conversation ribbon. It links
to the corresponding original reader message, or opens Label detail locally.
Turns begin at recorded user-role events; leading agent activity is turn one.
Spans describe retained evidence, not CPU duration or completed work.

`LabelConversationCard` is a reusable conversation presentation: a whole-workflow
ribbon across the top, small message/branch/match badges over it, then title,
repository, duration, matching turn and source actions. Phase Flow and Labels over
time use a scrollable right sidebar; smaller displays put it below the chart.
Other discovery views use the same cards in a responsive grid.

Consecutive identical specific-label sets and captured overlap states compress into
blocks, bounded to 40 intervals. Width compresses volume; it is not elapsed time.
No dots or numeric badges are drawn. **Hatched overlap** is the default (the
existing `steps` URL value remains its stable identity). Diagonal stripes fill a
symmetric violin-like envelope centered vertically in the ribbon. It widens
monotonically with extra overlapping branches, approaches a bounded height that
preserves the edges, and disappears at zero overlap. Gaps in the hatching expose
the original colors. **Woven strands** and **Folded edge** remain comparisons.
Exact counts remain in hover and accessible descriptions. Overlap uses each
session's first-to-last recorded message/tool envelope, not CPU activity or
nesting depth. The fingerprint itself fills its square canvas; contextual
containers such as cards own any corner rounding.

Every deepest accepted sublabel contributes its additive message share to the
column's color stripes; there is no top-N cutoff. Parent-only matches remain at
the accepted level as Other, and unlabeled/no-match events retain explicit
coverage. Every message contributes one unit split across its roots and accepted
leaves, identically to Phase Flow. Stripe heights and hover percentages use this
same allocation. Hover shows all observed paths from Sender/Receiver through
families to sublabels, with Other and Uncategorized where present. The dense hierarchy
uses a compact 300px card, stays bounded to the viewport and scrolls on small screens, supports pointer handoff from the column, and dismisses
with Escape. Conversation cards receive the same taxonomy as the primary ribbon.
Unknown timestamps remain at the end in retained order and do not imply overlap.

## Label Detail and trend navigation

Label Detail inspects a single recorded branch with a stable, source-ID-anchored
window of up to 80 events. Selecting a mark within that window leaves the message
axis fixed. Previous/Next window and message actions reveal the corresponding
source; jumping to an off-window source or another branch reveals that message.
`label_detail_window={session,start}` restores the branch and first message ID.
Sender and Receiver tracks include their descendants. Their trees expose
parent-only Other matches and Uncategorized / Not labeled / No accepted match.
Every track, including a collapsed parent, can select a source message. A faint
column aligns the selected message through all visible tracks.

The source inspector shows the accepted taxonomy as an indented tree, with
stored model scores beside their own labels. Implied ancestors without stored
scores show a dash; scores are never inferred from their children. A label's
arrow opens its trend with repository, model, authorship and date context
preserved. Returning from Agent Thinking retains thinking events in Label Detail.

The trend dashboard includes an expandable **Labels in this period** hierarchy.
It lists every observed label, including those grouped under Other labels in the
chart, and distinguishes parent-only Other from the chart's tail aggregate.
Selecting any node drills the chart and matching conversation cards; breadcrumbs
return to a broader taxonomy level. Coverage nodes are first-class drill targets.
The table uses unsmoothed distinct-event counts over the filtered population;
ancestors deduplicate simultaneous descendant matches. Labels overlap, so its
sibling counts are not an additive partition. Percentages retain the full
population denominator. This differs intentionally from Phase Flow's additive
message shares. Human input, agent output and thinking retain their own
population definitions and units through these links.

## Labels over time dashboard

`/analyze?label_lab=distribution` has its own dev-only Analyze navigation entry.
It reuses the native `StackedColumns` line chart, `RangeControl`, `PeriodControl`
and `RollingControl`. Repository/provider/scope use the common lab population;
generation-model and absolute date filters are shared with Phase Flow. Dates use
ISO instants with exclusive ends; buckets use the existing local-calendar helpers,
including DST transitions. Chart legend selections change visible series and
matching cards, never the denominator. Dragging the chart narrows dates.
Both measures support Linear and Logarithmic (`log(1 + value)`) scales, including
percentage points. Zeros remain zero. Click a period to pin the native inspection,
then a named label to drill into its specific labels and matching conversations.
Inspection links retain repository, model and authorship filters, cover the
contributing smoothing window, preserve the excluded portion of a partial
custom-start bucket via `label_dates.omit`, and reset smoothing
for the detailed interval. The Other-label union is a coverage aggregate rather
than an individual taxonomy node and has no taxonomy drill link.

Use `label_audience=human` for Human Words, `thinking` for Agent Thinking, or
`machine` for Agent Output. Human Words counts only recorded authored human input
segments (typed, feedback, resent), excluding templates and agent-supplied input;
it hides generation-model controls. Agent Output excludes retained thinking.
Agent Thinking includes only agent-role thinking/reasoning blocks, independently
of the lab's visible-prose scope. Each version names its unit and denominator.
Missing authorship is never inferred from Sender/Receiver function. Opted-in
Human Words and Thinking dashboards link to their matching prototypes.

The lines are **message prevalence**, not additive message shares: each message
counts once per matching family or specific label, even if several leaves share
a family. Percent = matching messages / all messages after population/model
filters; labels can overlap and percentages can total over 100%. Uncategorized
is explicit. Specific-label mode keeps twelve leading labels and an Other-labels
union; Other can overlap a visible label on a multi-label message. Smoothing uses
a trailing mean for counts and pooled numerator/denominator for percentages,
including preceding history. Empty count intervals are zero; percentages with
zero denominator are missing. Untimed messages are counted separately. At most
5,000 native intervals are displayed, with a visible notice if capped.

Phase and matching-turn positions retain original branch order after filtering,
rather than renormalizing the filtered messages into a new conversation. These
charts describe the restricted labeled-workflow sample, not all production traffic.

## Inclusive prefix browsing

A prefix is a sequence of tag predicates at successive branch states. A node
for A matches A+B as well as A. Ancestor tags match accepted descendants. Legacy
pipe-separated predicates retain AND semantics. Each column shows the selected
parent context and next choices, with compact hierarchy labels; common and all
tag modes retain a bounded-height scroll area. Node counts overlap, because a
message can match several tags; they are explicitly non-additive. Unknown labels
stop paths, never skip to a later known state. Captured ends are not completion
claims. Independent branches remain independent sequences.

## Workspace timeline prototype

The lab adapter constructs session rows and timestamp density from recorded
snapshot events, then renders the actual `ParallelActivity` component. It supplies
an explicit activity override from the read-only tool ledger, with the native
category mapping (read/write/run/git/network/delegate/wait/poll/other). Recorded
start and end timestamps own tool widths; missing ends remain point observations.
The current 200-workflow sample includes 27,192 tool calls. Old snapshots without
a tool ledger fall back to recorded delegation observations. The existing timeline brush,
zoom and session hierarchy folding remain available. Each session begins as one
ribbon. Common message modes form tall backdrops, rarer modes render narrower
and later, and tool actions render above them as dark mineral dots joined by
recorded duration lines. Hit testing retains covered modes and tools. Session identity and
child-session folding live in the first column; Events and its compact 20-pixel
label/activity rows live in the second. The 38-pixel session header remains
visually distinct. Marks have no strokes/borders and may fill their row height. Hover hit testing
includes all overlapping observations, including covered marks.

Expand **Events** into **Message modes** and **Tool actions**. Message modes separate visible
Sender/Receiver/Uncategorized messages from **Thinking**, then follow function
and taxonomy ancestry down to individual messages. Thinking is a message kind;
its Sender/Receiver functions appear only inside Thinking, avoiding duplicate
sibling tracks. When thinking has only one role, that role is folded into the
Thinking heading instead of adding a duplicate row. Tool actions expand by category and tool name to individual retained
observations. A grouped source mark remains one source observation with its
recorded count; the UI never fabricates its missing individual calls. Thinking labels span from the preceding observation to their timestamp; other
labels span to the next observation. Tool boundaries split these attributed
intervals. Gaps over 30 minutes remain blank. Attribution is estimated elapsed
context, not measured processing time. Unknown timestamps cannot be placed on
the clock. All ribbon carets use
ViewDisclosure with stable session and category/message identities; closed
branches render lazily.

Actual workspace pages use **Hierarchy & usage / Timeline**
(`explorer=table|timeline`). Timeline replaces Parallel activity; saved
`explorer=parallel` links open Timeline and retain their navigation and window. A single execution
without linked parents or children instead has **Conversation / Timeline**
(`reader_view=conversation|timeline`); Conversation retains the standard reader.
Workspace Sessions is always visible beneath its view tabs, including on older
links that hid the section. Timeline always shows specific labels, including
thinking, and uses the same
recursive ribbons as the experiment. Message leaves open their exact transcript
entry; on a single-agent page this switches back to Conversation.

Beside an open conversation, Timeline becomes a static overview: each session's
Events tree is collapsed, the columns fit the sidebar, and zoom, resize and event
controls are inert. Session names remain interactive: selecting one opens or
switches the conversation on the right without expanding its Events tree. Clicking, dragging or keyboard-activating the overview closes
the reader through the ordinary workspace navigation action. The full-width
Timeline then restores its saved window, column widths and disclosures. The
preview never writes its collapsed presentation into URL state. Single-agent
merged pages retain normal editing, including on narrow screens.

The Events column shows numeric counts of distinct retained message/tool events
instead of a mode swatch. When all visible Events trees are closed, it shrinks
to a small count column. Expanding any visible tree restores its saved detail
width; shrinking the column never overwrites that width in URL state.

In the full Timeline, hovering previews a segment; clicking pins that same
content. A pinned message excerpt, Caller text span or tool preview becomes a
source link that reveals its original transcript entry. Arrow keys browse a
focused ribbon and Enter or Space pins its segment, moving keyboard focus to
the link. Escape or an outside click dismisses the inspection; dragging still
brushes the time window. Pinning is transient and never changes URL selection;
following the link preserves the timeline's range, columns and disclosures,
with the reader alongside it. Single-agent source links switch to Conversation.

Timeline hover and pinned cards display primary content instead of protocol
wrappers: content strings and text-block arrays, nested message bodies,
delegation prompts, commands, and simple read targets. The native projection
extracts known envelope text **before** its 640-character display cap so a
clipped JSON wrapper cannot consume the preview. The raw source excerpt and
Caller origin runs remain separate and retain their original byte positions.
Unknown structured payloads stay visible as formatted JSON; commands and
literal human text retain their contents and line breaks. Source links always
refer to the original retained event.

The work-page timeline requests `labels=1` from the existing parallel endpoint.
It reads the requested canonical sessions' own message membership and local
label ledger, including aliases and unowned legacy root messages. It does not
load the experiment snapshot or start labeling. Each lane is bounded to 2,400
message excerpts, with an explicit truncation notice and a 30-minute context
margin for elapsed attribution across viewport edges. Dirty labels are withheld; those messages use the translucent Uncategorized
mode until labels are available. The snapshot-based workspace study remains
available in the opted-in lab for comparison. Timeline selection, time range and
recursive disclosures are portable URL state; native Timeline always uses
specific labels.

The native Timeline legend follows the modes, tool colors, errors and Caller
origin spans drawn in the selected window, including elapsed attribution across
its edges. Mode families group their present sub-labels; only directly drawn
states get swatches. Typed text has an outlined **Human typed** key. Run tools
use bright green, delegation blue, read/search coral like Deliberation, and
errors bright red. Frame uses blue shades, while uncategorized modes have a
translucent fill.

The native Timeline has a top-level **Caller** lane. Root user inputs, retained
human-authored child inputs and H-segment inputs move out of the execution's
message track so they appear once. External automated callers are identified as
Automated input; recorded automated subagent assignments stay
with their agent. Missing authorship stays Unknown origin, never Typed by role
alone. Caller messages retain their original session, transcript and message IDs
for navigation. The existing authorship ledger supplies compact category/byte
runs in original text order, including repeated categories and unknown gaps; no inference runs on page load. Typed text has a bright white outline in dark mode and a black outline in light mode.
Diagonal hatching marks likely pasted, copied/quoted or re-sent text; templates
use a square grid, harness context a diamond grid, attachments rings, automation
dashes and unknown origin dots. Pattern cells retain their CSS-pixel size across
row heights, clock zoom and column resizing. Mixed messages preserve all
categories. Horizontal proportions describe byte position within each message, not typing time.
Expand **Text origin** to inspect a message and its constituent spans. Origin
textures overlay the same message-mode colors used in the Caller ribbon; origins
never introduce a second color scheme. Origin rows use concise names without
percentages. Hovering an origin span shows its own bounded excerpt (up to 240
characters), read from the original byte range, including spans beyond the
message preview. Repeated origins retain distinct excerpts. Tool starts are screen-sized circles with
thin duration lines and no end marker, including when rows are compact or zoomed.
Within each overlapping group, tools occupy separate tracks across the ribbon's
height; ended tracks are reused, and isolated calls stay centered. Marker size
adapts to track spacing while remaining circular. Unknown ends remain points.
The native Timeline retains short calls individually instead of aggregating
them, so overlap is not erased at wide zooms. Hovering a tool track selects that exact call and shows
its recorded command, arguments or file path (up to 640 characters). Preview
bodies are read only for bounded visible calls and never enter the hierarchy
snapshot. Single-message/tool cards use inline labels without one-item counts,
and omit the generic title and instructional footer.

The window selector retains Parallel activity's frame and grab handles. Its header
shows Whole conversation/workspace or Showing the selected duration of the full
duration, with Reset at the upper right. The selected cutoffs appear inside the
drag control and update during selection, panning and edge resizing; a clamped
caption keeps narrow selections readable at either edge. It uses the selection
border color and is hidden when the whole range is selected. Separate window and
zoom/offset breadcrumb rows are removed. Caller-origin key textures use denser
cells so their small swatches remain distinguishable. The
legend separates colored message fields from tool dots/lines. Its main message-mode
key groups the observed specific-label colors beside their parent modes; there is
no separate specific-label disclosure. Empty tracks retain their centerline with
no empty-window placeholder. Axis dates appear at the first tick and at local-day
changes, with the time ticks aligned underneath. Tick density adapts to the
clock column width so zoomed detail panels keep their labels readable. The explanatory Timeline
paragraph is removed. Run/Bash uses the terminal
icon. Session and Events resize by dragging their header grips or
using the arrow keys; Home/double-click resets a column. The clock always fills
the remaining space through the right edge, with no outer resize grip. Widths
restore through `timeline_columns={execution,events,time:0}`; older fixed clock
widths are ignored. Compact ownership branches and child-session controls share
the left gutter, including through expanded Events rows. Session titles use the
first line; outline numbers such as 1.2 appear beside the model on the second.
Default agent rows use the ownership branches alone; only child-fold controls
and the Caller retain gutter icons. Caller meets the agent rows at a stronger
two-pixel divider, and the first timestamp is inset from the Events resize grip.
Overview and ribbon drags use their final horizontal pointer position even
above or below the canvas. Resize handles clamp independently at the full
window's ends, panning preserves duration, and cancellation releases capture
without committing a new window.
The chart grows vertically with its rows; only horizontal
overflow belongs to the chart, while the enclosing page/pane owns scrolling.

## Read-only data and portable state

The exporter opens SQLite `mode=ro`, sets `query_only`, and reads one transaction,
including committed WAL contents. It never initializes, migrates, infers, labels
or writes a catalog. The export retains the same 200 root identities, native
session order, parent/call/result identities and linked child transcripts. Empty
handles resolve to their retained child transcript once; cycles and unresolved
lineage stay explicit. Dirty child labels are withheld. Generation-model metadata
is separate from the labeling encoder. Output is atomic and 0600; private
excerpts and screenshots belong in gitignored `.context/`.

```sh
python3 tools/label-trajectories.py \
  --catalog /Volumes/euclid/pharos-dev-labels/catalog/catalog.sqlite3 \
  --roots-from .context/label-trajectories-v2.json \
  --output .context/label-trajectories-v2.json --limit 200

go run ./cmd/pharos --config /Volumes/euclid/pharos-dev-labels/library.toml dev-ui \
  --assets internal/archive/assets --port 8812 \
  --label-trajectories-data .context/label-trajectories-v2.json \
  --open-path '/analyze?label_lab=flow'
```

The authenticated, same-origin dev UI serves this explicit snapshot with
`no-store`; regular Pharos has no snapshot endpoint or lab navigation entry.
View/filter inputs use the shared URL contract, including generation model, chart/date/legend state, table breakdown,
function/column selection, result page, fingerprint treatment, metadata lane,
prefix predicates and timeline labels. Carets use `ViewDisclosure` with stable
taxonomy/record IDs. The prototype's window is bound to its workflow ID; another
workflow restores full extent. Native work pages always show specific labels; prototype label,
expansion and event state stay in the `label_timeline_*` namespace so native
work-page bindings cannot write foreign analysis URLs. Hover, brush drafts and scroll remain transient.

Unit tests cover conservation through every taxonomy level, Other/Uncategorized,
model provenance, raw matching events, inclusive tag predicates and boundaries,
ribbon volume/depth, turns, clocks and source identities. Browser tests cover
rendering at five widths and restoration of filters, carets, selected messages,
matching links and the reused timeline's label tracks. Python tests preserve
read-only extraction and generation-model/encoder separation.
