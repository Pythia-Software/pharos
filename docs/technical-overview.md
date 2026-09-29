# Pharos technical overview

Pharos is a local-first macOS library for finding past work across TL1, Conductor, Codex, Claude, Google Antigravity, and supported desktop exports. A second capability, preserving and reclaiming **TL1-owned** workspaces through a custody-aware owner hook, is mothballed; see [`docs/reclamation/README.md`](reclamation/README.md) for what remains and how to resume it.

The primary implementation is Go 1.26 with SQLite FTS, a local concept index, an authenticated loopback API, a read-only MCP server, and a responsive desktop UI. The Go service and UI are embedded in the macOS `.app`; the destructive TL1 workflow is deliberately held back.

The command-line executable is `pharos`, and a per-user installation keeps its configuration in `~/Library/Application Support/Pharos`.

## Safety defaults

- Every configured source is read-only. No home-directory scan occurs: `pharos probe` and the new-Mac panel check only a fixed list of known agent locations, and nothing is indexed until you opt in.
- No source is reclaimed. TL1 reclamation is mothballed and excluded from the default build.
- The archive never calls `rm -rf`, `git worktree remove`, `git worktree prune`, or `git gc`. TL1 owns the worker handshake, lock/lease, atomic recheck, and exact-resource removal.
- Preservation must fit the 100,000,000-byte logical cap, reside on the pinned volume, and pass a complete hash/reopen check before acknowledgement.
- Heavy work is deferred unless CPU, memory, and authoritative TL1/agent activity signals all say the machine is truly idle. Unknown owner activity means “not idle.”

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

The UI has separate Library, Usage, Tools, MCP, and Settings areas. Library, Usage, Tools, and MCP call history
use the shared query-table pattern: schema-driven columns, field discovery,
filtering (including OR/NOT), multi-sort, paging, persistent saved views,
optional aggregate metrics, column reorder, and resize.

- **Library** searches conversation text, changed files, and tool URLs. Text search reads retained user messages, agent responses, and thinking from `messages_fts`, groups hits by conversation, and links to the matching message. Unquoted words use AND; fuzzy expansion reads nearby vocabulary terms, while quoted phrases match in order. Case and separator matching are optional. The UI's text search does not use workspace vectors. File search uses `change_files`; a file change has workspace scope and is attributed to a conversation only when its work item, time span, or sole-conversation workspace supports that link. URL search reads `tool_urls` and excludes unopened web-search result links. The search narrows the Library query table, whose filters then apply to the matching work; the table also offers repository, source, PR, sub-agent, compaction, cost, and other structured filters. Work detail shows evidence-linked summaries, attempted/checkpointed/integrated changes, conversations, metrics, PR associations, and receipts.

Past Work also shows when an archived commit appears on the local `origin/main` history. It links the first mainline commit containing that work, whether it arrived through a merge commit or directly. This uses local Git objects only; squash merges and work without a retained commit ID cannot be attributed by ancestry. Refresh the local `origin/main` ref and index a source to update these associations.
- **TL1** appears once a TL1 source is indexed. It compares each flavor across agent configurations (advance, escalation, and agent error rates, cost per useful result, time, tokens, cache reuse), clusters errors by normalized signature and who can fix them, traces human attention and review findings to their causes, segments work by TL1's large enqueues (defaulting to the latest, so analysis follows the most recent flavor revisions), and ranks concerns and savings opportunities, each with a copyable investigation prompt. Flavor and candidate drill-downs and a per-run query table sit underneath. See [`docs/tl1-analysis.md`](tl1-analysis.md).
- **Usage** has two views, switched at the top of the page, and remembers the last one. **Tokens** is the token-usage query table: one row per agent session, local day, and model, with uncached input, cache reads, cache writes, output, and reasoning kept separate. Provider, model, repository, source, agent kind, and day/week/month are all filter and group-by fields, so metrics such as "cache reads per week by model" are a saved view. A stacked chart above the table shows tokens or cost per day, week, or month over the last 30 days to all time, split by token type, provider, model, repository, or agent kind, and follows the table's filters. Preset buttons set up common breakdowns. Linked mirrors of the same work are counted once. **Your writing** is described below.
- **Tools** analyzes tool use from retained transcripts. A daily summary table groups calls by tool, shell program and subcommand (`git status`, `go test`, `sed`), model, repository, and agent kind, with error counts by kind, durations, the tokens each result added to the context, the tokens re-read by later requests until compaction, and their API-equivalent cost. A per-call table filters and sorts every call; a summary row drills into its calls, and each call opens with its parsed commands, input, and result. Each call also records the sites it reached: URLs from web fetches, browser navigation, Codex web searches, and network commands such as `curl`, plus any web search query and the links it returned. Presets cover error rates, failure kinds, top commands, shell calls a dedicated tool could make, slow tools, test and build time, context cost, sites reached, and web searches. Definitions are in [`docs/tool-analytics.md`](tool-analytics.md).
- **MCP** controls local agent access, provides a copyable stdio connection definition and setup prompt, and offers a query-table of recent tool calls with filters, saved views, metrics, response-size estimates, duration, result counts, truncation, and errors.
- **Sources on this Mac** lists every configured source, path availability, coverage, last index attempt and successful index, and errors. Each card has an enable switch in its header. One action area offers **Find sources on this Mac**, **Capture this Mac**, and **Index captured files**. The header button beside the library disk runs **Capture and Index** in sequence.
- **Library drive** (in the header) names the drive holding the library and what is holding or writing the library (capture, index, backup, Git lookups, Library view refreshes), with progress. Its panel says how to disconnect the drive: always **Eject**, which in the app releases the library first and then ejects the drive, or says why not; in a plain browser it says how to eject in Finder. See [Library status and Eject](configuration.md#library-status-and-eject).
- **Macs and captures** (in Settings → Sources) lists this Mac and every Mac whose captures are in the library, with when each source was captured and indexed and which need indexing. A newly added Mac is captured right after onboarding, then offered **Index now**.
- **Library drive** checks and **Backups** (in Settings → Health) show the drive's encryption, Spotlight, file system, free space, volume pin and backup age, each with what to do and a copyable command, and back the library up to a folder on another drive.
- **Library drive** in the header shows live indexing progress and the latest completed index result, including updated workspace and conversation counts and any error.
- **Your writing** (in Usage) estimates how much text you typed or dictated into agent chats. Each user message is split into typed text, harness instructions, one-click templates, attachments, agent output copied from the previous 48 hours, re-sent text, likely pastes, and prompts sent by scripts or other agents. A query table lists one row per conversation, sortable and filterable by each kind of input and by tokens and cost, so "deepest conversations" is one click; the chart and breakdown above it follow the filters. The transcript reader shows the split for each message, and Settings has cards that open each Usage view. Definitions are in [`docs/human-authorship.md`](human-authorship.md).
- **Usage → Carbon Impact** estimates the electricity and CO₂e behind your token usage, all time or the last 30 days, split by token type (uncached input, cache writes, cache reads, output) and model tier. Energy per token comes from published inference measurements, with cache reads charged far less than recomputed input; you choose the electricity grid, data-center overhead (PUE), and a low, central, or high estimate. It is an estimate, not a measurement: factors and sources are in [`carbon/`](../carbon/README.md).
- **Library upgrade** appears when a catalog indexed by an older version needs its derived data brought up to date: repository merges, token attribution, harness versions, loaded instructions, and the tool ledger, run in that order as one resumable background job. See [Upgrading an existing library](releases-and-updates.md#upgrading-an-existing-library).
- **Health** shows retrieval coverage/freshness, index size, and external-storage health. Retrieval-only is shown as an intentional capability.

Delegated work is retained as a recursive `agent_sessions` tree, including sub-agents of sub-agents and links back to each session's messages. Session and workspace usage preserve uncached input, cache reads, cache creation, output, reasoning, and any unclassified aggregate remainder separately so pricing can be applied without reconstructing provider envelopes.

Run diagnostics with `dist/Pharos.app/Contents/MacOS/pharos --config "$HOME/Library/Application Support/Pharos/archive.toml" doctor`. Configuration is documented in [`docs/configuration.md`](configuration.md).

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

## Search, API, and MCP

The Library UI sends its text, file, and URL searches to the Library query table as `find=` (with `find_kind`, `find_fuzzy`, `find_case`, and `find_separators`), which keeps the work `/api/library/find` hits and adds each row's best match and match count; `/api/library/find` still returns the hits themselves. Its text path uses FTS vocabulary and message verification without vector ranking. Legacy `/api/search`, query-table `search=`, and MCP conversation discovery still retain the previous concept/feature embedding behavior. No archive content leaves the machine. Extractive summaries cite retained source locators.

The workspace page loads a workspace in pieces, since one long session with many sub-agents can retain hundreds of megabytes. `/api/work/{id}` returns the workspace without messages, plus what the page used to derive from all of them: each conversation's message count and first prompt, pull request links found in any message, and the files its tool calls touched. `/api/work/{id}/conversations/{id}` returns one conversation for the reader. Its long tool outputs and event bodies are shortened past 2 KB, with JSON keeping its structure so error flags, exit codes, and token usage survive, and are marked `text_clipped`/`raw_clipped`. `/api/messages/{id}` returns a message's original when the reader opens it. Finding text across a workspace's conversations runs on the service (`/find`). In the reader, a turn renders 150 events at a time and adds more as it scrolls; event details are built when opened.

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
GET  /api/query/{library|activity|usage|…}/distinct?field=&q=&limit=
GET  /api/query/{library|activity|usage|…}/field-stats?fields=a,b
POST /api/query/{library|activity|usage|…}/aggregations
```

The query endpoints implement the `@pythia-software/query-table-*` 0.4.2 wire
contract. Their allowlisted field definitions live in [`schemas`](../schemas), and
the same documents drive the React frontend and Go executor. Pharos uses a
map-backed Go adapter because the upstream compiler currently emits PostgreSQL;
the catalog remains SQLite and query values never become SQL text.

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

The existing `search_work`, `get_work_detail`, `get_conversation_excerpt`,
`get_change_set`, `trace`, `query_metrics`, and `get_receipt` tools remain
available. `get_conversation_excerpt` is a compatibility alias for bounded
message windows. MCP exposes no arbitrary deletion tool.

The MCP page controls a shared enabled flag in the local catalog. Turning it off
hides tools from new listings and rejects calls from already-connected clients;
it does not alter another client's configuration. Tool-call history retains the
latest 5,000 calls with an allowlisted, shortened argument summary and response
metrics, never response bodies. The page provides per-tool totals and filters
for tool and status. Output token counts are estimates from bytes.

## Sharing conversations

`GET /api/share?id=<work>&id=<work>…` (up to 200; `/api/share/work/<id>` for
one) returns the works as an attachment: the app's own page with its assets
inlined (including the query-table bundle) and the data embedded as JSON in
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
- **Tables:** Library, Tools (summary and calls), and Usage (Machine Tokens),
  each holding only the selected works' rows. Tools summary rows come from the
  same SQL and record builder as the rollup (`toolUsageRecord`), run over a
  temporary cube that excludes every other work. Human Words, Carbon Impact,
  MCP, TL1, search, and settings need the live library and are left out.
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

The SwiftUI wrapper starts the loopback-only Go service embedded beside it and presents its authenticated interface in WebKit. The catalog, configuration, staging data, and preserved archive remain outside the app bundle.

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

The local [release workflow and in-app update check](releases-and-updates.md)
use these settings to publish ad hoc signed versioned GitHub releases by
default, with optional Developer ID signing and notarization.

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
**Feedback** shows the saved annotations and copies a
structured Markdown report containing selectors, bounds, visible or selected
text, computed styles, view context, and a source-file hint suitable for pasting
into a coding-agent conversation. Saved annotations and the note currently being
composed survive app refreshes. They remain local in WebKit storage and are never
uploaded by the archive service.

## Development and verification

For new interface icons, follow the [icon drawing and usage guide](iconography.md).

```sh
go test ./...
go vet ./...
(cd web && npm ci && npx tsc --noEmit && npm run build)
(cd web && npm run check-bundle)
./launch.sh --no-open

# Compatibility/reference suite during the transition
PYTHONPATH=src python3 -m unittest discover -s tests -v
python3 -m compileall -q src
```

The Go tests cover stable cross-language IDs, source configuration, canonical
ingestion, API compatibility, the query-table allowlist/filter/sort/pagination/
aggregation contract, semantic search vectors, and native Conductor
repository/PR/tool/sub-agent extraction. `macos/build-app.sh` reinstalls
`web/` from its lockfile and rebuilds the frontend when npm is available, and
otherwise embeds the checked-in bundle with a warning. `npm run check-bundle`
rebuilds into a scratch directory and fails unless the checked-in
`internal/archive/assets/query-tables.{js,css}` match `web/src` byte for byte;
run it before committing frontend changes.
The Python compatibility suite continues to cover the mothballed preservation and
reclamation reference; `go vet -tags reclamation ./...` keeps the parked Go
queue code compiling. The TL1 hook must maintain
its own disposable integration tests because its repository concurrency
protocol is owner-specific.

`go.mod` pins the Go toolchain (`toolchain go1.26.8`); an older local `go`
downloads it automatically through the Go module proxy.

### Trying UI changes against a real library

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
