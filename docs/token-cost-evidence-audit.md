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
