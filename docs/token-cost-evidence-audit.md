# Token cost evidence audit

Missing usage, an explicit zero, and usage without a known price are different
cases. The read-only audit inventories every stored agent session and checks
its hourly ledger with Pharos's historical price book. It includes partially
unpriced sessions and retained mirrors, not just the Library's representative
rows.

```sh
mkdir -p .context/token-cost-audit
PHAROS_USAGE_AUDIT_CATALOG=/path/to/catalog.sqlite3 \
PHAROS_USAGE_AUDIT_OUTPUT="$PWD/.context/token-cost-audit/inventory.jsonl" \
  go test -tags probe -run '^TestUsageCostAudit$' -v -timeout 60m ./internal/archive
```

The probe opens SQLite with `mode=ro` and pins a read transaction. It runs no
initialization, migrations, capture, reindexing, or repairs. The output must be
a new file; it is created with owner-only permissions. It contains private
session identities, source locators, and retained delegation/result evidence,
so keep it outside version control. Large libraries can take several minutes.
Use a backup for a busy library: the read snapshot holds WAL checkpoint history
until the audit finishes.

Each JSONL row identifies a session and distinguishes:

- Usage owned by a linked child conversation, including the same exact-handle
  resolution used by the session hierarchy.
- Failed launches without execution evidence.
- Explicit zero usage reported by the source, such as rate-limit errors.
- Missing retained session messages, or messages without recognized usage.
- Usage-copy references, when present.
- Usage whose model or historical price is unavailable, including partially
  unpriced sessions, and partial prices with uncategorized tokens or missing rates.
- Usage recoverable by the current accounting code from retained messages.

Absence in retained messages does not prove that the provider never recorded
usage. Inspect the preserved capture or original transcript before concluding
that the source itself lacks it. Counts in delegation-launch envelopes belong
to the launching agent. An untyped token number in a result's prose cannot
establish input/cache/output costs.

After an accounting fix, replay the price gaps and structured result evidence
from a completed inventory:

```sh
PHAROS_USAGE_AUDIT_CATALOG=/path/to/catalog.sqlite3 \
PHAROS_USAGE_AUDIT_INPUT="$PWD/.context/token-cost-audit/inventory.jsonl" \
PHAROS_USAGE_AUDIT_OUTPUT="$PWD/.context/token-cost-audit/replay.jsonl" \
  go test -tags probe -run '^TestUsageCostAuditReplay$' -v -timeout 60m ./internal/archive
```

Replay reports before/after token coverage, model buckets, and historical
prices. It writes only the report, not the library. Preserve the inventory and
run logs with the replay; compare against the same library state.

Structured Agent results describe the final request. Recovery requires a
completed run with zero tool calls, a matching typed token breakdown and total,
a proved delegation identity, and no retained accounting in that child stream.
Resumes use the original agent's model when that identity is available. Custom
agent defaults and missing resume models remain unknown.

Run/session `modelUsage` counters retain their exact model identities and
chronological deltas. Mixed-model remainders are split only when all token
dimensions reconcile after subtracting retained requests by model family.
Ambiguous remainders remain unpriced. `usageAttributionVersion` makes the normal
library upgrade replay existing stored records after these accounting changes;
the audit itself never runs that upgrade.


## Scoped child evidence

The attribution repair also retains exact Agent/Task harness footers separately
in `agent_session_token_evidence`. A `subagent_tokens` counter is a reported
minimum that may cover only the final request. It is not a categorized run total
and does not change the audit's priceable accounting inventory. The delegation
API and reader expose it with its source message; branch rollups reconcile it
against overlapping aggregate remainders. This makes evidence such as BYOC's
116,837-token report visible on the child without moving the parent's launch
usage or inventing a child API price. Attribution version 7 replays all providers
so this metadata is backfilled from retained messages.


Repair preserves positive legacy session totals and ledger rows where retained
messages have no reconstructable accounting. In mixed workspaces, token metrics
include those preserved facts. Explicit zeros and settled copies remain
reconcilable. Categorized recovered results are subtracted from process claims;
a uniquely linked child transcript with its own accounting supersedes the
parent's recovered result, including in the reader. Ambiguous model remainders
stay unpriced rather than counting a recovered request twice.

## Synthetic error models and summary repair

`<synthetic>` describes a source error envelope. It remains on that message,
including the retained raw envelope, but cannot replace a real execution model
in the Claude or Conductor parser's inherited state. TL1 uses its configured
attempt model only when the transcript has no real execution model.

Conversation and agent-session summaries follow these rules:

1. Use the last real assistant/request model in that session's source order.
   Several real models followed by an error therefore summarize to the last
   real model. Advisor iterations retain their usage models without replacing
   the primary execution model. The summary is not an allocation of the whole
   session's usage.
2. Without ordered request evidence, use a single positive real usage model.
   Several real usage models without request chronology summarize to
   `Unknown model`; sorted model names do not establish chronology.
3. Without execution evidence, retain the configured summary fallback when
   available. A synthetic-only transcript with no configured real model keeps
   its placeholder. User messages and child-session replies cannot establish
   the parent's observed execution model.

The hourly usage ledger remains the pricing authority. A result naming one
model identifies that aggregate's model even when its `modelUsage` entry only
contains a context window. Naming several models with only context metadata
does not allocate aggregate usage. Exact per-model counters can allocate it
only when every token category reconciles after retained requests are
subtracted. Otherwise the remainder retains its placeholder or becomes
`Unknown model`, even when the session summary identifies a real model.

Attribution version **9** replays retained accounting under the existing
preservation contract, then repairs summary columns from retained messages and
the persisted ledger. Original files are unnecessary. Positive legacy facts
without reconstructable envelopes survive; explicit zeros and usage copies
still supersede obsolete facts. A legacy empty usage model inherits its old
session model for pricing: the repair makes that old effective model explicit
before changing the summary, merging overlapping hourly buckets without
changing token categories. It never assigns such usage to the new summary.
Conversation, session, message and request identities remain stable.

Claude extractor `claude-v3-execution-model`, Conductor extractor
`conductor-v15-execution-model`, and TL1 index
`tl1-analysis-v5-execution-model` revisit unchanged originals when they remain
available. Provider golden records carry these versions. The stored-data repair
also runs before a complete ingest marks an older workspace current, so an
unreconstructable legacy session does not miss the summary repair.

### November 10, 2026 read-only inventory

The selected Euclid catalog was opened with SQLite `mode=ro`. These are stored
session rows across sources and mirrors, not deduplicated executions:

| Category | Session rows | Tokens | Cost effect |
|---|---:|---:|---|
| Synthetic summary, one real positive usage model: Claude | 130 | 417,944,302 | Label only; $0 |
| Synthetic summary, one real positive usage model: TL1 | 77 | 175,319,360 | Label only; $0 |
| Existing synthetic usage: Conductor | 41 | 14,577,591 | No recoverable allocation; stays unpriced |
| Exact real-model allocation recovered from that synthetic usage | 0 | 0 | $0 recovered cost |
| Previously real-model-priced aggregate remainders lacking a supported split, within those 41 Conductor sessions | 8 | 2,141,887 | Remove $39.9753136 of unsupported pricing |

The last row is a separate accounting correction, not a benefit of relabeling
the 207 sessions or newly recovered model usage. Those eight sessions contain
both synthetic usage and other aggregate usage. The 41-session replay retains
all **1,589,639,789** session tokens and every token-category total; known cost
changes from $819.1306652 to $779.1553516 as the unsupported real-model portions
become unknown. This is a bounded dry run, not a whole-library cost forecast.
The live catalog was not migrated. Private per-session inventory, replay and
comparison files are in `.context/synthetic-*` for this workspace.

### Applying and validating the backfill

1. Combine the shared attribution policy with assignments 01, 02 and 04 before
   deploying. This change starts from attribution version 8 and selects 9.
   If another combined change has already shipped version 9, choose a new
   version above the deployed one so existing workspaces replay the combined
   rules. Keep session-summary policy separate from child-model allocation;
   a parent summary is never evidence of a child's actual model.
2. Use a disposable catalog backup first. Run the normal library upgrade or
   `pharos repair-usage-attribution` against that backup's configuration. Both
   use the same per-workspace transaction and version marker; source capture
   and mass source-text edits are unnecessary.
3. Compare conversation/session/message/request IDs, raw message evidence,
   every session and workspace token category, and the hourly ledger. Report
   summary-only changes, exact usage attribution changes, and unresolved
   allocations separately. Count a pricing removal due to unsupported prior
   allocation separately from newly recovered prices. Do not extrapolate
   stored-row totals into deduplicated spend.
4. Repeat repair: it should repair zero workspaces. Reindex available originals
   with the new extractor versions and confirm the summaries remain corrected
   while synthetic raw/message models remain visible. Missing originals must
   not prevent the stored-data repair.

Focused parser, token-usage, pricing and attribution tests cover real replies
followed by errors, synthetic-only runs, real model changes, configured attempt
fallbacks, isolated child evidence, context-only results, exact counters,
missing stored messages, preserved legacy pricing and idempotent replay.
The replay probe also reports before/after known costs and all unpriced tokens
independently of any selected placeholder bucket in its input inventory.

Validation: the focused parser, attribution, preservation, pricing and provider
conformance checks passed. The full Go fast tier remained incomplete at its
ten-minute limit under concurrent machine load; earlier maintenance and Ask
deadline failures passed their direct retries. The normal `-tags probe` build
also encounters pre-existing Git API compile errors in
`outcome_stages_probe_test.go`; the read-only replay above was validated by
compiling the normal `go list` source/test file set plus
`usage_audit_probe_test.go` explicitly. No live migration was run.
