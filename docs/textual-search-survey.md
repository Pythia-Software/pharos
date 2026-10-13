# Textual search: survey, current coverage, and implementation plan

Implementation update (2026-10-12): the shared lexical engine, bounded surfaces, Library controls, MCP/HTTP API, and replica measurements are documented in [Text search implementation and performance](performance/text-search.md). The survey below describes the original baseline and remaining design space.

Research date: **2026-10-12**. Code reviewed at `dd79751165462b5a954fb5e2793641f14c684be45`.
This is a design survey, not an implementation. External capabilities are grounded in the linked vendor documentation; Pharos coverage is grounded in the code below. Cost and effort assessments are engineering estimates, not benchmarks of the user's library.

## Recommendation

Build one search model with explicit **text surface**, **matching semantics**, **scope**, and **completeness**, shared by the UI and MCP. Preserve a distinction between ranked discovery and exhaustive evidence search. The largest immediate gains do not require replacing SQLite: expose Boolean expressions, mixed phrases, prefix and proximity search, push filters ahead of candidate limits, and make the existing role/kind and regex capabilities accessible together.

Regex should be admitted according to the work it would perform. A selective regex over indexed commands can be cheaper than a common-word search over all messages. Conversely, “user messages only” can still include enormous pasted logs. Use candidate bytes and index coverage, not the operator's name or a role label alone, to decide whether a query runs interactively, requires a narrower scope, or becomes a resumable background scan.

Three findings change the starting point:

1. **We already have more capability than Library exposes.** MCP `search_messages` has lexical search, mixed quoted phrases, role/kind/date/repository filters, exact result counts, and pagination without a candidate cutoff. Transcript find and query-table text predicates already support regex. The Human Words message table already allows text predicates over classified user messages.
2. **Different search paths produce materially different answers.** Library verifies only its first 5,000 candidate messages and applies table filters afterward. MCP conversation discovery ranks a different bounded lexical population and mixes in similarity. The exhaustive audit tool searches a broader text population. Adding controls without reconciling these differences will preserve confusing failures.
3. **Our “semantic” search is a small local feature model.** It hashes words, character trigrams, and word pairs into 384 dimensions with a hand-written concept map. It is useful infrastructure, but is not a trained language embedding model. Better lexical controls and better semantic retrieval are separate projects.

## 1. Survey of search systems

The following **21 systems** span consumer search, legal research, e-discovery, academic databases, documents, messaging, code, logs, and search engines. The transfer column is a proposed application to Pharos, not a claim that the product has an identical conversation feature. Syntax and limits vary by product; we should borrow capabilities without importing incompatible grammars wholesale.

| System | Documented capabilities worth studying | Transfer to Pharos |
|---|---|---|
| Google Search | Quotes, exclusion, domain, file type, before/after dates, language and content filters. [Google help](https://support.google.com/websearch/answer/2466433) | A simple bar plus visible filter chips; explicit source and date scopes. |
| Bing | Boolean combinations and search keywords for site, extension, linked file types, language, and region. [Operators](https://support.microsoft.com/en-us/bing/advanced-search-options), [keywords](https://support.microsoft.com/en-us/bing/advanced-search-keywords) | Separate “text mentions a URL” from “a tool visited this host”; scoped field filters. |
| Westlaw | Boolean terms/connectors; same sentence/paragraph; ordered and unordered word distance; minimum term frequency. [Connectors](https://www.thomsonreuters.com/en-us/help/westlaw-precision/searching/search-with-terms-and-connectors), [ATLEAST](https://legal.thomsonreuters.com/blog/westlaw-edge-tip-what-is-the-at-least-functional-and-how-do-i-use-it/) | Find a substantive discussion, not a stray mention; preserve order and structural boundaries. |
| LexisNexis / Nexis Uni | Ordered proximity, approximate sentence proximity, Boolean connectors, and frequency commands. [Terms and connectors](https://supportcenter.lexisnexis.com/app/answers/answer_view/a_id/1102765/~/using-terms-%26-connectors-on-nexis-uni) | Explicit distance units; make connector precedence visible. |
| CourtListener | Searchable legal metadata fields, citation lookup, and cited/citing relationships. [Advanced search](https://courtlistener.com/search/advanced-techniques/), [citation lookup](https://www.courtlistener.com/c/), [citation API](https://wiki.free.law/c/courtlistener/help/api/rest/v4/citations) | Exact PR/commit/worktree identity and evidence links; distinguish reference from association. |
| Relativity dtSearch | Proximity, stemming, fuzziness, wildcard and specialized syntax; saved searches and review workflows. Its regex operates on indexed words, an important distinction from whole-body regex. [Syntax](https://help.relativity.com/RelativityaiR/Content/Relativity/dtSearch/Using_dtSearch_syntax_options.htm), [regex semantics](https://help.relativity.com/Server2025/Content/Relativity/Regular_expressions/Using_regular_expressions_with_dtSearch.htm), [searching overview](https://help.relativity.com/Server2025/Content/Relativity/Searching/Searching.htm) | Search-within-results, explainable expansion, saved evidence sets, explicit regex domain. |
| Microsoft Purview eDiscovery | Keyword Boolean/proximity conditions combined with message and document property conditions; keyword reporting. [Condition builder](https://learn.microsoft.com/en-us/purview/edisc-condition-builder) | First-class source/participant/time filtering and per-query hit reports. |
| ProQuest Dialog | Fielded search, exact fields, truncation, unordered NEAR and ordered PRE, compound expressions. [Syntax guide](https://proquest.libguides.com/proquestdialog/searchtips) | Independent field scope, token distance, and exact-value semantics. |
| PubMed | Field tags, controlled vocabulary/term mapping, proximity in supported fields, query translation and search-history combination. [Search help](https://pubmed.ncbi.nlm.nih.gov/help/) | Show expansions and interpreted queries; version domain dictionaries; reusable query sets. |
| Google Drive | Type, owner, title/content and date-oriented advanced search. [Search help](https://support.google.com/drive/answer/2375114) | Search specific surfaces and ownership/provenance metadata. |
| Gmail | Participants, subject, attachments, filename, date, size, labels, and AROUND proximity. [Search operators](https://support.google.com/mail/answer/7190?hl=en) | Combine “who said it,” “when,” and “what kind of content” in one query. |
| Slack | Channel/person/date modifiers, saved/pinned items, result sorting and filtering. [Search help](https://slack.com/help/articles/202528808-Search-in-Slack) | Conversation/agent scopes, saved evidence, nearby-message navigation. |
| Adobe Acrobat | Whole-word/case options, multiple-document and index search, proximity and stemming under specified modes. [PDF search](https://helpx.adobe.com/acrobat/using/searching-pdfs.html) | Scope-dependent capabilities, context navigation, text-extraction coverage. |
| Obsidian | Boolean groups, regex, path/content/properties, case modifiers, line/block/section/task scopes, query explanations. [Search plugin](https://obsidian.md/help/plugins/search) | Same paragraph/code block/turn searches and a readable interpretation of the query. |
| GitHub Code Search | Literal strings, Boolean groups, regex, path globs, content/language and symbol-definition qualifiers; regex excludes look-around. [Syntax](https://docs.github.com/en/search-github/github-code-search/understanding-github-code-search-syntax) | Preserve punctuation in code and commands; separate path from body; specify regex dialect. |
| Sourcegraph | Regex, repository/revision/file scopes, commit/diff/symbol result types, projection, result counts and timeouts. [Query language](https://sourcegraph.com/docs/code-search/queries/language) | Search archived changes, project to conversations or files, expose execution bounds. |
| ripgrep | Literal/regex matching, multiline search, and an optional PCRE2 engine with different performance behavior. [Project README](https://github.com/BurntSushi/ripgrep/blob/master/README.md), [FAQ](https://github.com/BurntSushi/ripgrep/blob/master/FAQ.md) | A fast bounded scan is a valid execution strategy; engine choice changes semantics and cost. |
| Splunk | Regex event filtering and named capture extraction as distinct operations. [regex](https://help.splunk.com/en/splunk-enterprise/spl-search-reference/9.4/search-commands/regex), [rex](https://help.splunk.com/en/splunk-enterprise/search/spl2-search-reference/rex-command/rex-command-overview-syntax-and-usage) | Extract error codes, versions and paths from search results for grouping and comparison. |
| Elasticsearch | Fielded queries, boosts, fuzzy matching, phrase proximity, wildcards and minimum matching clauses; explicit expansion/performance controls. [Query string](https://www.elastic.co/docs/reference/query-languages/query-dsl/query-dsl-query-string-query), [fuzzy query](https://www.elastic.co/docs/reference/query-languages/query-dsl/query-dsl-fuzzy-query) | Structured query trees; separate eligibility from scoring; bounded expansion. |
| PostgreSQL + pg_trgm | Trigram similarity and index-assisted LIKE/regex; patterns with no extractable trigrams can degrade to a full-index scan. [pg_trgm](https://www.postgresql.org/docs/current/pgtrgm.html) | Regex feasibility depends on extractable constraints. This is a design example, not a database-migration recommendation. |
| SQLite FTS5 | Boolean, phrase, prefix, initial-token and NEAR operations; column filters, ranking, snippets, vocabulary and configurable tokenization. [FTS5 reference](https://www.sqlite.org/fts5.html) | Much of the desired lexical foundation is available in the engine already in use. |

## 2. What Pharos supports today

These paths must be assessed separately. “Supported” below never means every interface supports it.

| Code key / surface | Current behavior | Important boundary |
|---|---|---|
| **L — Library text find** | Implicit AND, optional vocabulary-based fuzziness, a whole-query quoted phrase, case option, separator option, conversation hits/snippets; Library table adds structured filtering and grouping by work. | 5,000 candidate messages before text verification; selected rows of kinds `message/thinking/reasoning/analysis` and appropriate roles. No arbitrary Boolean grammar, regex or proximity control. |
| **A — MCP `search_messages`** | AND terms, mixed quoted phrases or whole-query phrase mode; repository/conversation/source/provider/roles/kinds/message-time filters; count plus chronological pages and evidence locators. | Searches indexed `messages.text`, including tool/metadata kinds; no `selected=1` restriction. Operators such as `OR` are deliberately searched as words. No case/literal/regex/fuzzy/proximity controls. |
| **D — MCP conversation discovery** | Ranked conversations, lexical evidence plus title and feature-vector similarity; repository/source/provider/model/effort/file/PR/conversation-time filters. | Lexical path keeps best 500 from newest 25,000 matching FTS rowids before conversation filters. Newest indexed is not necessarily newest message time. Total is a discovery result count. |
| **G — Legacy `/api/search` / table `search=`** | Lexical, mixed phrases, optional substrings and feature similarity; structured work filters; optional explanations. | Similar 500/25,000 bounds; query parsing and result unit differ from L and A. |
| **P — MCP passage search** | Lexical ranked snippets within a named conversation; bounded output. | `ftsQuery` flattens words into AND; no exposed phrase/operator language, regex, role/kind filters or exhaustive continuation. |
| **R — Transcript reader find** | Literal/Go-regex matching, case control, reader event visibility, tool-output handling, shown/hidden hit counts across a work's conversations. | Searches parsed reader events; may scan a large work. Rendered events and stored message text are not the same corpus. |
| **Q — Query tables** | Allowlisted structured fields, AND/OR/negation, text equals/contains/prefix/suffix/regex/not-regex/length, null/set/numeric/time filters, regex extraction for sorting, aggregation and saved views. | Predicates apply to exposed columns, not arbitrary corpus fields. Their existence does not imply indexed text execution. |
| **W — Human Words messages** | Q predicates over classified user-message text; sender, subagent, categories, rules, date, typed/pasted/etc. word counts; highlighted authorship spans. | A category filter selects messages containing that category; text predicates still inspect the full message. “Has typed words” does not mean the matching text was typed. Classification is heuristic. |
| **F/U — File and URL find** | File-path/old-path substring search with attribution; URL/host search over tool evidence excluding `search_result` links. | File search is case-insensitive regardless of the text options; fuzzy/separator toggles are not general path controls. URL find has backend case support, while the Library UI sends the option only for text. |

Code anchors:

- L, F/U: [`conversation_search.go`](../internal/archive/conversation_search.go), especially `findText`, `textMatch`, `findFiles`, `findURLs`; [`library_find_filter.go`](../internal/archive/library_find_filter.go); UI [`index.tsx`](../web/src/index.tsx), `findParameters` and `LibraryPage`.
- A: [`mcp_audit.go`](../internal/archive/mcp_audit.go), `searchMessagesTool`, `auditMessageQuery`, `searchMessages`; [`mcp_audit_test.go`](../internal/archive/mcp_audit_test.go), including the explicit literal-operator test.
- D/P: [`mcp_conversations.go`](../internal/archive/mcp_conversations.go); discovery schemas in [`mcp.go`](../internal/archive/mcp.go).
- G: [`search_explain.go`](../internal/archive/search_explain.go), [`library_search_query.go`](../internal/archive/library_search_query.go), [`catalog.go`](../internal/archive/catalog.go); `ftsQuery` in [`util.go`](../internal/archive/util.go).
- R: [`transcript_find.go`](../internal/archive/transcript_find.go), especially `transcriptFindPattern`, event extraction, and `WorkConversationMatches`.
- Q/W: [`schema.go`](../internal/querytable/schema.go), [`sql_table.go`](../internal/archive/sql_table.go), [`writing_messages.go`](../internal/archive/writing_messages.go), [`writing_messages.schema.json`](../schemas/writing_messages.schema.json), [`human-authorship.md`](human-authorship.md).
- Indexes: [`schema.sql`](../internal/archive/assets/schema.sql), `insertConversationFTS` in [`ingest.go`](../internal/archive/ingest.go), [`substring_index.go`](../internal/archive/substring_index.go), [`tool_search.go`](../internal/archive/tool_search.go).
- Similarity: [`semantic.go`](../internal/archive/semantic.go), particularly `concepts`, `semanticFeatures`, `semanticEmbed` and `vectorScores`.

### Useful capabilities available immediately

For an exhaustive lexical audit of stored user messages, an agent can already call `search_messages` with:

```json
{
  "query": "\"search quality\"",
  "roles": ["user"],
  "kinds": ["message"],
  "from": "2026-01-01",
  "limit": 20,
  "max_output_tokens": 4000
}
```

Continue with the returned `next_offset` until null, retaining the filters. This matches a token phrase within indexed messages, including pasted text in user messages; it does not establish that the words were personally typed or that source history is complete.

The Human Words message table can already combine a `text matches_regex` predicate with repository/date/authorship fields. The tool-call table can combine `command matches_regex` with program, status, repository and time. Reader find handles regex within a work. These are existing paths to expose and unify, although each still needs its own cost/coverage assessment.

### Coverage and semantics problems to fix before adding many controls

**Phrase parsing is inconsistent.** In L, only a query starting and ending with quotes takes the phrase branch. `"cache invalidation" race` becomes three ANDed words; `"cache invalidation" "race condition"` is treated as one token sequence. A already parses mixed phrases correctly. D/P use the simpler `ftsQuery` and do not preserve phrase grouping. A shared grammar would remove this entire class of surprises.

**Filtering after candidate selection can hide legitimate hits.** L's case/separator verification runs after its 5,000-candidate cutoff, and repository/date table predicates narrow the resulting workspace set afterward. D's lexical shortlist is also built before conversation filters. A repository filter cannot recover candidates already discarded. A already places its filters in the match/count query before pagination, though that logical ordering alone does not prove an efficient physical query plan.

**“Exact” is overloaded.** An exact token sequence can match punctuation or whitespace variants. Literal matching means matching the original characters; whole-word matching adds boundaries. L's literal separator verification still begins with a word-index candidate query, and punctuation-only input has no words to search. A's phrases are token phrases. Neither is a general verbatim search mode.

**Fuzzy is more limited than the checkbox suggests.** L fixes the first two letters, uses distance 1 for words of 4–6 runes and 2 for longer words, and keeps up to 24 vocabulary alternatives. It mixes edit distance with suffix heuristics and a special deceive/deception family. Typos in the first two letters can be missed. The final verifier and vocabulary planner need one explicit expansion contract, including Unicode normalization, so valid candidates are neither lost nor mislabeled.

**The substring index is deliberately partial.** `messages_trigram` indexes only `message`, `result`, `delegation`, and `delegation_result`. It does not separately cover `thinking`, `reasoning`, `analysis`, tool calls/results, or metadata. A comment cites about 16 GB of 17 GB being tool/metadata text in one large catalog; that is historical code commentary, not a measurement of this library. The current fallback also ignores terms shorter than three characters.

**Reasoning availability varies by source.** R can extract thinking blocks from a stored JSON message, but this is not equivalent to a stored `kind=thinking`. Some adapters do not retain those events at all; the [Antigravity fixture](../internal/archive/adapter_antigravity_test.go) explicitly checks that model reasoning was not kept. The native [adapter logic](../internal/archive/adapters.go) also distinguishes retained text blocks from other event content. A future self-talk search must report available retained content, distinguish missing from empty, and avoid presenting a kind filter as complete coverage across providers. Content absent from the archive cannot be recovered by adding a query operator.

**Global relevance statistics can be a poor fit for prose.** The word index includes tool text and metadata as well as prose. L filters result rows but uses the shared index's ranking statistics. Separately scoring prose is worth evaluating; an additional index is justified only if retrieval improvement pays for its storage and maintenance.

## 3. Capability matrix: 100 controls and their implementation paths

Status: **Yes** = exists on the named surface; **Partial** = narrower or inconsistent; **No** = no general implementation found in reviewed paths. The code keys above supply the evidence for current status. This is a capability inventory, not a promise to ship all 100.

Effort: **S** = mostly parser/API/UI plumbing after the shared model exists; **M** = substantive planner, verifier or index work; **L** = new extraction, relational semantics, retrieval infrastructure or durable jobs. These are relative sizes, not calendar estimates.

Scope: **I** = plausible across the indexed corpus, subject to selectivity; **B** = verify only budgeted candidate bytes or a bounded collection; **X** = extraction/backfill required; **J** = broad execution may need a background job. A capability can need more than one.

### Matching and Boolean composition

| # | Capability / example | Pharos today | Required support; cost |
|---|---|---|---|
| 01 | All terms: `cache race` | **Yes:** L/A/D/P/G implicit AND. | Unify tokenization and matching unit; preserve existing defaults. **S/I** |
| 02 | Any term: `cache OR buffer` | **Partial:** Q OR over fields; no text-query OR. | Parse a Boolean tree; union postings; cap broad expansions. **S/I** |
| 03 | Exclude term: `cache NOT redis` | **Partial:** Q negation, no corpus-text operator. | Positive candidates minus excluded matches; define message versus conversation exclusion. **S/I** |
| 04 | Nested Boolean groups | **Partial:** Q grouping, absent from main text languages. | Shared parser, documented precedence, parse-error offsets and UI round trip. **M/I** |
| 05 | At least k of n query clauses | **No** general text operator. | Count matching clause IDs per candidate; distinguish this from repeated occurrences. **M/I** |
| 06 | Exact token phrase | **Yes/Partial:** A; L whole query; G mixed phrases; D/P flatten phrases. | Reuse one phrase node everywhere; surface token semantics. **S/I** |
| 07 | Mixed words, multiple phrases and Boolean | **Partial:** A supports mixed AND phrases; L does not. | Compose phrase nodes with words and Boolean nodes. **M/I** |
| 08 | Verbatim string, including punctuation | **Partial:** R; L quoted separator mode subject to word candidates; Q contains. | Literal node with safe substring candidates and original-text verification. **M/I+B** |
| 09 | Whole word versus substring | **Partial:** word search and separate legacy substrings; R can express boundaries. | Explicit boundary rule; do not equate ASCII regex `\b` with multilingual words. **M/I+B** |
| 10 | Case-sensitive or insensitive | **Yes:** L/R; Q regex flags. A lacks a control. | Shared case setting with conservative candidates and offset-safe verification. **S/I+B** |
| 11 | Accent-sensitive/insensitive matching | **Partial:** word-index normalization, no consistent control; verifier differs. | Define folding; align index, query and verifier; strict mode verifies originals. **M/I+B** |
| 12 | Prefix token: `migrat*` | **Partial:** Q field starts-with, no main token-prefix operator. | Add prefix node; benchmark common prefixes; optional dedicated prefix indexes. **S/I** |
| 13 | Suffix token: `*Handler` | **Partial:** Q field ends-with/R regex; no token operator. | Vocabulary expansion or reversed-token index; bound expansion. **M/I+B** |
| 14 | Token wildcard: `colo?r`, `auth*ion` | **Partial:** R/Q regex can approximate; no token glob. | Distinguish one-character from zero-or-more wildcard; bounded vocabulary match. **M/I+B** |
| 15 | Path glob: `src/**/test_*.go` | **No** in F; path substring only. | Path-aware glob parser, slash semantics, narrow path table first. **S/B** |
| 16 | Infix substring: `log_que` | **Partial:** G trigram; Q contains; R literal. | Expose indexed substring mode with per-surface coverage and final verification. **M/I+B** |
| 17 | One/two-character or punctuation-only query | **Partial:** word tokens where applicable; R scans; G drops short substring terms. | Bounded literal scans; optional specialized index only for measured demand. **M/B+J** |
| 18 | Whole-text regex: `timeout.*retry` | **Partial:** R/Q; selected command fields have trigram acceleration. | Shared Go-regex verifier plus conservative prefilter and byte budgets. **M/I+B+J** |
| 19 | Multiline regex, anchors, word boundaries | **Partial:** Go syntax in R/Q; no clear cross-surface controls. | Define per-event/document boundaries and flags; never silently concatenate messages. **M/B** |
| 20 | Look-around/backreferences / PCRE dialect | **No:** current Go regex does not provide them. | Prefer structured NOT/proximity alternatives. PCRE would need isolated resource-limited execution; defer. **L/B+J** |

### Expansion, proximity and local structure

| # | Capability / example | Pharos today | Required support; cost |
|---|---|---|---|
| 21 | Configurable typo distance | **Partial:** L automatic thresholds and fixed-prefix constraint. | Expose max edits, prefix length and expansion limit; report expansions. **M/I** |
| 22 | Adjacent-letter transposition | **Partial:** current Levenshtein counts a swap as two edits. | Optional Damerau-style distance in vocabulary matching and verifier. **M/I** |
| 23 | Spelling suggestions / “did you mean” | **No** user-facing suggestion contract. | Vocabulary frequency and distance ranking; offer alternatives without silently changing exact queries. **M/I** |
| 24 | Stemming and lemmatization | **Partial:** heuristic suffixes in L/G/D similarity. | Versioned language-aware stem/lemma expansion; keep literal mode separate. **M/X+I** |
| 25 | Synonyms, abbreviations and aliases | **Partial:** fixed concept map in similarity; repository aliases for discovery. | Explicit query-time dictionaries, phrases and provenance; configurable on/off. **M/I** |
| 26 | Domain taxonomy / hierarchical subjects | **No** controlled topic vocabulary. | Versioned tags or concept hierarchy; descendants expansion; distinguish inferred labels. **L/X** |
| 27 | Phonetic name matching | **No.** | Optional phonetic keys for a small entity/name table; poor default for code. **M/X+B** |
| 28 | OCR-tolerant matching | **Partial:** ordinary fuzzy words might help retained text. | OCR ingestion if needed; confusion-aware expansion and extraction confidence. **L/X+B** |
| 29 | Semantic / concept similarity | **Partial:** G/D hashed feature similarity. | Evaluate trained local embeddings, chunking and retrieval quality; preserve lexical requirements. **L/X** |
| 30 | Query language / multilingual analysis | **Partial:** Unicode word indexing; parsers differ; similarity is ASCII-oriented. | Language metadata, compatible tokenization and benchmark cases, including CJK. **L/X** |
| 31 | Unordered token proximity: `NEAR(cache race, 8)` | **No** exposed operator. | Compile suitable proximity nodes to the existing positional engine. Define distance exactly. **S/I** |
| 32 | Ordered distance: cache before invalidation | **No** general operator; R regex is a rough workaround. | Positional candidates plus ordered verifier; do not equate character regex with token gaps. **M/I+B** |
| 33 | Phrase slop / intervening words | **No** configurable phrase gap. | Ordered span matching with documented gaps/transposition policy. **M/I+B** |
| 34 | Same sentence | **No** linguistic boundary operator. | Candidate terms plus sentence spans; handle abbreviations, code and logs explicitly. **M/B+X** |
| 35 | Same paragraph | **No.** | Paragraph-span verification; preserve original boundaries through extraction. **M/B** |
| 36 | Same line | **Partial:** R/Q regex can constrain newlines. | First-class line scope and line positions; no new corpus-wide index initially. **S/B** |
| 37 | Same Markdown section/block/task | **Partial:** authorship classifies some code, R parses events. | Derived Markdown spans/headings/task state with offsets. **L/X+B** |
| 38 | Negative proximity: A with no nearby B | **No.** | Search A candidates, inspect bounded neighborhoods for B; scope negation locally. **M/I+B** |
| 39 | Minimum/maximum occurrence count | **Partial:** hit counts exist; no term-frequency predicate. | Count occurrences under defined overlap/token rules; avoid confusing message count with occurrence count. **M/I+B** |
| 40 | Density / proportion of text discussing a term | **No** general text-density filter. | Match counts plus token/span denominator; long logs need explicit interpretation. **M/B** |

### Searchable surfaces and metadata scopes

| # | Capability / example | Pharos today | Required support; cost |
|---|---|---|---|
| 41 | User versus assistant versus tool/system | **Yes:** A roles; W user-message subset; L combines prose roles. | Shared role filter in Library and all search APIs; role is not authorship. **S/I** |
| 42 | Retained thinking/self-talk only | **Partial:** A kinds and R thinking; source representations differ. | Canonical retained event surfaces; coverage metadata; optionally selective trigram index. **L/X+B** |
| 43 | Assistant final answer versus interim commentary | **Partial:** R reader categories; no general normalized channel search. | Preserve source channels or mark inferred classification; index channel metadata. **M/X** |
| 44 | Human-typed text only | **Partial:** W categories/spans; text predicate covers whole message. | Search individual classified typed spans; do not join disjoint spans into false phrases. **M/X+B** |
| 45 | Pasted, quoted, re-sent, template or harness text | **Partial:** W selects messages by category/rule. | Span-scoped text matching; include classifier version and attribution links. **M/X+B** |
| 46 | Delegation prompt / agent-to-agent communication | **Partial:** A delegation kinds and sender/lineage metadata. | Canonical surface plus sender/receiver IDs, including embedded delegation content. **M/X+B** |
| 47 | Tool name/server/program/command | **Yes:** Q tool-call fields and command trigram index. | Expose same filters within text search; join by tool-call identity. **S/I** |
| 48 | Tool input versus output | **Partial:** A kinds/R events; no common body-surface search. | Extract searchable input/output parts linked to calls; large outputs remain budgeted. **M/X+B+J** |
| 49 | Structured JSON field values | **Partial:** normalized Q fields; no arbitrary JSON path search. | Allowlisted JSON paths and typed extraction, materialize frequently queried fields. **M/X+B** |
| 50 | Title, purpose, summary or outcome only | **Partial:** Q work fields/D title boost; no unified full-text field scope. | Small field index plus explicit field selector and field-local provenance. **M/X+I** |
| 51 | Repository/project/workspace | **Yes:** A/D/G/Q; L table filters after match generation. | Resolve identity, push filters before caps and expose exact versus contains. **M/I** |
| 52 | Provider/source/host/account | **Partial:** source/provider widely supported; host/account not uniform. | Shared typed scope schema and selective metadata indexes. **M/I** |
| 53 | Model and effort | **Yes:** D/G/Q in relevant datasets; absent from A. | Filter on the same recorded message configuration, not unrelated models in its workspace. **M/I** |
| 54 | Message time, conversation start, activity or ingestion time | **Partial:** A message dates; D conversation dates; Q other timestamps. | Named time fields, timezone, inclusive/exclusive bounds and missing-time policy. **M/I** |
| 55 | Root/subagent/depth/descendants | **Partial:** Q and stored lineage; not a universal text scope. | Indexed lineage traversal or materialized root ID; limit descendant expansion. **M/I** |
| 56 | Selected branch versus every retained message | **Partial:** L selected-only; A all indexed; inconsistent exposure. | Explicit branch/selection policy and duplicate attribution. **M/I** |
| 57 | Coverage, stale source, missing reasoning | **Partial:** A/D freshness/coverage output and status tool. | Filterable coverage fields and source/surface completeness; unknown is not false. **M/I** |
| 58 | Changed/read/written/mentioned file | **Partial:** F changed paths, Q tool paths; mentions are text. | Separate relations and scopes; preserve workspace-only attribution where uncertain. **M/X+I** |
| 59 | URL host/path/query; opened versus returned versus mentioned | **Partial:** U excludes search-result links, Q URL/host fields. | Structured URL parts and explicit relation; normalize host separately from case-sensitive paths. **M/X+I** |
| 60 | Extension/MIME/document type | **Partial:** path suffix text filters; no general attachment-content corpus. | Materialize known types; add extraction only for explicitly indexed artifacts. **M/X** |
| 61 | Has attachment/link/code block/tool error | **Partial:** relevant Q fields, authorship labels and reader parsing. | Shared existence predicates over normalized parts/relations. **M/X+I** |
| 62 | Character/byte/token length and numeric ranges | **Yes/Partial:** Q character length, metrics and byte counts in certain datasets. | Expose units distinctly; persist text byte counts for planning. **S/I** |
| 63 | Raw source versus extracted/displayed text | **Partial:** stored `text`, `raw_text`, reader events; no unified selection. | Label representations; retain JSON/source-path mapping; avoid duplicate hits. **L/X** |
| 64 | Missing/null/empty metadata or text | **Yes:** Q null/length/set semantics; no shared text-search contract. | Specify absent, empty and unavailable separately; scope negative-only queries. **S/I+B** |

### Relationships, aggregation and evidence sets

| # | Capability / example | Pharos today | Required support; cost |
|---|---|---|---|
| 65 | Same event/message versus same conversation | **Partial:** terms generally co-occur in one indexed message; results group later. | Explicit matching unit; intersect conversation IDs for cross-message AND. **M/I** |
| 66 | Same user turn (prompt through response) | **Partial:** reader constructs turns; not searchable as a relational unit. | Stable turn IDs and event membership; search/join within turn. **M/X+I** |
| 67 | A followed by B within n messages/turns | **No** general sequence query. | Candidate ID sets, source-order range join and deterministic tie handling. **L/I+B** |
| 68 | A then B within elapsed time | **No** general temporal text join. | Timestamp range joins; explicit handling of absent/skewed timestamps. **L/I+B** |
| 69 | Tool input A with its own output B | **Partial:** tool call/result pairing exists, no joint text grammar. | Join matches through call/result IDs; never pair unrelated messages by proximity. **M/I+B** |
| 70 | Parent request with child-agent response | **Partial:** lineage and delegation evidence exist. | Traverse actual delegation edges; return both evidence handles and ambiguity. **L/I+B** |
| 71 | Mentions/cites exact PR, commit, worktree or message | **Partial:** `trace`, `trace_worktree`, PR links and file evidence. | Typed entity IDs plus mention index; separate references from verified ownership/outcome. **M/X+I** |
| 72 | Exact duplicates / mirrored conversations | **Partial:** hashes, identity links and D mirror collapse. | User-selectable deduplication unit; retain all origins and raw occurrence counts. **M/I** |
| 73 | Near duplicates / copied passages | **Partial:** authorship quoted/re-sent detection; no general duplicate-search tool. | Candidate fingerprints then similarity verification on small surfaces. **L/X+B** |
| 74 | Added/removed text, revisions and change-only search | **Partial:** change sets exist; no general text-diff query. | Index retained diff hunks with side/revision/path; do not imply full repository history. **L/X** |
| 75 | First/last mention; novelty since checkpoint | **Partial:** A chronological pages can be inspected; no dedicated operation. | Indexed time/order with direct first/last projection; explicit snapshot baseline. **M/I** |
| 76 | Regex captures as columns/groups | **Partial:** Q regex-extracted sorting; no full capture-result interface. | Typed capture projection with offsets; null/nonmatch semantics and aggregate support. **M/B** |
| 77 | Facet counts over text hits | **Partial:** Library/Q metrics over selected works. | Exact/lower-bound counts for matched messages, conversations and workspaces separately. **M/I+J** |
| 78 | Concordance / every occurrence in context | **Partial:** R navigation; A/L short snippets, not a global occurrence list. | Occurrence IDs/spans, bounded context windows and paging inside long documents. **M/B** |
| 79 | Search within a prior result set | **Partial:** query-table refinement over matched works. | Named immutable result IDs or a composed query; expose snapshot versus live semantics. **M/I+B** |
| 80 | Compare query sets: intersection/difference/overlap | **Partial:** Q Boolean conditions, no saved text-set algebra. | ID-set operations and overlap counts; useful for testing revised search expressions. **M/I+J** |

### Ranking, diagnostics and workflow

| # | Capability / example | Pharos today | Required support; cost |
|---|---|---|---|
| 81 | Lexical-only versus similar versus hybrid | **Partial:** chosen indirectly by endpoint. | Explicit retrieval mode; strict clauses constrain every mode; show similarity-only evidence. **M/I** |
| 82 | User-controlled field/term boosts | **No** unified control; current scores are fixed. | Scoring nodes separate from required filters; evaluate retrieval quality. **M/I** |
| 83 | Sort by relevance/date/frequency/length | **Partial:** Q sorts; A fixed chronological; L fixed ranking. | Shared sort schema and stable tie-breaker; frequency sort may need full evaluation. **M/I+J** |
| 84 | Snippet size, multiple snippets, match highlighting | **Partial:** fixed snippets and reader find. | Return original-text match spans with explicit offset units; optional bounded surrounding context. **M/B** |
| 85 | Group/project by occurrence/message/turn/conversation/work/file | **Partial:** separate fixed result units in L/A/D/Q. | Match once, project explicitly; counts and best-hit selection use the chosen unit. **M/I** |
| 86 | Explain interpretation, expansions and ranking | **Partial:** G `explain=1`; D relevance reasons. | Shared normalized query, expanded terms, scope, matched clauses and score components. **M/I** |
| 87 | Exhaustive versus top-k results and count accuracy | **Partial:** A exhaustive indexed matches; L limited flag; D ranked totals. | `exact/lower_bound/unknown`, stop reason, index coverage and snapshot; never imply no matches after a cutoff. **M/I+J** |
| 88 | Stable cursor pagination across updates | **Partial:** offsets and per-call transaction in A. | Keyset cursor plus catalog generation; snapshot/result job for stable audits. **M/I+J** |
| 89 | Time/byte/candidate limits and cancellation | **Partial:** request contexts, hard caps and output budgets; no shared work budget. | Budget every stage including vocabulary, verification, parsing, sorting and counts. **M/I+B** |
| 90 | Resumable exhaustive scan / export job | **No** general text-search job found. | Durable cursor/checkpoints, read consistency, progress, cancellation and result expiry. **L/J** |
| 91 | Query autocomplete and field/operator discovery | **Partial:** Q schemas/distinct values; no text capability endpoint. | Scope-aware fields/operators, vocabulary suggestions and estimated cost class. **M/I** |
| 92 | Parse-error diagnostics and query preview | **Partial:** A quote/mode validation; R regex errors. | Character-positioned errors, visible query tree, escape assistance; no silent operator reinterpretation. **M/I** |
| 93 | Saved searches / named views | **Yes/Partial:** Q saved views; not a unified text-query object. | Persist full versioned search specification including scopes and mode. **S/I** |
| 94 | Alerts for newly matching evidence | **No** general saved-text-search alerts found. | Incremental evaluation after indexing with deduplication and backfill policy; user opt-in. **L/J** |
| 95 | Export reproducible evidence results | **Partial:** standalone conversation sharing; not an audit search manifest. | Export query, generation, coverage, locators and matched spans; independent output limits. **M/J** |
| 96 | Query history, rerun and result-count comparison | **Partial:** MCP call history and saved views. | Common search history with exact specs and generations; compare like-for-like counts. **M/I** |
| 97 | Cross-interface parity | **No:** substantial divergence documented above. | One typed AST/planner/evidence model for UI, HTTP, MCP and compatible offline search. **L/I** |
| 98 | Relevance feedback and pinned exemplars | **No** general text-search feedback loop. | Record useful/irrelevant judgments and exemplars; evaluate before personalization. **L/X** |
| 99 | Query-by-example / similar to this passage | **Partial:** D/G free-text similarity could approximate; no message-handle interface. | Resolve chosen passage, retrieve neighbors, exclude self/copies and return grounded evidence. **M/X** |
| 100 | Answer a question with cited search evidence | **Partial:** agents can orchestrate search and reads; no dedicated retrieval contract. | Better retrieval plus bounded evidence bundles; keep answer generation separate from match/count guarantees. **L/I+B** |

## 4. Which surfaces can afford which queries?

We should inventory **bytes, row counts, largest row, vocabulary size and index coverage per surface** before assigning production thresholds. Reasoning may be smaller than tool output, but it is not necessarily small; user-role text can contain pasted megabytes. Authorship classification may also be pending or stale.

| Surface | Current useful infrastructure | Sensible initial policy | Work needed |
|---|---|---|---|
| Titles, purpose, summaries, labels | Structured work/conversation data and Q | Broad exact/contains/glob/regex with byte bound; indexed terms for scale | Small field index if measurements warrant it; consistent field semantics |
| Paths, hosts, URLs, entity identifiers | `change_files`, `tool_urls`, `tool_calls`, repository/PR/identity metadata | Exact/range/prefix first; bounded glob/regex over narrow fields | Appropriate normalized columns and indexes; path/URL-specific verification |
| Shell commands/program/subcommand | `tool_command_fts` and authoritative Q predicates | Selective regex globally; broad patterns require metadata scope or job | Better literal extraction, statistics, exposed planner and limits |
| Classified human-typed spans | `message_authorship_spans` offsets and categories | Rich matching over typed spans; small bounded scans first | Span-aware candidate verification or derived span index; classifier freshness |
| Whole user messages | Word index, prose trigram coverage, W dataset | Indexed text broadly; regex after narrowing or a measured bounded scan | Surface metadata indexes; byte stats; avoid mixing pasted content into typed results |
| Assistant visible prose | Word index and relevant prose trigrams | Same as user prose; term/proximity before expensive verification | Consistent extraction, channel labels and duplicate handling |
| Retained reasoning/self-talk | Some stored kinds, reader block parsing | Indexed words where retained; regex only on actual extracted surface bytes | Source coverage audit, canonical parts, optional trigram backfill by measured demand |
| Delegation prompts/results | Dedicated kinds, lineage; some trigram coverage | Rich matching with sender/receiver and parent/child scopes | Typed events and stable relationship joins |
| Tool input and structured result fields | Call ledger and normalized attributes | Commands/error fields broadly; other fields with narrow predicates | Allowlisted projections; index the frequently used ones |
| Full tool outputs and raw metadata | Broad word index; no general body trigram index | Indexed words/phrases/proximity; regex on proved candidates or a background scan | Measured optional chunk indexes; storage budget, progress and cancellation |
| One conversation, work or selected result set | Conversation index, P bounds, R parser | Rich local queries if actual bytes fit the budget | Don't assume one workspace is small; keyset scanning and progress for outliers |
| Attachment/PDF/document bodies | Some references/captions and captured text; no universal extraction service | Search only explicitly extracted, retained content | New extraction/OCR pipeline, version/coverage metadata and source locators |

Do not extend `messages_trigram` to every tool output by default. Prefer the existing command index, narrow structured fields, canonical prose/typed spans, and candidate verification. If measurements later justify a large-output index, consider opt-in per source, bounded chunks, or a separate code/text index with an explicit disk budget. Chunk boundaries need overlap or adjacent-chunk verification for finite-span queries; arbitrary unbounded regex cannot be made exhaustive by choosing one fixed overlap.

### Regex admission and planning

Keep the existing Go engine as the first dialect. It guarantees matching time linear in input size for a fixed expression, but that does not make scanning tens of gigabytes interactive. [Go regexp documentation](https://pkg.go.dev/regexp) Compilation, row fetches, decoding, allocation and result enumeration also consume time.

| Query shape | Candidate strategy | Interactive admission |
|---|---|---|
| `timeout.*retry` on commands | Mandatory `timeout`/`retry` literals through the command trigram index, then regex verification | Usually promising; use measured selectivity |
| `(?:timeout\|deadline).*retry` | `(timeout OR deadline) AND retry` candidates, if all branches are safely represented | Admit if selective; current helper is more conservative |
| `(?i)HTTP.*500` | Case-fold-compatible necessary literals, otherwise no prefilter | Only after proving index/verifier normalization cannot lose matches |
| `^.{0,20}$` on typed spans | Surface metadata and length candidates; inspect span text | Bounded scan or job depending on bytes |
| `\b[A-F0-9]{40}\b` on all tool output | No useful mandatory literal | Ask the user to narrow via actionable UI or offer a background scan |
| `foo\|[0-9]+` on all text | One branch has no mandatory literal; filtering only on `foo` is unsound | Bounded scan/job unless another conjunct narrows it |
| `NOT /error/` over all messages | Complement over an explicitly defined universe | Require a scope and appropriate budget; output limit does not bound work |

The existing `regexRequiredLiteral` helper extracts conservative literals from concatenation, capture and required repetition. It avoids case-folded literals and many other forms; command acceleration requires at least three ASCII runes. Reuse that implementation as a starting point, not as a complete regex planner. The safety property is **candidate superset**: prefiltering must never remove a true match. Alternation, optional repeats, Unicode folding and negative predicates need special care. An index still being backfilled cannot establish absence in its unindexed population.

Use a budget based on **candidate bytes** as well as rows. Proposed starting values for benchmarking, not shipping commitments: roughly 1–2 seconds and 16–64 MiB of verified text for an interactive scan, adjusted for hardware and cold external drives. Bound query size, expanded terms, regex compilation complexity, maximum individual input, occurrence enumeration and concurrency too. A small response `limit` or MCP token budget does not constrain count queries, sorts, or nonmatching scans.

On budget exhaustion, return partial results with a stop reason and continuation/job option. Never discard the remaining candidates and return an apparently exact zero. Go regex calls do not accept a context internally, so check cancellation between bounded units and handle giant individual messages explicitly. Splitting text requires preservation of the query's boundary semantics.

## 5. A shared implementation model

### Query contract

Keep convenient syntax for humans and a typed JSON tree for tools. Both should compile into the same internal representation. Begin with explicit operators rather than exposing raw SQL or raw FTS syntax.

Illustrative **proposed** request, not a current API:

```json
{
  "version": 1,
  "surface": ["user_typed"],
  "scope": {"repository_ids": ["repo-id"], "message_time": {"from": "2026-01-01"}},
  "where": {
    "all": [
      {"near": {"terms": ["search", "quality"], "max_gap": 8, "ordered": false}},
      {"not": {"phrase": "example only"}}
    ]
  },
  "match_unit": "span",
  "return_unit": "message",
  "retrieval": "lexical",
  "sort": [{"field": "message_time", "direction": "desc"}],
  "completeness": "exhaustive",
  "execution": {"max_verified_bytes": 33554432, "timeout_ms": 1500},
  "page": {"limit": 20, "cursor": null}
}
```

`completeness=exhaustive` requests that contract; it cannot override a work budget. The service must either finish, return an explicit incomplete/resumable result, or reject the plan with a useful narrowing suggestion.

Make units unambiguous. `message:(A AND B)` requires one message containing both; `conversation:(A AND B)` can use two different messages. `conversation:(A NOT B)` excludes a conversation with B anywhere in the eligible scope; `conversation:(message:(A NOT B))` merely needs an A-message without B. A role constraint can apply to the entire eligible universe or to an individual clause; show the chosen interpretation.

Use distinct operators for token phrase, literal string, regex and semantic similarity. Quotes should have one stable documented meaning across surfaces; a verbatim mode can be a separate control. Unicode normalization and punctuation rules must be versioned. Query expansion must be visible, and strict predicates must constrain semantic results as well as lexical ones.

### Index and extraction changes

1. **Create a shared search package first.** Reuse A's exhaustive lexical path, L's fuzzy expansion, R's extraction and Q's predicates behind one query model. Maintain compatibility adapters for existing callers; do not silently change `OR` from a literal in old tool requests to an operator.
2. **Add cheap searchable-unit metadata.** Track byte length, surface, role, kind, conversation/workspace/root IDs, source timestamp, selection state and extraction version. Evaluate composite/partial indexes with real query plans. The existing `messages_prose_idx` and comments already document a case where an apparently useful role index causes a workspace query to scan too much.
3. **Normalize searchable parts where needed.** A derived `search_parts` relation can hold `(part_id, message_id, source_path, surface, role/channel, turn_id, text, extraction_version)`. Raw JSON needs source paths and decoded-text offsets; authorship uses original byte spans. Do not assume they share one offset system. Avoid duplicating large text when references suffice.
4. **Keep positional word search as the broad baseline.** Add richer parsing and verification before building new corpus-wide indexes. The current word index stores one indexed `text` field and an unindexed message ID; field-scoped search needs metadata joins, separate parts, or new field indexes.
5. **Add selective substring indexes only with evidence.** Commands already have one. Measure typed spans and retained reasoning before adding them. A partial surface index needs a manifest saying exactly which sources, extraction versions and generations it covers.
6. **Extract expensive structure incrementally.** Persist turn/paragraph/code/task/entity data only when query demand justifies it. Reuse reader extraction logic to avoid a second definition of the transcript, with fixtures proving that indexed text maps back to displayed evidence.
7. **Treat learned retrieval separately.** A local embedding pipeline needs chunk definitions, model/version tracking, deletion/backfill handling and evaluation. Approximate nearest-neighbor retrieval may later help scale, but current flat scoring should be measured first. It cannot supply exhaustive lexical counts or prove absence.

### Execution and results

Plan in this logical order: resolve scope and eligible surfaces → generate conservative indexed candidates → apply required relational/metadata restrictions before any correctness-affecting cap → verify text/structure → rank or enumerate → group/project → page → construct snippets. SQLite may reorder physical operations; use `EXPLAIN QUERY PLAN` and measurements to decide whether joins, rowid ranges or materialized candidate IDs are appropriate. Syntax order is not an execution guarantee.

Ranked discovery can have a documented recall budget. Exhaustive mode needs complete candidate generation and verification or an explicit incomplete outcome. Broad exact counts/facets can dominate latency; support `count=none`, an exact on-demand count, or a background exact count. Separate result-page size from search work and from rendering/output-token budgets.

Suggested response metadata:

```json
{
  "interpreted_query": "...",
  "surface": ["user_typed"],
  "match_unit": "span",
  "return_unit": "message",
  "catalog_generation": 123,
  "items": [],
  "next_cursor": null,
  "count": {"value": 0, "relation": "unknown", "unit": "message"},
  "completion": {"complete": false, "reason": "byte_budget"},
  "coverage": {"indexed_surface_complete": true, "source_history_complete": "unknown"},
  "execution": {"strategy": "trigram_then_verify", "verified_bytes": 33554432},
  "expansions": []
}
```

Each result should identify source representation, message/part, matching clause, original evidence locator, timestamp, and exact match spans. Return an occurrence count separately from matched-message and conversation counts. Budget-limited counts are lower bounds only if the implementation has actually counted distinct verified matches; otherwise mark them unknown. Content/index freshness, extraction completeness, execution completeness and output clipping are separate facts.

For interactive pages, a keyset cursor plus generation can invalidate on changes. For reproducible audits, use materialized result IDs or a pinned source snapshot and retention policy; holding an SQLite read transaction indefinitely can interfere with WAL cleanup. A snapshot of IDs alone does not preserve evidence text that later changes, so retain content hashes/versions or an export manifest.

### UI and MCP

Use one search bar with an advanced builder. Provide visible chips for **where to search**, **who/content type**, **time**, **matching mode**, and **result unit**. An expandable panel can hold case, normalization, proximity, regex and completeness. Show active expansions and an interpretation preview. Offer narrowing suggestions based on the plan, such as “tool outputs in this conversation” or “commands in this repository,” rather than a generic query-too-expensive message.

Expose a shared capability description to MCP: supported nodes, fields, surfaces, coverage, dialect, and budgets. Keep task-oriented tools if useful, but make them wrappers around the same query semantics. Agents should be able to request exact matches, exhaust pages, and distinguish “not found” from “not fully searched” without trying several unrelated endpoints.

## 6. Recommended sequence

| Stage | Concrete deliverable | Main code areas | Completion criteria |
|---|---|---|---|
| **1 — Consistent exact search** | Shared query model; Library/MCP parity for roles, kinds, message dates, mixed phrases, Boolean groups, explicit result units and completeness. Fix filtering before caps. | `mcp_audit.go`, `conversation_search.go`, `library_find_filter.go`, query-table integration, `web/src/index.tsx`, `mcp.go` | A rare repository match survives thousands of unrelated common-word hits; equivalent UI/tool requests return the same evidence IDs. |
| **2 — More lexical power** | Prefix, unordered proximity, ordered verification, literal/case modes, visible fuzzy/synonym expansion and diagnostics. | Shared parser/planner; existing word/vocabulary/trigram indexes; original-text verifier | Phrase/punctuation/Unicode fixtures agree; unsupported syntax errors clearly; exhaustive mode loses no true matches. |
| **3 — Rich bounded surfaces** | Typed-span, retained-reasoning, delegation and command/output scopes; regex with admission budgets, captures and progress. | `writing_messages.go`, authorship spans, reader extraction, `tool_search.go`, schema/migrations | Planner proves a candidate superset; scanned bytes stay bounded; missing/backfilling surface coverage is explicit. |
| **4 — Relational evidence search** | Same-turn/sequence/call-pair/parent-child search; facets, concordance, reusable query sets and resumable scans. | Search-part/turn relations, call ledger, lineage, result job storage | Relationships are evidence-backed, large scans resume safely, counts have clear units and snapshot semantics. |
| **5 — Retrieval-quality research** | Judged query set; field/ranking experiments; optionally local trained embeddings and query-by-example. | `semantic.go`, conversation documents, ranking layer | Demonstrated precision/recall gains on held-out questions with acceptable index size and latency. |

Stages 1–2 are the strongest near-term investment. Regex on commands/user text can be surfaced sooner through existing Q/W/R behavior, but it should not be advertised as a unified global regex engine until its scope and completeness are explicit. Do not begin with a blanket trigram index, a new database service, or an LLM rewrite of every query.

## 7. Concrete queries the design should make possible

These are proposed human-readable examples; they are not accepted syntax today. They are also useful acceptance scenarios.

| User question | Proposed expression / operation | Why the scope matters |
|---|---|---|
| Where did I personally ask for a search overhaul? | `surface:user_typed (search NEAR/8 (quality OR inadequate))` | Excludes pasted agent prose and harness text; return original span and message. |
| Where did an agent discuss retries while thinking? | `surface:reasoning (timeout PRE/12 retry)` | Search only retained extracted reasoning; list sources without that surface. |
| Which commands run tests but skip race testing? | `surface:command /\bgo\s+test\b/ NOT /(?:^\|\s)-race(?:\s\|$)/` | Structured command candidates; Go regex syntax; no need to scan output bodies. |
| Where did I use the exact identifier `foo::bar`? | `surface:user_message literal:"foo::bar"` | Preserve punctuation; token phrases alone cannot establish a literal match. |
| Which conversations discuss both migration and rollback, in different messages if necessary? | `conversation:(migration AND rollback)` | Cross-message intersection, rather than the current same-message conjunction. |
| Which tool call received permission denied after writing a file? | `call:(tool:write_file AND output:"permission denied")` | Join to that call's result, not any error elsewhere in the conversation. |
| Which sessions had a test failure followed by success within five turns? | `sequence(tool.status:error, tool.status:ok, program:go, subcommand:test, max_turns:5)` | Requires actual call status and ordering; success text alone is not proof. |
| Find a hash-shaped string in one retained output | `conversation:ID surface:tool_output /\b[0-9a-f]{40}\b/` | Useful regex without mandatory literals is admitted only when the selected bytes fit. |
| Where did agents use a source, versus just receive a search result for it? | `url.host:sqlite.org relation:opened` | Use recorded URL provenance, not generic text matching. |
| Find repeated discussion, not one mention | `surface:assistant_text occurrences(cache)>=5` | Frequency unit must be occurrences in the selected unit, not matching messages. |
| What new matching evidence appeared after my last audit? | `saved_search:search-quality since_generation:123` | Stable baseline, source timestamps versus ingestion timestamps, mirror handling. |

## 8. Validation and remaining measurements

The following existing focused tests passed during this survey:

```sh
go test ./internal/archive -run 'TestLibraryFind|TestMCPSearchMessages|TestToolCommandSearchIndex' -count=1
go test ./internal/archive -run 'TestTranscriptFindMatchesReaderFixtures|TestWorkConversationMatches|TestWritingMessagesFilterBySpanAndSplitText' -count=1
```

They validate existing behavior, including lexical filters/phrases/pagination, current Library fuzziness, command indexing, reader agreement and authorship spans. They do not establish that the proposed features are implemented or that production-scale latency is acceptable. No production database was benchmarked or migrated.

Before implementation decisions about additional indexes, collect:

- Per-surface text bytes, row counts, maximum/p95 row sizes, distinct terms and overlap between raw and extracted representations. Use stored/incremental statistics where possible; an initial census is itself a potentially expensive task.
- Actual source-by-source reasoning/response/metadata retention and extraction coverage, including unselected messages, mirrored sources and pending authorship/index backfills.
- Warm and cold latency on internal and external drives; p50/p95, candidate and verified bytes, memory, index growth, ingest/backfill throughput, and cancellation latency. Include simultaneous indexing and searches.
- Selectivity curves for common/rare terms, one/two-letter prefixes, leading wildcards, regex with/without mandatory literals, giant single outputs, pure negatives, and common words inside rare repository scopes.
- A labeled set of real questions with expected evidence. Evaluate precision@k, recall over known fixtures, ranking quality and false-zero rate independently; do not use a relevance score as proof of correctness.

Implementation tests should include parser precedence/escaping, mixed phrases, Unicode and punctuation alignment, positive/negative proximity, cross-message versus same-message semantics, typed-span boundaries, unavailable reasoning, case verification after many candidates, early-filter correctness, exhaustive pagination during updates, mirror attribution, and regex candidate-superset properties. Use a bounded brute-force reference on generated fixtures to validate each optimized plan. Large-corpus performance tests should verify work actually avoided, not just a short returned page.

The design target is that a user or agent can state what text they mean, where to look, which relationships matter, and whether they need the best few leads or every retained match—and receive results whose limits and evidence are understandable.
