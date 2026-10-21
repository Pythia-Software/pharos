# Usage query presentation

Each query keeps its full column catalogue, filters, grouping and saved URLs. Fresh defaults emphasize the row's grain and a few comparable measures. Explicit saved columns and native saved property layouts take precedence.

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
