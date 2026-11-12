# Pharos technical overview

Pharos is a local-first macOS library for finding past work across TL1, Conductor, Codex, Claude, Google Antigravity, and supported desktop exports. A second capability, preserving and reclaiming **TL1-owned** workspaces through a custody-aware owner hook, is mothballed; see [`docs/reclamation/README.md`](reclamation/README.md) for what remains and how to resume it.

The primary implementation is Go 1.26 with SQLite FTS, a local concept index, an authenticated loopback API, a read-only MCP server, and a responsive desktop UI. The Go service and UI are embedded in the macOS `.app`; the destructive TL1 workflow is deliberately held back.

The [portability guide](portability.md) maps the shared/native boundaries,
compatibility rules and regression checks. This groundwork prepares future
ports; macOS remains the supported desktop target. Native Windows/Linux hosts,
distributions and a Web product are deferred.

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
The native window's page-recovery probe adds `?routing=1`, which reports busy
while catalog initialization holds the broker's routing lock, even before the
window learns the selected library. It waits and reloads automatically once
routing is available, instead of exhausting retries against broker liveness.
Catalog opens reuse a completed initialization for the same executable, schema,
and Library projection, while still registering the current Mac. A new build or
an older catalog runs the full migration and backfill pass before serving pages.

Managed sessions buffer disposable navigation and derived view state in memory
when catalog writes are busy. Pending values are visible to subsequent page
loads and are coalesced before a low-priority writer persists them; the bounded
buffer can be lost when the service stops. Explicit history removals, feedback
annotations, drafts, saved queries and settings still use durable writes. Ask's automatic page visits
also coalesce and retry from the current history; explicit history removals
retain their ordering.

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
- Every free-space check that admits a write (capture copies and compression, backups and cloud archives, adoption, catalog compaction) reads the space the destination's file system offers Pharos through `internal/platform/fsinfo`, and refuses the write when that reading fails or its sum would overflow; a failure is never taken as zero or as plenty. The space available is what the caller may use (`f_bavail` on macOS and Linux; `lpFreeBytesAvailableToCaller` on Windows, where a quota can make both it and the reported total smaller than the disk). Writing nothing needs no reading. Displays (Storage, Library drive, a history plan's free bytes) still show an unreadable capacity as 0 bytes, a known limitation.
- Three native operations have macOS/Linux helpers and refuse elsewhere with an error matching `errors.ErrUnsupported`: reading a file's Finder info for the legacy alias flag (`finder_info_*.go`; a failed read never marks a file launchable, and bookmark aliases are detected in common code), opening cloud setup files and downloaded archives with `O_NONBLOCK` (`input_open_*.go`; elsewhere the open is refused rather than replaced by a stat and a blocking open, and callers still check the opened handle is regular), and replacing the MCP process after a memory fault (`process_replace_*.go`; elsewhere the restart fails through its existing path and the process keeps answering).
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
- **Sources on this Mac** lists every configured source, path availability, coverage, last index attempt and successful index, errors, and what its kind of source never records or records only sometimes (see [content capabilities](#content-capabilities)). Each card has an enable switch in its header. It offers **Find sources on this Mac** and links to Settings → Background Processing, which shows what runs Frequently, Hourly and Daily and has **Capture & index** (capture this Mac, index captures from every Mac, then any pending library upgrade) and, separately, **Capture this Mac** and **Index captured files**. The header's **Index changes** button incrementally indexes this Mac's sources, then refreshes GitHub, identities, Git integration, Tools, Human Words, and findings using live inputs by default and retained inputs only when explicitly enabled. Acquisition rollout is staged; see the [source acquisition matrix](incremental-sync.md#source-acquisition-rollout).
- **Library drive** (in the header) names the drive holding the library and what is holding or writing the library (capture, index, backup, Git lookups, Library view indexing), with progress and estimated completion times based on recent successful runs. In the app, **Eject** at the right of the panel's title releases the library, stops agents' Pharos MCP servers running from the drive, quits, and ejects it. See [Library status and Eject](configuration.md#library-status-and-eject).
- **Macs and captures** (in Settings → Sources) lists this Mac and every Mac whose captures are in the library, with when each source was captured and indexed and which need indexing. **Open Capture & index** links to the capture and index controls and their progress. A newly added Mac is captured right after onboarding, then offered **Index now**.
- **Library drive** checks and **Backups** (in Settings → Health) show the drive's encryption, Spotlight, file system, free space, volume pin and backup age, each with what to do and a copyable command, and back the library up to a folder on another drive.
- **Settings → Backups** also provides the local backup controls and a cloud backup wizard: a six-step calculator/configuration/commands/test/first-backup/test-restore flow, an opt-in compact wizard with GCP defaults, four primary calculator controls, collapsed detailed options, linked regional settings, and a calendar-month cost graph comparing linear growth with a flat library using a measured full-backup compression sample, database-saved configurations, and exact copyable provisioning commands and prompts. AWS/GCP use native revision-count retention; Azure uses previous-version age expiry. The selected design stores restricted upload credentials in the Pharos database and uses provider-managed encryption, with cloud-account access for restore. Optional password protection uses age public-key encryption with a password-encrypted recovery bundle; setup asks for the password only in Terminal, with separately copyable preparation and provisioning blocks. The Test, First backup and Test restore steps use server-side credential import, restricted-access probes, real provider uploads and recorded version IDs. The backend makes consistent full snapshots, embeds recovery bundles, and validates downloaded archive digests, file manifests and SQLite integrity in isolated folders. Protected verification prompts only in Terminal; `pharos cloud-restore` also recovers without the original database. Daily, weekly and every-30-day schedules persist in SQLite and activate after a verified full restore. They bind to a Mac and library path, catch up once after downtime, serialize destination uploads, and use bounded retries. Settings offers pause/resume, schedule editing, repeat manual backups and version selection for restore tests. The cloud entry carries a Beta chip. Durable upload restart-resume journals remain future work. See [Cloud backups](cloud-backups.md) for pricing assumptions and the implementation plan.
- **Library drive** in the header shows live indexing progress and the latest completed index result, including updated workspace and conversation counts and any error.
- **Your writing** (in Analyze) estimates how much text you typed or dictated into agent chats. Each user message is split into typed text, harness instructions, one-click templates, attachments, agent output copied from the previous 48 hours, re-sent text, likely pastes, and prompts sent by scripts or other agents. A query table lists one row per conversation, sortable and filterable by each kind of input and by tokens and cost, so "deepest conversations" is one click; the chart and breakdown above it follow the filters. The chart splits words by kind of input, repository, app, or provider; its key and a dragged time range filter the table the same way as on Tokens, and a time filter counts only the words sent inside it. A second query table lists one row per user message, filterable by the categories and rules that labelled its text, and shows messages either as rows or as text highlighted span by span, each span explaining on hover why it counted as what. The transcript reader shows the split for each message, and Settings has cards that open each Analyze view. Definitions are in [`docs/human-authorship.md`](human-authorship.md).
- **Analyze → Agent Experience** (preview, on by default and controlled in Settings → Background Processing) shows what agents ran into while they worked, found in their replies and thinking: obstacles, surprises, discoveries, workarounds, unchecked claims and more. A query card opens matching events with links into the reader; the Agent Events dashboard compares rates per 1,000 agent messages. See [`docs/agent-events.md`](agent-events.md).
- **Analyze → Outcomes** shows what became of sessions' changes: spend by outcome label over time, rework rate by project, cost per kept change by model, and how many sessions reached each evidence tier. A library without Git draws all of it from transcripts. See [`docs/projects-and-outcomes.md`](projects-and-outcomes.md#analyze--outcomes).
- **Analyze → Spend by Branch** shows what agents spent on each branch of your repositories, turn by turn when a session switches branches, with how much spend has a recorded branch. See [`docs/projects-and-outcomes.md`](projects-and-outcomes.md#spend-by-branch).
- **Analyze → Carbon Impact** estimates the electricity and CO₂e behind your token usage, all time or the last 30 days, split by token type (uncached input, cache writes, cache reads, output) and model tier. Energy per token comes from published inference measurements, with cache reads charged far less than recomputed input; you choose the electricity grid, data-center overhead (PUE), and a low, central, or high estimate. It is an estimate, not a measurement: factors and sources are in [`carbon/`](../carbon/README.md).
- **Library upgrade** appears when a catalog indexed by an older version needs its derived data brought up to date: repository merges, token attribution, harness versions, loaded instructions, and the tool ledger, run in that order as one resumable background job. See [Upgrading an existing library](releases-and-updates.md#upgrading-an-existing-library).
- **Health** shows retrieval coverage/freshness, index size, and external-storage health. Retrieval-only is shown as an intentional capability.
- **Logs** (`/settings/logs`, `diagnostic_logs.go`) collects diagnostics by source: page errors the shell reports (`page-errors.js`, POST `/api/logs/page`), slow, failed and panicking requests (traced in `ServeHTTP`; the log pages' own reads are not), the service's stderr by run (the broker tees each service's stderr into `service.log` with start and exit lines, so it keeps the panics the service's own capture cannot), the app's output (`app.log`, written by `ServiceLog` in the Mac app), failed or interrupted operations from the operation journal, and recent macOS crash reports for Pharos and its web processes. `/api/logs/summary` marks the sources with recent errors or warnings for the sidebar and the menu bar's Logs submenu; `/api/logs/report` is the text the page copies for an agent. See [Logs and crash reports](configuration.md#logs-and-crash-reports).
- **Storage** (on Health, `/api/health/storage/breakdown`) splits the library drive's space into captured originals (latest copies, earlier versions, manifests) and Pharos's catalog (database, write-ahead log, caches), and tabulates both by Mac, source and repository. Sizes come from the drive checks' cached walk of the library, which counts each hard-linked file once. Originals by Mac and source are measured. Nothing records which repository a capture's or the catalog's bytes belong to, and measuring that would read the whole catalog, so repository shares, and the catalog's split by Mac and source, are estimated from each conversation's message count in its sightings (`storage_breakdown.go`). The drive checks read those counts when they walk the drive and keep them with the sizes, so the breakdown answers from memory; a sync's commits do not invalidate it, and the size-history row each measurement records does not hold the sizes back while the catalog writer is busy. The catalog's copy of a conversation belongs to the Mac and source it was written from; a merged copy is spread across every Mac that saw it.

Imported Claude chats without any authoritative usage receive versioned estimates in the existing usage and tool tables. `is_estimate` (or `duration_is_estimate`) distinguishes predictions; ordinary token/cost cells show `*`. Output tokens include thinking; missing input/cache usage stays unavailable, and priced output costs are partial. Reported usage, including zero, excludes the entire conversation and transactionally supersedes predictions. Tools processing backfills existing imports and refreshes changed assumptions without source reimport. See [shipping and backfill](../estimation/README.md#shipping-and-backfilling-existing-imports) and the [model evaluation report](claude-estimation-report.md).

Home, recent conversations, the menu bar and MCP also carry this provenance. MCP's default complete-quality metrics exclude partial estimates; available-quality queries include flagged predictions. Measured request/context analyses (including Model Requests, Session Shape and Findings) exclude response estimates because a response can span several real requests. The [reader policy](../estimation/README.md#consumption-and-measured-analysis-readers) covers raw storage availability and the remaining consumers.

Delegated work is retained as a recursive `agent_sessions` tree, including sub-agents of sub-agents and links back to each session's messages. Session and workspace usage preserve uncached input, cache reads, cache creation, output, reasoning, and any unclassified aggregate remainder separately so pricing can be applied without reconstructing provider envelopes.

Run diagnostics with `dist/Pharos.app/Contents/MacOS/pharos --config "$HOME/Library/Application Support/Pharos/archive.toml" doctor`. Configuration is documented in [`docs/configuration.md`](configuration.md).

`pharos labeling` prepares a read-only snapshot of conversation text as a versioned dataset for offline labeling by a large model, runs labeling jobs and exports training data; see [`docs/library-labeling.md`](library-labeling.md).

**Local labels** (on by default, controlled by `labels_enabled`) label every message's function (requests, steering, feedback, reports and so on, taxonomy `candidate-0.3`) on this Mac with the `kestrel-68m` encoder. Pharos builds each message's items with `labeling prepare`'s own code, tokenizes and frames them in pure Go, and runs the model in a separate worker process (`cmd/pharos-labeler-ort`, ONNX Runtime) over a small versioned stdin/stdout protocol, so the `pharos` binary stays pure Go. Labels are a derived ledger (`message_labels`) rebuilt after authorship in the analysis chain, incrementally, never by a page; see [`docs/local-labeler.md`](local-labeler.md).

Compact fingerprints in tables and conversation cards use background-generated
PNGs with a durable, versioned queue and deduplicated storage in the catalog.
The two profiles are 1024×128 and 1024×256, each with light/dark appearances.
Layout and rasterization run entirely in Go, with cross-language fixture parity
against the TypeScript model and the original PNG renderer.
Detailed timelines retain interactive rendering. See
[Compact fingerprint images](fingerprint-images.md) for processing, invalidation,
recovery and API details.

## Sources and identity

Each kind of source is one entry in `providerRegistry` (`provider_registry.go`). Its `providerSpec` declares the adapter, what capture copies and which of a database's tables it keeps, how retained inputs are acquired and named, where probe looks for it, whether setup offers it and in what order, whether background updates read it, its rank among mirrors, and where a person keeps its global instructions (which Fixes and Findings point at). Code that depends on a source's kind reads the spec rather than switching on the kind, so adding a provider is one entry plus its adapter (`TestAProviderNeedsOnlyItsSpec`). Parsers still name their own harnesses and channels.

### Source access

Adapters reach their source only through `sourceFiles` (`capture_fs.go`), so the same code reads a capture as it was captured and a live source as it is:
- **Files:** `Stat`, `Info`, `Open`, `OpenContent`, `ReadFile`, `Tail`, and listing (`ReadDir`, `WalkDir`, `Glob`). Under a capture view, a file stored compressed is read and sized as its original.
- **Databases:** `SQLite` opens the captured copy, decoded if needed, or the live file down the [locked-database ladder](#reading-a-locked-database).

`liveSourceAccess` names the few adapter functions that read the OS directly, each with its reason:
- the installed Antigravity version;
- Git lookups in the user's repositories;
- probe's counts of a location before it is a source;
- snapshot generations capture indexing has already materialized.

`TestAdaptersReadThroughTheAccessLayer` parses the adapter files and fails on any other direct `os`, `filepath`, `sql` or `exec` access.

Where probe looks is a function of the host alone. `resolveRoots(spec, hostEnv)` yields a provider's data folders, profile folders, applications and the folders its variables name, in the order probe prefers. `hostEnv` carries the home, environment, application folders and a listing function, so a test resolves any home without touching this Mac.

### Content capabilities

Each spec also declares what its records hold (`Content`, `provider_capabilities.go`), for each of:
- the folder and Git branch;
- the model, effort and speed (fast mode);
- per-request token usage;
- tool calls and results;
- thinking;
- message times;
- subagents;
- a title the tool gave;
- the tool's version;
- a cost the tool reports.

Each is supported, varies (with a note saying how), unsupported, or not applicable (a folder for a web chat). `GET /api/providers` serves the declarations, and `testdata/providers.json` keeps a copy for the JavaScript tests. The setup wizard takes its tools, their names, summaries, folder hints and usual locations from the endpoint (`Summary`, `FolderHint`, `SetupWith`, `OfferWhenFound` and `Export` in the spec), and its review says what each chosen tool doesn't record, with the whole table a click away. Each Settings source card lists what its kind never records and what it records only sometimes.

A conversation's `coverage` is a completeness word (`complete`, or `partial` when content it lists could not be read). Conductor once stored its extractor version there. That now has its own column, `conversations.extractor`, and the catalog upgrade moved existing values.

| Kind | Acquisition | Mutation capability |
| --- | --- | --- |
| `tl1` | Registered installations via consistent SQLite snapshots and native transcripts | None |
| `tl1-export` | Owner-produced canonical JSON | TL1 release contract only |
| `conductor` | Consistent read-only SQLite snapshot transaction and schema inspection | None |
| `codex` | Native rollout/session JSONL, including rollouts Codex compressed to `.jsonl.zst` (when both forms exist, the newer is read, and the plain one when they are as new) | None |
| `claude` | Native project session JSONL, with each session's subagents. Only the active branch is selected: a rewound or edited turn is kept, unselected, and its usage still counts. A reply Claude Code copied into a subagent, a sidechain or a resumed session counts once, and advisor iterations count under their own model ([token accounting](token-accounting.md#accounting-contract)) | None |
| `antigravity` | Per-conversation step transcripts (`transcript_full.jsonl`) with titles, workspaces, and subagent parents from `conversation_summaries.db`, and each model request's served model and token usage from the protobuf `gen_metadata` rows of `conversations/<id>.db`. Antigravity leaves its cost and credit fields unset, so cost comes from Pharos's price table | None |
| `copilot` | GitHub Copilot CLI sessions under `~/.copilot/session-state`: each session's `events.jsonl` with its `workspace.yaml` name and place, and an older release's flat `<id>.jsonl` for the same session, read with it and deduplicated by event ID (an event without one is known by its content). Prompts with what was attached as Copilot describes it (a selection's text, a GitHub reference's URL, number and title; a blob without its bytes), replies, reasoning (OpenAI models give summaries), tool calls with results (the content the model was sent, and a fuller `detailedContent` such as a diff beside it as `details.detailed_content`), exit codes and durations, and the system prompt as context. An MCP tool is named `mcp__<server>__<tool>` from the server and tool the request or, in older releases, the call's start records. A subagent's events, in its parent's log under an `agentId`, become a child conversation. Token usage is each request's `session.usage_record`, linked by `apiCallId` to the reply and tool calls it paid for; a request reported again, under its `apiCallId` or `providerCallId`, updates its one record. Whatever the last `session.shutdown` total holds beyond those receipts (all of it, from a release that wrote none) is one more usage record. `session-store.db` is Copilot's index of the logs and repeats usage rows, so it is not read. SDK apps and VS Code share this runtime and folder, so a session's channel is the client `workspace.yaml` names as its creator (`client_name`): `github/cli` is CLI, `vscode` and `vscode-agent-host` IDE, and any other or none unknown | None |
| `opencode` | OpenCode sessions in `~/.local/share/opencode/opencode*.db` (the 2.x `session_v2` schema): prompts, replies, reasoning, tool calls with results (a failure's error when it has no other content), exit codes and durations, in `seq` order, with content inline or in `session_part` (which wins when it has rows). What OpenCode adds to a prompt (synthetic, system and skill messages) is context, a command the person ran (`shell`) is a call with its result, and a compaction that completed is a boundary; one that completed or failed is a request with its own model and usage. A subagent's session (`parent_id`) is a child conversation in its parent's workspace, its prompts marked as the parent's, and the launch's result names it (`metadata.sessionID`). Token usage is each assistant message's `tokens` (input without cache, output without reasoning); what the session's totals hold beyond its messages and compactions, from requests an undo deleted, title generation and a summary made ahead that no compaction took, is one more usage record, and a change to the totals alone reads the session again. A fork (`fork_session_id`) leaves each message it copied from its source (`<fork event>_<seq>`, under the source's seq), tool runs included, to that source while the source holds that seq; one an undo or deleting the source removed stays as the fork's history without its usage, which the source may have been indexed with, and a change to the source reads the fork again; a deleted fork indexed from an older capture keeps its copies when a newer capture holds the source. A command run from a subagent's prompt stays the person's. Media in `blob` is captured with the sessions. A request's tier is the `serviceTier` OpenCode keeps from an OpenAI Responses reply (`native`); a fast mode chosen as its own model (`<model>-fast`) is not read from the name, which other models share. Credentials and settings in the same database are neither read nor captured. A database in the older 1.x schema is reported, not read, as are sessions a 1.x release wrote beside the 2.x tables after OpenCode moved its sessions (those newer than every moved one) | None |
| `openclaw` | Native OpenClaw session windows in `agents/<id>/agent/openclaw-agent.sqlite`, with plain or zstd events and verified file/SQLite cold archives (including chunk manifests), plus canonical reset/deletion generations in `session_transcript_archives` even before their derived files are published. Archived generations keep distinct identities when a physical session ID is reused; published derivatives are omitted while their database owner is present. Legacy session JSONL, compressed JSONL and reset/deleted transcript files are read with their `sessions.json` metadata, decoded once per directory per discovery pass. Message IDs and branch selection are preserved; workspace summary text uses selected messages, and explicit rewrites contribute usage once. Prompts, replies, tools, reasoning, model, thinking level, timestamps and native parent links are read when present. SQLite wins over a legacy copy of the same session. Each agent has its own identity namespace. Missing or damaged session payloads are reported without hiding other sessions or agents; preflight inventories file metadata without loading embedded cold blobs, and previously indexed damaged history is kept | None |
| `hermes` | Native Hermes `state.db`, including named profiles: messages, tool calls/results, recorded reasoning, session context and actual reported cost. Compression continuations form one conversation; fork/reset/delegate markers establish a boundary only when they name the immediate parent, so inherited markers do not split later compression segments. Explicit forks and resets stay separate, and an explicit delegate marker or tool session establishes a child agent. Native message IDs are preserved, including legacy text IDs. Compaction copies sharing native UIDs, display identities or the native legacy key (role, content, timestamp and tool fields) are read once, retaining the original content and the newest generation’s selection so compaction and rewind do not hide or revive turns. Synthetic `model_only` and hidden rows remain model context. Tool calls and results use native occurrence UIDs, including lists for reused provider IDs; provider IDs remain in the retained envelopes. Legacy calls without occurrence UIDs are distinguished by their durable message identity. Recorded reasoning precedes the reply. Model usage replaces overlapping session counters, preserves any unattributed residual and adds auxiliary usage; these are aggregate totals at their recorded reporting time, never fabricated individual model requests. Each profile has its own identity namespace | None |
| `cline` | Cline 4.x sessions under `~/.cline/data/sessions`: each session's `<id>.messages.json`, Anthropic content blocks (prompts, replies, thinking, tool calls with results and errors), with the system prompt as context, tools the provider ran (`metadata.modelToolActivities`) as calls with results, and its `<id>.json` manifest for title, place, branch, client and cost. The client is the session's channel (`cli`; `vscode`, `jetbrains`, `neovim` IDE; `desktop`; `web`) only when a person started it (`sessionHistoryOrigin.mode` `user`), not for an imported history or a scheduled run. Cline's `<user_input>` wrapper is removed from prompts; its mode notices, hook context (`displayRole` system) and the user-role messages it writes itself (its synthetic `kind`s, `userRunSpan` 0, continuation prompts) are context. A result's operation list (`{query, result, error, success}`) is read as its text, and a failed operation fails the call. Token usage is each request's `metrics`, on the assistant message that ended it; Cline's input includes its cache reads. A teammate's transcripts, legacy editor task folders and the compacted context are not read | None |
| `vscode-copilot` | GitHub Copilot Chat sessions in a VS Code user folder (VS Code, Insiders, VSCodium or Positron): each session's journal (`chatSessions/<id>.jsonl`, operations replayed onto the session, a last line still being written left for the next read) or an older release's `<id>.json`, and its window's `workspace.json` for the folder (a remote window's path is kept with the remote's name). Prompts, replies (also Markdown VS Code flagged as vulnerable, `markdownVuln`), thinking, tool calls with their input from Copilot's record of the model's rounds and their results, terminal exit codes and durations; a call shown again as it changes is one call. What was attached to a request (`variableData.variables`: pasted code, files, selections, symbols, text a command or setting supplied; not tool references or image bytes) is an `attachment` record. A request VS Code sent itself (`isSystemInitiated`, such as a background terminal's completion notice) is injected context, not a human turn; its reply and usage are kept. A call denied or skipped (`isConfirmed` false, or kind Denied or Skipped; VS Code saves every call as complete) is never a success from what VS Code showed: when the person decided, or no one is recorded, its result has status `declined`; when a hook or VS Code's risk assessment did (`isConfirmed.source`, or an older release's hidden denied call, which only a preToolUse hook made), `blocked_by` names it and the call counts as `hook_blocked`. An MCP tool (`source.type` mcp) is named `mcp__<server>__<tool>`, the server its label, the tool what follows VS Code's prefix (its server's name, dots made `_`). A compaction Copilot saved (`result.metadata.summaries`, or an older `summary`) is a `compaction` marker after the last call of the round it names, or before the request's prompt when that round is an earlier request's. An undone request (`shouldBeRemovedOnSend`, or an older `isHidden`) stays indexed but unselected, whole or from its undo stop (`afterUndoStop`) on (from the code fence before it when the stop precedes a code block that is not standalone); its usage still counts, and the session's purpose and outcome come from what is selected. A call the rounds do not name keeps the input, output and failure VS Code saved for it (`toolSpecificData.rawInput`, `resultDetails`). A subagent (`runSubagent`) is a child conversation holding its prompt, its own calls and its answer, on the model it ran on (`modelName`); one started by another subagent is that one's child. Token usage is per request: each model's totals (the `modelTotals` list) when Copilot reports them; otherwise the output over all rounds (`completionTokens`) and only the last round's input (`promptTokens`), marked `input_scope: last_round` since it undercounts. Copilot's own transcripts, its `agent-traces.db` and the editing sessions are not read | None |
| `cursor` | Cursor's chats (composers) in its user folder: each composer in `globalStorage/state.vscdb` (`cursorDiskKV`), its messages (`bubbleId:<composer>:<message>`) in the order its own header list gives, or from Cursor 3 its chunks of headers (`composerConvHeaders`), or in releases before either the messages it holds itself (a list, or `conversationMap` by ID). Prompts, replies, thinking, tool calls with their input and results (a command's printed output, with its exit code, whether it was stopped, declined or cancelled, as details), terminal exit codes and durations, errors. A subagent (`subagentInfo`) is a child conversation, its prompt marked as its parent's; a fork drops the copy of its parent's messages it starts with, and the copies of subagents that copy started are not read; a side chat is a child conversation with the person's own prompts. A message a composer's headers name that Cursor no longer holds makes the conversation `partial`, as does a lost or damaged chunk of headers, after which a fork's copy of its parent can no longer be told by position and is kept as the fork's; what was indexed from those messages is kept, and the rest is read as it is now. A deleted composer is read from an older capture with the rest of its group, so its copies are known and nothing newer is rolled back. The folder is the composer's own (`workspaceIdentifier`), else the window whose database lists it. Token usage is the token count Cursor kept on a request's last reply before Cursor 3; Cursor 3 keeps none, only the context meter, kept as `context_tokens_used`. Fast mode is a parameter of the chat's selected model (`fast`, or `speed` in newer releases); Cursor keeps only the chat's current setting, so it applies to all of its requests. Sign-in tokens, settings and each composer's encryption keys are neither read nor captured | None |
| `cursor-cli` | The Cursor CLI's (`cursor-agent`) chats in `~/.cursor`: each chat's `chats/<hash>/<chat>/store.db`, its messages in the order its latest root lists them (`meta.latestRootBlobId`, a protobuf blob of message blob IDs), and its `meta.json` (title, folder, last activity). Prompts, replies, thinking (encrypted in the reply, so read as text from the thinking step just before the step of the reply's next text or call, at the time it began; without one, only that it happened), tool calls with their results, exit codes, durations and failure flags (`isError` on the result or its outcome, or an `error` output), and the context meter, kept as `context_tokens_used`. Each prompt, reply and call is timed by the turns the root lists (field 8: the prompt's send time, each reply's start, each call's start and end), a reply sent again by its own step, and a step an older root's earlier copy of its turn lists read once; without one, a prompt by the time the CLI stamps on it, and nothing before a time already placed. A compaction's summary (`isSummary`) is a boundary, not a prompt. A Task subagent's chat (`meta.subagentInfo`) is a child of the chat that started it, its prompts that agent's. A message only an older root lists was rewound, and is kept but not selected. A blob is read only when it is what its ID (its sha256) names, and is a root only when it is neither a prompt (which holds a message ID) nor a prompt or step a turn names. A message whose blob is gone or unreadable, the latest root, any blob that is not what its ID names, or a message no root lists (its root deleted) makes the conversation `partial`; what was indexed from it is kept, and without its latest root the longest root left orders the messages. A message that is both unreadable and moved between branches keeps the selection it was indexed with. A message only older roots list is timed by the prompts before it in its own root, and is not summarized; a chat rewound to before its first reply keeps the outcome it had until it has a new one. The CLI keeps no token usage. The provider is `cursor`; the harness `cursor-cli`. Each chat's encryption key and the CLI's sign-in are neither read nor captured; the text copies in `projects/*/agent-transcripts` are not read | None |
| `zed` | The threads of Zed's own agent in `~/Library/Application Support/Zed/threads/threads.db`: one row a thread, its content a JSON document, zstd-compressed when `data_type` says so. Prompts (what a mention brought in is context, not the prompt), replies, thinking (or that it was redacted), tool calls with their results, and how a terminal command ended from the header Zed writes before its output (which it saves as a result that did not fail): the exit code of `Command "<the call's command>" failed with exit code N.`, a timeout, a stop by the person (interrupted) or an unexpected end as failures, else 0 when there is only output or none; what the output itself says is not read as an outcome. A compaction is a boundary that keeps its summary. Threads of releases before the agent, whose messages have roles and segments, are read too, a call's result found by its ID in its own message (release 0.1.0 keeps it on the user message after the call), since a provider can give later calls the same ID; a later call reusing an ID is told apart by its occurrence (`<id>#2`), so each is its own call. A thread's `speed` (fast mode, as Zed sends it to Anthropic or OpenAI) is its current setting, so it applies to all of its requests. A subagent (`parent_id`) is a child conversation in its thread's workspace, its prompt marked as its parent's; the work keeps that workspace and its name once Zed deletes the thread that started it. Usage is each turn's last request (`request_token_usage`), and what the thread's sum of every request (`cumulative_token_usage`) adds beyond them. Zed keeps no time per message: each takes the thread's start, and the thread ends when it was last saved. The folder is the thread's first folder, else its project's first worktree. Threads with external agents (Claude Code, Codex and others through ACP) are kept by those agents, and read as their own sources | None |
| `chatgpt-export` | User-provided ChatGPT export: the ZIP, the folder it unzips to, or one of its conversation files; newer exports split the conversations into numbered `conversations-NNN.json` files. Each conversation is a tree of messages: replies, the model's thoughts as reasoning (each thought's text, or its summary when it holds none; a "Thought for N seconds" recap is source metadata, not measured latency), o1's reasoning summary (written in older exports as a tool's reply that no call asked) as a reasoning summary, messages to a tool (code for Python, a browser search) as tool calls with the tool's reply as their result (a reply that runs on into more, such as each page a browser click quotes, gives each its call, and the call's output is all of them on one branch, the current one where it runs through them, never two alternatives; Python code that raised an exception, recorded in how its run ended, failed), and the images and audio a message holds as attachments. The custom instructions and memory ChatGPT adds, and other messages it hides from the person (such as its system prompt), are context, not human text. Only the branch ending at the current node (or, when the export names none, the newest message with none after it) is selected; regenerated and edited turns are kept unselected. Every node with text keeps its ID, so a read replaces what an older one indexed. A message without content makes its conversation `partial`, keeping what was indexed from it; a conversation not in ChatGPT's shape, or a file that can't be read, is reported, and the rest is read. The export records no token usage | None |
| `claude-ai` | User-provided Claude.ai export: the ZIP, its `conversations.json`, or the folder holding it. Each chat message's blocks become messages (text, thinking summaries as reasoning, tool calls and results, attachments); the date, memory and tool notes Claude.ai adds to a person's turn are context, not human text. Only the branch ending at the current leaf (or, when the export names none, the newest leaf) is selected. The inspected exports name no model and record no token consumption; explicit assistant usage is retained when supplied. Supplementary project, memory, reflection, artifact and cloud files become searchable archive documents, with cloud transcript roles preserved. See [the export audit](claude-export-audit.md) | None |
| `claude-ai-settings` | Claude Desktop HTTP conversation-list cache, filtered to organization/conversation identity, selected model, effort, thinking mode and observation provenance. Immutable projections are captured before ingestion; cache eviction does not retract them. These metadata records populate `claude_chat_settings_observations`, creating no workspaces, conversations, messages or execution usage. Reader joins require matching configured account and conversation UUID and preserve organization separation | None |
| `canonical` | Documented interchange JSON | None |

Claude.ai export collection is shared by the onboarding UI, authenticated local
API and `claude-import` CLI. `claude_import.go` journals verified parts and
credential-free snapshot inventories; browser download capabilities stay in
host-local state. `claude_export.go` resolves captured inventories, validates ZIP
layouts and nested artifact checksums. The existing adapter streams conversation
shards, validates a snapshot before publishing its records, and preserves account
UUID metadata without changing persisted identities. Capture retains every
collected category for offline reindexing. See [Claude export collection](configuration.md)
for intake, repair and watched-folder usage.

### Provider conformance

`TestProviderConformance` (`conformance_test.go`) reads each provider as production does. For each provider, it:
1. builds a synthetic source;
2. captures it;
3. deletes the source;
4. reads the records back from the capture alone.

It fails on any of these:
- **A capture that reads differently from the live source:** after paths are normalized, the two reads must match.
- **A credential the capture holds:** each case lists the secrets its fixture writes. The scan finds a secret as written, as hex or base64, inside a zstd or gzip stream, or in a database value.
- **Records that differ from the golden file**, `testdata/ingestion/<kind>.json`.
- **A record the index does not write.**
- **What the records hold against what the provider declares:**
  - something it records but declares unsupported or not applicable;
  - something it declares supported or varying that the fixture never shows.

A second read of the same capture must give identical records. Each fixture is also read in a second new folder. Between the two folders, only the fields `conformancePathHashes` names, each with the reason its hash includes the fixture's path, may differ; the golden file shows them as `$PATH_HASH`. Any other difference fails as a read that is not deterministic. A declared capability a fixture cannot show is listed with its reason in `conformanceGaps`, and an entry the fixture comes to show fails.

`PHAROS_UPDATE_GOLDEN=1` rewrites the golden files. It refuses to change a provider's records while the provider's index version stays the same, because libraries would keep the records they already hold. `PHAROS_UPDATE_GOLDEN=same-version` writes them anyway, for a change stored records need not pick up.

Each conversation records its agent harness and first and last observed harness versions. Claude Code takes `entrypoint` and `version` from JSONL events; Codex takes `originator` and `cli_version` from every `session_meta`, including resumed sessions. Conductor keeps its own harness identity and inherits a linked native conversation’s version with source `alias`. For Antigravity on this Mac, Pharos records when it first observes each installed app or CLI version and attributes that version only to later conversation activity. Older conversations and captures from another Mac keep the version empty when the transcript cannot establish it. Version sources distinguish transcript evidence, native aliases, and the installed app. Existing catalogs fill these fields during the next identity reconciliation, using retained Claude messages and Codex source or captured files.

Each Claude Code and Codex conversation also records the instruction files and skills its harness loaded, in `conversation_instructions` (`conversation_id`, `harness`, `path`, `kind`, `bytes`, `hash`, `repo_path`, `first_seen_at`, `last_seen_at`). Claude Code's `instructions` attachment lists each loaded file with its type and content: `Project`, `User`, `Local`, `AutoMem`, and `Managed` become the lowercased kind, a `nested_memory` attachment (a subdirectory's `CLAUDE.md`) is `nested`, `invoked_skills` is `skill`, and each name in a `skill_listing` is `skill_listing`. Codex's `# AGENTS.md instructions for <dir>` message and `world_state.agents_md` become `agents_md` rows for `<dir>/AGENTS.md`, and each entry of its `<skills_instructions>` listing is a `skill_listing` row for the skill's `SKILL.md`. `bytes` and `hash` (SHA-256) are of the content last loaded, empty when the transcript names a file without it; `repo_path` is the path relative to the repository checkout, empty for agent-home files such as memory and bundled skills. Ingest replaces a conversation's rows whenever it writes the conversation; Conductor wrappers carry none (the native conversation does), and TL1 runs get theirs from the Claude or Codex transcript. Antigravity transcripts record nothing about loaded rules. Libraries indexed before the record existed fill it in the library upgrade, from source files or captures. The record is what instruction drift and the context an instruction change adds are measured from ([`docs/auto-optimization-design.md`](auto-optimization-design.md), G7).

### Reading a locked database

A harness's SQLite database is opened read-only (`openSourceSQLite`). When the harness keeps it from being read in place, the read steps down a ladder:

1. **In place:** `mode=ro` and `query_only`, waiting up to five seconds for a lock.
2. **Without locks** (`immutable=1`): only on a volume whose native mount flag says it is mounted read-only (`MNT_RDONLY` on macOS, `ST_RDONLY` on Linux), which nothing can write, when SQLite cannot create the shared-memory file it needs to read a WAL database and nothing is waiting beside the database to be written into it (no WAL frames, no hot journal). A mount whose flags cannot be read, and any mount on a platform without that observation (Windows), is not taken as read-only: the read takes the next step.
3. **From a copy:** otherwise, for a WAL database, the database and its WAL are copied to a private folder (`0700`, in the Mac's temporary folder) and the copy is read with its WAL folded in. The original must be unchanged across the copy: its identity, size and time, its header's change counter, and its WAL's size, time and salts. The copy folder must be on the same disk as the original, so a copy, which holds everything the harness keeps (credentials included), never lands on a library drive. One pass over a source (an index, or an adapter's discovery or group read) copies a database once: its discovery and every conversation group it reads open that copy, without waiting for the lock again, and the copy is removed when the pass ends. The copy is made after the pass's discovery began, so a group read from it is never older than what was planned for it, and a write since shows as a change the next pass reads. Outside a pass, a copy is removed when its database closes; one a crash left is removed by the next process that opens a harness's database (each copy's folder is named for its process, and a copy is removed once that process has ended or its ID belongs to a process started later).

The read waits instead, failing as busy so it is tried again on the next update, when no copy would be consistent:
- **A hot rollback journal** (one SQLite has not zeroed) means a write is half done in the main file.
- **A rollback-journal database held locked:** nothing on disk tells an idle holder from one part way through a write. It can spill a transaction into the main file without journaling it (`journal_mode` `MEMORY` or `OFF`), and in exclusive locking mode SQLite stops counting its commits. A WAL database's holder writes only whole commits to the main file, by checkpoint, and its uncommitted frames are ignored when the copy is read, so a WAL database is copied.

A source whose last read was not in place says so on its Settings card (`sqlite_reads` in `GET /api/sources`) until it is read in place again. Pharos's own catalog, its backups and its captures are opened with `openReadOnlySQLite`, never copied.

Deduplication uses account-scoped native IDs. Conductor aliases are retained as evidence-backed identity links after all adapters run; repeated identical prompts are never merged by content hash. The Conductor extractor inspects each row and selects the materially populated `content`, `full_message`, or `text` field, recording that decision in its evidence locator.

ChatGPT managed ZIP collection, media/reference presentation, source omissions and raw-only data are documented in [remaining export gaps](chatgpt-export-gaps.md) and [unmodeled export data](chatgpt-export-unmodeled-data.md).

No claim is made that ChatGPT desktop’s local cache is complete. ChatGPT is indexed from a user-provided supported export, and its health row makes that acquisition boundary visible.

### Projects

Every dashboard, table and MCP filter groups work by project, so a library
without Git groups as completely as one with it. A workspace's project is its
repository; else the one repository with a checkout at or above its folder
(`folder_repository_id`); else its working folder (`project_folder`, a home
directory shown as `~`, temporary directories as one `/tmp` project); else
none, as for a chat export. Ingest, repository merges and capture recovery
resolve the two workspace columns, and requests only read them through
`projectJoin`/`projectNameSQL` in `projects.go`. Datasets expose
`project_name` beside `repository_name`, and analysis catalogs read old
`repository_*` breakdown links as their project counterparts
(`web/src/project-analysis.ts`). The rule, the tiers of outcome evidence
built on it and what Pharos does without Git are in
[projects-and-outcomes.md](projects-and-outcomes.md).

Session outcomes (`outcomes.go`) are Tier 1 facts every harness that records
tool calls gives: the files a session edited, verification after its last
edit, commits and pull requests printed in its tool output, and rework, the
share of its added lines later sessions removed within the
`outcome_rework_days` window (`outcome_lines.go` keeps lines only as
fingerprints). The tools stage derives them after the tool ledger for the
sessions `session_outcome_dirty` marks, into `session_files` and
`session_outcomes`; the Library reads the label from them
(`outcome_label`, `outcome_why`, `web/src/outcomes.ts`).

Tier 2 (`outcome_git.go`) links those sessions to commits in each clone of
their repository this Mac recorded, from a hash the session printed or inferred from its edit window
and files (a commit several sessions could have made goes to the one whose
checkout made it or whose kept lines it adds, `outcome_git_signals.go`),
and marks which reached mainline or were reverted. It runs only
read-only git, at low priority, after the tool summaries in background
stages (never on a request), and skips a repository whose refs and sessions
are unchanged (`git_repository_scans`). It stores facts, not content, in
`git_commits`, `session_commit_links` and `session_git`, and a scan keeps
another Mac's links to commits this Mac's clones lack; the Library reads
`git_check`, `linked_commits` and `landed_commits`, and a repository Git
could not check reads "Not checked" with the reason.

The Files page (`file_history.go`, `file_tree.go`, `work_files.go`;
`/files`, `/api/file-history`, `/api/file-tree`, `/api/file-commit`,
`/api/work/{id}/files`) links files, sessions and commits both ways: the
sessions that edited a file (`session_files`), each edit's call
(`session_file_edits`) and the rework between them (`session_file_rework`),
and in a checked repository its commits (`git_commit_files`) and the tree at
the mainline tip (`git_tree_files`), both filled by the Tier 2 scan. Library
file search reads `session_files` beside `change_files`. A file's Changes tab
(`file_changes.go`, `/api/file-changes`) turns each edit's call input into a
diff. Code, blame, history, commit diffs and a directory's last commits
(`file_git.go`; `/api/file-content`, `/api/file-blame`, `/api/file-log`,
`/api/file-diff`, `/api/file-commit-git`, `/api/file-last-commits`) are the
only requests that run Git: read-only, bounded, two at a time, and only in
the checkout the person chose for the repository on this Mac
(`repository_checkout.go`, `repository_checkouts`,
`/api/repositories/{id}/checkout`); nothing they read is stored.

Analyze → Outcomes reads the `outcomes` dataset (`outcomes_table.go`): one
row per Library session, with its label, evidence tier, branch, main model
and the cost the Library gives it, counted at its start. Its catalog
(`web/src/outcomes-analysis-catalog.ts`) disables, with the reason, a
breakdown needing a tier no session reached.

Spend by branch (`branch_usage.go`) records, per session, hour and model,
the tokens each branch's turns used, from the `gitBranch` Claude Code puts
on each event. A background stage after commit linking keeps it current.
The `branch_usage` dataset (`branch_usage_table.go`) splits the priced
ledger by it, for sessions in repositories only.

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

The workspace page loads a workspace in pieces, since one long session with many sub-agents can retain hundreds of megabytes. `/api/work/{id}` returns the workspace without messages, plus what the page used to derive from all of them: each conversation's message count and first prompt, pull request links found in any message, and the files its tool calls touched. `/api/work/{id}/conversations/{id}` returns one conversation for the reader. Its long tool outputs and event bodies are shortened past 2 KB, with JSON keeping its structure so error flags, exit codes, and token usage survive, and are marked `text_clipped`/`raw_clipped`. `/api/messages/{id}` returns a message's original when the reader opens it. Finding text across a workspace's conversations runs on the service (`/find`). In the reader, a turn initially renders 50 events and adds pages of 150 as it scrolls; event details are built when opened. Page Find (Cmd/Ctrl+F, `web/src/page-find.ts`) searches the reader in the browser, including turns, pages and details that are not rendered yet, and renders the part holding the match it steps to; see [page Find](url-view-state.md#page-find).

Catalog data reads use a read-only connection pool, so they do not wait for
the catalog writer, which a sync can hold for seconds at a time. Requests
that save data still use the serialized writer:

- **Settings → App preferences → Pause indexing during queries** optionally lets
  interactive query, search, reader and MCP requests pause ingestion before
  each record, and search-index and Library maintenance between batches.
  It defaults off so sustained browsing does not prevent indexing
  progress. The saved library preference `pharos-pause-indexing-for-queries-v1`
  applies without restarting, independently of the activity panel and manual or
  automatic indexing. Pauses never hold the catalog writer or a SQLite
  transaction: a record already being written finishes before yielding.
  Turning the setting off releases paused work even during a query.
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
- Session Shape derives small pending updates in a read snapshot. Larger
  backlogs serve available stored statistics, with coverage notices for older
  statistics and sessions not measured yet. Maintenance refreshes the view as
  it publishes statistics; reading a dashboard never waits for full catch-up.
- The tool rollup rebuilds in the background. Until the first rollup in a new
  layout exists, tool pages report that it is rebuilding.
- MCP call history is queued in memory and stored by a background writer, and
  on close. `query_statistics` snapshots live in the answering process's memory.

`TestRequestReadsDoNotWaitForTheCatalogWriter` holds the writer, as a sync
does, and checks page reads, query datasets and MCP tools against it.
`TestInteractiveQueriesPauseIngest` and `TestIndexCompletesAtomicWorkspaceDuringQuery`
check that indexing yields without blocking other writers, an open record keeps
making progress during a query, and cancellation rolls it back for safe replay.

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
GET  /api/providers
GET  /api/activity
GET  /api/library/status
GET  /api/health/drive
GET  /api/mcp
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
POST /api/query/{library|activity|usage|…}/query-v3
```

The query endpoints implement the published Query Table **1.0.7** V3 wire
contract. Their allowlisted field definitions live in [`schemas`](../schemas),
and the same documents drive the React frontend and generated Go SQLite schemas.
Tables, charts, metrics, filter suggestions, field statistics and identity
lookups use `/query-v3` with the upstream executor and canonical computed
columns. The old `/rows-v2`, `/metrics` and `/aggregations` routes answer 410
Gone. Legacy domain row and metadata APIs retain their host adapters.
See [`Query Table integration`](query-table-integration.md) for endpoints,
execution limits and the remaining host integration workarounds. Query values never become
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

`list_projects` lists projects instead, repositories and folders alike,
with the same counts and the number of conversations without a project (a
chat export without a folder). Its names are what the `project` filter of
`search_conversations`, `search_work`, `search_messages`, `search_text` and
`query_statistics` takes: a repository by name or remote, which includes the
folders checked out inside it, or a folder project as `~/path`, any part of
it, or an absolute path. A folder name never reaches a repository checked out
inside that folder, which is a project of its own. `query_statistics` can also group by `project`. A project
filter that matches nothing is an error that points to `list_projects`.

`list_outcomes` lists sessions with their outcome label, its evidence and
tier, and the local Git state with its reason (`mcp_outcomes.go`);
`get_file_history` returns the file timeline, and `get_branch_spend` sums
the spend-by-branch rows per branch. All three take `project` and answer for
folders from transcripts alone. `list_projects` resolves a working directory
(`path`) to its project. File filters read `session_files` as well as
change sets (`fileFilterSQL`), and `query_statistics` filters and groups by
`outcome_label`.

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

Open `/analyze?parallelism=1` in that dev UI. The URL opens a compact
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
The legacy `/analyze?analyze_view=concurrency` link opens the Parallelism dashboard.

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

See [Thinking dashboards](thinking-dashboards.md) for request timing semantics,
statistical support, URL state and the native evidence-query integration.
