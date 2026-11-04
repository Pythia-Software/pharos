# Message labels in Pharos

Stored message labels appear in three production surfaces: the Message Labels
dashboard, its native Query Table, and conversation timelines. Reading these
views does not start inference. Configure labeling in
[Background Processing](local-labeler.md#turning-it-on).

## Dashboard and query pages

**Analyze → Message Labels** (`/analyze?analyze_topic=labels`) offers
**Over time - line**, **Over time - bar**, and **Compare groups**. Charts always
break down by Sender / Receiver, General Label or Specific Label. Repository,
generation model, text source and coverage remain filters. The shared chart
components provide brushing, smoothing, log scales, legend selection, pinned
inspection and supporting-query links. The selected line/bar form survives
cumulative display, saved cards and URL restoration.

Legend outlines show role, general-label and specific-label ancestry. Category
names open the same menu as leaves, applying only/add/remove/exclude actions to
all descendants. Category text reflects full, partial or hidden selection. All
matching labels are shown; older series caps do not fold them into Other.

The Messages metric counts distinct messages. Its trend percentages divide by
all distinct matching messages in each interval. A message may carry several
labels, so percentages can sum above 100%; hiding a series leaves its peers'
denominator unchanged. Non-additive counts and overlapping shares use independent
lines or side-by-side bars. Label matches counts label observations instead.
Generation model and labeling encoder are separate fields.

Supporting cards open **Query → Message labels**, grouped into conversations.
Cards and Table share filtering, grouping, sorting and pagination. Both display
the same Thread Ribbon fingerprint; Table can drill into the underlying label
observations. Only visible rows request ribbon evidence. Large conversations use
a bounded retained-message sample, identified on hover.

Dashboard URLs use `analyze_chart`, `analyze_filters` and the shared time-range
contract. Query URLs use `q_message_labels`, including its registered
`label_results` host presentation. `browse_site` and `site_link` expose
`analyze/labels` and `analyze/query/message_labels`. The Home/Card Library recipes
cover all messages, human input and agent thinking. Retired `label_lab` URLs
redirect to the dashboard, preserving compatible population and chart filters.

## Fingerprint accounting and appearance

Sender functions use cool cyan, blue, green and violet hues; Receiver functions
use warm yellow, orange, coral and magenta hues. Difficulty is red; Challenge and
Reject sit near red. Deepest accepted labels supply the individual stripes.
Uncategorized evidence stays neutral and translucent. Color does not encode
model scores.

Each message contributes one unit to a fingerprint column. A message with both
roots allocates half to each; deepest labels within a root divide its weight
equally. Ancestors receive their children's sums. Parent-only matches appear as
Other, including Generic root matches. Uncategorized distinguishes Not labeled
from Labeled / no match. The hover hierarchy and stripe heights use the same
weights, so every parent's children add to its value. These additive fingerprint
shares differ from the dashboard's overlapping distinct-message frequencies.

Centered diagonal hatching widens smoothly with extra overlapping recorded
branches, remains distinguishable above three branches, and disappears with no
overlap. Overlap uses recorded session envelopes, not CPU activity. Width
compresses message volume rather than duration. Fingerprints have square
intrinsic corners; containing cards may clip them. Hover is viewport-bounded,
scrollable, keyboard-accessible and retains the complete observed hierarchy.

## Conversation timelines

Workspace and single-agent timelines use the normal conversation routes and
shared clock. A session's compact ribbon expands recursively through message
modes and tool actions into individual observations. Native parent identities
define nesting. Caller rows distinguish typed, pasted, template, harness and
attachment content through patterns, with message-mode colors beneath them.
Typed text receives a theme-aware outline. Composition spans follow text order
within a message; tool spans use recorded times and separate overlapping calls.

Label widths attribute elapsed time between observations rather than claiming
measured processing duration. Thinking reaches back to the preceding observation;
long idle gaps stay blank. Hovering reveals retained content, including unwrapped
tool arguments. Clicking pins the popover and provides a source-message link.
Selecting a session opens its reader alongside a collapsed, static timeline;
interacting with Events or the clock returns to the full-width editable view.
See [Delegation explorer](delegation-explorer.md) for the shared navigation.

## Stored query projection

`message_label_facts` stores small facts once per eligible message;
`message_label_memberships` stores integer message/label pairs; and
`message_label_names` stores taxonomy names and ancestry. Transcript bodies,
titles and repository metadata remain in their source tables and join only when
consumed. Accepted ancestors are suppressed when a more-specific label is present.
Messages without an accepted label retain an explicit coverage observation.

`messageLabelFactsAlgorithm` versions the projection independently of inference;
the library's local calendar is included in its build version. Transactional
invalidation triggers queue changed conversations. The `maintenance.labels.query`
daemon backfills and refreshes a bounded number per transaction and resumes after
restart. Edits to messages, authorship, accepted labels, coverage or taxonomy
invalidate affected conversations. Titles and repositories are read at source.

A queued conversation immediately leaves the stored branch. Its live projection
supplies observations in the same query snapshot until replacement commits.
Initial builds and algorithm/calendar upgrades retain the complete live path
until publication. Maintenance advances the shared query-cache generation.
Read-only catalogs and CLI reads never start maintenance or inference. The query
tables are disposable; the classifier ledger remains authoritative.

Hierarchy filtering uses exact-parent or dot-separated descendant-prefix
predicates. Multi-label matches still count a message once with
`count_distinct(message_id)`. Stored integer memberships avoid repeated
per-message probability-array or bitmask decoding for current conversations.
