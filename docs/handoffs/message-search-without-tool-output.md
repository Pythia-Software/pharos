# Handoff: stop word-indexing tool output

## Assignment

Every message with text goes into the word index `messages_fts`, tool output
included. Tool results are about 84% of the catalog's message text, so they
make up most of the index: an estimated 11 of `messages_fts_data`'s 13.8 GB.
They also make up 20 of the 25.6 GB in `messages_fts_content`, the stored copy
removed by [the companion handoff](message-search-without-text-copy.md).
Stop indexing tool output in `messages_fts`, migrate existing catalogs, and
make every search that used to reach tool output say plainly what it now
covers.

The product decision is made: tool output is no longer word-searchable across
the whole library. This handoff is about doing that cleanly, not about
reopening it.

Both handoffs rebuild the same index. If neither has landed, do them in one
rebuild, or land this one first so the text-copy migration has less to copy.

## Evidence

Measured with `dbstat` on the read-only backup `/Volumes/euclid/Pharos Backup
Tuesday October 27th 7AM/catalog/catalog.sqlite3` (94.5 GB, 6.3 M messages).
Per-kind text came from a 1% rowid sample of `messages` (72,660 rows):

| Kind | Share of message text |
|---|---|
| `tool_result` | 84% |
| `metadata` (system and assistant) | 9% |
| `tool_call` | 5% |
| `message`, `result`, `delegation*`, other | 2% |

**Do not open the live catalog** (`/Volumes/euclid/Pharos/catalog/catalog.sqlite3`)
with SQLite, not even read-only. Use a backup copy opened with `?immutable=1`.

## Scope

"Tool output" means `kind='tool_result'`. Keep indexing:

- `tool_call`: inputs such as commands, file paths and queries. They are
  small, and agents search them.
- `delegation_result`: a sub-agent's final report, which is prose and is
  already in the substring index's `substringKinds`.
- `result`: prose.

`metadata` is 9% of the text and is not conversation content either. Measure
what searches would lose before including it, and record the decision in the
PR. Don't fold it in silently.

Tool commands and query text stay searchable through their own indexes,
`tool_command_fts` and `tool_query_text_fts`. Neither indexes tool results.

## What reaches tool output today

Change each of these, and give each one a test of the new behaviour:

- **`search_text`, surfaces `tools` and `all`** (`text_search.go`,
  `textSurfacePredicate`). These use the `word_index` strategy over
  `messages_fts`. Afterwards, a tool-result match can come only from a scoped
  scan (`scoped_scan`, which needs a conversation, a workspace or at most 100
  workspaces). The result's coverage field must say that tool output is not
  word-indexed. `get_search_capabilities` must describe the same thing.
- **`search_messages`** (`mcp_audit.go`). Its `kinds` description says
  "Defaults to all indexed kinds, including raw metadata and tool output."
  Update the description. A request for `kinds: ["tool_result"]` must answer
  with an explanation, not an empty list that looks like "no matches".
- **`search_conversations`, `trace`/provenance, `query_statistics` text
  filters, `search_explain`** (`mcp_conversations.go`, `mcp_provenance.go`,
  `mcp_statistics.go`, `search_explain.go`). They match `messages_fts`
  without a kind filter, so they lose tool-output hits automatically. Check
  their descriptions and tests for promises about tool output.
- **Library search and Find** (`text_search.go` with `workspaceRows`, and
  the UI in `internal/archive/assets/`). Check whether any copy says it
  searches tool output.
- **`repairMessageSearch`** (`message_search.go`) re-projects envelope kinds,
  `tool_result` among them. It must not reinsert tool results.
- **Write paths** (`ingest.go`): `replaceMessageFTS` and
  `insertConversationFTS` select `WHERE text<>''`. Add the kind filter in one
  shared place, as `substringKinds` does for the trigram index. Keep
  `message_fts_rows`, `messages_trigram` and `text_search_documents`
  consistent: a message absent from `messages_fts` has no `message_fts_rows`
  row, and the trigram index keys off that table.
- **Audits** (`incremental_audit.go`) flag messages without FTS rows. Teach
  them that tool results are expected to be absent.

## Migration

- Delete existing tool-result rows from `messages_fts` in bounded batches
  through the normal writer. Commit each batch in its own short transaction
  and record progress in `meta` so it resumes. The deletes go through
  `message_fts_rows`, followed by the `message_fts_rows` rows themselves.
- With a contentful `messages_fts`, deletes free content pages at once but
  leave the index to shrink through FTS5 merges. Run an
  `INSERT INTO messages_fts(messages_fts, rank) VALUES('merge', N)` loop in
  bounded steps afterwards, or rebuild if the companion handoff is rebuilding
  anyway.
- As with any drop, the file shrinks only with `VACUUM`. Leave reclaiming to
  the explicit step the companion handoff introduces, or introduce it here if
  this lands first.
- Surface progress in Background work, like `substringIndexProgress`.

## Constraints

- Requests never write: no migration work on a read path.
- Extend the existing search tests (`text_search_test.go` covers the `tools`
  surface, and the MCP tests cover the tool descriptions). Don't add
  `*_regression` files.
- Don't touch `messages.text`. Tool output stays stored and readable in the
  reader and through `get_conversation_messages`; only its word index goes.

## Done when

- New ingests never put a `tool_result` in `messages_fts`, and an existing
  catalog migrates in the background, resumably.
- Every surface listed above either still works or explains, in its result
  and its tool description, that tool output is not word-indexed, and
  points to a scoped `search_text` for searching inside a conversation's tool
  output.
- The PR records `dbstat` before and after on a backup copy, the
  migration's wall time, and how many searches in a sample of real MCP
  `search_*` calls (the `mcp_calls` table on the backup) would have returned
  fewer results.
- `go test ./...`, `npm --prefix web run check` and
  `npm --prefix web run test:full` pass.
