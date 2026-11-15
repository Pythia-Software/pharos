# Session time bounds

Agent session timing has three explicit states in
`agent_sessions.time_bounds_source`:

| Source | Meaning |
| --- | --- |
| `message-span` | First through last valid timestamp in the messages assigned to this session, after usage-stream reassignment. One observation legitimately has a zero span. |
| `attempt-envelope` | The enclosing attempt's recorded start and, if valid, completion. This is a reported envelope, not active execution time or the timestamps of individual requests. |
| `unavailable` | No supported timing measurement. Existing rows default to this until positively identified; a legacy row may still carry unclassified dates. |

The shared root-envelope policy requires a complete root conversation, no
parent, and an explicit envelope. TL1 supplies that evidence only when its
attempt lists exactly one transcript, including summary-listed files and files
on disk. Even an unavailable second file prevents assigning the whole attempt
to the first. A partial transcript cannot use the envelope. Embedded subagents
and child conversations never inherit it. If any assigned message has a valid
timestamp, its narrower observed span wins, including a single observation.

An ongoing attempt can supply a known start with no end. Missing, invalid or
reversed completion never becomes a completed zero-duration run. An invalid
start supplies no envelope. A recorded completion equal to the start is valid.
TL1 no longer substitutes its start for missing completion. Usage can still be
placed in an attributed start-hour bucket without claiming a request timestamp.

The independently retained `conversations.session_bounds_json` preserves the
source and both supported endpoints across usage replay and catalog recovery.
This requires catalog schema 25 and TL1 extractor/index version
`tl1-analysis-v5-session-bounds`. `usageAttributionVersion` remains **8**.

Delegation exposes the session's `time_bounds_source`, uses `reported` quality
for fallback envelopes, and leaves clock span unknown without both ordered
endpoints. Its measured activity still wins over the fallback. Session Shape's
wall/active durations remain derived only from message timestamps. Usage tables
still price the unchanged model, token categories and attributed usage hour.

## Bounded repair

`RepairSessionBounds` is separate from `RepairUsageAttribution`. It does not
replay transcripts, change version stamps, rebuild usage, reprice requests or
change identities. Each transaction examines at most 100 candidate rows, in
session-ID order, revalidating evidence while holding the catalog writer. A
successful second pass performs no updates. Interrupted runs can be repeated.

Candidates must be roots in both the session and conversation, with both
session endpoints missing and no linked child conversation. The repair rejects
any retained conversation with nonempty message timestamps. It accepts an
explicit retained root envelope from any source. For legacy TL1 rows it also
requires:

- Exactly one retained attempt links to the conversation's native identity.
- Its transcript list has exactly one nonempty filename, and the full native
  identity matches the installation, attempt and filename.
- The conversation start matches the valid attempt start, and its end matches
  the supported completion. For an attempt without completion, a historical
  start-as-end is tolerated only to recover the start; no end is written.
- The conversation coverage is complete.

Unsupported rows remain unknown. Existing message spans, one-sided session
bounds and subagents are untouched. The only updated columns are session
`started_at`, `ended_at`, `time_bounds_source`, and conversation
`session_bounds_json`.

On a library already opened by this build, preview and apply with:

```sh
go run ./cmd/pharos --config /path/to/config.json repair-session-bounds
go run ./cmd/pharos --config /path/to/config.json repair-session-bounds --apply
go run ./cmd/pharos --config /path/to/config.json repair-session-bounds --apply
```

The default is preview. These commands use the normal leased catalog open,
including initialization/schema migration. For a strictly read-only initial
inventory, use the projected-copy procedure below instead of opening the live
library through the CLI. Do not delete usage-attribution stamps or run the
usage-attribution repair for this timing correction.

## Disposable-copy validation

Create a fresh projection directly from a read-only source snapshot:

```sh
python3 tools/copy-session-bounds-cohort.py \
  /path/to/catalog.sqlite3 .context/session-bounds-copy.sqlite3
PHAROS_SESSION_BOUNDS_COPY="$PWD/.context/session-bounds-copy.sqlite3" \
  go test -tags probe -run '^TestSessionBoundsDisposableCopy$' -v ./internal/archive
```

The projection is deliberately not a library backup. It copies every session,
conversation, usage row, model request, tool call and usage-attribution stamp
in the affected TL1 cohort, the linked attempts, and message identities/times.
It omits message bodies and unrelated library tables. It refuses to overwrite
an existing file, uses SQLite `mode=ro` for the source, and never initializes
or repairs the source. The probe requires the disposable-copy marker, previews,
applies, runs a second pass, and compares SHA-256 checksums of every copied
non-timing fact. Use a fresh destination to repeat the experiment.

The November 10, 2026 Euclid validation found the following counts of stored
session rows, including source and mirror copies:

| Measurement | Before | After |
| --- | ---: | ---: |
| Root sessions missing a start, with retained conversation bounds | 4,369 | 154 |
| Those sessions with positive token counts | 1,778 | 16 |
| Complete attempt envelopes recovered | 0 | 4,214 |
| Ongoing attempt starts recovered, end unknown | 0 | 1 |
| Second-pass updates | — | 0 |

The remaining 154 comprise 98 missing attempt links and 56 multiple-transcript
attempts. All 15 copied model requests retained unknown request timestamps.
The copied facts checksum stayed
`89d30e24f80792adc192c8f06e4bf4e0ccb57aac7da31036be31d822c31ccb2e`.
The live catalog was not repaired. This snapshot contains one additional
tokenless root compared with the assignment's earlier 4,368-row audit.

The assignment's example session,
`agent-session_d65887a20f16565ba3ba00f30b99b483`, recovered
`2026-04-27T00:54:56.000Z` through `2026-04-27T01:29:12.000Z` with
`attempt-envelope` provenance. Its 21,638 tokens were unchanged.

On this branch, unrelated stale API calls in
`outcome_stages_probe_test.go` prevented compiling the complete `probe` test
set. Validation used a temporary Go `-overlay` replacing only that unrelated
probe with an empty package file; the tracked file was unchanged. The commands,
copy, overlay and output are retained in `.context/session-bounds/`.
While that unrelated probe remains stale, add
`-overlay .context/session-bounds/probe-overlay.json` to the validation's
`go test` command in this workspace.
