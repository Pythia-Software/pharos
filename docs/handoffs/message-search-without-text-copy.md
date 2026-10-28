# Handoff: stop the word index keeping its own copy of every message

## Assignment

`messages_fts` is a contentful FTS5 table. Besides its index it stores a
complete second copy of every message's searchable text, in
`messages_fts_content`. On the production library that copy is 25.6 GB,
27% of the catalog. Remove the copy without changing what any search finds.
Migrate existing catalogs in the background, and don't make readers or the
sync wait.

[Stop word-indexing tool output](message-search-without-tool-output.md) is a
separate handoff. Either can land first, but read both: they rebuild the same
index, and doing them in one rebuild saves a pass over 24 GB of text.

## Evidence

Measured with `dbstat` on the read-only backup `/Volumes/euclid/Pharos Backup
Tuesday October 27th 7AM/catalog/catalog.sqlite3` (94.5 GB, 6.3 M messages):

| Object | GB |
|---|---|
| `messages` (text ≈ 24 GB, of which tool results ≈ 84%) | 33.9 |
| `messages_fts_content`, the copy this handoff removes | 25.6 |
| `messages_fts_data`, the index itself | 13.8 |
| `messages_trigram_data`, already contentless | 2.1 |

**Do not open the live catalog** (`/Volumes/euclid/Pharos/catalog/catalog.sqlite3`)
with SQLite, not even read-only. A long read pins the WAL and blocks Pharos's
writer. Measure on a backup copy opened with `?immutable=1`. Backups may have
an older schema than your checkout.

## How the index works today

- **Schema.** `internal/archive/assets/schema.sql`:
  `messages_fts USING fts5(message_id UNINDEXED, text, tokenize='unicode61')`.
  The `text` column holds `pharos_lexical_text(kind, text)`
  (`message_search.go`). That is a deterministic projection of
  `messages.text`: JSON envelopes get field barriers and decoded whitespace;
  other kinds pass through unchanged.
- **Row identity.** FTS rowids are not message rowids. `message_fts_rows`
  maps `message_id` to `fts_rowid`. `messages_trigram` reuses the same rowids
  and is already `content='', contentless_delete=1`, which is the model to
  follow.
- **Writes.** `ingest.go`: `replaceMessageFTS`, `deleteMessageFTS`,
  `insertConversationFTS` and `deleteConversationFTS`. `replaceMessageFTS` runs
  after the `messages` row has been upserted, so the old text is already gone
  when the FTS row is deleted. This matters for the design (below).
- **Reads of the stored content:**
  - `snippet(messages_fts, …)` in `mcp_conversations.go` (two sites) and
    `mcp_audit.go`;
  - `messages_fts.message_id` in `mcp_audit.go`, `mcp_provenance.go`,
    `mcp_conversations.go`, `mcp_statistics.go` and `search_explain.go`;
  - `f.text` in `repairMessageSearch` (`message_search.go`), which compares
    the stored lexical form to recompute it;
  - `f.text` in `incremental_audit.go`;
  - `COALESCE(fr.message_id, f.message_id)` in `text_search.go`.

  Its comment already notes that reading `f.message_id` makes FTS5 read the
  stored content row.
- `messages_vocab` is an `fts5vocab` over `messages_fts`. It works with any
  content mode.
- `catalog.go` sets automerge and crisismerge, and backfills
  `message_fts_rows` from `messages_fts` once.

## Design

Recommended: **contentless with `contentless_delete=1`**, like
`messages_trigram`.

- Identity comes only from `message_fts_rows`. Replace every
  `messages_fts.message_id` and `f.message_id` read with a join through
  `message_fts_rows`.
- Snippets are made in Go from `messages.text` and the matched terms, with
  `projectMessageLexicalText`. Its offset map already translates lexical
  offsets back to the stored text. Keep the existing snippet shape: about 18
  or 28 tokens around the hit, with ` … ` ellipses. Strip `lexicalBoundary`,
  as snippets do today.
- `repairMessageSearch` can't compare `f.text`. Use `message_search_state`
  versions instead, which already exist for this purpose, or reindex the
  envelope kinds whenever `messageSearchVersion` changes.
- The audit's `fts_text` becomes the recomputed lexical text.

The alternative is **external content over a view**:
`content='messages_fts_source'`, a view that selects
`r.fts_rowid AS rowid, m.id AS message_id, pharos_lexical_text(m.kind, m.text) AS text`
through `message_fts_rows`. This keeps `snippet()` working unchanged. But
FTS5 removes an external-content row's tokens by reading the content table's
current values. Every write path would have to delete the FTS row **before**
`messages` changes, or issue the `'delete'` command with the old text. Today
`replaceMessageFTS` runs after the upsert, so a straight switch would silently
corrupt the index. Choose this only if Go-side snippets prove unworkable, and
test the update ordering explicitly.

## Migration

- Build the new table beside the old one (`messages_fts_v2` or similar) in
  bounded batches, for example per conversation or per rowid range. Commit
  each batch in its own short write transaction through the normal writer
  (`writeTransaction`), so syncs interleave. Record progress in `meta` so the
  build resumes after a restart.
- New ingests write both tables until the switch. Readers move to the new
  table once it is complete. Then drop `messages_fts` and
  `messages_fts_content`.
- Expect about 14 GB of extra index while both exist. Check free space first
  and refuse with a clear status rather than filling the drive.
- Dropping the old table frees pages inside the file; it does not shrink the
  file. The file shrinks only with `VACUUM`, which needs about the catalog's
  size in free space and hours on the drive. Make reclaiming a separate,
  explicit step (Settings or `pharos` CLI), reported in Library health. Don't
  run it automatically.
- Surface the progress in Background work (`docs/background-work.md`), as
  the substring index's backfill does (`substringIndexProgress`).

## Constraints

- Requests never write: no migration work on a read path.
- Data-safety tests stay. Extend the existing search tests rather than adding
  `*_regression` files.
- Don't change ranking, cursor semantics (`text_search.go` cursors hash the
  strategy) or which messages match.

## Done when

- A new catalog has no `messages_fts_content`, or no content table of any
  kind.
- An existing catalog migrates in the background, resumably, and searches
  keep working throughout.
- All of these return the same results and snippets as before:
  - `search_text` (every surface);
  - `search_messages`;
  - `search_conversations`;
  - Library search;
  - provenance;
  - MCP audit;
  - `get_search_capabilities`.

  Show this with the existing tests plus a comparison on a backup-sized
  copy.
- The PR records `dbstat` before and after on a backup copy, the migration's
  wall time, and its peak extra disk use.
- `go test ./...`, `npm --prefix web run check` and
  `npm --prefix web run test:full` pass.
