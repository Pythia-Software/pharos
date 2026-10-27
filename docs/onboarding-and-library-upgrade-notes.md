# Library management upgrade notes

Include these behavior changes in the release that introduces managed libraries.

- **Keep Pharos running for agent access.** Managed MCP tools now use the running
  application and its assigned library. Closing the window keeps MCP available;
  quitting Pharos makes it report offline. This also applies to existing bundled
  `pharos mcp` entries once their library is registered. Reopen Pharos and connect
  to the assigned library to resume. A standalone CLI installation remains a
  separate mode with direct-catalog access.
- **Eject remembers your setup.** Eject and Release suspend connections for the
  current run. Relaunch reconnects the selected library when available; they do
  not erase MCP assignments or automatic delivery settings.
- **Working UI state survives relaunch.** Annotations and drafts, navigation
  history, last-used table queries and panel state are stored in the library,
  scoped to this Mac and application. Browser caches remain temporary. Existing
  session data in an older web origin is not automatically migrated. Already
  discarded temporary sessions cannot be recovered; saved queries and library
  preferences survive.
- **Restore through recovery.** Use **Restore a backup** or
  `pharos library-recover --backup BACKUP --to NEW_FOLDER`. Recovery verifies a
  separate candidate, creates its writable identity and leaves schedules paused.
  A copied backup cannot be made writable by editing `volume_id` alone. Keep the
  original backup until you have reviewed the recovered library.
- **Storage format compatibility.** This build writes catalog schema 21 and
  capture manifest version 5. Upgrade every Pharos copy that writes the same
  library. These guards combine durable SQLite evidence with delivery retention;
  source paths, raw evidence identities, machine and channel observations travel
  through library transfers. Recovery checks published row-evidence closures as
  well as native files before publishing a candidate.
- **Source setup remains explicit.** The guide detects installed tools, asks
  which source folders to approve, preserves their data, then offers indexing.
  It does not start history backfill or enable the experimental
  `retained_input_capture` or `sqlite_record_capture` source options. Large
  database captures may take longer than transcript copies.
