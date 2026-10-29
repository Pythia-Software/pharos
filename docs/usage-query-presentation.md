# Usage query presentation

Each query keeps its full column catalogue, filters, grouping and saved URLs. Fresh defaults emphasize the row's grain and a few comparable measures. Explicit saved columns and native saved property layouts take precedence.

Analyze uses Query Table's native WHERE controls alongside columns, grouping,
ordering and metrics. Grouped queries keep source WHERE and result HAVING
separate. The former optional field-filter form is no longer needed.
Older `analyze_mode=search` and `analyze_search=1` links still open the query.
Dashboard links open records directly, where the same inline controls apply.

The embedded **Search in** chip names its text source, such as **Conversation
Message Text** or **Workspace Tool Command Text**. It applies keyword conditions
to one selected text field in each source record. It reuses the homepage's visual
rules and syntax editor: Boolean groups, exclusions, words, phrases, prefixes,
proximity, literal text, regex, case and phrase-spacing controls. Message scopes
and corpus-based word variations do not apply to record fields. Searches use
the same unicode61 verifier as message search and never select parent workspaces.
The restriction runs before grouping, metrics, sorting and pagination. Query-v3
executes it; older row/metric protocols reject it instead of ignoring it.

A keyword clause is part of its table's query definition: Query Table 0.9.7's
host query state ([handoff](handoffs/query-table-saved-host-query-state.md)),
registered by `web/src/host-query-state.ts`. Save, Update, Saved, Default,
Share, Reset and Undo/Redo treat the clause and the table query as one
definition, and adding, editing or removing the clause is one history entry.
Its definition (field and editor rules, without the editor's rule IDs) is
mirrored in the per-dataset `record_<dataset>` URL parameter; only that
dataset's parameter belongs to its table. Saves made before host state, and
Reset, have no clause. A clause that cannot be read, names a field outside the
allowlist or exceeds Query Table's 4,000-character host value fails the whole
restore, so the query never runs without it. Each request compiles the clause
it runs with into `record_search`, which the service validates and runs; the
service also refuses a request whose host state declares a clause without
`record_search`, or `record_search` without a declared clause. It checks that
both are present, not that they match: matching them would need the editor's
compiler on the server.
Search keeps its content search (`text_builder`, or `kind`, `search` and
`case`) and its Table/Conversations view the same way, as does the older Human
messages table's `message_query`; message search cursors stay out of the
definition. Returning to a dashboard explicitly resets keyword conditions, and
switching datasets drops them.

Query Table keeps each host value within 4,000 characters, where Pharos used to
accept keyword drafts of up to 28,000 (Analyze) or 24,000 (Search's
`text_builder`). Editing past the limit says the search is too large; an older
link or save that carries a larger draft fails to restore, and Reset page opens
the table without it. Pharos has asked upstream to raise the limit.

A bare reload restores the last query whose rows and metrics loaded
(`persistLast: "succeeded"`), never one that failed, timed out or was
cancelled. A table's default saved query is restored on every bare visit, so
Pharos skips a default whose rows the service rejected (HTTP 400 or 422) or
that ran past the client's 45 s deadline (`rejectDefaultQuery` in
`web/src/storage.ts`), and says so. Try again runs the default, and the default
is restored again once it loads, from Saved say. A failed or slow (12 s) query
keeps Retry, Reset table and Reset page (`fresh=1`, which bypasses the last-used
and default queries). The deadline stays, so a stalled connection ends as a
failure.

| Query | Compact rows | Native Details focus | Drilldown |
| --- | --- | --- | --- |
| Token usage | Day, conversation, repository, model + effort, tokens, cost, output, cache share | Token breakdown, pricing coverage, attribution, Carbon estimates and exact time bounds | Source conversation near the first recorded usage; root conversation separately |
| Conversation activity | Time, conversation, event kind, root model, estimated human input, output | Distinct human/output grain, root attribution, repository and provenance | Exact human message; output's source conversation near its recorded time |
| Human words by conversation | Conversation, repository, last message, typed words/turns/share, supplied words | All authorship categories, agent consumption, comparison measures | Messages filtered to this work |
| Human messages | Time, classified two-line snippet, category, typed/other words, conversation | Retained classified text, classification rules and source spans, word breakdown | Exact message, with the reader's reveal and highlight |
| Tool calls | Time, tool, invocation/target, status, duration, context added, conversation | Input/result excerpts, truncation, parsed commands/URLs, request context and costs | Separate exact call and result message links |
| Tool summary | Tool, command, day, repository, model + effort, calls, error rate, context added, cost | Complete grouping identity, reliability counts, timing/context/cost breakdowns | Calls preserving all 13 summary dimensions |
| MCP calls | Time, tool, sanitized arguments preview, status, duration, estimated output, results | Native searchable/copyable JSON arguments, error text, response sizes and truncation | Explicit conversation/message/work IDs in the recorded arguments; response text is not recorded |
| Skill evidence | Time, skill, evidence kind, status, loaded bytes, conversation | Path, invocation/load identity, model context, exact evidence/body references | Evidence and retained body separately; native Tool-call details |
| Parallel activity | Hour, unit, repository, global/repository concurrency, unit/elapsed hours | Counting/gap assumptions, source unit/location and all overlap shares | Agent conversation near the selected hour; workspace-counted units link to Library works at that exact checkout location |

Details use Query Table's native modal and inline expansion, property picker, per-mode saved layouts and shareable active-row state. All remaining fields stay selectable. Property payloads are fetched by item identity on demand, independently of collection filters; no transcript payload is added to list requests. Existing relationships remain available.

Source links are real anchors (including modified-click behavior). Exact messages use `message` and, for multi-call envelopes, the provider `call` ID; aggregate time records use `at`, labelled as nearby activity. The reader materializes collapsed/virtualized messages, reveals the selected event, opens ancestors, scrolls and highlights it. Missing identifiers never create a broken `undefined` link. Query-to-query links use explicit identity filters and retain relevant Carbon context.

Analyze's nine ordinary query tables always end with one **Drill in** column per
destination, then native **Detail**. Human words by conversation exposes work
and messages separately; tool calls expose call and result messages separately;
skill evidence exposes evidence, retained body and tool-call details separately.
Unavailable destinations show a dash in their own column. Saved data columns,
widths and explicit sorts remain in effect. Query drilldowns specify a sort:
time ascending within one conversation/work/unit, time descending across a
population, with identity breaking timestamp ties. Grouped results receive an
explicit key/summary sort; their Records drilldown also applies the appropriate
record order. Grouped results show native group Detail
([handoff](handoffs/query-table-group-detail.md)): their columns end with the
link summaries, then Detail, which shows the group's keys, summaries and link
summaries. Link summaries in Detail open the same exact-match records as the
Records drill-down, which stays the table's trailing column. Search and the
other tables with Detail end grouped results with it too.

Every Analyze query opens with native **Fit All to Screen**, including restored
queries and drilldowns. The `fitToScreen: true` presentation preference travels
in the encoded `q_<dataset>` URL parameter and adapts to the viewer's table area
after loading. Filters, sorts and selected data columns are preserved. Manual
column sizing can override the fit during the current visit.
Since Query Table 0.9.7, fitting keeps each column wide enough for its Detail
and JSON buttons and scrolls the table when they do not fit
([handoff](handoffs/query-table-fit-screen-controls.md)).

This is host integration against the published Query Table 0.9.2 release. No dependency patches or generated-bundle edits are involved.

Long tool inputs/results start with a short preview and expand in place, keeping status/timing/context visible before payloads. MCP uses the public native `JsonValueView` with its full tree/search/copy controls inside a property renderer. Nothing changes the library's component implementation. Unrepresented fields and raw payload metadata remain available through the property picker.

The Tool invocation/target column is a display convenience. Filtering, sorting and grouping use the original command/path/query/URL fields; this avoids introducing an unindexed combined-text search and preserves those fields' distinct meanings.

## Validation — 2026-10-21

Used the existing isolated development catalog; production was untouched. All nine query views were opened against real data, inspected in native modal and inline expansion, and replayed from an active-row URL. Every default property loaded. Desktop screenshots for Tool calls, Human messages and MCP calls were visually reviewed under `.context/usage-presentation/`.

| Query | List + render | Open Details | Default properties |
| --- | ---: | ---: | ---: |
| usage | 1.13 s | 1.88 s | 21 |
| conversation_activity | 3.03 s | 0.86 s | 12 |
| writing | 1.03 s | 0.38 s | 21 |
| writing_messages | 0.41 s | 0.11 s | 16 |
| tool_calls | 0.42 s | 0.17 s | 22 |
| tools | 1.01 s | 0.18 s | 29 |
| mcp_calls | 0.43 s | 0.14 s | 12 |
| skill_usages | 0.52 s | 0.13 s | 15 |
| parallelism | 3.02 s | 1.93 s | 16 |

These are serial first-open observations, including rendering, rather than universal latency bounds. The targeted audit exercised three date/filter combinations per query view and replayed their URLs: **27 cases, 20 Green, 7 Yellow, zero Red/Black**, with no execution, URL or transition errors; the slowest sampled completion was 3.84 s. The earlier 432-case audit remains the broader baseline. Private evidence: `.context/usage-audit/presentation-final/` and `.context/usage-presentation/`.

Nine live source destinations were also opened: usage time, activity message, human message, tool call, tool result, skill evidence, skill body, parallel activity time and MCP requested message. All revealed and highlighted the intended retained event. Opening a shortened result fetches the original; its replacement body now preserves the source highlight and scroll target. Browser coverage also checks multiple calls inside one retained message, a collapsed call group, native modal focus and mobile layouts.

Validation passed: Go fast and full suites; JS type/schema/recipe/bundle checks and 575 unit tests; the full JS matrix; 35 selected structure/focus browser cases, plus the focused source-loading and modal checks after the reader fix. Query Table packages and Go module remain at published 0.9.2, with no local dependency patches.
