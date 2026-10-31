# Pharos technical overview

Pharos is a local-first macOS library for finding past work across TL1, Conductor, Codex, Claude, Google Antigravity, and supported desktop exports. A second capability, preserving and reclaiming **TL1-owned** workspaces through a custody-aware owner hook, is mothballed; see [`docs/reclamation/README.md`](reclamation/README.md) for what remains and how to resume it.

The primary implementation is Go 1.26 with SQLite FTS, a local concept index, an authenticated loopback API, a read-only MCP server, and a responsive desktop UI. The Go service and UI are embedded in the macOS `.app`; the destructive TL1 workflow is deliberately held back.

The command-line executable is `pharos`, and a per-user installation keeps its configuration in `~/Library/Application Support/Pharos`.

The native app uses an application broker to open named libraries independently
of its own location. Each library service has an allocated loopback endpoint and
private runtime credential. The broker owns the primary connection, explicit MCP
assignment and verified library transfers. See [Libraries and application setup](library-management.md)
and the [implementation design](onboarding-and-library-design.md).

Startup keeps the native lighthouse visible until the first web page finishes
loading or its navigation is cancelled or diverted to a download. Later reloads
and navigations keep the current page visible without the startup overlay.
The broker answering `/api/ready` alone does not mean its library is open.
Catalog opens reuse a completed initialization for the same executable, schema,
and Library projection, while still registering the current Mac. A new build or
an older catalog runs the full migration and backfill pass before serving pages.

Channel and Machine attribute work to its initiating conversation and device,
including delegated usage. Execution machine records each session's placement
separately. These dimensions are available across query tables, charts, cards,
and MCP; see [channel and machine](channel-and-machine.md) for the evidence and
inheritance contract. Unknown placement remains visible in totals.

## Safety defaults

- Filesystem imports use live readers by default; experimental `retained_input_capture` opts into `capture_acquire.go` before planning/parsing. Immutable input inventories, row-evidence pins, and temporary native leases keep readers stable while capture continues. Accepted copy evidence is transactional with catalog rows; source acquisition/index summaries are separate. SQLite defaults and remaining rollout gates are documented in [Index changes](incremental-sync.md#source-acquisition-rollout).
- Every configured source is read-only. No home-directory scan occurs: `pharos probe` and the new-Mac panel check only a fixed list of known agent locations, and nothing is indexed until you opt in.
- No source is reclaimed. TL1 reclamation is mothballed and excluded from the default build.
- The archive never calls `rm -rf`, `git worktree remove`, `git worktree prune`, or `git gc`. TL1 owns the worker handshake, lock/lease, atomic recheck, and exact-resource removal.
- Preservation must fit the 100,000,000-byte logical cap, reside on the pinned volume, and pass a complete hash/reopen check before acknowledgement.
- Nothing gates background work on the machine being idle. Index changes runs on its interval, maintenance runs whenever it has work, and only capture copies, local backups and adoption run at low (utility) disk priority. The machine's load average is recorded only as a forecasting feature. [Background work](background-work.md) is the contract for how background operations are registered, journaled and reported.

## Quick start

```sh
./launch.sh
```

This one command builds the Go service and Swift wrapper, initializes
`~/Library/Application Support/Pharos/archive.toml` if it is missing,
builds the macOS app, and launches it. Existing configuration is preserved.

Edit that configuration to set `archive_root`, paste the value reported by
`dist/Pharos.app/Contents/MacOS/pharos volume-id /Volumes/euclid`, and set `enabled = true`
only on source paths you want indexed. Then ingest from the workspace when you
want to refresh the library:

```sh
dist/Pharos.app/Contents/MacOS/pharos --config "$HOME/Library/Application Support/Pharos/archive.toml" ingest
```

To keep the app and its whole library on an external drive that moves between
Macs instead, run `macos/install-library.sh /Volumes/euclid/Pharos`. The app
then uses the `library.toml` beside it, whose paths are relative to its own directory;
see [Portable library](configuration.md#portable-library). On each Mac the
drive is plugged into, double-click **Add This Mac.command** beside the app: it
finds that Mac's Claude Code, Codex, Antigravity, Conductor, and TL1 history, captures it
onto the drive, and indexes it ([Adding a Mac](configuration.md#adding-a-mac)).
To bring an existing per-user install along without re-indexing, add `--adopt`
with its `archive.toml`; see [Moving an existing install onto a drive](configuration.md#moving-an-existing-install-onto-a-drive).

The UI has separate Library, Analyze, Tools, Findings, MCP, and Settings areas. Library, Analyze, Tools, and MCP call history
use the shared query-table pattern: schema-driven columns, field discovery,
filtering (including OR/NOT), multi-sort, paging, persistent saved views,
optional aggregate metrics, column reorder, and resize.

- **Home** shows saved query cards and Dashboard Catalog suggestions. The catalog’s **All Cards** button opens the browsable Card Library at `/cards`. Completed covers are cached in the browser's IndexedDB, isolated by library and keyed by query, template, schema, and time zone. Revisiting the page shows saved covers immediately while current results refresh behind a small status icon; a failed refresh keeps the last cover. Snapshots expire after seven days and are capped at 400 covers or 16 MiB per library. Once foreground cards finish, Home warms the catalog's default covers, before the Card Library page is opened. Cold requests prioritize cards near the viewport as it scrolls; grouped requests take independent queue slots rather than serializing inside a metrics batch. A first visit still computes its initial covers, and these browser snapshots do not precompute while the app is closed.

- **Library** searches conversation text, changed files, and tool URLs. Text search reads retained user messages, agent responses, and thinking from `messages_fts`, groups hits by conversation, and links to the matching message. `messages_fts` word-indexes every message kind with text except tool output (`tool_result`, most of a catalog's text; see `tool_output_index.go`): a search of the tools or all surface scoped to a conversation or at most 100 workspaces also scans the stored text of the tool output in scope, while Library Find's workspace rows and unscoped searches cover the other kinds and say so. Catalogs written before that remove their tool-output postings in the background (`maintenance.search.tool-output`), then run a bounded merge; the file shrinks only with `VACUUM`. Unquoted words use AND; fuzzy expansion reads nearby vocabulary terms, while quoted phrases match in order. Case and separator matching are optional. The UI's text search does not use workspace vectors. File search uses `change_files`; a file change has workspace scope and is attributed to a conversation only when its work item, time span, or sole-conversation workspace supports that link. URL search reads `tool_urls` and excludes unopened web-search result links. The search narrows the Library query table, whose filters then apply to the matching work; the table also offers repository, source, PR, sub-agent, compaction, cost, and other structured filters. Work detail shows evidence-linked summaries, attempted/checkpointed/integrated changes, conversations, metrics, PR associations, and receipts.

Past Work also shows when an archived commit appears on the local `origin/main` history. It links the first mainline commit containing that work, whether it arrived through a merge commit or directly. This uses local Git objects only; squash merges and work without a retained commit ID cannot be attributed by ancestry. Refresh the local `origin/main` ref and index a source to update these associations.
- **TL1** ingestion preserves workflow data and native transcripts. Its dedicated analysis pages are removed for now. Archived conversations remain searchable, flavor recommendations remain in Findings, and attempts remain queryable in Analyze. The TL1 analysis APIs and MCP tools are also retained.
- **Analyze** has two views, switched at the top of the page, and remembers the last one. **Tokens** is the token-usage query table: one row per agent session, local day, and model, with uncached input, cache reads, cache writes, output, and reasoning kept separate. Provider, model, repository, source, agent kind, and day/week/month are all filter and group-by fields, so metrics such as "cache reads per week by model" are a saved view. A stacked chart above the table shows tokens or cost per day, week, or month, split by token type, provider, model, repository, or agent kind. The chart, the summary cards, and the table share one set of filters: clicking a key filters to that provider, model, repository, or agent kind (⌘-click adds more; clicking the lone selection, or its ×, clears it), dragging across columns or choosing a range preset sets a time filter, and a filter typed into the table narrows the chart and cards too. Token types are not rows, so clicking one only narrows the chart. When a few columns dwarf the rest (the tallest is over twice the 80th percentile of non-empty columns, rounded up to a round number), the chart breaks its axis there: linear below in the lower half, logarithmic above in the upper half, marked with broken-axis slashes and a "log scale" label. The Human Words chart does the same. Preset buttons set up common breakdowns. Linked mirrors of the same work are counted once. **Your writing** is described below.
- **Analyze dashboards** (Tokens and Cost so far) offer views beside the trend: **Compare groups** (bars or shares), **Distribution** (box or histogram), **Two measures** (a scatter), and **Hour / weekday cycle** (hour of day or day of week). Each view can count usage records or whole conversations, agent sessions, or delegated runs, and can hide groups below a minimum sample. Each catalog lists its measures, breakdowns and units; the engine, its query shapes, and how a Home card maps to a dashboard state are in [`docs/dashboard-analysis.md`](dashboard-analysis.md).
- **The site map** lists every page, its options, every card, and which dashboard settings combine, generated from the TypeScript that defines them, so Go can browse the site and build links. See [`docs/site-map.md`](site-map.md).
- **Tools** analyzes tool use from retained transcripts. A daily summary table groups calls by tool, shell program and subcommand (`git status`, `go test`, `sed`), model, repository, and agent kind, with error counts by kind, durations, the tokens each result added to the context, the tokens re-read by later requests until compaction, and their API-equivalent cost. A per-call table filters and sorts every call; a summary row drills into its calls, and each call opens with its parsed commands, input, and result. Each call also records the sites it reached: URLs from web fetches, browser navigation, Codex web searches, and network commands such as `curl`, plus any web search query and the links it returned. Presets cover error rates, failure kinds, top commands, shell calls a dedicated tool could make, slow tools, test and build time, context cost, sites reached, and web searches. Definitions are in [`docs/tool-analytics.md`](tool-analytics.md).
- **Findings** turns recurring, fixable patterns (failures, instruction drift between harnesses, CLI friction, context-heavy commands, orientation and exploration cost, cost outliers, and TL1 flavor problems) into plain-language cards with a proposed change. Findings go into per-target prompts ("carts") saved in the library; copying one is the intervention that starts a before-and-after measurement with a fixed analysis plan, guard metrics, and weekly net savings. Manual Index changes runs a full findings pass; scheduled indexing defers findings. Capture & index and separate index actions retain daily full discovery and watched-finding measurements between daily passes. See [`docs/findings.md`](findings.md).
- **MCP** controls local agent access, provides a copyable stdio connection definition and setup prompt, and offers a query-table of recent tool calls with filters, saved views, metrics, response-size estimates, duration, result counts, truncation, and errors.
- **Sources on this Mac** lists every configured source, path availability, coverage, last index attempt and successful index, and errors. Each card has an enable switch in its header. It offers **Find sources on this Mac** and links to Settings → Background updates, which shows what runs Frequently, Hourly and Daily and has **Capture & index** (capture this Mac, index captures from every Mac, then any pending library upgrade) and, separately, **Capture this Mac** and **Index captured files**. The header's **Index changes** button incrementally indexes this Mac's sources, then refreshes GitHub, identities, Git integration, Tools, Human Words, and findings using live inputs by default and retained inputs only when explicitly enabled. Acquisition rollout is staged; see the [source acquisition matrix](incremental-sync.md#source-acquisition-rollout).
- **Library drive** (in the header) names the drive holding the library and what is holding or writing the library (capture, index, backup, Git lookups, Library view indexing), with progress and estimated completion times based on recent successful runs. In the app, **Eject** at the right of the panel's title releases the library, stops agents' Pharos MCP servers running from the drive, quits, and ejects it. See [Library status and Eject](configuration.md#library-status-and-eject).
- **Macs and captures** (in Settings → Sources) lists this Mac and every Mac whose captures are in the library, with when each source was captured and indexed and which need indexing. **Open Capture & index** links to the capture and index controls and their progress. A newly added Mac is captured right after onboarding, then offered **Index now**.
- **Library drive** checks and **Backups** (in Settings → Health) show the drive's encryption, Spotlight, file system, free space, volume pin and backup age, each with what to do and a copyable command, and back the library up to a folder on another drive.
- **Settings → Backups** also provides the local backup controls and a cloud backup wizard: a six-step calculator/configuration/commands/test/first-backup/test-restore flow, an opt-in compact wizard with GCP defaults, four primary calculator controls, collapsed detailed options, linked regional settings, and a calendar-month cost graph comparing linear growth with a flat library using a measured full-backup compression sample, database-saved configurations, and exact copyable provisioning commands and prompts. AWS/GCP use native revision-count retention; Azure uses previous-version age expiry. The selected design stores restricted upload credentials in the Pharos database and uses provider-managed encryption, with cloud-account access for restore. Optional password protection uses age public-key encryption with a password-encrypted recovery bundle; setup asks for the password only in Terminal, with separately copyable preparation and provisioning blocks. The Test, First backup and Test restore steps use server-side credential import, restricted-access probes, real provider uploads and recorded version IDs. The backend makes consistent full snapshots, embeds recovery bundles, and validates downloaded archive digests, file manifests and SQLite integrity in isolated folders. Protected verification prompts only in Terminal; `pharos cloud-restore` also recovers without the original database. Daily, weekly and every-30-day schedules persist in SQLite and activate after a verified full restore. They bind to a Mac and library path, catch up once after downtime, serialize destination uploads, and use bounded retries. Settings offers pause/resume, schedule editing, repeat manual backups and version selection for restore tests. The cloud entry carries a Beta chip. Durable upload restart-resume journals remain future work. See [Cloud backups](cloud-backups.md) for pricing assumptions and the implementation plan.
- **Library drive** in the header shows live indexing progress and the latest completed index result, including updated workspace and conversation counts and any error.
- **Your writing** (in Analyze) estimates how much text you typed or dictated into agent chats. Each user message is split into typed text, harness instructions, one-click templates, attachments, agent output copied from the previous 48 hours, re-sent text, likely pastes, and prompts sent by scripts or other agents. A query table lists one row per conversation, sortable and filterable by each kind of input and by tokens and cost, so "deepest conversations" is one click; the chart and breakdown above it follow the filters. The chart splits words by kind of input, repository, app, or provider; its key and a dragged time range filter the table the same way as on Tokens, and a time filter counts only the words sent inside it. A second query table lists one row per user message, filterable by the categories and rules that labelled its text, and shows messages either as rows or as text highlighted span by span, each span explaining on hover why it counted as what. The transcript reader shows the split for each message, and Settings has cards that open each Analyze view. Definitions are in [`docs/human-authorship.md`](human-authorship.md).
- **Analyze → Carbon Impact** estimates the electricity and CO₂e behind your token usage, all time or the last 30 days, split by token type (uncached input, cache writes, cache reads, output) and model tier. Energy per token comes from published inference measurements, with cache reads charged far less than recomputed input; you choose the electricity grid, data-center overhead (PUE), and a low, central, or high estimate. It is an estimate, not a measurement: factors and sources are in [`carbon/`](../carbon/README.md).
- **Library upgrade** appears when a catalog indexed by an older version needs its derived data brought up to date: repository merges, token attribution, harness versions, loaded instructions, and the tool ledger, run in that order as one resumable background job. See [Upgrading an existing library](releases-and-updates.md#upgrading-an-existing-library).
- **Health** shows retrieval coverage/freshness, index size, and external-storage health. Retrieval-only is shown as an intentional capability.
- **Storage** (on Health, `/api/health/storage/breakdown`) splits the library drive's space into captured originals (latest copies, earlier versions, manifests) and Pharos's catalog (database, write-ahead log, caches), and tabulates both by Mac, source and repository. Sizes come from the drive checks' cached walk of the library, which counts each hard-linked file once. Originals by Mac and source are measured. Nothing records which repository a capture's or the catalog's bytes belong to, and measuring that would read the whole catalog, so repository shares, and the catalog's split by Mac and source, are estimated from each conversation's message count in its sightings (`storage_breakdown.go`). The drive checks read those counts when they walk the drive and keep them with the sizes, so the breakdown answers from memory; a sync's commits do not invalidate it, and the size-history row each measurement records does not hold the sizes back while the catalog writer is busy. The catalog's copy of a conversation belongs to the Mac and source it was written from; a merged copy is spread across every Mac that saw it.

Delegated work is retained as a recursive `agent_sessions` tree, including sub-agents of sub-agents and links back to each session's messages. Session and workspace usage preserve uncached input, cache reads, cache creation, output, reasoning, and any unclassified aggregate remainder separately so pricing can be applied without reconstructing provider envelopes.

Run diagnostics with `dist/Pharos.app/Contents/MacOS/pharos --config "$HOME/Library/Application Support/Pharos/archive.toml" doctor`. Configuration is documented in [`docs/configuration.md`](configuration.md).

`pharos labeling` prepares a read-only snapshot of conversation text as a versioned dataset for offline labeling by a large model, runs labeling jobs and exports training data; see [`docs/library-labeling.md`](library-labeling.md).

## Sources and identity

| Kind | Acquisition | Mutation capability |
| --- | --- | --- |
| `tl1` | Registered installations via consistent SQLite snapshots and native transcripts | None |
| `tl1-export` | Owner-produced canonical JSON | TL1 release contract only |
| `conductor` | Consistent read-only SQLite snapshot transaction and schema inspection | None |
| `codex` | Native rollout/session JSONL | None |
| `claude` | Native project session JSONL | None |
| `antigravity` | Per-conversation step transcripts (`transcript_full.jsonl`) with titles, workspaces, and subagent parents from `conversation_summaries.db`, and each model request's served model and token usage from the protobuf `gen_metadata` rows of `conversations/<id>.db`. Antigravity leaves its cost and credit fields unset, so cost comes from Pharos's price table | None |
| `chatgpt-export` | User-provided `conversations.json` | None |
| `canonical` | Documented interchange JSON | None |

Each conversation records its agent harness and first and last observed harness versions. Claude Code takes `entrypoint` and `version` from JSONL events; Codex takes `originator` and `cli_version` from every `session_meta`, including resumed sessions. Conductor keeps its own harness identity and inherits a linked native conversation’s version with source `alias`. For Antigravity on this Mac, Pharos records when it first observes each installed app or CLI version and attributes that version only to later conversation activity. Older conversations and captures from another Mac keep the version empty when the transcript cannot establish it. Version sources distinguish transcript evidence, native aliases, and the installed app. Existing catalogs fill these fields during the next identity reconciliation, using retained Claude messages and Codex source or captured files.

Each Claude Code and Codex conversation also records the instruction files and skills its harness loaded, in `conversation_instructions` (`conversation_id`, `harness`, `path`, `kind`, `bytes`, `hash`, `repo_path`, `first_seen_at`, `last_seen_at`). Claude Code's `instructions` attachment lists each loaded file with its type and content: `Project`, `User`, `Local`, `AutoMem`, and `Managed` become the lowercased kind, a `nested_memory` attachment (a subdirectory's `CLAUDE.md`) is `nested`, `invoked_skills` is `skill`, and each name in a `skill_listing` is `skill_listing`. Codex's `# AGENTS.md instructions for <dir>` message and `world_state.agents_md` become `agents_md` rows for `<dir>/AGENTS.md`, and each entry of its `<skills_instructions>` listing is a `skill_listing` row for the skill's `SKILL.md`. `bytes` and `hash` (SHA-256) are of the content last loaded, empty when the transcript names a file without it; `repo_path` is the path relative to the repository checkout, empty for agent-home files such as memory and bundled skills. Ingest replaces a conversation's rows whenever it writes the conversation; Conductor wrappers carry none (the native conversation does), and TL1 runs get theirs from the Claude or Codex transcript. Antigravity transcripts record nothing about loaded rules. Libraries indexed before the record existed fill it in the library upgrade, from source files or captures. The record is what instruction drift and the context an instruction change adds are measured from ([`docs/auto-optimization-design.md`](auto-optimization-design.md), G7).

Deduplication uses account-scoped native IDs. Conductor aliases are retained as evidence-backed identity links after all adapters run; repeated identical prompts are never merged by content hash. The Conductor extractor inspects each row and selects the materially populated `content`, `full_message`, or `text` field, recording that decision in its evidence locator.

No claim is made that ChatGPT desktop’s local cache is complete. ChatGPT is indexed from a user-provided supported export, and its health row makes that acquisition boundary visible.

## UI feedback

The **Send feedback** header tool reuses element annotations for optional bug
reports, feature requests, and prompts. Users review their commentary and choose
whether to attach element screenshots, captured URLs, element text, recent
diagnostics, and a contact email. Sending explicitly uploads that report to the
independent Go feedback service; archive indexing and search remain local.
`internal/uifeedback` owns the reusable protocol, GCS store, Pythia-authenticated
admin inbox, and notifications. `cmd/feedback` hosts it separately from Pharos's
loopback service. The admin UI uses Query Table over the complete cached report
population, with tags and permanent deletion. See [UI feedback](ui-feedback.md)
for the protocol, privacy boundaries, deployment, and operation.

## Search, API, and MCP

The Library UI sends its text, file, and URL searches to the Library query table as `find=` (with `find_kind`, `find_fuzzy`, `find_case`, and `find_separators`), which keeps the work `/api/library/find` hits and adds each row's best match and match count; `/api/library/find` still returns the hits themselves. Its text path uses FTS vocabulary and message verification without vector ranking. Legacy `/api/search`, query-table `search=`, and MCP conversation discovery still retain the previous concept/feature embedding behavior. Search and MCP do not upload archive content. Extractive summaries cite retained source locators.

The workspace page loads a workspace in pieces, since one long session with many sub-agents can retain hundreds of megabytes. `/api/work/{id}` returns the workspace without messages, plus what the page used to derive from all of them: each conversation's message count and first prompt, pull request links found in any message, and the files its tool calls touched. `/api/work/{id}/conversations/{id}` returns one conversation for the reader. Its long tool outputs and event bodies are shortened past 2 KB, with JSON keeping its structure so error flags, exit codes, and token usage survive, and are marked `text_clipped`/`raw_clipped`. `/api/messages/{id}` returns a message's original when the reader opens it. Finding text across a workspace's conversations runs on the service (`/find`). In the reader, a turn initially renders 50 events and adds pages of 150 as it scrolls; event details are built when opened.

Requests never wait for the catalog writer, which a sync holds for seconds at
a time. They read through a read-only connection pool and never write:

- **Settings → App preferences → Pause indexing during queries** optionally lets
  interactive query, search, reader and MCP requests pause ingestion at record
  and message boundaries, and search-index and Library maintenance between
  batches. It defaults off so sustained browsing does not prevent indexing
  progress. The saved library preference `pharos-pause-indexing-for-queries-v1`
  applies without restarting, independently of the activity panel and manual or
  automatic indexing. Turning it off releases paused work even during a query;
  preference saves let the writer progress without waiting for queries to finish.
  Status polls do not pause work.
- Cached Library and derived views keep their last computed generation for the
  entire ingest; cold values still compute on demand. Once the ingest ends,
  reads observe the new generation and refresh. Freshness may lag throughout a
  long ingest so pages remain usable without repeated whole-catalog rebuilds.
- Conversation origins are published at the end of each ingest and by the
  Library maintenance loop, at most once a minute while a sync runs. Cached
  views may retain the previous origins until the ingest ends.
- Dirty parallelism timings and MCP metric projections are computed in the
  request's snapshot without being stored; maintenance stores them.
- The tool rollup rebuilds in the background. Until the first rollup in a new
  layout exists, tool pages report that it is rebuilding.
- MCP call history is queued in memory and stored by a background writer, and
  on close. `query_statistics` snapshots live in the answering process's memory.

`TestRequestReadsDoNotWaitForTheCatalogWriter` holds the writer, as a sync
does, and checks page reads, query datasets and MCP tools against it.
`TestInteractiveQueriesPauseIngest` and `TestIndexYieldsInsideAtomicWorkspace`
check that indexing yields, readers see the previous atomic record, and a
cancelled record rolls back and can be replayed.

The HTTP service binds only to loopback and requires either a bearer token or its HttpOnly UI cookie. Core endpoints are:

```text
GET  /api/search?q=&repository=&source=&file=&pr=&substring=&limit=&offset=
GET  /api/library/find?kind=text|file|url&q=&fuzzy=&case=&separators=&limit=&offset=
GET  /api/search/status
GET  /api/work/{workspace_id}
GET  /api/work/{workspace_id}/conversations/{conversation_id}
GET  /api/work/{workspace_id}/find?q=&depth=messages|thinking|tools|responses&regex=&case=
GET  /api/messages/{message_id}
GET  /api/conversation/{conversation_id}?limit=&offset=
GET  /api/change/{change_set_id}
GET  /api/trace?file=&pr=
GET  /api/receipt/{receipt_or_operation_id}
GET  /api/health
GET  /api/health/carbon
GET  /api/sources
GET  /api/activity
GET  /api/library/status
GET  /api/health/drive
GET  /api/mcp
GET  /api/mcp/calls?limit=&offset=&tool=&status=
POST /api/mcp/enabled
POST /api/sources/{name}/sync
POST /api/sources/{name}/enabled
POST /api/sources/sync
GET  /api/upgrade
GET  /api/upgrade/preview
POST /api/upgrade
POST /api/query/{library|activity|usage|…}
GET  /api/findings
GET  /api/findings/{id}
POST /api/findings/{id}/action
POST /api/findings/cart/prompt
POST /api/findings/cart/copy
POST /api/findings/settings
POST /api/findings/refresh
GET  /api/query/{library|activity|usage|…}/distinct?field=&q=&limit=
GET  /api/query/{library|activity|usage|…}/field-stats?fields=a,b
POST /api/query/{library|activity|usage|…}/metrics
POST /api/query/{library|activity|usage|…}/query-v3
```

The query endpoints implement the `@pythia-software/query-table-*` 0.6.0 wire
contract. Their allowlisted field definitions live in [`schemas`](../schemas), and
the same documents drive the React frontend and generated Go SQLite schemas.
V2 rows and metrics use the upstream SQLite harness with formulas, paired
metrics, distributions, shown-row scopes, and canonical computed definitions.
Every chart and metric reads `/metrics` or `/query-v3`; the legacy
`/aggregations` route answers 410 Gone. Plain counts and sums, and Machine
Time's fact cubes, are answered by host fast paths inside `/metrics` that give
the adapter's exact results. Legacy row requests keep Pharos's map adapter and
indexed SQLite paths. See
[`Query Table integration`](query-table-integration.md) for endpoints, bounds,
local upstream patches, and performance verification. Query values never become
SQL text. Both the in-memory and SQLite executors support text length filters (`length_gt`,
`length_lt`, `length_eq`, counting characters; zero length matches empty text
but not NULL) and sorts on a regex-extracted value (the first capture group, or
the whole match; a number field's extraction orders numerically). Text-array
fields with `filter.editor: "set"` get query-table's ANY/ALL/NONE/EMPTY tag
editor; its metadata stays in the browser, and the server sees only `includes`
and `is_null` predicates, where an empty array counts as null.

Legacy Library query-table requests use `search=` for text; quoted multi-word
phrases require an ordered match in one message. `substring=1` also matches
inside words, and the rows request accepts `explain=1` to add each row's `why`:
its score parts, best matching messages with matched text, and related-term
breakdown.

Source controls only change ingestion participation or perform an explicit
read-only refresh. They never delete source data.

`pharos mcp` exposes read-only conversation discovery in four layers:
`search_conversations` returns compact ranked cards; `get_conversation_overview`
returns an extractive preview with message references;
`search_conversation_passages` finds matching passages within a conversation;
and `get_conversation_messages` reads bounded windows or chunks of a long
message. The discovery tools accept `max_output_tokens` as an approximate
response budget (defaulting to 700–1,200 depending on the tool). Search cards
include coverage and index freshness; conversation documents are generated
locally and existing catalogs are backfilled on opening.

`list_repositories` comes before them: the repositories with sessions, most
recently active first, each with its conversation count, latest message
(`last_message_at`, the latest conversation end or start), and latest capture
(`last_indexed_at`). It also returns each source's last successful sync per
host, with the error of a later failed attempt, and the number of conversations
outside any repository, which a `repository` filter never matches. `query`
matches a repository's name, remote, aliases, or checkout paths, and `path`
returns the repository checked out at or above a directory, so an agent can
find the name to pass as `search_conversations`' `repository` filter. That
filter, and those of `search_work` and `list_findings`, must match a
repository's name or remote: one that matches none is an error rather than an
empty result. When the value is a checkout path, or a directory name in one of
at most three repositories' checkout paths (such as a worktree's), the error
names those repositories.

`list_findings` and `get_finding` read findings: compact cards, then one
finding's extracted facts kept apart from its evidence handles, which are
labeled as untrusted transcript content. Neither changes a finding's state; only
copying a prompt in Pharos starts a measurement.

`browse_site` and `site_link` find and build links to the web UI's pages
from the [site map](site-map.md). `browse_site` lists one level of it at a
time: pages, a dashboard's views and settings, filterable fields with their
operators and most frequent values, and cards. `site_link` checks a page,
options, filters and window against the map and returns the canonical link,
a title, and a one-sentence summary of what the page shows. Its errors name
the argument and list the valid choices. Given a link, it returns the
canonical form or says why the map cannot express it.

The existing `search_work`, `get_work_detail`, `get_conversation_excerpt`,
`get_change_set`, `trace`, `query_metrics`, and `get_receipt` tools remain
available. `get_conversation_excerpt` is a compatibility alias for bounded
message windows. MCP exposes no arbitrary deletion tool.

Settings → MCP controls a shared enabled flag in the local catalog. Turning it off
hides tools from new listings and rejects calls from already-connected clients;
it does not alter another client's configuration. Tool-call history retains the
latest 5,000 calls with an allowlisted, shortened argument summary and response
metrics, never response bodies. A call is recorded after it is answered. Analyze → Pharos MCP provides guided charts;
Analyze → Query → MCP calls provides filtering, grouping, metrics, and call details. Output token counts are estimates from bytes.

## Sharing conversations

`GET /api/share?id=<work>&id=<work>…` (up to 200; `/api/share/work/<id>` for
one) returns the works as an attachment: the app's own page with the reader's
model (`assets/reader-model.js`) and the query-table bundle inlined, its other
scripts left out, and the data embedded as JSON in
`#pharosShareData`. When that element exists, `ui.py` sets `SHARE` and answers
the reader's `/api/work/…` requests (overview, one conversation at a time, and search across a work's conversations) from it, routes between pages with `?page=` and `?work=`
(a file cannot change its path), and hides the library chrome
(`assets/share.css`). The Library selection UI is `ShareSelection` in
`web/src/index.tsx`.

- **Same code:** the conversation reader and the tables are the app's, so
  changes to them reach shared files without extra work. In a shared file
  `QuerySurface` passes each table its rows as `clientRows`, so filtering,
  sorting, metrics, and charts run in the browser (`applyQuery` and
  `applyAggregations` from query-table-core) rather than on the service.
- **Tables:** Library, Tools (summary and calls), and Analyze (Machine Tokens),
  each holding only the selected works' rows. Tools summary rows come from the
  same SQL and record builder as the rollup (`toolUsageRecord`), run over a
  temporary cube that excludes every other work. Human Words, Carbon Impact,
  MCP, search, and settings need the live library and are left out.
- **Privacy:** the payload is whitelisted (`sharedWork`, `sharedFieldNames` in
  `share.go`), so a new catalog column stays out of shared files until it is
  listed. Message evidence locators and the sources of copied spans (which name
  other conversations) are removed, as are the Library's `location` and
  `owner`. Transcript text, raw events, tool output, and file paths are kept.
  Each tool call's dialog keeps the first 3,000 characters of its input and
  result; the transcript holds the rest.
- **Inert:** a `Content-Security-Policy` meta tag (`default-src 'none'`) means
  the file makes no network requests.
- **Failures:** the page first requests the same URL with `check=1`, which
  builds the export and returns JSON, so a failure shows as a message instead
  of being saved as the file (`shareDownload` in `ui.py`).
- **macOS wrapper:** `Content-Disposition: attachment` responses become a save
  panel that replaces an existing file when confirmed (`WKDownloadDelegate` in
  `PharosApp.swift`). `tools/dev-ui.sh`
  proxies API calls to the installed service, so Share needs a build that has
  the endpoints.

## Preservation, TL1 release, and scheduling (mothballed)

Reclamation is mothballed. The Upcoming tab, protect/snooze controls, and the
`upcoming`/`protect` commands are excluded from the default build, and the Go
service refuses `preserve`, `reclaim`, `reconcile`, `tick`, and `worker`. The
preservation package format, release state machine, and idle scheduler survive
in the Python reference implementation and the owner contract
([`docs/tl1-contract.md`](tl1-contract.md)).
[`docs/reclamation/README.md`](reclamation/README.md) lists every parked
piece and the steps to resume. The launchd templates under
[`macos/launchd`](../macos/launchd) belong to that parked scheduler.

## Native macOS wrapper

The quick-start launcher handles initialization, the absolute CLI path required
by Finder, building, and opening the app:

```sh
./launch.sh
```

Use `./launch.sh --no-open` to initialize and build without opening the app.

The SwiftUI wrapper runs from Applications. It starts the application broker (`pharos app`), which starts one loopback-only Go service per connected library, and presents the selected library's authenticated interface in WebKit. Libraries (catalog, configuration, staging data and preserved archive) are separate folders on this Mac or on a drive, outside the app bundle.

### Code signing

`macos/build-app.sh` (and therefore `launch.sh`) signs what it builds: first the
embedded service (`local.pharos.service`), then the bundle
(`local.pharos`), both with the hardened runtime. The build fails if
`codesign --verify --strict --deep` rejects the result.

| Variable | Effect |
| --- | --- |
| `PHAROS_CODESIGN_IDENTITY` | Keychain signing identity (name or SHA-1). Unset: ad-hoc signing. |
| `PHAROS_HARDENED_RUNTIME=0` | Sign without the hardened runtime, for example to attach a debugger. |
| `PHAROS_UNIVERSAL=1` | Build arm64 and x86_64 slices with `lipo` instead of the native architecture only. |
| `PHAROS_VERSION` | Set both bundle version fields to a numeric `major.minor.patch`; defaults to `0.2.0` for local builds. |
| `PHAROS_CODESIGN_TIMESTAMP=1` | Request a secure signing timestamp, required for Developer ID notarization. |
| `PHAROS_ENABLE_UPDATES=1` | Enable Sparkle production updates; requires Developer ID signing. Set by the release script, disabled for local builds. |

The local [release workflow and in-app updates](releases-and-updates.md)
use these settings to publish Developer ID signed, notarized GitHub releases
and a signed Sparkle appcast, exclusively on the configured deployment Mac.
Development builds/tests use no release private keys or Keychain access.
Ad hoc releases are explicit and never enter the
production update feed. Sparkle is embedded in the Swift wrapper; the Go CLI
holds a shared per-install lease so app replacement waits for all users of
the old executable to stop.

A stable identity matters when Pharos runs from an external drive. macOS asks
before an app reads files on a removable volume and records the answer against
the app's designated requirement. The service is started by the app, so its
file access counts as Pharos's. An ad-hoc signature's designated requirement is
its cdhash, which changes whenever the code does, so a rebuilt Pharos can be
asked again or silently lose access. With a keychain identity, the requirement
is the bundle identifier plus the certificate, which survives rebuilds.
Permissions are still stored per Mac, so expect one prompt on each Mac.

Without an Apple Development or Developer ID certificate, run
`macos/create-signing-identity.sh` once. It creates a self-signed identity named
"Pharos Local Code Signing" in your login keychain and trusts it for code
signing only (macOS asks for your password). Then build with
`PHAROS_CODESIGN_IDENTITY="Pharos Local Code Signing"`. A self-signed identity
is for your own Macs only: Gatekeeper rejects it for downloaded copies, the key
exists only on the Mac that created it, and replacing the certificate changes
the designated requirement. The script header lists the details. Builds are not
timestamped or notarized.

To inspect a signature:

```sh
codesign -dvvv dist/Pharos.app                           # Identifier, Signature=adhoc or Authority=…, flags=…(runtime)
codesign -d -r- dist/Pharos.app                          # designated requirement
codesign -dvvv dist/Pharos.app/Contents/MacOS/pharos
codesign -d -r- dist/Pharos.app/Contents/MacOS/pharos
codesign --verify --strict --deep --verbose=2 dist/Pharos.app
```

An ad-hoc build reports `designated => cdhash H"…"`. A build signed with the
self-signed identity should report
`designated => identifier "local.pharos" and certificate leaf = H"…"`.

### UI feedback

The app includes a dependency-free visual annotation tool. Choose **Annotate** in
the bottom-right corner (or press Option-A), hover to identify an element, click
it, and add a note. While hovering, press **Up Arrow** to select progressively
higher parent elements or **Down Arrow** to return toward the original child.
Drag the grip or heading to move the annotation toolbar, feedback panel, or note
composer. The target label, numbered annotation markers, and status messages can
also be dragged out of the way. Focus a grip or marker and use the arrow keys to
move it (Shift moves one pixel at a time). Positions survive refreshes and stay
within the window when it is resized; markers keep their offset from the
annotated element as the page scrolls.
**Feedback** shows the saved annotations and copies a
structured Markdown report containing selectors, bounds, visible or selected
text, computed styles, view context, and a source-file hint suitable for pasting
into a coding-agent conversation. Saved annotations and the note currently being
composed survive app refreshes. They remain local in WebKit storage and are never
uploaded by the archive service.

## Development and verification

For new interface icons, follow the [icon drawing and usage guide](iconography.md).

### Nested page layouts

Analyze, Optimize, and Settings share the sidebar and mobile menu in
`web/src/app-navigation.tsx`, driven by `app-navigation-model.ts`.
Optimize groups the Findings dashboard, table, and settings under Findings. Findings settings uses
`/findings?view=settings`; old `/settings/optimization` links redirect there.
Page content can use `web/src/nested-pages.tsx` for its shared layout and heading.
A `NestedPage` tree supplies labels, destinations, descriptions, and stable
page IDs. The selected page's ancestry determines breadcrumbs and which menu
branches open; the tree can nest further without adding another renderer.
Use `NestedPageLayout` for another area with this shape, and `PageLink` for
its links. Plain local clicks call the existing `pharosNavigate` app router;
modified clicks, downloads, and external links retain browser behavior.
`useLocationRevision` subscribes to the host router's notifications, including
Back/Forward. Changing pages closes the mobile menu and moves keyboard focus
to the destination heading.

Settings panels in `ui.py` declare their route, title, and description with
`data-settings-route`, `data-page-title`, and `data-page-description`. Both the
classic router and `settings-workspace.tsx` read this registry, so a new panel
does not require a second menu or route list. The Settings adapter places the
existing content nodes inside the shared layout once, preserving classic
event handlers, form state, and child React roots. Panel scripts retain data
loading and controls; the shared layout owns navigation and headings. Keep
page-specific charts and forms outside the navigation component.

Test commands, test tiers and the rules for writing tests are in
[AGENTS.md](../AGENTS.md). Before committing:

```sh
tools/setup.sh      # install pinned npm packages and generate embedded assets
go test ./...        # go test -short ./... is the fast tier
go vet ./...
(cd web && npm run check)
```

`npm run check` runs the typecheck, the schema and recipe-snapshot checks,
the frontend build, `check-bundle` and the JS unit tests.

`./launch.sh --no-open` builds and installs the app for a per-user setup; it
is not a test.

The Go tests cover stable cross-language IDs, source configuration, canonical
ingestion, API compatibility, the query-table allowlist/filter/sort/pagination/
aggregation contract, semantic search vectors, and native Conductor
repository/PR/tool/sub-agent extraction. `macos/build-app.sh` runs
`tools/setup.sh`, which requires Node 24/npm, reinstalls `web/` from its
lockfile, checks the generated schemas and builds the frontend. The generated
`internal/archive/assets/query-tables.{js,css}` are gitignored and explicitly
required by `go:embed`, so a clean checkout needs setup before Go compilation.
`npm run check-bundle` rebuilds into a scratch directory and checks the local
bundles byte for byte; `npm run check` builds them first. Browser and e2e test
runs also build them before loading or embedding them.
Use [`tools/update-branch.py`](../tools/update-branch.py) to merge master and
verify the merged source before committing; see
[branch updates](query-table-local-development.md#updating-a-branch).
`go vet -tags reclamation ./...` keeps the parked Go reclamation queue code
compiling. The TL1 hook must maintain
its own disposable integration tests because its repository concurrency
protocol is owner-specific.

`go.mod` pins the Go toolchain (`toolchain go1.26.8`); an older local `go`
downloads it automatically through the Go module proxy.

### Trying UI changes against a real library

For backend and frontend development against an isolated production snapshot,
run `tools/dev.sh` (macOS, APFS SSD, Python 3.11+, Go, and npm). Every launch
builds the checkout, snapshots `/Volumes/euclid/Pharos` into the single shared
`/Volumes/euclid/pharos-dev` directory, and starts the local backend on 8798
and watched frontend on 8799. SQLite's online backup includes committed WAL
contents and reads production without migrations. Captures and preserved files
are separate APFS copy-on-write copies. Capture locks prevent copying while a
capture is changing those files; stop the capture and retry if it is busy.
The old dev copy is retained until preparation finishes. Each launch replaces
all dev edits with a fresh copy of production.

The generated dev configuration has its own API token, paths, and executable.
Host source files (`hosts/*.toml`) and shared source declarations are retained,
so existing Macs remain configured. Library-local source paths are relocated
to the dev directory; paths to agent data on the Mac remain read-only sources.
Integration credentials are not inherited from the production root configuration
or environment. Automatic source sync is disabled in the copied catalog.
Normal backend maintenance still runs against the dev database. Stop with Ctrl-C; all three
dev processes stop together. The shared destination is locked for the entire
run, so another workspace cannot refresh it while it is in use.

```sh
tools/dev.sh
# Optional overrides, and browser-free startup:
tools/dev.sh --source /Volumes/euclid/Pharos --destination /Volumes/euclid/pharos-dev --no-open
```

Frontend edits rebuild automatically; refresh the browser to see them. Go edits
require stopping and rerunning the command, which takes a new snapshot.
`--port N` and `--ui-port N` override the backend and frontend ports. Initial
database copying needs approximately the catalog's size in free space (currently
65 GB), and subsequent refreshes temporarily retain both database copies.
This script refuses to replace a destination it did not create. Its safety
checks can be run with `python3 -m unittest discover -s tools -p 'test_dev.py'`.

`tools/dev-ui.sh` serves this checkout's UI on <http://127.0.0.1:8799/> against
the Pharos service already running for the library at `PHAROS_LIBRARY`
(default `/Volumes/euclid/Pharos`; set `PHAROS_CONFIG` for a per-user
`archive.toml`) and opens it in the browser; `--no-open` only prints the
sign-in URL, and `--port N` moves it. Rerunning the script first stops any dev
UI running from a checkout of this repository, including other worktrees, then
opens the UI in a new tab. Pages and `internal/archive/assets/*.js` are read
from disk on every request; refresh the browser to load saved changes. The
script also runs `npm run watch`, which rebuilds `query-tables.{js,css}` as
files under `web/src` are saved (`--no-watch` skips it). Every `/api/` call
goes to the running service with its token; the carbon responses use this
checkout's factors and sources so comparisons reflect the branch. The branch
never opens the catalog, but buttons act on the real library. A UI change that
needs a new or changed endpoint needs the branch's service instead. The tab
title reads "Pharos (dev)".

The concurrency visual prototype reuses an existing dev snapshot without
refreshing it or upgrading its catalog. The timing reader opens SQLite read-only;
the exported personal data stays in `.context/` and is served only by an opted-in,
authenticated dev UI. With the snapshot's backend already running:

```sh
python3 tools/concurrency-prototype.py \
  --catalog /Volumes/euclid/pharos-dev/catalog/catalog.sqlite3 \
  --output .context/concurrency-real-data.json
go run ./cmd/pharos --config /Volumes/euclid/pharos-dev/library.toml dev-ui \
  --assets internal/archive/assets --port 8801 \
  --concurrency-data .context/concurrency-real-data.json
```

Open `/analyze?analyze_view=tokens&parallelism=1` in that dev UI, or choose
**Parallelism…** beside the pricing prompt in the Machine Tokens header. The URL opens a compact
modal with one selectable view. The summary's **Parallelism** link opens it over
the current page; adding `parallelism=1` to any page URL opens the same modal,
and closing it preserves that page and its other URL parameters.
Graph, counting unit, gap, date range, repository, selection, zoom, and conversation-list choices
persist in the library's interface preferences.
Range offers individual days, the last 3/7/30 captured days, all captured time,
and an inclusive custom From–Through range. Presets end at the latest captured
day and require enough timing coverage. Dragging shows the selected interval
and its duration before zooming; Escape cancels a drag.
The info button beside View opens calculation definitions and timing coverage
in a separate modal. Conversation rows start with the repository, followed by
the title and timing columns, with the open link last; selecting a ribbon or
title highlights its workspace or session, according to Count by.
The header's Usage summary includes **Parallelism**, using the saved counting
unit, gap and repository for each full trailing window, including idle time. Timing windows
outside the prototype export show unavailable instead of treating seven days as all time.
The legacy `/analyze?analyze_view=concurrency` link redirects to the modal.

Ribbon rows prefer continuity: continuing workspaces keep their row while it
fits within the active count, and arriving checkouts reuse rows last occupied
by the same repository when available. Simultaneous arrivals are grouped by
repository. Shrinking stacks compact to exactly the active workspace count;
zoom crops the selected range's layout without reassigning rows.

Count by selects **Workspaces** (one unit per checkout) or **Sessions** (one unit
per independently started root conversation, including overlapping conversations
in the same checkout). It applies to both Threaded and Model activity, the chart,
its statistics and the header summary. Source mirrors are removed; linked
delegated compute is folded into its root conversation and workspace.
Model activity is an input-to-response estimate,
excluding observed tool waits; it is not measured inference telemetry. The user
view bridges gaps only within a root conversation, up to one hour. Both use a
fixed one-hour idle cutoff for working time and its 2+ workspace or session percentage.
Average parallelism divides workspace or session time by the full selected date range
(24 hours for an ordinary day), or by the full interval when zoomed, including idle time.
The reader defaults to the last seven captured days and the `America/Denver` timezone;
`--days` and `--timezone` override those defaults.


Historical preservation jobs (`capture_history*.go`) keep their fixed-cutoff
inventory, pins, receipts and cursors independently of the catalog. They reuse
native SQLite row evidence and immutable filesystem inventories; typed degraded
recovery envelopes rescue catalog-only entities. Fresh rebuilds use production
parsers/writers before opening a separate rollback oracle for comparison. See
[the operator contract](capture-history.md); this facility does not run on
startup or authorize production migrations.
