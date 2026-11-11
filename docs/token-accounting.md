# Token accounting and source audit

The September 23, 2026 audit read the configured raw sources, not only the Pharos catalog. It scanned 4,100 Codex JSONL files (1,390,171 records), 1,579 Claude files (413,012 records), 6,708 TL1 transcript files (274,495 records), and Conductor's SQLite message columns (1,648,674 JSON objects in `content`). Counts are an audit snapshot; active sources continue changing. Three available non-temporary TL1 databases were also inspected for token columns: transcript summary tables retain cost, but the token evidence is in the transcripts. No ChatGPT or canonical export is configured locally; those paths are covered with synthetic tests.

## Why information was missing

| Source/layer | Lost or misinterpreted information | Correction |
| --- | --- | --- |
| Codex native | 83,242 `token_usage_record` events were skipped. They contain request usage, response/turn/thread IDs, and turn/thread totals. Compaction records can carry the last accounting checkpoint. | Preserve these events and their complete envelopes. Prefer response identities over duplicate legacy snapshots; retain missing-prefix usage as unattributed checkpoint remainder. |
| Claude native | 176,068 assistant envelopes carry usage. Repeated message IDs and streaming updates could be counted repeatedly; non-assistant SDK results were discarded. | Preserve envelopes, merge partial usage by request ID, retain SDK results and streaming usage. |
| Conductor | Block extraction stripped `message.usage` from 443,333 usage-bearing assistant envelopes. Only terminal results contributed to imported metrics. Shorter alternate message columns could contain richer accounting than the chosen display column. | Retain original accounting alongside extracted display text and retain alternate accounting columns. |
| Conductor subagents | 51,469 task progress reports were not interpreted as cumulative task counters. | Reconcile the latest task checkpoint against retained child request usage using tool/task identities. |
| Conductor result summaries | The viewer deliberately discarded result usage. `modelUsage` uses camelCase and can be cumulative across resumed runs, unlike top-level per-run `usage`. | Retain both. Difference per-model counters by session/model, and reconcile aggregate remainder against request evidence. Counter decreases begin a new accounting epoch. |
| TL1 | Transcripts include Claude SDK results, nested cache details, context estimates, and 398 Codex `turn.completed` usage events. The old native parsers discarded several of these. | Reuse the corrected native parsers and derive workspace totals from retained transcript accounting. |
| Canonical/ChatGPT exports | Extracting display text discarded structured metadata/usage when present. | Preserve accounting envelopes. Never infer model consumption from visible text length or an ambiguous metadata token count. |
| Catalog/API | Messages sharing a timestamp were ordered by hashed IDs, scrambling accounting checkpoints. | Store source order and raw accounting independently of display-text hashes; expose both and reconciled conversation usage through detail/API/MCP reads. |
| Library/Health | Claude cache reads and writes were omitted from the total. | Store a reconciled `total_tokens`; canonical input includes cache, while cache/reasoning remain separately available subsets. |

## Accounting contract

* Request IDs identify repeated reports, not additional requests. Streaming fields update earlier fields; missing fields do not erase previously reported values. The final merged request contributes once.
* Codex response records take precedence over legacy `token_count` snapshots in the same retained conversation. Thread checkpoints fill missing prefix usage, except in a rollout whose `session_meta` says it continues another thread (`forked_from_id`, `history_base`, or a nonzero `subagent_history_start_ordinal`: a fork, forked subagent or revert page). Its counters start at that thread's usage, which the other thread's rollout counts, so the starting value is excluded. Legacy counter decreases retain the last reported request rather than treating a lower context counter as additional full-history spend.
* Run/task/session summaries are evidence, not extra requests. Only their positive remainder beyond already attributed usage is added. The UI displays the reported aggregate, attributed amount, remainder, and conflicting lower aggregate in the tooltip. Unattributed usage stays on its accounting event; it is not assigned to arbitrary tools.
* Claude Code `cost-state` is a whole-process cumulative claim. When request usage exists, it is retained as evidence and excluded from its conversation's direct usage. After the root and its `subagent` conversations are written, the latest claim for each model is compared with their combined request usage. The positive difference is recorded on the root as `session-remainder`, including side calls without request transcripts. Claims and requests are matched by model family, because Claude Code names a claim by its context variant (`claude-opus-5-5[1m]`) while requests carry the base ID (`claude-opus-5-5`). A claim below the child requests contributes zero. If the whole group has no request usage, the original `cost-state` fallback remains.
* Claude Code copies replies into other transcripts. A forked subagent starts with a copy of its parent's conversation, a sidechain replays its parent's replies under new request IDs, and a resumed or forked session's file starts with copies of the earlier session's replies, under the new session's ID. A reply counts once (`claude_branch.go`, `claude_usage_copies.go`):
  * **Within a session**, a reply is identified by its message ID and request ID, and by its message ID alone for an Anthropic `msg_` ID. Of the transcripts holding it, the fullest record counts (a reply's usage grows as it streams), then the shallowest transcript, then the first by native ID. A session whose subagent transcripts Claude Code removed is settled from what was stored.
  * **Across sessions**, a reply is identified by account and an Anthropic `msg_` message ID, whatever request ID a sidechain gave its copy, or by account, message ID and request ID for another message ID (`claude_usage_copies`). Of the conversations holding it, the one with the fullest record counts it (a reply's usage grows as it streams, so a session read before the reply finished holds less), then the one that started first, then by native ID, whichever was indexed first. A library import settles its Claude Code workspaces the same way. A reply with neither an Anthropic message ID nor a request ID is not matched across sessions, since a gateway can give every reply one message ID.
  * The other conversations keep their record as evidence, wrapped as `{"type":"usage_copy","copy_of":…,"copied":…}`, which neither accounting engine reads. A session made only of copies counts no tokens.
  * Only Claude Code's own transcripts are settled. A Conductor or TL1 conversation that mirrors a native one keeps its replies, since Library counts one workspace of a mirror group.
  * A `cost-state` claim is compared with the group's requests as recorded, copies included, so a reply counted elsewhere is never added back as `session-remainder`.
* Every reply Claude Code sent counts, on the conversation's active branch or not: a rewound or edited turn was still a request. Only the active branch is selected for display (the path from the last prompt or reply back to the start, with compactions joined to what they summarized, and each reply's other lines, its calls' results and the notes under them). A transcript whose turns name no parents, as one another tool wrote may, is one branch.
* A Claude reply's `usage` is its main model's alone. Each `advisor_message` iteration in `usage.iterations` is counted beside it, under the advisor's model; other iteration types repeat the reply's own usage and are not counted again. An advisor's request has a context of its own, so it is left out of the context growth between the conversation's requests and of the reader's context samples.
* Conductor can emit one id-less assistant usage event per content block. Adjacent events in the same stream with the same prompt size are merged until a user turn or tool result; the largest output count represents that request. Delegation results with an `agentId` or `runId` link a zero-usage delegation session to its child Claude conversation, marked `in-child-conversation`. Library counts the child session only when that link exists; an unlinked delegation remains the one counted sub-agent, and a child in a separate workspace does not count itself.
* Claude base input, cache creation, and cache read counts are separate. OpenAI cached input and reasoning counts are subsets of input and output. Cache TTL breakdowns are used only when their parent cache-creation count is absent. [Claude caching documentation](https://platform.claude.com/docs/en/build-with-claude/prompt-caching), [OpenAI usage documentation](https://developers.openai.com/api/docs/guides/agents-api/observability).
* Claude streaming delta usage is cumulative within the request. [Claude streaming documentation](https://platform.claude.com/docs/en/build-with-claude/streaming).
* Context occupancy, context limits, estimated-token deltas, and compaction pre-token counts are not consumed-token totals. They remain available as context evidence and are excluded from consumption accounting.
* Original envelopes retain per-model reports, cache TTLs, iteration details, tool-request counts, service tier, model/context settings, and unknown future fields. Those fields are not blindly added together: iteration details, parent counters, and child counters can overlap.
* Consuming-layer attribution is separate from the accounting ledger. Request usage belongs to its agent session. Run/task/session remainders are stored with their scope in `agent_session_usage.attribution`. When an agent session has delegates, those remainders remain unallocated within its branch and Self excludes them. A leaf session retains its aggregate usage and cost as Self because it has no delegates sharing that usage; Self + children includes them once, including their API-rate estimate. Legacy rows keep their previous interpretation until the stored-message attribution repair runs.
* A proved Agent/Task result's final, separate harness footer can report a `subagent_tokens` counter without categories. `agent_session_token_evidence` retains its source message and `last-request` scope separately from priced usage. It establishes a minimum that may cover only the last request, not a complete run total. Self shows the maximum of retained direct usage and the reported minimum. Repeated observations are not summed. Inclusive totals reconcile uncovered minima against aggregate remainders only inside the same branch; unrelated roots cannot cover one another. These observations never create token categories, API cost, or extra billed ledger rows. The reader shows a separate evidence badge without changing spent-token totals or context occupancy.
* Missing usage is unknown, not zero. Partial categories contribute only their reported counts. Tokens are not dollars or a reconstructed bill. Sources without request-level evidence cannot support request-level attribution.

Go's importer (`TestTokenUsageFixtures`) and the conversation reader's model (`web/tests/unit/reader/tokens.test.mjs`, in node) run the same language-neutral accounting fixtures in `testdata/token-usage-fixtures.json`. Integration tests check preservation through parsing, SQLite, and the detail API. A local comparison additionally checks raw-versus-parsed category totals on representative real source files/sessions.

A one-time local validation, made while Pharos still had a Python importer, compared 12 recent files/sessions from each of Codex, Claude, Conductor, and TL1: all 48 retained identical supported token-category totals before and after parsing. After a backed-up, bounded repair of the 115 already-indexed Conductor workspaces, 14,605 original accounting envelopes were recovered. The catalog's reported total changed from 2,526,281 to 654,706,757 tokens. Independent Go, Python, and browser reconciliation agreed for all 115 repaired conversations, and SQLite's integrity check passed. The sources remained paused. This is recovered reported accounting, not a provider billing reconciliation.

## Usage over time

`agent_session_usage` is an hourly ledger under `agent_sessions`. Ingest writes each session's reconciled usage by the UTC hour, reported model, and recorded effort of the accounting event that carried it, so a session that runs past midnight or switches models lands in the right day, model, and effort. Ledger totals always add up to the session totals. The Usage query table (`/api/query/usage`) turns the ledger into one row per session, local day, model, and effort, then drops linked mirror workspaces the same way Library does. `uncached_input_tokens` is input minus cache reads and cache writes. `model_family` combines provider prefixes, snapshot dates, and `-1m`/`[1m]` context variants; `model` keeps the reported string. Metrics grouped by `day`, `week` (Monday start), or `month` are returned in date order, newest first.

When a catalog opens, sessions stored before the ledger existed (or written by the former Python importer) get one row at their start hour, marked `attribution = session-start`. Run `pharos refine-usage` to rebuild precise hourly rows from the retained messages for just those conversations. It commits each workspace separately, resolving Claude group ownership once per workspace, and is safe to rerun. Progress counts conversations. Accounting events that have no timestamp are also marked `session-start`.

Run `pharos repair-usage-attribution` after upgrading an existing catalog. It adds the advisor usage records a stored reply's usage holds (TL1's runs too), settles copied Claude Code replies (above), then recomputes sessions, hourly usage, model requests, and workspace token metrics from stored messages, one workspace per transaction. A workspace whose copies change rebuilds every workspace that holds the same replies. `usage_attribution_state` records the attribution version with each commit, so an interrupted run resumes at the next workspace. An unchanged source is replayed when its attribution version is old; ingest leaves the workspace pending if any stored conversation was omitted or could not be replayed. Runtime scales with the retained Claude messages and can take tens of minutes on a large library; the command reports progress every 100 workspaces. The Library's dirty-row triggers refresh its cached rows; Usage and cost queries read the corrected ledger. Rebuilding does not need source transcripts. Positive legacy session totals and ledger rows are preserved when their accounting envelopes were not retained. Process claims subtract those preserved child facts before computing their remainder, so retaining them does not charge them twice. Mixed-workspace metrics include those saved facts; explicit zero reports and settled usage copies can still clear old derived counts. A Claude Code workspace recovered from a saved catalog keeps the usage it was saved with, unless its replies settle differently beside this catalog's conversations or it was saved without advisor records; then its usage is derived again from what is stored. The `/api/health` response and `pharos health` group roots from the last 30 days by source and provider, count sessions whose totals exceed their request sums by more than 5% plus 100,000 tokens, and label sources with aggregate-only counters as `session totals only`.

### Agent session model labels

An embedded agent session uses evidence owned by that execution. Its own
request envelopes take precedence over a single conflicting usage model;
several real request or usage model strings produce `Multiple models`.
Aliases and context variants remain distinct recorded identities. Placeholder
models, copied accounting envelopes, parent configuration, assistant answers and
tool-output prose do not establish a child's executing model. An explicit Agent result model is
observed child evidence; an explicit launch `input.model` is only a configured
fallback. The retained launch and raw request strings remain available when
configured and observed models disagree. With neither, the label is
`Unknown model`. A separate child transcript can supply its own evidence before
falling back to launch configuration. A transcript's main session may use that
transcript's own conversation configuration.

The next Library upgrade automatically runs **Correct agent session models**
once to correct existing **subagent session rows**, including those whose native
source has disappeared. No manual command is needed. This pass updates only
`agent_sessions.model` and its independent completion marker
`agent_session_model_version=2`; it does not advance `usageAttributionVersion`
or replay token accounting. Catalogs completed by version 1 are pending again.
The step runs after retained child-link restoration and the tool index, so its
completion marker covers the evidence those steps restore. Normal ingestion and
lineage-only restoration refresh linked launch labels in their existing
transaction without scheduling the one-time repair again. It reads retained
stream membership, request envelopes, positive usage models and linked child
transcripts. A usage model
that merely echoes the parent configuration is not used as proof for an embedded
child. Each workspace commits through the normal writer, which invalidates cached
views. An interrupted pass can be restarted; completed labels are skipped on
repeat scans. The optional maintenance command
`pharos --config PATH repair-session-models` always rescans, so it also handles old
rows imported after the upgrade.

The command reports `sessions`, `updated`, `multiple`, `unknown`, and
`pricing_fallback`. These are stored session rows, including mirrors, rather
than deduplicated executions. `multiple` and `unknown` describe the resulting
summary; `pricing_fallback` counts rows left untouched because a positive legacy
usage bucket has an empty model and still depends on the session label for
pricing. Those rows require a separately reviewed accounting migration.
Per-model tokens, categories, hourly usage, IDs, hierarchy and linked-child
ownership are preserved. Costs continue to use `agent_session_usage.model`.
Delegation search and JSON responses read the corrected session label; external
child metadata prefers its own main-session summary. Ledger filters and exports
retain the original per-model identities.

The opt-in `TestSessionModelCohortReplay` probe copies the narrow single-Haiku
mismatch cohort from a catalog attached with SQLite `mode=ro` into a disposable
catalog, applies the actual repair twice, and compares every usage row and its
historical price. It does not initialize or modify the source catalog:

```sh
PHAROS_SESSION_MODEL_CATALOG=/path/catalog.sqlite3 \
  go test -tags probe -run '^TestSessionModelCohortReplay$' -v -timeout 20m ./internal/archive
```

Validation on November 10, 2026 copied 2,412 mismatched subagent-session rows
from Pharos On Euclid. All 2,412 became Haiku labels; the second repair updated
zero rows. Every hourly usage row, token category and historical price compared
identically before and after. The source catalog remained read-only.

| Source | Mismatched rows before | Mismatched rows after |
| --- | ---: | ---: |
| Conductor | 2,271 | 0 |
| TL1 | 127 | 0 |
| Claude | 14 | 0 |

This validates the narrow demonstrated cohort, not every session's model.
The broader read-only inventory found 14,532 subagent-session rows: 10,737 with
one real positive-usage model, 48 with multiple, and 3,747 without a real
positive-usage model. The latter groups require their own retained evidence and
are reported as multiple/unknown where appropriate; they are not assigned to
Haiku by this repair. No positive blank-model pricing fallbacks were found in
that inventory.

At this revision the general `probe` build is blocked by unrelated stale Git
API calls in `outcome_stages_probe_test.go` (`gitCheckout` and `inferLinks`).
The cohort validation used a temporary Go compiler overlay replacing only that
probe with an empty `package archive` file. Production code and the cohort
probe ran unchanged; ordinary Go checks do not use that overlay.

## Cost

Usage and Library rows have a **Cost (API $)** column: the API list-price equivalent of the tokens on the day they were used. Each ledger entry's model is resolved through `model_alias_on_date` (for example, bare `opus` resolves to the model it meant that day) and priced from `cost_on_date` by category. Uncached input, cache reads, 5-minute and 1-hour cache writes, and output each use their own rate. Reasoning is already included in output. A cache write with no TTL split is charged at the 5-minute rate. **Cost at today's prices** prices the same model at its current rate, so trends aren't skewed by price changes. Long-context premiums, batch/priority tiers, and subscriptions are not modeled, so cost is a lower bound.

`price_status` is `priced`, `assumed` (a confirmed `assumption` row, where no provider published a price; shown as `≈`), `partial` (unclassified tokens or a category without a rate; shown with a trailing `+`), or `unpriced` (no confirmed price for that model on that day; shown as `—`). Settings → Health reports the priced share of usage. Prices come from the checked-in `pricing/cost_changes.json`; see [`pricing/README.md`](../pricing/README.md) for the contract and the review flow. The Usage page's **Copy refresh prices prompt** button (or `pharos pricing prompt`) produces an agent prompt for refreshing prices.

## Refresh and limitations

### Context-growth viewer

Overview accounting is built from the transcript's normalized message/ownership model, not from rendered DOM badges. This keeps turn totals, all context samples, compactions, and highest-spend navigation available when long conversations defer their turn bodies. Opening a turn binds its existing profile samples to the newly rendered rows and adds subagent summaries without recounting usage. Profile navigation materializes only the destination turn.

The scrolling rail uses request **input context**, including cached input, on a linear width scale. Run, task, thread, and turn consumption totals never set profile width. Legacy checkpoints use their `last_token_usage` input, not cumulative input. Exact response records supersede duplicate legacy samples. Each subagent has its own context history and color. Compactions break that stream's profile; the next observed size is a new baseline, not a fabricated zero. Collapsed groups retain striped, compressed profiles and can be reopened by clicking the rail.

Event badges show net input-context change between retained requests. Grouped rows add these changes, never the context sizes themselves. Initial samples show a baseline; missing input remains unknown; estimated evidence is labeled. These changes can include intervening assistant output, multiple tool results, user text, trimming, and other prompt changes. They cannot establish that one specific file added an exact number of tokens. Negative changes are preserved. Badge color follows a continuous log scale of the change's size: sea at 1K tokens or less, then indigo at 10K, magenta at 100K, and coral at 1M or more. The scale avoids the app's green and brass, so it never reads as success or a control. Compactions are marked in the neutral text color. Dashed `tokens spent` badges separately show reconciled turn/subagent consumption (including descendants). The profile follows scroll positions, so its area is a qualitative guide, not a mathematically exact consumption total.

Schema version 3 adds `messages.raw_text` and `messages.source_order` without removing existing data. Extractor/fingerprint versions change for all affected source adapters, so a subsequent sync revisits unchanged files. Sync preferences are preserved. Bounded `repair-existing conductor` can refresh only already-indexed Conductor workspaces without enabling discovery of paused sources.

Evidence already discarded by an earlier importer requires re-reading its original source. If the source has been deleted, detailed attribution cannot be recovered from a retained workspace total. Cross-source copies remain separate archived conversations; archive-wide totals should not be interpreted as a deduplicated provider bill. Health groups workspace totals by recorded conversation model; original multi-model accounting is retained in result evidence, and must be inspected for exact model allocation.

## Agent effort levels

`effort` is recorded with conversations, messages, agent sessions, model requests, tool calls, and hourly usage. Codex turn contexts, explicit Claude/Conductor settings, TL1 attempts, Antigravity selector suffixes, and canonical/ChatGPT metadata supply the level when present. Missing or explicitly reset levels stay unknown. Multi-effort sessions have no single session effort; request usage retains each level. Aggregate usage that cannot be assigned across effort changes stays unknown, including Claude process remainders.

Library exposes all observed `efforts`; Usage, Tools, Tool calls, and Skills expose `effort` for selection, filters, grouping, and metrics. Usage offers an Effort chart split and a model-and-effort preset. The reader shows conversation and turn effort. CLI and HTTP work search accept `effort` (`pharos search --effort high`); MCP work and conversation search accept the same filter.

Use **Model + effort** (`model_effort`) in Usage, Tools, Tool calls, Skills, and TL1 attempts to filter, sort, or group recorded pairs such as `claude-opus-5-5 · high` and `claude-opus-5-5 · low`. Missing effort appears as `Unknown effort`. Library exposes the observed pairs as `model_efforts`, and the Usage chart can split by Model + effort. Separate model and effort filters, sort keys, and grouping keys can also be combined. CLI/HTTP/MCP searches with both model and effort require a matching pair in the same conversation setting or message, so separate models elsewhere in the workspace cannot satisfy the two filters.

Existing catalogs migrate automatically without changing token totals. Parser version changes cause retained source captures to be reparsed on the next index/sync. Old messages whose source never recorded effort, or whose source is no longer available, remain unknown. Effort is a usage dimension; API list prices still follow model, date, and token category.
