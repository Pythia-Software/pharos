# Plugins: implementation plan

Plan, 2026-11-02, revised after the first design review, for
[plugins](plugins.md). Nothing here is built. The public
contracts are in [`contracts/plugins/`](../contracts/plugins/); this document is
the service's side: components and their Go interfaces, the catalog tables, the
export datasets, how runs fit the [operation contract](background-work.md), the
invariants and their tests, and the order of work.

Names below are proposals. Everything lives in `internal/archive` (one package)
in files prefixed `plugin_`, `connector_`, `privacy_` and `receiver_`.

## Components

```text
                   install link / Settings
                            │
                 ┌──────────▼───────────┐       consent, Mac allowance
                 │ pluginInstaller      │──────▶ plugin_installations, plugin_consents
                 └──────────┬───────────┘
                            │
 index finished ─▶ ┌────────▼─────────┐  due   ┌────────────────┐
 clock tick     ─▶ │ pluginTriggers   │──────▶ │ pluginRuns     │ (an Op per run)
                   └──────────────────┘        └──┬──────────┬──┘
                                       scripted   │          │ declarative
                                  ┌───────────────▼──┐  ┌────▼───────────────┐
                                  │ processSupervisor│  │ declarativeExporter│──▶ receiverClient
                                  └───────┬──────────┘  └────┬───────────────┘
                     request.json, token  │                  │ same token
                                  ┌───────▼──────────────────▼──┐
                                  │ connectorAPI (loopback)     │◀── privacy filter,
                                  │ grant checks, accounting    │    export datasets
                                  └───────┬─────────────────────┘
                     result.json, drops   │ (end of run)
            ┌─────────────────────────────▼───────────────────────┐
            │ import: dropValidator → dropAdopter / usageImporter │
            │ export: deliveryLedger.commitRun                    │
            └─────────────────────────────────────────────────────┘
```

### Manifest and installation

```go
// PluginManifest is a decoded pharos-plugin/1 document, validated against the
// embedded contracts/plugins schemas and then against the rules that span
// documents: every field a declarative send names is in its read permissions,
// group keys and periods are ones the dataset allows, the receiver URL is
// under a destination.
type PluginManifest struct {
	ID, Name, Publisher, Version, Direction, Description string
	Permissions PluginPermissions
	Params      json.RawMessage // a JSON Schema
	Triggers    []PluginTrigger
	Guards      PluginGuards
	Limits      PluginLimits
	Registration *PluginRegistration
	Supports    []string
	Identity    struct{ OnReinstall string }
	Run         PluginRun // Interpreter+Entry, Executable, or Declarative
	UpdateURL   string
}

func parseManifest(data []byte) (PluginManifest, error)
// permissionsHash is sha256 over the RFC 8785 canonical JSON of Permissions
// with every schema default filled in, so spelling out a default never asks
// for consent again. Consent, grants, reports and hello all carry it.
func (m PluginManifest) permissionsHash() string

type pluginPackage struct {
	Manifest   PluginManifest
	Source     string // URL or local path, shown as the publisher's origin
	BundleHash string // sha256 over the bundle's sorted file list and contents
	Files      map[string][]byte
}

type pluginInstaller interface {
	fetch(ctx context.Context, source string) (pluginPackage, error)
	// consentView is the one screen: who, what is read or produced, where it
	// goes, how often, what stays out, and for an exporter a preview of
	// today's delivery computed through the connector API with a dry-run grant.
	consentView(ctx context.Context, pkg pluginPackage) (consentView, error)
	install(ctx context.Context, pkg pluginPackage, params json.RawMessage) (installationID string, err error)
	// register runs OAuth for native apps: loopback listener, PKCE, browser,
	// code exchange; stores refresh token and salt in the installation row.
	register(ctx context.Context, installationID string) error
	update(ctx context.Context, installationID string, pkg pluginPackage) (updateDecision, error)
	uninstall(ctx context.Context, installationID string, retractAll bool) error
	// allowOnThisMac resolves the manifest's interpreter on this Mac and checks
	// it really runs (on a Mac without the Command Line Tools /usr/bin/python3
	// is a stub that offers to install them), then records it with the Mac
	// settings for a scripted plugin.
	allowOnThisMac(ctx context.Context, installationID string, settings macAllowance) error
}
```

Bundles are copied to `<library>/plugins/<plugin id>/<bundle hash>/` and run
from there. A bundle never changes in place; an update writes a new folder.

### Triggers

```go
// pluginTriggers decides which installations are due. It reads trigger state
// from the catalog and is told about index completions; it never runs work.
type pluginTriggers interface {
	due(now time.Time) []dueRun
	indexFinished(summary indexSummary) // new and changed exportable items, tokens
	recordOutcome(installationID string, outcome runOutcome, now time.Time)
}
```

Schedules follow `cloud_schedule.go`: a persisted next run, one catch-up after
downtime, retries after 1, 6 and 24 hours, then the regular schedule; five
consecutive failures pause the installation. Guards (minimum interval, runs a
day, quiet hours, power, idle) are checked when a run is due, not when it is
scheduled. `threshold` counts only exportable items, so private activity never
fires it.

### Runs and the operation contract

Each run is an `Op` with kind `plugin.run` and variant `import` or `export`,
subject the installation ID, and steps:

| Step | Import | Export (scripted) | Export (declarative) |
| --- | --- | --- | --- |
| `prepare` | staging folder, request, token | same | same |
| `script` | process runs | process runs | `hello`, queries, `batches` |
| `review` | first run after a change: held for **Does this look right?** | — | — |
| `adopt` / `commit` | validate drop, adopt files and usage | commit served items, cursor | `commit` at receiver, then ledger |

Proposed kinds-table row (to be added to [background work](background-work.md#3-the-kinds-table)):

| Kind | Process | Holds | Affects | Stop | On stop or eject | Latency | Journal | Pri |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `plugin.run/import` | svc | `D:w` (script, staging); `D:w W:w` in `adopt` | captures, usage | `plugin` | SIGTERM to the process group, SIGKILL after 10 s; staging discarded; run `interrupted` | record | always | bg |
| `plugin.run/export` | svc | `D:r`; `W:w` in `commit` | — | `plugin` | as above; nothing acknowledged | record | always | bg |

`D:r` because the connector API reads the catalog; reads take no writer hold,
keeping to "requests never write". Background Processing lists one row per
installed plugin, under a **Plugins** heading, with its own schedule.

### Process supervision

```go
type processSupervisor interface {
	// start runs argv in dir with env as its own process group, stdin closed,
	// stdout and stderr captured to a 1 MiB ring each.
	start(ctx context.Context, argv []string, dir string, env []string) (*pluginProcess, error)
}

type pluginProcess interface {
	// wait returns when the process exits or a limit stops it.
	wait() (exitCode int, stopped *limitStop)
	stop(reason string) // SIGTERM to the group, SIGKILL after 10 s
}

type limitStop struct {
	Limit string // runtime, cpu, memory, bytes, requests, rows, served_bytes
	Value, Max int64
}
```

Limits: wall time by deadline; CPU by `RLIMIT_CPU`; memory by sampling the
group's resident size every 2 s; staging bytes by measuring `output_dir` every
2 s and by `RLIMIT_FSIZE` per file; API limits by the connector API, which
revokes the token and calls `stop`. The process runs in the utility I/O tier
with lowered priority, like capture (`capture_iopolicy_darwin.go`).

### Connector API

```go
// connectorRun is one run's grant and accounting. It lives in memory for the
// run; nothing is written while requests are served.
type connectorRun struct {
	Token        string
	Installation installationRow
	Grant        PluginPermissions
	Limits       runLimits
	AsOf         time.Time          // run start minus the library's export delay
	Snapshot     readSnapshotSource // short read transactions only
	mu           sync.Mutex
	used         connectorUsage
	served       map[servedKey]servedItem // (dataset, item) → version and fields hash
	periods      map[periodKey]string     // (series, period) → fingerprint served
}

type connectorAPI interface {
	open(run runHandle, grant PluginPermissions, limits runLimits) (*connectorRun, error)
	handler() http.Handler // /connector/v1/*, token-authenticated
	close(token string) connectorAccount // revokes the token; returns what was served
}
```

Every handler does the same things, in this order: authenticate the token;
check the request against the grant (dataset, fields, mode, group keys, text,
export), refusing with the error body naming what was refused, before looking
up whether an item exists; compile an export query; apply forced filters
(privacy, `as_of`, restrictions); run it in a short read transaction; redact
and count the response, refusing with `429` before a limit and revoking past
it.

### Export queries

```go
// exportQuery is a decoded export-query/1 document.
type exportQuery struct {
	Where   []exportTerm
	Group   *exportGroup // Period, Keys, Summaries
	Having  []exportTerm
	Select  []string
	OrderBy []exportOrder
	Limit   int
}

// checkExportQuery walks every field the query names (filters, period, keys,
// summaries, having, select, order) and refuses the first that is not granted,
// not allowed as a key or period, or not allowed by the mode. It is complete:
// the language names fields only in those places.
func checkExportQuery(dataset exportDataset, grant datasetGrant, query exportQuery) error

// compileExportQuery turns a checked query into the Query Table request for the
// dataset's source, adding forced filters. Filters, keys and ordering on a
// redacted field compile to the redacted expression, never the original.
func compileExportQuery(dataset exportDataset, run *connectorRun, query exportQuery) (qt.QueryRequestV3, error)
```

The language mirrors Query Table's semantics, so compiling is a mapping, not a
translation: literals and `any` become `WhereTerm`s, `period` a calendar key
with its interval and time zone, summaries `SummaryColumn`s, `having` the
result's where. When Query Table changes its wire format, only
`compileExportQuery` changes.

### Export datasets

The first versions. Fields map onto today's [schemas](../schemas); IDs are
opaque per receiver key, and hashed or dropped fields follow the manifest's
redaction. Group keys are listed per dataset; anything else can be selected in
rows mode but not grouped by.

**`usage/1`**: rows of the `usage` dataset (one per session, hour and model).

| Field | Source | Group key |
| --- | --- | --- |
| `at` | `usage.hour` (period field) | period |
| `provider`, `model`, `model_family`, `effort` | same names | yes |
| `harness`, `channel`, `machine` | same names | yes |
| `repository_name` | `usage.repository_name` | yes |
| `session_kind`, `run_role` | same names | yes |
| `work_item` | `usage.workspace_id`, as a `library/1` ID | no |
| `uncached_input_tokens`, `cache_read_input_tokens`, `cache_creation_5m_input_tokens`, `cache_creation_1h_input_tokens`, `output_tokens`, `reasoning_output_tokens`, `unclassified_tokens`, `total_tokens` | same names | no |
| `cost_usd`, `price_status` | same names | `price_status` |

**`automation-usage/1`**: imported usage records, from `imported_usage`. Fields
`at` (period field), `provider`, `route`, `model`, `served_model`, `effort`,
`harness`, `automation_kind`, `automation_job`, `automation_dataset`,
`automation_role` (group keys), the token fields as in `usage/1`,
`cost_usd`, `cost_status` (group key), `estimate_usd`, `duration_ms`,
`outcome` (group key). `cost_usd` is present only when `cost_status` is
`priced` or `assumed`.

**`library/1`**: work items, from the `library` dataset: `id`, `activity_at`
(period field), `title`, `repository_name`, `outcome`, `channel` (group keys:
`repository_name`, `outcome`, `channel`), and totals over the work item's
activity up to `as_of`: `conversation_count`, `turn_count`, `token_count`,
`cost_usd`, `pr_count`, `changed_file_count`, `providers`, `models`,
`harnesses`.

**`conversations/1`**: one row per conversation. No dataset has this grain
today; it is a new projection over `conversations` and `agent_sessions`: `id`,
`work_item` (a `library/1` ID), `parent` (for subagents), `role` (root or
subagent), `started_at` (period field), `last_activity_at`, `provider`,
`model`, `effort`, `harness`, `channel`, `machine`, `repository_name`, `title`,
`message_count`, `human_messages`, `total_tokens`, `cost_usd`. Group keys:
`role`, `provider`, `model`, `effort`, `harness`, `channel`, `machine`,
`repository_name`.

## Catalog tables

```sql
CREATE TABLE IF NOT EXISTS plugin_installations (
  id TEXT PRIMARY KEY,                   -- inst_…
  plugin_id TEXT NOT NULL,
  state TEXT NOT NULL,                   -- active, paused, needs_consent, uninstalled
  pause_reason TEXT,                     -- failures, disconnected, superseded, takeover_needed, …
  manifest_json TEXT NOT NULL,           -- the consented manifest
  version TEXT NOT NULL,
  bundle_hash TEXT NOT NULL,
  permissions_hash TEXT NOT NULL,
  params_json TEXT NOT NULL DEFAULT '{}',
  sender_id TEXT NOT NULL,
  hash_key BLOB NOT NULL,                -- from the receiver's salt, or random
  credential_json TEXT,                  -- refresh token; never served to scripts
  library_instance TEXT NOT NULL,        -- the writable instance it last ran in
  last_version INTEGER NOT NULL DEFAULT 0, -- the version clock's high-water mark
  receiver_receipt TEXT,
  cursor_json TEXT,                      -- last acknowledged
  change_position TEXT,                  -- declarative exporters; opaque token
  installed_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  uninstalled_at TEXT                    -- rows are kept for identity.on_reinstall = keep
);
CREATE UNIQUE INDEX IF NOT EXISTS plugin_installations_live_idx
  ON plugin_installations(plugin_id) WHERE uninstalled_at IS NULL;

CREATE TABLE IF NOT EXISTS plugin_mac_allowances (
  installation_id TEXT NOT NULL REFERENCES plugin_installations(id),
  host_id TEXT NOT NULL,
  interpreter_path TEXT,
  settings_json TEXT NOT NULL DEFAULT '{}',
  allowed_at TEXT NOT NULL,
  PRIMARY KEY(installation_id, host_id)
);

CREATE TABLE IF NOT EXISTS plugin_consents (
  id INTEGER PRIMARY KEY,
  installation_id TEXT NOT NULL REFERENCES plugin_installations(id),
  kind TEXT NOT NULL,                    -- install, update, reconsent, code_change, takeover
  version TEXT NOT NULL,
  bundle_hash TEXT NOT NULL,
  permissions_hash TEXT NOT NULL,
  difference_json TEXT,
  decision TEXT NOT NULL,                -- yes, no
  decided_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS plugin_trigger_state (
  installation_id TEXT PRIMARY KEY REFERENCES plugin_installations(id),
  next_due_at TEXT,
  retry_at TEXT,
  last_run_at TEXT,
  last_success_at TEXT,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  runs_day TEXT,
  runs_that_day INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS plugin_runs (
  run_id TEXT PRIMARY KEY,               -- a retry is a new run
  installation_id TEXT NOT NULL REFERENCES plugin_installations(id),
  retry_of TEXT,
  op_id TEXT,
  host_id TEXT NOT NULL,                 -- the Mac that ran it
  library_instance TEXT NOT NULL,
  trigger TEXT NOT NULL,
  started_at TEXT NOT NULL,
  as_of TEXT,                            -- exporters
  finished_at TEXT,
  status TEXT NOT NULL,                  -- complete, partial, failed, interrupted, held, discarded
  limit_reached TEXT,
  exit_code INTEGER,
  message TEXT,
  permissions_hash TEXT NOT NULL,
  requests INTEGER NOT NULL DEFAULT 0,
  rows INTEGER NOT NULL DEFAULT 0,
  served_bytes INTEGER NOT NULL DEFAULT 0,
  dropped_bytes INTEGER NOT NULL DEFAULT 0,
  transferred_bytes INTEGER,
  items INTEGER NOT NULL DEFAULT 0,
  records INTEGER NOT NULL DEFAULT 0,
  retractions INTEGER NOT NULL DEFAULT 0,
  receipt_json TEXT,
  full_resend_reason TEXT,
  replace_all INTEGER NOT NULL DEFAULT 0,
  cursor_json TEXT,                      -- as returned; installed only if acknowledged
  log_path TEXT
);
CREATE INDEX IF NOT EXISTS plugin_runs_installation_idx ON plugin_runs(installation_id, started_at);

CREATE TABLE IF NOT EXISTS plugin_ledger (    -- pruned after 180 days
  run_id TEXT NOT NULL REFERENCES plugin_runs(run_id),
  seq INTEGER NOT NULL,
  dataset TEXT NOT NULL,
  item_ref TEXT NOT NULL,                -- internal ID, or series:period for aggregates
  opaque_id TEXT,
  action TEXT NOT NULL,                  -- served, retracted
  version INTEGER NOT NULL,
  fields_hash TEXT,
  at TEXT NOT NULL,
  PRIMARY KEY(run_id, seq)
);
CREATE INDEX IF NOT EXISTS plugin_ledger_at_idx ON plugin_ledger(at);

CREATE TABLE IF NOT EXISTS plugin_deliveries (  -- never pruned
  installation_id TEXT NOT NULL REFERENCES plugin_installations(id),
  dataset TEXT NOT NULL,
  item_ref TEXT NOT NULL,
  subject_ref TEXT NOT NULL,             -- the work item or usage record whose privacy governs it
  opaque_id TEXT NOT NULL,
  served_version INTEGER NOT NULL,
  served_fields_hash TEXT NOT NULL,      -- what was sent, to tell a real change from none
  served_at TEXT NOT NULL,
  acknowledged_version INTEGER,
  acknowledged_at TEXT,
  state TEXT NOT NULL,                   -- held, retraction_queued, retracted
  retraction_queued_at TEXT,
  retracted_at TEXT,
  PRIMARY KEY(installation_id, dataset, item_ref)
);
CREATE INDEX IF NOT EXISTS plugin_deliveries_subject_idx ON plugin_deliveries(subject_ref);
CREATE INDEX IF NOT EXISTS plugin_deliveries_queued_idx
  ON plugin_deliveries(installation_id) WHERE state = 'retraction_queued';

CREATE TABLE IF NOT EXISTS plugin_period_deliveries (
  installation_id TEXT NOT NULL REFERENCES plugin_installations(id),
  series TEXT NOT NULL,                  -- the send's name
  dataset TEXT NOT NULL,
  period TEXT NOT NULL,                  -- period start
  served_version INTEGER NOT NULL,
  served_fingerprint TEXT NOT NULL,      -- over all the period's groups
  acknowledged_fingerprint TEXT,
  served_at TEXT NOT NULL,
  acknowledged_at TEXT,
  PRIMARY KEY(installation_id, series, period)
);

CREATE TABLE IF NOT EXISTS plugin_imports (
  run_id TEXT NOT NULL REFERENCES plugin_runs(run_id),
  host_id TEXT NOT NULL,                 -- derived imp_… host
  source_name TEXT NOT NULL,
  original_path TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  adopted_at TEXT NOT NULL,
  PRIMARY KEY(run_id, host_id, source_name, original_path)
);

CREATE TABLE IF NOT EXISTS imported_usage (
  plugin_id TEXT NOT NULL,               -- the namespace's owner
  namespace TEXT NOT NULL,
  record_id TEXT NOT NULL,
  at TEXT NOT NULL,
  ended_at TEXT,
  provider TEXT NOT NULL,
  route TEXT,
  model TEXT NOT NULL,
  served_model TEXT,
  effort TEXT,
  harness TEXT,
  harness_version TEXT,
  host_id TEXT,                          -- NULL: the Mac that imported it
  uncached_input_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_input_tokens INTEGER NOT NULL DEFAULT 0,
  cache_creation_5m_input_tokens INTEGER NOT NULL DEFAULT 0,
  cache_creation_1h_input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_output_tokens INTEGER NOT NULL DEFAULT 0,
  unclassified_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL,                         -- only when cost_status is priced or assumed
  cost_status TEXT NOT NULL,
  priced_model TEXT,
  estimate_usd REAL,                     -- exposure; never summed into cost
  estimate_basis TEXT,
  duration_ms INTEGER,
  outcome TEXT,
  automation_kind TEXT NOT NULL,
  automation_job TEXT,
  automation_dataset TEXT,
  automation_role TEXT,
  labels_json TEXT NOT NULL DEFAULT '{}',
  installation_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(plugin_id, namespace, record_id)
);
CREATE INDEX IF NOT EXISTS imported_usage_at_idx ON imported_usage(at);

CREATE TABLE IF NOT EXISTS privacy_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  default_state TEXT NOT NULL DEFAULT 'shareable',
  export_delay_seconds INTEGER NOT NULL DEFAULT 259200   -- three days
);

CREATE TABLE IF NOT EXISTS privacy_rules (
  id TEXT PRIMARY KEY,
  position INTEGER NOT NULL,
  audience TEXT NOT NULL DEFAULT '*',    -- '*' = every exporter; the only value for now
  subject_kind TEXT NOT NULL,            -- workspace or usage
  filter_json TEXT NOT NULL,             -- Query Table where terms over library or automation usage fields
  outcome TEXT NOT NULL,                 -- private, shareable
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS privacy_rule_history (
  id INTEGER PRIMARY KEY,
  at TEXT NOT NULL,
  rules_json TEXT NOT NULL,              -- the whole ordered list after the change
  default_state TEXT NOT NULL,
  export_delay_seconds INTEGER NOT NULL,
  changed_states INTEGER NOT NULL,
  queued_retractions INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS privacy_marks (
  subject_kind TEXT NOT NULL,            -- workspace or usage
  subject_id TEXT NOT NULL,              -- usage marks may name a namespace or a job
  audience TEXT NOT NULL DEFAULT '*',
  state TEXT NOT NULL,
  marked_at TEXT NOT NULL,
  PRIMARY KEY(subject_kind, subject_id, audience)
);

CREATE TABLE IF NOT EXISTS privacy_states (
  subject_kind TEXT NOT NULL,            -- workspace or usage
  subject_id TEXT NOT NULL,
  audience TEXT NOT NULL DEFAULT '*',
  state TEXT NOT NULL,                   -- private, shareable
  reason_kind TEXT NOT NULL,             -- mark, rule, default
  reason_ref TEXT,
  settled INTEGER NOT NULL DEFAULT 0,    -- exportable only when settled
  decided_at TEXT NOT NULL,
  PRIMARY KEY(subject_kind, subject_id, audience)
);

CREATE TABLE IF NOT EXISTS export_changes (  -- the feed behind /changes; kept 30 days
  position INTEGER PRIMARY KEY AUTOINCREMENT,
  subject_kind TEXT NOT NULL,            -- workspace, usage
  subject_id TEXT NOT NULL,
  dataset TEXT NOT NULL,
  item_ref TEXT,                         -- NULL for a time range of aggregates
  range_from TEXT,
  range_to TEXT,
  event_at TEXT NOT NULL,                -- when the activity happened
  written_at TEXT NOT NULL               -- when Pharos recorded it
);
CREATE INDEX IF NOT EXISTS export_changes_written_idx ON export_changes(written_at);
```

Writers append to `export_changes` in the transactions that change what an
exporter could see: indexing, attribution, privacy decisions, usage imports.
Readers never write. A change becomes visible to a run when
`max(written_at, event_at + delay) <= the run's start`, so activity surfaces
as it ages past the cutoff, and a late write about old activity surfaces at
once. Visibility is computed when the feed is read, so changing the delay
needs no rewrite. A token is the installation's last visible position,
encrypted with its hash key; it does not move when only private rows were
passed, and its value says nothing about them. Rows older than 30 days are
pruned whatever any installation has read; a token older than that gets
`cursor_expired`.

Versions are not stored per item. `nextVersion` stamps whatever a run serves,
and `plugin_deliveries.served_fields_hash` tells a real change from a
re-send, so a run sends an item again only when what it would send differs.

## Invariants and tests

From [plugins](plugins.md#recordkeeping), with the test that pins each. All are
Go tests on `testCatalog`/`testServer` unless marked.

| Invariant | Test |
| --- | --- |
| 1. Only exportable items are served (settled, not private, inside permissions), including through precomputed paths | `TestConnectorServesOnlyExportableItems`: marks, rules, unsettled work items, a forced fast path; compares every dataset's rows and aggregates with and without a private work item |
| 2. Every served item gets a delivery row whatever the run's status | `TestFailedRunsStillRecordWhatTheyWereServed` |
| 3. A retraction is queued for every delivery no longer exportable | `TestMarkingSentWorkPrivateQueuesRetractions` |
| 4. A run sends exportable items that changed since acknowledged | `TestChangesFollowAcknowledgedState` |
| 5. Ledger pruning never touches delivery state | `TestLedgerPruneKeepsDeliveries` |
| 6. Opaque IDs are stable per receiver key and unrelated across receivers | `TestOpaqueIDsDependOnlyOnTheReceiverKey` |
| 7. A mark or rule change commits with the retractions it queues | `TestPrivacyChangeAndRetractionsCommitTogether` |
| 8. Nothing with activity after `as_of` is served, except retractions | `TestExportDelayHoldsBackRecentActivity`, including a delay change |
| 9. Versions only increase, across a restore | `TestVersionsMoveForwardAfterRestore` |
| 10. A restored or copied library sends nothing until resumed, then replaces everything | `TestRestoredLibraryPausesThenTakesOver` |
| No side channels: tokens, versions, counts and 403/404 do not move for private activity | `TestPrivateActivityLeavesNoTrace` |
| Export queries naming any ungranted field, key or period are refused, wherever the field appears | `TestExportQueryChecksEveryField` |
| A cursor older than the feed's retention gets `cursor_expired`, and a full send follows | `TestExpiredChangeTokensResync` |
| Limits stop a run: runtime, memory, bytes, requests | `TestSupervisorStopsARunAtEachLimit` (slow) |
| A drop cannot escape, name a real host, or carry an undeclared kind or namespace | `TestDropValidationRefusesMistakes` |
| Re-importing usage never double counts; estimates never count as cost; another plugin cannot overwrite a namespace | `TestUsageImportIsIdempotentAndOwned` |
| Adoption merges and never deletes | `TestAdoptionReplacesAndKeeps` |
| Uninstalling keeps imported data | `TestUninstallKeepsImports` |
| Contracts and their examples agree (Python) | `contracts/plugins/test_contracts.py` |

## Order of work

Importers first, since the labeling usage import is wanted now and needs no
privacy machinery; exporters after privacy.

1. **Contracts and docs** (this change).
2. **Plugin runtime and transcript importers:** manifests, installations,
   consent, Mac allowance with interpreter checks, triggers, supervisor,
   `plugin.run/import`, drop validation and adoption, **Does this look
   right?**, `pharos plugin install|list|test|run|uninstall`. `ssh_pull.py`
   becomes a catalog plugin.
3. **Usage imports:** `imported_usage`, the usage views reading it, the
   automation channel, and a labeling ledger importer.
4. **Privacy:** settings with the export delay, rules, work item marks,
   states, settled gating, the Library badge, filter and rule editor with
   previews, and **Leaving soon**.
5. **Export datasets and connector reads:** the export query checker and
   compiler, `usage/1`, `automation-usage/1`, `library/1`, `conversations/1`,
   forced filters (fast paths and fact cubes included), redaction,
   accounting, and the `export_changes` feed.
6. **Exporters:** the version clock, delivery ledger, retractions and whole
   period restatements, scripted exporters, egress views.
7. **Declarative exporter and receiver protocol:** registration, receiver
   client, library instances and takeover, `declarative-export/1`, the Pythia
   plugin.
8. **Pharos to Pharos:** `/export` bundles with filtered SQLite records, and a
   bundle importer.

## Hazards

- **Precomputed answers bypass row filters.** Host fast paths inside
  `/metrics` and Machine Time's fact cubes answer counts and sums without
  scanning rows; the connector API must either apply the privacy and `as_of`
  filters to them or not use them. Invariant 1's test forces one.
- **Long reads pin the WAL.** Connector pages must be short read transactions,
  and a cancelled request must not leave a statement open (see the modernc
  cancel fix).
- **Writes on read paths.** All accounting stays in memory until the run ends;
  `TestRequestReadsDoNotWaitForTheCatalogWriter` should cover the connector
  routes.
- **Eject.** A running plugin is a hold; eject stops its process group and
  discards staging, and a declarative run that has not committed acknowledges
  nothing.
- **Clock.** The version clock trusts the Mac's clock only to move forward
  from the stored high-water mark; a clock set far ahead would push versions
  ahead, which is harmless but permanent.
- **Metadata is current, activity is delayed.** A work item past the cutoff
  shows its current title and repository, which may reflect activity newer
  than `as_of`. Titles are the likeliest to; if that matters, `library/1` can
  later carry the title as of `as_of` instead.
- **Time zones.** Periods are in the query's time zone, by default the
  library's; changing the library's time zone restates every period an
  installation was sent.
