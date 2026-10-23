# Agent messages and retained reasoning search

Implementation and follow-up design, 2026-10-23. Complements [text search performance](performance/text-search.md) and
the broader [text search survey](textual-search-survey.md).

## Implemented reasoning surface

The current branch normalizes saved thinking, analysis-channel text and source
reasoning summaries into canonical reasoning messages during ingestion. Native
Claude/Codex, Conductor and TL1 Codex exec parsers retain supported plaintext
reasoning. `reasoning_format` distinguishes `text` from `summary`. Redacted,
encrypted and empty reasoning is recorded as unavailable metadata, never as
searchable thought. Original envelopes remain available as accounting/provenance
evidence, without copying usage onto every extracted block.

The resumable **Index saved reasoning** Library upgrade recovers old metadata
envelopes and reasoning retained only in `raw_text`. It preserves existing IDs,
block order and source locators, and does not load unrelated user/tool bodies
to find thinking. Existing full-text and surface indexes serve the extracted
text; no index construction or envelope parsing runs during search.

`search_text` with `surface: "reasoning"` returns `reasoning_coverage`, including
pending conversation counts and retained reasoning counts. The capabilities
endpoint's `reasoning_extraction` additionally breaks down source/provider
counts, bytes, summaries and unavailable records. Normalization completeness
describes retained catalog text, independently of missing source history.

The combined agent target, dedicated agent index and batch endpoint below remain
proposals. No production catalog was migrated in developing this change.

## Baseline investigation

The requested workflow is concrete: search ten correction phrases over the last
seven days, looking only in agent replies and saved reasoning, then inspect the
matching conversations. It needs predictable scope, few tool calls, and useful
evidence even when some phrases are common or reasoning coverage is incomplete.

The current API already provides more of this than the reported answer suggests:

- `search_text` accepts `surface: "assistant"` and `surface: "reasoning"`, dates,
  repository/source/provider filters, and Boolean OR over quoted phrases.
- `search_messages` accepts exact `roles` and `kinds` filters, although its query
  language deliberately treats OR as a word. It searches all kinds by default.
- The connected `get_search_capabilities` endpoint confirmed both surfaces and
  ready metadata/substring indexes on 2026-10-23. It does not report reasoning
  document counts or source-specific retention.

Thus, an inability to restrict searches is a tool-discovery/use problem with the
current API. We cannot establish which tools or versions Claude had in that
conversation, or why it could run only two more calls, from this quote alone.

The investigation identified these backend gaps before implementation:

| Path | Behavior before implementation | Consequence |
| --- | --- | --- |
| Search surfaces | Assistant means `kind=message, role=assistant`; reasoning means explicit `thinking/reasoning/analysis` kinds. | A role filter alone cannot reliably combine them: legacy reasoning can have another role. |
| Native Claude ingestion | `messageText` takes text blocks; the block loop handles tools and skips thinking. | Saved thinking is not emitted as searchable reasoning. Some original envelopes survive as accounting evidence, but not every event does. |
| Native Codex ingestion | Handles message items, tool events and selected metadata; has no explicit reasoning-event branch or analysis-channel classification. | Reasoning events can be skipped; analysis-channel message text can be classified as an ordinary reply. |
| Conductor ingestion | Splits text/tool blocks; no thinking-block case. | Mixed envelopes can lose thinking; otherwise retained JSON may remain metadata. |
| TL1 Codex exec ingestion | Completed agent messages and tool items have branches, reasoning items do not. | Retained reasoning items need explicit handling. |
| Reader | Can split retained JSON `thinking` blocks into reasoning events at display time. | Seeing reasoning in the reader does not establish that the reasoning search surface indexes it. |
| Lexical index | Agent searches use the global `messages_fts` postings, then restrict surface through metadata. | Common phrases/words can traverse irrelevant tool and user postings. |
| Substring index | Does not cover explicit thinking/reasoning/analysis kinds. | Reasoning regex/literal search lacks this pruning path. |

These statements come from `text_search_api.go`, `text_search.go`,
`text_search_plan.go`, `adapters.go`, `adapter_conductor.go`, `adapter_tl1.go`,
`token_usage.go`, `substring_index.go`, and `assets/reader-model.js` in
`internal/archive`. The older replica measurements found zero explicit reasoning
messages; that is historical evidence, not a census of today's library.

## API recommendation

Extend the shared search engine, keeping HTTP, MCP and Library semantics aligned.

1. Add `surface: "agent"`, defined as the union of assistant reply text and
   retained reasoning. Preserve the existing two surfaces for separate searches.
   Exclude user prompts, tool arguments/results, metadata, and parent-agent task
   prompts (`role=agent`). Include replies from main agents and subagents; their
   lineage remains available through conversation evidence. Avoid using
   `roles: ["assistant"]` as the implementation of this union.
2. Make the tool description explicit: “For agent messages and chains of thought,
   use surface=agent; reasoning searches only saved reasoning text.” Include one
   multi-phrase example. Callers should not have to discover role/kind spellings.
3. Support the simple combined search with the existing OR grammar. This is one
   search, with a single result stream and cursor, not ten separately completed
   searches.

Proposed call, using a fixed seven-day interval so cursor requests keep their
original scope:

```json
{
  "surface": "agent",
  "query": "\"I see now\" OR \"I was wrong\" OR \"my mistake\" OR \"I misread\" OR \"actually\" OR \"wait\" OR \"I realize\" OR \"that was incorrect\" OR \"I overlooked\" OR \"turns out\"",
  "from": "2026-10-16T18:00:00Z",
  "to": "2026-10-23T18:00:00Z",
  "limit": 30
}
```

Quotes mean token phrases, so `"wait"` also finds `wait,`. Literal punctuation
matching remains a separate mode. Correction phrases are retrieval hints, not
evidence by themselves that an agent made a mistake.

For “try each phrase,” add a bounded `search_text_batch` MCP tool and matching
HTTP batch endpoint, backed by the same `Catalog.SearchText` engine. Accept at
most ten named query entries and shared scope/surface filters. Each entry carries
its own query or typed tree, result limit, continuation cursor and completion
state. Return a compact per-query result with evidence previews and exact or
lower-bound counts. A common term such as “actually” must not consume all result
slots or prevent the other nine queries from getting a first slice of work.

Use round-robin bounded slices within a shared request time/byte/output budget,
with explicit `not_started` entries when the budget cannot serve every query.
Bound concurrency rather than launching ten full scans. Share scope resolution
and compiled metadata where practical. Output trimming must preserve each
entry's cursor before any omitted hits. Existing single-query response shapes
and cursors remain unchanged. Batch completion does not require exact corpus
counts before returning evidence.

Both APIs should report structured surface coverage: extractor/index versions,
ready/backfilling state, retained message/byte counts, and per-source retention
status. Distinguish supported extraction with zero retained rows, unsupported
extraction, unavailable source content, and pending backfill. Execution completion
and source coverage remain independent; finishing a search does not prove the
archive contains every thought. Use maintained counters/coverage records, not a
whole-catalog text census on each request.

## Retain and normalize before optimizing

First emit ordinary canonical `MessageRecord` rows for explicit saved reasoning,
using `kind=reasoning`, `role=assistant` for newly normalized data. Recognize
provider-specific thinking blocks, reasoning summaries/text, reasoning items,
and message channels. Preserve whether the source supplied a summary or fuller
reasoning text. Encrypted/redacted payloads and token counts are not reasoning
text. Never reconstruct omitted content from replies or tool output.

Keep provider-native event/block order, timestamps, model/effort, conversation
lineage and source locators. IDs must be deterministic and preserve existing
reply IDs where possible. Do not concatenate text on opposite sides of a
reasoning or tool block into a new phrase match. Preserve block boundaries for
matching and snippets, with offsets measured in the decoded canonical text.
Legacy raw JSON needs an exact source/block locator; its escaped bytes do not
share decoded-text offsets.

Use the existing canonical message model first. Add a derived parts relation
only where retained mixed envelopes cannot be represented faithfully without
changing existing message identity; do not build a second generic transcript
model merely to add a filter. Share extraction fixtures with reader tests so
reader-visible text and searchable surfaces agree. Retain source locators and
normalization version either way.

Version the adapters and reindex available captures/source files. A catalog-only
pass can recover only text actually retained in its envelopes. Report sources
requiring recapture or whose original text is unavailable. Start with native
Claude/Codex and their Conductor/TL1 paths; audit other adapters before advertising
equivalent coverage. Antigravity currently has an explicit test requiring
reasoning omission, so changing its retention is a separate deliberate decision.

Adding reasoning must not create extra model requests, duplicate usage from a
shared raw envelope, change human-word totals, or silently duplicate evidence
across mirrors. Cover those accounting and identity invariants during reindex.

## Make queries do less work

Reuse SQLite and the existing bounded execution/cursor machinery. After canonical
retention works, measure a dedicated agent-text FTS5 index containing replies and
reasoning, excluding tool and user text. A candidate layout has separate indexed
reply/reasoning columns, one original block per row, compact canonical locators,
and the existing Unicode61 tokenizer. Column-restricted MATCH serves either
surface; the combined surface searches both. Keep snippets in canonical text
instead of storing another full copy for display. Measure contentless/external
content storage against the current FTS storage and deletion requirements before
choosing a physical layout.

This trades some additional postings and ingest work for avoiding irrelevant
global postings. Benchmark it against today's metadata-filtered global index;
do not call it faster until the replica shows less work and lower latency. The
existing human search can keep its current path initially.

Store filterable surface/date/conversation/workspace metadata and byte lengths
beside the compact row mapping. For narrow recent scopes, compare metadata-first
candidate enumeration against FTS-first retrieval. Filtering after a candidate
cap must never lose valid matches. Date filters alone do not guarantee a cheap
plan; current metadata planning falls back once more than 256 eligible rows are
found. Retain conservative planning budgets and exact date verification.

Prioritize token phrases and OR, which cover the motivating example. Extend
trigram coverage to reasoning only after measuring its retained bytes and index
cost. Regex without safe mandatory literals remains a bounded surface scan;
neither batching nor an index should imply instant exhaustive arbitrary regex.

Backfills must be resumable, versioned and concurrent with ingestion, following
the existing small-surface index pattern. Update/delete/replace paths must keep
postings and locators consistent. Publish readiness only after coverage is
complete; a partial new index cannot silently replace the old search path.
Include normalization/index generations in cursors and cache keys. Searches
must not synchronously reparse source files or build indexes.

## Delivery and acceptance

Ship in three reviewable increments:

1. Retention, normalization, coverage, combined agent surface and clearer tool
   descriptions. Existing OR queries then support the workflow in one call.
2. Measure and add the agent-only lexical path if it improves representative
   workloads; complete its background migration and cursor/readiness handling.
3. Add batch requests for independently reported phrase searches, with fair
   scheduling and output-budget-safe continuation.

Extend existing adapter, text-search, cursor and MCP tests. Cover mixed
reply/thinking/tool messages, reasoning-only events, analysis channels, encrypted
or missing text, legacy non-assistant reasoning roles, subagents, block-boundary
phrases, and mirror/accounting invariants. Ensure the combined surface excludes
matching user/tool/parent-prompt text. Batch tests must show that a common first
query cannot starve later queries or lose results during pagination/trimming.

Measure on a consistent scratch backup: ten correction phrases for seven days
and all time; each surface separately and combined; rare, common and absent
phrases; cold/warm runs; and searches during backfill/ingest. Record p50/p95 first
page latency, full-enumeration latency, candidate/body reads, bytes verified,
index size, backfill duration and ingest overhead. Proposed initial target:
under one second warm p95 for a combined first evidence page, and under two
seconds warm p95 for a ten-query first batch on the representative replica.
Report whether all entries received work and how much remains. Exact all-time
counts/full enumeration have separate measurements and no implied one-second SLA.

No production benchmark or migration has been run for this proposal. The existing
performance report explicitly records unresolved broad-query latency, so its
successful phrase measurements should not be generalized to these new surfaces.
