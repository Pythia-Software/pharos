# Scatter Plots

Analyze → Dashboards → Outcomes → Scatter Plots pairs metrics from different
dashboards. Conversations are the default points; workspaces, agent sessions and messages are
also available. Search either axis by metric or dashboard name. The menus group
results by their source dashboard and show only metrics recorded for the chosen
point type. Applied choices, colors, axis scales, and range live in
`analyze_chart` and `analyze_filters`.

The individual dashboard view menus no longer offer Two measures. Recommended
scatter cards use the standalone dashboard and its entity metrics for both their
previews and destinations. Typed words versus API cost is a recommended card.
Card creation derives its title and description from the selected axes, point
type, filters and range through the shared dashboard card metadata functions.

## Metrics audit

`web/src/scatter-metrics.ts` reuses the guided dashboard catalogs, adding source
measurements that the old scatter menus did not expose. Identical quantities
have one home; genuinely different counts retain their qualifiers.

| Dashboard | Available measurements and additions |
| --- | --- |
| Conversations | Human messages, activity conversation reach and recorded output; usage-based reach retains its distinct population in Tokens and Cost |
| Human Words | Typed and all user words, typed-message counts, word shares and per-message means; pasted, copied, template, attachment and automation words |
| Tokens and Cost | Costs and pricing population, input/output totals and shares, delegation counts and ratios; uncached input, cache reads/writes and reasoning tokens |
| Tool Calls, Tool Summary, Tool Time | Call counts, failures, truncation, recorded durations and context; added/removed lines and input/result sizes use the individual call ledger, whose identities support pairing |
| Skills | Evidence counts, saved instruction size, file loads and reach |
| Compaction | Recorded boundaries and conversation reach |
| Session Shape | Existing session statistics and diagnostics; wall/idle time, first response, turn-cycle p90, longest unprompted run, edited files and peak context |
| Agent Experience and Agent Events | Event totals, shares, per-message/turn/conversation rates and coverage denominators |
| Message Labels | Label matches, distinct labels, messages and conversations |
| Carbon Impact | Estimated electricity and CO₂e, using saved Carbon settings |
| Parallelism and Work Hours | Conversation activity hours, overlap and turn counts; human engaged hours and agent hours stay separate because they can overlap. Parallelism explicitly counts conversations with the saved activity gap |
| Waiting and Thinking | Mean recorded response wait and per-request input/output |
| Messages | Classified user-message word and character measurements, plus message-attributed labels, compactions, tool and agent-event evidence. Raw text word/character totals for all roles are blocked by the upstream aggregate-expression capability described in [the handoff](handoffs/query-table-scatter-text-metrics.md) |
| Habits | The entity-level activity measurements above are reused; library-wide streaks and day-calendar summaries have no value for one message or conversation |
| Pharos MCP | Server requests have no recorded conversation, workspace or message identity, so they cannot be matched to these points |

## Counting and missing data

`scatter` is an application-owned Query Table dataset. Its generated metric
specifications and schema come from the TypeScript catalogs; `generate-schemas`
updates both. It uses the published Query Table engine to group each selected
metric's source independently, then joins by recorded identity. It never joins
raw messages onto usage records. Ratios pool their own numerator and denominator
before joining; one axis's pricing or evidence filter never narrows the other.

A conversation point is one recorded transcript; delegated transcripts remain
separate points. Workspace points group by recorded workspace identity. Hourly
activity sources describe top-level conversation activity; their notes retain
that distinction. Session measurements include full sessions overlapping the
range; timestamped sources count matching records in the range.

Agent session points use the stored `agent_sessions.id`, including embedded
helpers. Usage retains its recorded session ID. Message-based measurements use
the embedded helper's membership when recorded, otherwise the conversation's
main session; conflicting helper memberships remain unknown. Tool and skill
ledgers retain their directly recorded owner. Session Shape measurements attach
to the conversation's main session because they describe that stream with
embedded helper messages excluded. Helpers without their own recorded transcript
have no Session Shape measurements. Conversation-level activity hours cannot be
assigned to individual agent sessions and are unavailable for this point type.

Message points include only measurements attributed to the exact message.
Daily usage costs, session duration and hourly activity are not distributed
across messages. Missing measurements remain missing, while measured zeroes
remain zero. Points require values on both axes. A logarithmic axis cannot draw
zero. The chart draws at most 2,000 points, ordered by X; source grouping must be
complete within 10,000 entities, otherwise the request asks for a narrower range.
Requests select only the metric fields they need, up to sixteen per population query; inspecting one entity can request its full set of measurements.
