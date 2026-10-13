# Conversation and workspace naming survey

Survey date: **2026-10-13**. Code baseline: `f686378bb7d26cb7368e7404773b30c1aacd4d85`.

Implementation update: the agreed extraction, capture, inheritance and cached
human-word fallback rules are documented in [Conversation and work names](conversation-naming.md).
The coverage tables below describe the baseline before these changes.

Pharos misses some useful names that already exist, particularly Codex's separate thread name and Claude's custom titles. Naming also depends on the capturing application: Codex inside Conductor or TL1 already has a meaningful container title, even when its native rollout has none. Recovering these names should precede generating new ones.

The survey covers every supported source kind, live indexing, portable captures, exports, and the distinction between a conversation and its containing work. Local source observations and the existing library are separate populations. The library's most recent successful source indexing was **2026-10-08**, so its counts describe stored coverage, not today's source state. No provider data or catalog records were changed.

## How the Work column gets its name

The Library schema labels `title` as **Work**. Its renderer displays that value directly; it does not select a title from prompts, PRs, summaries, or linked conversations. Library reads `workspaces.title` live. An authoritative ingest writes the adapter's `WorkspaceRecord.Title` into that column. See [Library schema](../schemas/library.schema.json), [renderer](../web/src/index.tsx#L253), [Library workspace fields](../internal/archive/library.go#L469), and [workspace upsert](../internal/archive/ingest.go#L1104).

The storage model has a workspace title but **no conversation title** in either `ConversationRecord` or the `conversations` table. Native Codex and ChatGPT normally make one work per conversation. Claude and Antigravity group subagents with their root. Conductor makes one work per **session**, rather than per native Conductor workspace. TL1 makes one work per **task**, containing its attempts. A container name can therefore help several conversations while describing a broader scope than any individual attempt. See [record types](../internal/archive/adapters.go#L31) and [catalog schema](../internal/archive/assets/schema.sql#L88).

## Coverage in the existing library

These counts came from read-only queries of the configured library. An adapter fallback is an exact match to the adapter's provider label plus the first eight characters of `source_id`. Generic titles are exactly `Untitled`, `New chat`, or `New conversation`, ignoring case and surrounding whitespace. A name outside these categories is not necessarily a good semantic title.

| Stored source kind | Works | Exact adapter fallback | Generic title | Interpretation |
| --- | ---: | ---: | ---: | --- |
| Codex | 6,319 | 6,319 | 0 | Every stored name is `Codex <id>`. |
| Claude | 2,387 | 161 | 0 | Most names are already extracted; coverage varies by harness. |
| Conductor | 6,990 | 4 | 355 | Session titles are usually present, but placeholder strings pass through. |
| Antigravity | 3,756 | 1,088 | 0 | Existing extraction handles both title stores; many conversations still lack a title. |
| TL1 | 14,948 | 0 | 0 | Task titles supply the Work names. No title equaled its source ID. |

There were no standalone ChatGPT or canonical source rows in this library snapshot; their coverage below is established by adapters and fixtures rather than these counts. `tl1-export` can preserve an exported `source_kind`, so it need not appear as a separate kind.

Existing declared native identity links connect **2,128 Codex works to Conductor** and **327 to TL1** with non-placeholder titles: **2,455 distinct Codex works** in total. Of these, **2,333 have exactly one distinct linked title** and **122 have two or more**. These are candidates for inherited names, not proof that every container title is the best title for the native session. Current [alias reconciliation](../internal/archive/ingest.go#L1303) records identity links without copying titles.

## Provider and capture matrix

| Provider or surface | Useful name available | What Pharos reads today | Does today's capture preserve it | Gap |
| --- | --- | --- | --- | --- |
| Codex desktop and clients using app-server | Optional user-facing `thread.name`; observed as `threads.name` and `session_index.jsonl` entries with `thread_name` | Only `session_meta.title` from the rollout; otherwise `Codex <id>` | **No** for the observed name stores: capture accepts rollout JSONL under `sessions/`, `archived_sessions/`, or filenames starting with `rollout` | Confirmed extraction **and capture** gap. |
| Codex CLI, `exec`, SDK and IDE rollouts | Named threads are possible, but a useful name is not guaranteed; DB `title` may simply be the first user message | Same rollout parser and ID fallback | Transcript yes; name sidecars and state DB no | Many runs have no separate name to recover. A prompt-derived label is a different category. |
| Codex subagents | Agent nickname/path identify the worker; a task prompt may describe its job | Nickname/path and parent are retained separately from Work title | Yes, when recorded in rollout metadata | Worker names are not semantic task titles. No separate name was present in the local subagent DB sample. |
| Claude Code CLI, SDK and IDE transcripts | `ai-title` / `aiTitle` where emitted; custom rename metadata and session summaries are additional sources | Last nonempty `aiTitle`; otherwise `Claude <id>` | JSONL title events yes; `sessions-index.json` no | Existing AI titles work. Custom title entries are not interpreted; index summaries are not captured or read. |
| Claude Code Desktop and cloud sessions | Desktop supports session renaming | Only transcripts reachable through a configured `claude` source | Conditional on local transcript availability | No dedicated desktop/cloud discovery or capture adapter. UI naming support does not establish local coverage. |
| Conductor with Claude, Codex or another recorded agent type | `sessions.title`; separate `workspaces.workspace_name`; optional workspace PR title | Session title, irrespective of agent provider; PR title enriches PR records | **Yes**: full conversation database snapshot | `workspace_name` is preserved but unused. `Untitled` is accepted as a real title. Workspace names need a policy because rows represent sessions. |
| TL1 with Claude or Codex executors | `tasks.title` | Task title, falling back to task ID | **Yes**: installation database snapshot plus transcripts | Strong existing Work naming. Attempt conversation names have no separate storage field. |
| Antigravity app, `agy` CLI and IDE | `conversation_summaries.title`; fallback `annotations/<id>.pbtxt` title | Summary DB title, then annotation title, then product label plus ID | **Yes**: both title stores are included | No identified unused title field in the supported stores; missing titles still require a fallback. |
| ChatGPT export | Conversation object's `title` | Export title, otherwise `ChatGPT <id>` | **Yes**: `conversations.json` | Title extraction is already present. A later cloud rename requires a new export. |
| Canonical export and `tl1-export` | Workspace `title`, optionally `purpose` | Workspace title, then purpose when title is absent/null, then source ID | **Yes**: exported JSON | Producer controls quality. Conversation-level `title` or `name` is not read. An empty-string `title` blocks the purpose fallback. |

Matrix implementation references: [adapter dispatch](../internal/archive/adapters.go#L178), [Codex title](../internal/archive/adapters.go#L854), [Claude title events](../internal/archive/adapters.go#L913), [Conductor session title](../internal/archive/adapter_conductor.go#L455), [TL1 task title](../internal/archive/adapter_tl1.go#L701), [Antigravity title stores](../internal/archive/adapter_antigravity.go#L423), [ChatGPT export](../internal/archive/adapter_exports.go#L84), [canonical export](../internal/archive/adapters.go#L403), and [capture selection](../internal/archive/capture_plan.go#L43).

## Codex has separate names and prompt previews

Official OpenAI documentation distinguishes the user-facing `thread.name` from other thread metadata. `thread/name/set` updates it; read/list/resume responses hydrate it when a name exists. A new or forked thread can return no name until one is set. This establishes an optional naming capability, not a promise that every CLI, SDK, IDE or desktop thread receives an automatic title. See [Codex app-server documentation](https://learn.chatgpt.com/docs/app-server).

On this Mac, `~/.codex/state_5.sqlite` contains both `threads.title` and `threads.name`. Its **1,334 threads** break down as follows:

| Recorded surface | Threads | Nonempty DB name | Nonempty DB title |
| --- | ---: | ---: | ---: |
| `exec` | 553 | 0 | 553 |
| `vscode` with legacy or `codex_sdk_ts` originator | 410 | 0 | 410 |
| `vscode` with `Codex Desktop` originator | 4 | 4 | 4 |
| Subagent source records | 367 | 0 | 243 |

All **1,210 nonempty DB titles exactly equal `first_user_message`**. They are prompt material rather than a separate concise name; many SDK/IDE titles begin with injected instructions. The four named desktop threads also appear in `session_index.jsonl` under the same thread IDs, with matching `thread_name` values. Their rollout files contain neither a `session_meta.title` nor an event with a name/title event type. In a separate sample of the **200 most recently modified available rollouts**, all had session metadata and **none had `title` or `name` in it**.

Consequently, changing the rollout parser alone cannot recover the observed desktop names. A future adapter could join the session index or state database by native thread ID, preferring a real name over a prompt preview. These stores are internal formats whose filenames/schema can vary by version; `state_5.sqlite` is an observation, not a filename to hardcode. Older global-state layouts were not established by this sample. The current `.codex-global-state.json` has no top-level thread-title map.

The current [Codex fingerprint and part discovery](../internal/archive/adapters.go#L469) also observe only accepted JSONL transcripts. A sidecar rename would remain invisible without extending incremental dependencies. Any implementation must preserve the name evidence in portable captures and version it independently of an unchanged transcript. Indexing an old rollout-only capture on another Mac cannot reconstruct an uncaptured source name.

## Claude offers more than the AI title event

Claude's adapter already understands `{"type":"ai-title","aiTitle":"…"}`. The local project tree contained **59 root transcripts**, **58 with nonempty AI title events**. Of those files, 29 joined to Claude sessions in the current Conductor database; 28 of those 29 had AI titles. The remaining files recorded SDK or CLI entrypoints. These facts establish title coverage, but do not prove which application generated each title.

The existing library provides a broader view: its root conversations split into **14 `claude-code/cli` works with 7 fallbacks**, **821 `claude-code/sdk-cli` works with 134 fallbacks**, and **1,552 `claude-code/sdk-ts` works with 20 fallbacks**. The provider alone therefore does not predict naming quality.

Claude Code supports `/rename`, including generating a name from history when no name is supplied. Its Agent SDK exposes a custom title separately from a display summary, and its rename operation appends a title entry to the transcript. Pharos's parser only interprets `ai-title`, so the documented custom rename path is an additional extraction target. Exact event encoding and precedence should be verified with a representative renamed-session fixture; no `custom-title` events appeared in the 59 local root transcripts. See [Claude commands](https://code.claude.com/docs/en/commands) and [Anthropic's session browser example](https://platform.claude.com/cookbook/claude-agent-sdk-05-building-a-session-browser).

Two local `sessions-index.json` files contained three entries, including **two nonempty summaries**. Pharos ignores these JSON files in both indexing and capture. Both summarized entries referenced transcripts that no longer exist, so they offer metadata-only evidence, not recoverable conversations or proof that those two current Library rows can be renamed. A future index fallback must match by session identity, handle stale entries, and distinguish summaries from deliberate names.

[Claude Desktop documentation](https://code.claude.com/docs/en/desktop) confirms session renaming, but desktop/cloud storage coverage needs its own representative source fixture before claiming the generic Claude adapter captures those titles.

## Container titles and remaining gaps

**Conductor:** All 29 local Claude sessions and 56 local Codex sessions had nonempty titles; two Codex titles were `Untitled`. All 44 native workspaces had `workspace_name` values distinct from the geographic directory names in the inspected examples. None had `user_set_workspace_name=1`, so that flag does not distinguish all meaningful names from placeholders. Twenty-eight had PR titles. Pharos snapshots these fields but uses only `sessions.title` for Work. Its incremental signal already includes the session, workspace and repository rows, so a workspace rename can be detected without transcript changes. A workspace name could fill a missing/generic session title, but should not replace a distinct session title indiscriminately. See [Conductor signals](../internal/archive/adapter_conductor.go#L238) and [PR enrichment](../internal/archive/adapter_conductor.go#L538).

**TL1:** One available local installation had **2,615 tasks, all with nonempty titles**; the other registry entry's database was unavailable. The adapter correctly uses a task title even when the executor is Codex. Export capture also retains a supplied task/workspace title. Per-attempt labels would require a separate conversation naming concept rather than changing the task's name.

**Antigravity:** Both native title stores are already read and captured, and the summary signal makes a title-only change reparse an otherwise unchanged transcript. In the library, **3,755 works have an `antigravity-cli` root harness and one has `antigravity`**; all 1,088 fallback names occur in the CLI group. No local Antigravity source tree was available on this Mac. The library establishes the gap's scale, while existing fixtures establish summary/annotation extraction. It does not establish that the CLI never generates names or that every fallback reflects a missing source title rather than older/incomplete capture evidence. See [title change signal](../internal/archive/adapter_antigravity.go#L415) and [capture tests](../internal/archive/adapter_antigravity_test.go#L238).

**Exports:** ChatGPT and canonical exports retain whatever the producer supplies. Canonical's `firstNonNil(title, purpose)` behavior means a missing/null title can use purpose, but an explicitly empty title falls through to the source ID. That is a small existing fallback inconsistency. Neither export adapter promotes a linked PR or message summary into Work automatically.

## Decisions for improving names

The evidence supports three distinct steps, in this order:

1. **Recover existing names.** Add Codex's optional name metadata to extraction, capture and incremental dependencies; recognize Claude custom titles and evaluate session-index summaries; use Conductor workspace names as a conditional fallback. Preserve whether the candidate is a deliberate name, a provider-generated name, a summary, or prompt text.
2. **Use names from declared identities.** The 2,333 Codex works with one distinct non-placeholder linked title are a substantial starting population that needs no new provider capture. Decide whether to display an inherited container title or store it as a fallback. Competing titles, broader TL1 tasks, and user renames need explicit precedence. Avoid inferring identity solely from a shared path or branch.
3. **Label the genuinely unnamed work.** Choose between a concise extractive label and a generated semantic title for the remainder. Existing `purpose` and summaries provide inputs, but `purpose` is up to 2,000 characters of the initiating message, not a title. Exclude injected instructions and pasted context when choosing the task text. Preserve a stable ID fallback and permit a better source name to supersede a derived label.

Before choosing the implementation, settle whether names belong to each conversation, each containing work, or both. Title provenance and override precedence matter because [authoritative reingest replaces the workspace title](../internal/archive/ingest.go#L1104), while a [non-authoritative copy does not improve it](../internal/archive/copies.go#L143). A generated title written directly into the existing column could otherwise disappear on the next sync.

Seven existing extraction/capture tests passed for Claude AI titles, Conductor session titles, TL1 task titles, canonical ingestion, and Antigravity summary/annotation capture and incremental updates. The missing Codex/Claude paths above were surveyed without implementing them.
