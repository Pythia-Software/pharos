# Text search: implementation and performance

Measured 2026-10-12 on the Mac running this workspace, against a consistent SQLite backup on `/Volumes/euclid`. These are measured development results, not a production SLA.

## What changed

Library Find, `POST /api/search/text`, and MCP `search_text` now use `Catalog.SearchText`. MCP `get_search_capabilities` and `GET /api/search/text/capabilities` describe the language, budgets, and index readiness. Existing `search_messages` keeps its exhaustive `terms`/`phrase` contract and treats operator words literally. Ranked discovery (`search_conversations` / semantic search) remains separate.

The new engine supports:

- `AND`, `OR`, binary `NOT`, parentheses, implicit AND, mixed quoted token phrases, and trailing `*` token prefixes. Operators are uppercase. Precedence is proximity, then AND/NOT, then OR. `NOT` needs a positive left operand.
- `timeout NEAR/8 retry` for either order, and `timeout PRE/8 retry` for that order. Operands are words or phrases; the distance counts intervening tokens, from 0 through 100. Use Boolean composition outside the proximity expression.
- `terms` mode for literal operator words, `phrase` for the entire query as a token phrase, `literal` for original punctuation/spacing, and Go/RE2 `regex`. Library's syntax box trims leading/trailing whitespace; visual exact-text rules and HTTP/MCP preserve it. Regex supports `(?m)` and `(?s)`, with up to 16 named/numbered capture ranges on the first match. Unsupported PCRE features produce errors.
- Original-case and quoted-separator checks; FTS5 `unicode61` token normalization otherwise. Exact-case checks also require original accent spelling. Ordered proximity and authored-span verification use an isolated in-memory FTS index with the same tokenizer.
- Explicit fuzzy expansion, returned in response metadata. Defaults: first three characters fixed, one edit for 4–6-character words, two for longer words, at most 24 alternatives, plus the existing word-family heuristic. Tools may set `fuzzy_prefix_length`, `fuzzy_max_edits`, and `fuzzy_max_expansions`. Expansion is not exhaustive edit-distance search. Phrases/proximity are not expanded. The UI defaults to exact tokens.
- Prose, user, assistant, explicit retained reasoning, tool messages, all stored text, and individual classified typed/pasted/quoted/resent/template/harness/automated/attachment spans. A category restricts matching to each original span; words elsewhere in the message cannot satisfy its query, and snippets stay within the span. Search does not trigger authorship classification.
- Repository, workspace, conversation, provider, source, exact role/kind, selected-message, and inclusive UTC message-date filters. Library search uses selected messages; MCP/HTTP default to all selection states unless `selected_only` is supplied.
- A typed JSON tree (`where`) as an alternative to the query string: `op`, leaf `text`, `children`, and proximity `distance`. Literal/regex nodes are supported at the root. For example:

```json
{
  "surface": "typed",
  "where": {
    "op": "and",
    "children": [
      {"op": "near", "distance": 8, "children": [
        {"op": "term", "text": "search"},
        {"op": "term", "text": "quality"}
      ]},
      {"op": "term", "text": "implement"}
    ]
  },
  "limit": 20
}
```

The unit is one message, or one classified span. Returning a conversation hit in Library groups matching messages; it does **not** change AND/NOT to cross-message semantics. Stable reverse index order makes enumeration cheap and resumable; it is not BM25 ranking or a chronological sort. `range_kind=excerpt` identifies lexical excerpt offsets; regex captures refer to exact original UTF-8 byte offsets. Message IDs, hashes, timestamps, and evidence locators remain available.

## Visual search editor

Library opens a visual editor that reads as one sentence down a narrow left column of grammar words: **Find** *target* **with** *condition*, then **and**, **or**, or **not** before each later row. Text rules and message or workspace filters are different engine concepts, but the editor presents them all as conditions: each is one full-width row holding its choices, a value box that wraps long text onto more lines (Enter searches; pasted line breaks become spaces), and **×** to remove it. A single **Add condition** menu offers words or a phrase, words to exclude, each message property, groups, and workspace properties. Above the rows, the search is read back in plain English as it is composed, with chosen values set apart from the connecting words; on Home this reading fills the search box itself and wraps rather than truncating. Home keeps the search at a hero measure of about 800 px. Elsewhere the editor responds to its own width: once it is wider than about 980 px (Library, the search dialog, Writing, and Tools on a desktop window), the conditions keep a comfortable measure and the reading moves into a panel beside them; narrower editors show it above. On phones, each text box takes its own line under its row's choices, and the footer settles into ordering, then options and the action. The second row's joining word is a menu that switches its group between all (**and**) and any (**or**). A rail along the left edge marks implicit brackets: OR alternatives that sit beside AND filters, and multiple AND NOT siblings, which are joined with OR inside that rail. A nested AND NOT only rejects its enclosing group, so it does not reject another matching alternative. Explicit nested groups are the only boxed containers, with their own **Add condition**, options, and **×**. There is no separate “Exclude messages” panel. Typed values use the interface font; query syntax, exact text, and regular expressions use monospace. A text condition's **⋯** menu contains its explanation and switching between Match and AND NOT; removing the last positive condition leaves an empty one to type into. Word, phrase, word-beginning, and ordered/unordered proximity controls produce typed query trees directly. Entering `AND`, quotes, or parentheses into a rule cannot introduce operators. Every group needs a positive rule; blank rules, incomplete proximity pairs, and reversed dates are shown as validation issues rather than silently omitted. Menus support keyboard navigation, and Escape dismisses them.

The target after **Find** replaces the separate **Search in** section. Besides conversation messages, it offers agent replies, saved reasoning, system/developer instructions, tool activity, classified portions of messages, modified files, and tool URLs. System/developer presets choose compatible underlying surfaces automatically. Any target other than the default shows one line describing what it covers. The same **Add condition** menu adds inline message provider/source, repository, UTC date, conversation/workspace ID, or workspace-property clauses. They stay directly editable and removable, including custom role/type restrictions from saved searches. These are global **AND** clauses outside the text group's all/any boundary: `(text A OR text B) AND message provider = Claude`. Workspace predicates appear as `and Workspace matches (…)`, including the original AND/OR grouping, with the existing table query editor available from that clause or **Result layout**. Workspace filters select containers and update immediately; message filters constrain the individual match and apply with Search. Exact text and regular expressions are separate match styles in **Options**, beside letter case, phrase punctuation, and word variations; each option explains itself there, and the trigger counts the settings that differ from the defaults.

Draft edits do not search on every keystroke. **Search messages** first calls `POST /api/search/text/validate`, which uses the engine's parser, option checks, and regex compiler without querying the catalog. Validation errors preserve the applied search and its results. A small **Rules / Syntax** switch sits beside the target; the target, additional AND filters, and sorting remain shared between both editors. Syntax supports expert queries and conversion where the visual rules can preserve their semantics; unsupported conversions show an error without changing the draft. File and URL search hide the switch, because message syntax does not apply. Word variation expansion must be disabled before exporting visual rules to syntax.

**Sort by** sits in the editor's footer, beside **Options** and the search button, with field, direction, and additional tie-breakers. It uses the table's existing query controller, so table headers, URLs, saved queries, and the advanced result-layout editor stay synchronized. Existing regex extraction and empty-value ordering are retained and visibly labeled. Ordering updates immediately, applies to workspaces in the current search batch, and does not change message enumeration or add corpus scans.

Applied searches persist in the `text_builder` URL parameter, including scope and filters. Bookmark previews use the same compiler and keep cursor and partial-batch coverage. Malformed saved rules show an error instead of silently running an unfiltered preview. Home and quick search use ordinary words by default and a **Keep words together** checkbox for phrases. Quick search replaces the earlier visual tree. Legacy syntax links and file/URL search remain supported.

The editor adds no search indexes or new corpus scans. `GET /api/search/text/options` reads distinct provider/source metadata with a one-second budget. On the same 5.47-million-message scratch replica, its two measured calls took **87 ms and 81 ms** (3 providers, 5 sources). The repeated query harness passed its 2.5-second interactive assertions; expensive typed-span and regex queries still returned partial work near the existing 1.5-second budget. These are warm development measurements, not percentile guarantees.

## Scope, limits, and continuation

Library table predicates over stored fields resolve eligible workspace IDs **before** text candidates are bounded, on both v1 and v2 row/metric paths. File and URL search also apply workspace scope before their existing cap. Computed-column predicates stay in the v2 engine. Whole-population distinct values/field stats retain their existing independent scope and discard the table's search cursor. They reuse their own cursorless search batch rather than trying to resume a cursor bound to different workspace filters.

Text pages return `complete`, `stop_reason`, `next_cursor`, cumulative matched-message `count`, `count_relation`, strategy, candidates, verified bytes, elapsed milliseconds, coverage, and skipped oversized records. A missing match in an incomplete page is not proof of absence. Continue when `next_cursor` is nonempty, even if `items` is empty. A timeout before a resumable position is reached returns `time_budget_no_progress`, no cursor, and a `stop_message` asking the caller to narrow the query or increase its time budget; it never reports completeness or offers an identical cursor. The last page may be empty when the preceding one exactly filled the limit.

Defaults are 1.5 seconds, 10,000 eligible candidates, and 16 MiB verified original text per request. Callers may increase these to 5 seconds, 50,000 candidates, and 64 MiB. The service may read one bounded look-ahead message while discovering a byte/page boundary. SQL cancellation and cancellation while waiting for a shared Library result are honored. Original text verification is bounded to `min(1 MiB, max_bytes)` per message. Regex additionally admits at most 1,000 compiled instructions and caps each message to `min(1 MiB, max_bytes, 8 MiB / instruction_count)`, because Go regexp cannot be interrupted inside an individual call. A message larger than the page's entire byte budget is skipped and advances the cursor; it cannot stall every subsequent page. `skipped_oversize` reports these omissions. Increase `max_bytes` and restart to revisit messages omitted only by that setting. Oversize skips permanently make that scan's count a lower bound; continuation does not pretend to have searched those records.

Unrestricted literal/regex scans over tools/all text require a conversation, a workspace, or at most 100 table-selected workspaces. Small surfaces can use the incremental metadata index. While that index builds, callers can use token search or explicitly narrow the scope. Regex/literal candidates use the existing partial trigram index only where its coverage is applicable and complete. Mandatory literals come from the regex syntax tree; unsupported alternatives get a bounded scan. Case-folding candidates exclude Unicode folding traps such as `S/ſ` and `K/K`.

Cursors bind query, scope, strategy, catalog path, and persisted search generations. Ordinary message appends and no-op writes preserve cursors; pages continue below their existing position, so later appends appear in a new search. Actual message edits/deletes/replacements, insertion below the existing rowid range, FTS mapping changes that reorder existing positions, and scope metadata changes still invalidate them. Authorship changes only invalidate classified-span searches. Fuzzy searches additionally watch appends because the vocabulary can change their bounded expansion set. The schema replaces the old triggers on reopen and rejects pre-upgrade cursors. Unrelated MCP call logging does not invalidate cursors. They preserve work across requests without holding a WAL snapshot open, but are not immutable historical exports. MCP output trimming rewinds to the last emitted result, so a smaller output budget cannot silently skip matches.

The Library says “in this batch,” shows partial counts and verification limits, and offers **Continue to next batch** / **Start over**. Search options persist in its URL. Each batch's table metrics refer to that batch, not an asserted whole-corpus total.

Coverage is independent from execution completeness. The measured replica contained **zero explicit thinking/reasoning/analysis messages**. An empty reasoning search says so; it does not mean the agents never reasoned. Embedded reasoning JSON and reasoning discarded by existing adapters are not recovered here. Authorship classifications can be missing/stale, and source history can be incomplete.

## Replica and index cost

The live catalog was opened read-only and copied with SQLite's backup API, including a pinned read snapshot, rather than copying its main file without the WAL. No production migrations, benchmarks, settings changes, or app replacement were performed.

Scratch copy: `/Volumes/euclid/Pharos-search-baku-20261012.noindex/catalog.sqlite3`.

| Measurement | Result |
|---|---:|
| Original backup size | 72,295,833,600 bytes (67.33 GiB) |
| Backup duration | 180.21 seconds |
| Workspaces / conversations | 34,400 / 31,800 |
| Stored messages | 5,472,401 |
| Assistant prose | 434,382 messages / 180,088,881 text bytes |
| User prose | 37,860 messages / 299,305,469 text bytes |
| Authorship spans | 43,300 total; 15,280 classified typed |
| Small-surface metadata backfill | 164.29 seconds, without the service's between-batch pauses |
| Added catalog allocation | 121,716,736 bytes (116.08 MiB, about 0.17%) |

`text_search_documents` stores IDs, surface, conversation, and byte length; it does not duplicate text. Old catalogs backfill 2,000 message rowids per transaction with pauses between batches. Triggers keep metadata and search generation current during ingest, edits, and deletion. Readiness is published only after the backfill finishes. Whole-table aggregation of raw text sizes exceeded a 180-second diagnostic limit; the successful size census above comes from the small metadata index.

## Query measurements

The harness requests 50 matching messages, at default budgets, except Library's internal batch size is 5,000. Both passes below use the same process; the second repeats the first. OS/storage caches were already warm from backup/indexing/earlier experiments. These are **not cold-cache benchmarks or p95 estimates**. The harness prints no private transcript text or identifiers.

| Query / surface | First pass | Repeat | Outcome |
|---|---:|---:|---|
| `heliotrope`, all stored text | 43 ms | 25 ms | All 11 matching messages |
| `test`, prose | 208 ms | 171 ms | 50 matches, resumable |
| `(timeout OR deadlock) NOT retry`, prose | 423 ms | 298 ms | 50 matches, resumable |
| `"race condition"`, prose | 94 ms | 67 ms | 50 matches, resumable |
| `migrat*`, prose | 372 ms | 285 ms | 50 matches, resumable |
| `timeout NEAR/8 retry`, prose | 198 ms | 146 ms | 50 matches, resumable |
| `retreive`, explicit fuzzy | 652 ms | 474 ms | 50 matches, resumable |
| `test`, one conversation | 212 ms | 109 ms | All 8 matching prose messages |
| Literal `foo::bar`, user | 13 ms | 13 ms | Complete, zero matches |
| `search`, typed spans | 1,500 ms | 1,500 ms | 31 / 33 verified matches; time-budget continuation |
| Regex `search.{0,80}(quality\|inadequate)`, user | 1,511 ms | 1,513 ms | Incomplete; time budget and 185 verification-size skips |
| Regex `[0-9a-f]{40}`, one conversation/all | 86 ms | 59 ms | 11 matches; 26 oversized messages skipped |

The previous Library SQL for `test` exceeded **5,002 ms before returning any rows**. It ranks all matching postings with BM25 before its 5,001-row cap. The new path prioritizes quick enumeration and truthful partial results, so this is a latency/behavior comparison, not proof of equal ranked top-k relevance. The old rare prose query completed in 12 ms; there is no claim that every query gets faster.

Observed problems changed the implementation:

1. Global postings with a conversation predicate still timed out. Narrow scopes now use verified FTS rowid bounds, derived from the mapping only when it covers every eligible message. Small metadata prevents giant tool bodies from being read just to determine those bounds.
2. Joining raw messages before filtering prose spent time loading irrelevant records. Ready small-surface metadata now rejects them earlier.
3. A two-character fuzzy vocabulary prefix exceeded 1.5 seconds. The new engine defaults to three fixed characters; callers can deliberately request broader expansion. The UI no longer enables fuzzy matching by default.
4. Fetching eligible IDs first and seeking FTS once per document both exceeded five seconds in query-plan experiments. Neither was adopted. Typed-span token queries instead stream bounded user-message candidates and verify individual spans with an in-memory FTS tokenizer; this returned 31–33 real matches within a page budget, where the initial global-postings plan returned none before timing out.
5. Broad user regex is still not an instant exhaustive query. Budgets, skips, and continuation are part of its visible result rather than hidden recall loss.

## Reproduce and validate

```sh
go test ./internal/archive -run '^TestTextSearch|^TestLibraryFind|TestLibraryQueryTableCombinesKeywordSearchWithFilters' -count=1
go test -race ./internal/archive -run '^TestTextSearch|^TestLibraryFind|TestLibraryQueryTableCombinesKeywordSearchWithFilters' -count=1
go test -c -tags probe ./internal/archive -o .context/search-performance/archive.test
PHAROS_SEARCH_REPLICA=/path/to/scratch/catalog.sqlite3 \
  .context/search-performance/archive.test -test.run '^TestTextSearchReplica$' -test.v
```

The replica harness opens SQLite read-only, refuses the known production path, and never calls `OpenCatalog`. Prepare a consistent backup and let the new metadata index finish on that scratch library first. Its default interactive assertion is 2.5 seconds including scheduling/cleanup tolerance; hitting a work budget must still report incompleteness.

Tests cover mixed phrases, operators and precedence, flat versus nested Boolean queries, prefix/proximity/ordered semantics, Unicode normalization and trigram candidate supersets, NOT within authored spans, exact-case exclusions, dates/roles/scopes, cursor continuation/staleness, MCP output-budget continuation, byte/candidate/regex-work limits, cancellation, trigger updates/deletes/replacements, regex captures and zero-width ranges, HTTP/MCP/Library parity, and the 5,100-unrelated-message early-filter regression. Focused tests, race checks, and `go vet ./...` pass. TypeScript, bundle/schema checks, existing frontend tests, and desktop/mobile browser checks exercise the UI. Browser checks verified successful Boolean search and visible parse errors with no uncaught page errors.

Visual editor validation adds model tests (`cd web && npm run test:text-search`), bookmark-preview regression tests, validation-only HTTP tests with a nil catalog, and structured-only Library requests on both API versions. Browser checks cover root/nested AND NOT, new negative groups, global metadata AND semantics alongside text OR, UTC date validation, workspace filtering, provider/source fallback entry, saved custom role/type restrictions, the unified filter menu, saved-state restoration, invalid syntax/regex retaining results, unsupported conversion errors, mobile width, and quick-search replacement. The opt-in `TestTextSearchPreview` (a probe, built with `-tags probe`) serves synthetic fixtures for browser checks without reading the user's library.

`go test ./...` encountered `TestFindingSnoozeUntilWorseStaysWoken` (“a doubled rate should wake the snooze”). The unchanged baseline reproduced that failure in all three isolated runs. Search changes do not alter that test or findings behavior. The final full run, `go test ./... -skip '^TestFindingSnoozeUntilWorseStaysWoken$'`, passed (archive package: 175.14 seconds).

Full-suite validation also caught an interaction with reference verification: copying the derived metadata table into another catalog could collide with its newly assigned message rowids. Reference snapshots now exclude that table and let message triggers rebuild it; insert triggers also handle SQLite `REPLACE` without depending on delete triggers. The historical-verification regression passed ten consecutive runs after the fix.

## Follow-up scope

This implements the shared lexical core, richer bounded surfaces, and resumable scans from the survey. Cross-message/conversation Boolean units, turn/call-pair/lineage query operators, normalized extraction of embedded reasoning, durable background search jobs, additional path/glob/entity features, and learned retrieval/ranking evaluation remain separate follow-up work. Standalone shared-file reader search and legacy ranked discovery retain their own contracts. The survey's 100-row inventory is still an inventory, not a claim that all operators now exist.

## Shared composer surfaces (2026-10-13)

Home and the menu / ⌘K overlay now edit one local draft with the same message composer used by Library. Opening from Library restores the whole applied query, including workspace OR filters, exclusions, sorts, selected columns, and metrics; closing and reopening retains an unfinished draft. Submission drops the old cursor and page offset. Workspace entry points add a visible workspace predicate that also applies when switching to files or URLs. Clause menus and help portal into the native dialog; per-editor DOM IDs keep focus out of the hidden Home editor.

Conversation Find / ⌘F uses the bounded engine within its visibly fixed conversation scope. It reports matching messages, supports previous/next navigation, reveals collapsed or display-filtered messages, and highlights the engine's returned text range when that retained text is present in rendered prose. It does not run the user's RE2 pattern in JavaScript. Its result ordering is explicitly within the current batch. Standalone shared HTML retains its local reader search.

Human Words uses the same message composer restricted to people-message and classified-span targets. Its table joins the exact returned message IDs, not the workspaces containing those messages. Rows, table metrics, distinct values, and field stats use that batch; separate message-mix charts are hidden while the bounded text search is active. Record predicates are applied inside the batch and do not change its cursor fingerprint. Tool calls, tool summaries, skill uses, and MCP calls share compact field controls and ordering, exposing only their schema's supported predicates. Their inputs are staged until Search.

The read-only Euclid replica checks found that exact broad record predicates are not uniformly interactive. Unbounded Human Words `text contains parser` exceeded 2.5 seconds; a trigram candidate experiment reached the SQL adapter's 1 MiB text-value limit instead. That experiment was removed. The bounded Human Words path now returned 57 token matches in **1,525 ms** and 67 regex matches in **1,524 ms**, including fetching the first 50 table rows. Both truthfully reported incomplete search coverage and provided continuation. Very large messages remain subject to the engine's documented verification limits.

An exact tool-command `contains parser` query took 754 ms in one read and exceeded 2.5 seconds under subsequent load; common `contains test` also exceeded 2.5 seconds. The compact record-search transport therefore requests a **2.5-second deadline** for body-field row and metric queries. An expensive exact query fails visibly with guidance to narrow by workspace, conversation, or date, or use bounded Library search. It is never silently treated as zero matches or truncated into an exact count. The replica predates the tool table's `effort` column, so these tool measurements project the searched field, ID, and ordering field through the production predicate planner; they do not validate current tool enrichment or all-column payload cost.

Validation: composer state round-trip tests, exact-message row/metric tests, cursor/metadata tests, existing message/Library/record tests, TypeScript, build, vet, bundle verification, and desktop/mobile browser checks. Browser checks cover the full Home/overlay/Library handoff, draft persistence, dialog menu focus and Escape, malformed syntax, scoped workspace search, conversation reveal/highlight, Human Words record identity, and Tools/MCP field selection. Screenshots and run logs are in `.context/search-performance/shared-*`.

## Review regression checks (2026-10-13)

Regression tests cover excerpts from later OR alternatives, including a preceding conjunction or exclusion that fails and a match beyond the first 160 bytes. Flat `NOT` and `AND NOT` chains are accumulated into one exclusion group; tests cover 18 exclusions, interspersed positive clauses, explicit nesting, and the existing query limits. Legacy URLs restore fuzzy matching when `fuzzy` is absent while respecting `fuzzy=0` and modern saved drafts; model and Home preview tests cover restoration and outgoing requests.

On the read-only Euclid scratch replica, two passes returned 50-hit pages in **43–897 ms** for a later OR alternative, **436–733 ms** for an OR following a failed conjunction, and **44–45 ms** for 18 distinct exclusions. These are bounded first-page timings, not exhaustive counts. The existing 2.5-second interactive assertion remains in the harness. Search regression tests, frontend model/preview/composer tests, TypeScript, build, vet, and bundle verification pass. Timing output is retained in `.context/search-performance/review-replica-distinct.txt`.

## Cursor review checks (2026-10-13)

The read-only replica reproduced SQL exhausting its budget before yielding any candidate: `test` with a future date bound took **1,501 / 1,504 ms**, and `test` with a nonexistent role took **1,502 / 1,502 ms**. All four pages returned `complete=false`, `time_budget_no_progress`, and no continuation cursor. This prevents retry loops but does not make those restrictive queries complete within the default budget. Other bounded cases stayed within the harness's 2.5-second assertion. Timing output is retained in `.context/search-performance/cursor-review-replica.txt`.

Deterministic SQLite driver hooks test timeouts before the candidate query, before its first row, and after an advancing row, on both initial and resumed pages. Regression tests also cover appends during an active page, unchanged ingest upserts, message edits/deletes/replacements, FTS remapping, classification changes, fuzzy vocabulary changes, and upgrading the original generation triggers. The focused race suite and `go test ./internal/... -skip '^TestFindingSnoozeUntilWorseStaysWoken$' -count=1` pass, as do build, vet, TypeScript, all 34 search/composer/preview frontend tests, and bundle checks. New trigger behavior is validated on synthetic catalogs; the replica remained read-only with its earlier schema.
