# Tool analytics

The Tools tab reads a ledger derived from retained messages: one `model_requests` row per model API request and one `tool_calls` row per tool call, joined to its result. Shell calls are also split into `tool_commands`, one row per simple command. The ledger is rebuilt with a conversation when it is ingested. A ledger version change makes existing conversations stale; start **Build tool ledger** in Tools or run `pharos build-tools` to rebuild from stored messages. This works without source files, so reclaimed TL1 work is covered too. On the library used to estimate this change, the full rebuild took roughly 1.5–2.5 hours; time depends on the machine and library size.

The Tools table reads `tool_usage_daily`, one row per local day and summary dimension. The Tool calls table reads `tool_calls` for its rows, and answers counts, metrics, filter values, and column stats from `tool_call_cube`: the calls grouped by workspace, day, and every field with few values, a quarter as many rows. Both are rebuilt when the ledger changes or the date does. A rebuild reads every call, so pages keep serving the previous build meanwhile: after a sync they can lag it by up to half a minute. Substring searches on command, program, subcommand, and their combined command name use a trigram index to narrow calls before applying the exact filter. Regex searches use it when the pattern requires a literal of at least three ASCII characters; other patterns use the regular scan. Existing catalogs build the index in the background and use the regular scan until it is complete. Other filters the cube cannot apply, such as a duration range, are counted across every call. Column stats leave out the distinct counts of per-call numbers and of long text for the same reason.

## Calls and outcomes

- **Tool category** groups tools across providers: command, read, search, edit, web, agent, plan, user, mcp, meta, other. Codex code-mode `exec` scripts that call one non-shell tool (`write_stdin`, `web__run`, ...) are recorded under that tool.

- **Program, subcommand, command category** come from a lexical reading of the command line. It splits on `&&`, `||`, `;`, `|`, `&`, and newlines, skips heredoc bodies, variable assignments, and wrappers such as `sudo`, `env`, and `timeout`, and reads subcommands for tools like git, go, npm, and cargo (`npm run build`, `python3 -m pytest`). The call's program is its first command that does real work, so `cd repo && git status | head` is `git status`. Command substitutions (`$(...)`, arithmetic, braces, and backticks) stay within their enclosing word; assignment-only substitutions name their inner program. Parenthesized subshells are split into their inner commands. Variables, aliases, and functions are not expanded.
- **Status** is `ok`, `error`, or `no_result` (no result was retained). A call is an error when the provider flagged it, a command exited non-zero, it was interrupted or timed out, or an MCP call failed. Exit code 1 from grep, rg, and diff means "no match" and is not an error.
- **Error type** is harness-imposed (`user_rejected`, `hook_blocked`, `harness_error`, `interrupted`, `timeout`), `nonzero_exit` for a failed command, or a tool-level kind (`file_not_read`, `edit_no_match`, `file_not_found`, `permission_denied`, `invalid_input`, `file_too_large`, `tool_error`).

**Error signature** keeps a normalized line from a failed result, after removing tool-runner headers and volatile paths, IDs, and numbers. Lines that name no cause are skipped: bare punctuation such as a JSON `{`, a lone path, JavaScript stack frames, and Node rethrow lines. A short label such as `usage:` or `Validation failed:` is joined with the next line. One-byte values like `0x8b` are kept, since they name the cause (gzip data read as text); longer hex values are hidden. It also covers test failures in calls whose shell exit looked successful. **Test failure** identifies a test command with a non-zero exit or failure markers in its output, including piped test runs; it does not change `status`. `harness_error` covers results beginning with a failed permission request and closed stream; `hook_blocked` covers tagged guard blocks. The same words printed in a command's output do not override its exit classification.

**Repository path** resolves a file path relative to its repository, including known Conductor and agent worktree layouts. `path_repository` names the matched repository and can differ from the conversation repository after a command changes directory. A worktree's repository comes from a known checkout at the worktree root or the clone it hangs off, then from the only repository with checkouts in the same Conductor repository directory (a clone kept inside a checkout, such as a test fixture under `.context`, is not one), and only then from the directory name, so renamed repositories keep one identity. The ledger also stores the repository's ID and the absolute path it resolved (`path_absolute`). When an index makes a checkout known or repositories merge, the calls under that checkout, or under its whole Conductor repository directory when the repositories with checkouts there change, are resolved again, so stored rows match a fresh build. Temp and agent-home paths have no repository path. `path_scope` distinguishes repository, temp, agent-home, and external paths.

Filter chips and value pickers show readable labels for these keys (`nonzero_exit` is "Non-zero exit", `vcs` is "Version control", `tl1-export` is "TL1 export"). The labels live in the schemas' static `options`. Queries, URLs, saved queries, and table cells keep the raw keys, and the picker matches either.

## Sites and web searches

Each call records the URLs it reached in `tool_urls`, and its first URL, site (host and port), all sites, and search query on the call:

- **Tool arguments:** URL-named inputs of any tool, such as WebFetch's `url`, browser navigation, and the pages Codex's hosted web search opens.
- **Shell commands:** URLs on a line that runs a network client (`curl`, `wget`, `git clone`/`fetch`/`push`, `urlopen`, `requests.get`, `fetch(`, and similar), including URLs assigned to variables such a line uses (`BASE=https://…; curl "$BASE/x"`). URLs in commit messages, PR bodies, `echo`, or XML namespaces are not counted.
- **Results:** pages a web tool reports opening ("Web search completed for: …").
- **Search results:** links a web search returned. They are listed in the call's details but are not counted as sites reached, since the agent may not have opened them.

Only `http`, `https`, `ws`, `wss`, and `ftp` URLs with a literal host are kept; `file://`, `chrome://`, and templated hosts (`https://$HOST/…`) are skipped. The **Sites** preset counts calls by site, and **Web searches** lists searches with their queries.

Codex's hosted web searches (`web_search_call`) are recorded as `web_search` calls. Sessions ingested before this was added need a re-sync of the Codex source; other calls only need the tool ledger rebuilt.

## Duration

`reported` durations come from the provider: Claude's `durationMs` where present, Codex's wall time, and Codex process durations. Otherwise the duration is the gap between the call's and the result's timestamps (`timestamps`). For Claude, that gap includes any time spent waiting for a person to approve the call.

## Tokens and cost

- **Call output tokens:** the emitting request's output tokens, split evenly across the calls it made.
- **Context added:** how much the result grew the context. It is measured as the growth in the next request's prompt over the previous request's prompt plus its output. That growth is split across the results (and any user turns) that arrived in between, in proportion to their size. Without usable request usage it is estimated at 4 bytes a token, plus 1,500 per image. `result_tokens_source` records which.
- **Context carried:** context added times the number of later requests in the same agent stream until the context is compacted. Each of those requests re-reads the result.
- **Cost:** API-equivalent cost at the day's list prices (see [token accounting](token-accounting.md)). Context cost prices the first send as a cache write for Claude (5-minute rate) or as uncached input for others, then the carried tokens as cache reads. Output cost prices the call's output share.

Conductor's Codex sessions report usage per turn, not per request, so their results are always estimated. Linked mirrors of the same work (a Conductor workspace and the native session it wraps) are counted once, as in Usage.

## Skills and MCP attribution

The **Skills** view in Tools reads `skill_usages`, a replayable ledger derived from retained messages alongside `tool_calls`. Each record keeps the skill name, path when known, evidence type, status, agent stream, evidence message, and optional loaded-body message. A skill used through a tool links to its original call; the Calls table exposes its skill names and paths without multiplying the call's count or cost.

- `explicit_invocation`: a `Skill` or `get_skill` tool names a skill, or a `SlashCommand` explicitly supplies a skill identity. Arbitrary slash commands are not assumed to be skills. Failed attempts and calls with no retained result remain visible as `error` and `no_result`.
- `file_load`: a read tool or a literal `cat`, `sed`, `head`, `tail`, `more`, `less`, or `bat` command targets `SKILL.md`. The skill name comes from its containing directory, which may differ from a plugin's invocation alias. Multiple distinct paths in one call produce separate evidence records linked to the same call. Searches, listings, edits, metadata-only commands, heredoc bodies, and unresolved shell variables or globs are not counted. Status belongs to the parent call; it does not prove each command in a compound shell call executed successfully.
- `harness_load`: Claude injects a skill body or records an `invoked_skills` attachment without a matching successful invocation. Matching bodies enrich the invocation instead of creating another usage. Identical attachment snapshots in one stream are deduplicated. `content_bytes` measures an explicitly retained skill body, not a listing, inferred file size, or the entire workflow.

Skill listings are availability, not usage. Loading instructions does not prove the agent followed them. The evidence-by-skill metric counts evidence records by type, not completed skill-guided tasks. Costs and durations remain on the parent tool call; there is no reliable transcript boundary for the subsequent workflow. Multiple skill files therefore never duplicate tool cost.

MCP calls expose both `mcp_server` and `mcp_method`. Native `read_mcp_resource`, `list_mcp_resources`, and `list_mcp_resource_templates` use their `server` argument when supplied; discovery without a server is still categorized as MCP. Browser MCP calls keep their `web` category and server attribution, so filter on server presence rather than just the `mcp` category. Structured MCP `isError` results are failures even without an outer provider error flag. Single-tool Codex code-mode scripts retain existing attribution; arbitrary multi-tool scripts remain opaque rather than inventing calls, results, or costs.

Existing libraries need **Build tool ledger** (or `pharos build-tools`) to derive this evidence from retained messages. The Claude parser now retains `invoked_skills` attachments; its version bump makes source indexing revisit available transcripts. A ledger rebuild alone cannot recover attachment evidence discarded by an older parser when the source or capture is gone.
