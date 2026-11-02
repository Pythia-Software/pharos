# Plugins: implementation plan

Plan, 2026-11-02, for [plugins](plugins.md). Nothing here is built. The public
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
// documents (namespaces, destinations).
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
	Run         PluginRun // Command or Declarative, never both
	UpdateURL   string
}

func parseManifest(data []byte) (PluginManifest, error)
// permissionsHash is sha256 over the canonical JSON of Permissions; consent,
// grants, reports and hello all carry it.
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
	// allowOnThisMac records the interpreter and Mac settings for a scripted plugin.
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
	Token       string
	Installation installationRow
	Grant       PluginPermissions
	Limits      runLimits
	Snapshot    readSnapshotSource // short read transactions only
	mu          sync.Mutex
	used        connectorUsage
	served      map[servedKey]servedItem // item → highest version served
	periods     map[periodKey]string     // period → fingerprint served
}

type connectorAPI interface {
	open(run runHandle, grant PluginPermissions, limits runLimits) (*connectorRun, error)
	handler() http.Handler // /connector/v1/*, token-authenticated
	close(token string) connectorAccount // revokes the token; returns what was served
}
```

Every handler does the same five things, in order: authenticate the token;
check the request against the grant (dataset, fields, mode, text, export),
refusing with the error body naming what was refused; apply forced filters
(privacy, restrictions); run the query in a short read transaction; redact and
count the response, refusing with `429` before a limit and revoking past it.

### Export datasets

```go
// exportDataset is one published, versioned dataset. Its field list never
// changes once released.
type exportDataset interface {
	Name() string                 // "usage/1"
	Fields() []exportField        // name, type, source field in the UI schema
	// rows and aggregate run Query Table requests over the source dataset,
	// restricted to granted fields, with forced filters applied.
	query(ctx context.Context, run *connectorRun, request qt.QueryRequestV3) (json.RawMessage, error)
	// changes lists items and periods changed after a position, exportable only.
	changes(ctx context.Context, run *connectorRun, after changePosition, limit int) (changePage, error)
}
```

### Privacy

```go
type privacyEngine interface {
	// decide computes effective states for subjects whose metadata has settled,
	// inside the writer transaction that settled them.
	decide(ctx context.Context, tx *sql.Tx, subjects []privacySubject) error
	// filter is the forced filter: a predicate over the source dataset that
	// keeps only exportable rows. Every connector query and every precomputed
	// path the connector uses goes through it.
	filter(dataset string) qt.WhereTerm
	setMark(ctx context.Context, subject privacySubject, state string) (retractionPreview, error)
	replaceRules(ctx context.Context, rules []privacyRule, preview bool) (retractionPreview, error)
}

type privacySubject struct {
	Kind string // conversation, workspace, usage
	ID   string // conversation or workspace ID, or "namespace:id"
}
```

`setMark` and `replaceRules` commit the new states, the `export_changes` rows
and the queued retractions in one transaction (invariant 7).

### Import adoption

```go
type dropValidator interface {
	// validate checks result.json and output_dir against the contract and the
	// manifest: file types, paths, declared machines, kinds, namespaces,
	// SQLite integrity, and that each kind's adapter parses its drop.
	validate(ctx context.Context, run runHandle, result RunResult) (dropPlan, error)
}

type dropAdopter interface {
	// adopt merges transcript drops into captures/<derived host>/<source>/
	// (replacing captured copies, keeping rewrites in history/, never marking
	// missing) and writes plugin_imports rows. Files are durable before the
	// catalog transaction commits.
	adopt(ctx context.Context, plan dropPlan) (adoptSummary, error)
}

type usageImporter interface {
	// upsert writes usage-records/1 lines into imported_usage by (namespace,
	// id), replacing older copies, and records the run.
	upsert(ctx context.Context, tx *sql.Tx, namespace string, records iter.Seq2[usageRecord, error], run runHandle) (usageSummary, error)
}
```

Derived host IDs are `imp_` plus a hash of the installation's stable identity
and the machine key, so a drop can never name `host_…` IDs of real Macs.
Adopted captures are then indexed like any other host's: the existing
`IndexCapture` path, attributed to the derived host.

### Delivery ledger

```go
type deliveryLedger interface {
	// commitRun records the run, its ledger entries and delivery state in one
	// transaction. served is everything the connector served; acknowledged is
	// true for complete and partial results (scripted) or a receiver commit
	// (declarative). Served items get a delivery row whatever the status.
	commitRun(ctx context.Context, run runRecord, served connectorAccount, acknowledged bool, cursor json.RawMessage) error
	// retractions lists queued retractions for an installation.
	retractions(ctx context.Context, installationID string, limit int) ([]retraction, error)
	// resetAcknowledged forgets acknowledged versions after a receiver asks
	// for everything; served state stays, so retractions still reach it.
	resetAcknowledged(ctx context.Context, tx *sql.Tx, installationID string, reason string) error
	prune(ctx context.Context, before time.Time) error // ledger only, never deliveries
}
```

### Declarative exporter and receiver client

```go
// declarativeExporter is a pharos-run/1 plugin compiled into Pharos. It gets
// the same request and token as a script and reads through the same
// connector API handler (in process, same accounting).
type declarativeExporter interface {
	run(ctx context.Context, request RunRequest, manifest PluginManifest) (RunResult, error)
}

type receiverClient interface {
	hello(ctx context.Context, request helloRequest) (helloResponse, error)
	batch(ctx context.Context, runID string, n int, body reportBatch) error
	commit(ctx context.Context, runID string, batches int) (commitResponse, error)
	unregister(ctx context.Context) error
}
```

The client refreshes access tokens from the stored refresh token, never sends
outside the manifest's `destinations`, and treats `401`/`403` as **paused:
disconnected at the receiver**.

## Export datasets

The first versions. Fields map onto today's [schemas](../schemas); IDs are
opaque per receiver, and hashed or dropped fields follow the manifest's
redaction.

**`usage/1`** — rows of the `usage` dataset plus imported usage.

| Field | Source |
| --- | --- |
| `day`, `week`, `month` | `usage.day`, `week`, `month` |
| `provider`, `model`, `model_family`, `effort` | same names |
| `harness`, `channel`, `machine` | same names; imported usage has its automation channel |
| `repository_name` | `usage.repository_name` |
| `session_kind`, `run_role` | same names |
| `uncached_input_tokens`, `cache_read_input_tokens`, `cache_creation_5m_input_tokens`, `cache_creation_1h_input_tokens`, `output_tokens`, `reasoning_output_tokens`, `unclassified_tokens`, `total_tokens` | same names |
| `cost_usd`, `price_status` | same names; imported usage's own cost when priced or assumed |
| `automation_kind`, `automation_job` | imported usage only |

**`library/1`** — work items, from the `library` dataset: `id`, `activity_at`,
`title`, `repository_name`, `outcome`, `conversation_count`, `turn_count`,
`token_count`, `cost_usd`, `pr_count`, `changed_file_count`, `providers`,
`models`, `harnesses`, `channel`.

**`conversations/1`** — one row per conversation. No dataset has this grain
today; it is a new projection over `conversations` and `agent_sessions`: `id`,
`work_item` (a `library/1` ID), `parent` (for subagents), `role` (root or
subagent), `started_at`, `last_activity_at`, `provider`, `model`, `effort`,
`harness`, `channel`, `machine`, `repository_name`, `title`, `message_count`,
`human_messages`, `total_tokens`, `cost_usd`.

## Catalog tables

```sql
CREATE TABLE IF NOT EXISTS plugin_installations (
  id TEXT PRIMARY KEY,                   -- inst_…
  plugin_id TEXT NOT NULL,
  state TEXT NOT NULL,                   -- active, paused, needs_consent, uninstalled
  pause_reason TEXT,
  manifest_json TEXT NOT NULL,           -- the consented manifest
  version TEXT NOT NULL,
  bundle_hash TEXT NOT NULL,
  permissions_hash TEXT NOT NULL,
  params_json TEXT NOT NULL DEFAULT '{}',
  sender_id TEXT NOT NULL,
  hash_key BLOB NOT NULL,                -- from the receiver's salt, or random
  credential_json TEXT,                  -- refresh token; never served to scripts
  receiver_receipt TEXT,
  cursor_json TEXT,                      -- last acknowledged
  change_position INTEGER,               -- declarative exporters
  installed_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  uninstalled_at TEXT                    -- rows are kept for identity.on_reinstall = keep
);
CREATE UNIQUE INDEX IF NOT EXISTS plugin_installations_live_idx
  ON plugin_installations(plugin_id) WHERE uninstalled_at IS NULL;

CREATE TABLE IF NOT EXISTS plugin_mac_allowances (
  installation_id TEXT NOT NULL REFERENCES plugin_installations(id),
  host_id TEXT NOT NULL,
  interpreter TEXT,
  settings_json TEXT NOT NULL DEFAULT '{}',
  allowed_at TEXT NOT NULL,
  PRIMARY KEY(installation_id, host_id)
);

CREATE TABLE IF NOT EXISTS plugin_consents (
  id INTEGER PRIMARY KEY,
  installation_id TEXT NOT NULL REFERENCES plugin_installations(id),
  kind TEXT NOT NULL,                    -- install, update, reconsent, code_change
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
  run_id TEXT PRIMARY KEY,
  installation_id TEXT NOT NULL REFERENCES plugin_installations(id),
  op_id TEXT,
  host_id TEXT NOT NULL,                 -- the Mac that ran it
  trigger TEXT NOT NULL,
  started_at TEXT NOT NULL,
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
  conversations INTEGER NOT NULL DEFAULT 0,
  records INTEGER NOT NULL DEFAULT 0,
  retractions INTEGER NOT NULL DEFAULT 0,
  receipt_json TEXT,
  full_resend_reason TEXT,
  cursor_json TEXT,                      -- as returned; installed only if acknowledged
  log_path TEXT
);
CREATE INDEX IF NOT EXISTS plugin_runs_installation_idx ON plugin_runs(installation_id, started_at);

CREATE TABLE IF NOT EXISTS plugin_ledger (    -- pruned after 180 days
  run_id TEXT NOT NULL REFERENCES plugin_runs(run_id),
  seq INTEGER NOT NULL,
  item_kind TEXT NOT NULL,               -- conversation, row, messages, period, retraction
  item_ref TEXT NOT NULL,                -- internal ID or dataset:period:group hash
  opaque_id TEXT,
  version INTEGER,
  fields_hash TEXT,
  at TEXT NOT NULL,
  PRIMARY KEY(run_id, seq)
);
CREATE INDEX IF NOT EXISTS plugin_ledger_at_idx ON plugin_ledger(at);

CREATE TABLE IF NOT EXISTS plugin_deliveries (  -- never pruned
  installation_id TEXT NOT NULL REFERENCES plugin_installations(id),
  item_kind TEXT NOT NULL,
  item_ref TEXT NOT NULL,
  opaque_id TEXT NOT NULL,
  served_version INTEGER NOT NULL,
  served_at TEXT NOT NULL,
  acknowledged_version INTEGER,
  acknowledged_at TEXT,
  state TEXT NOT NULL,                   -- held, retraction_queued, retracted
  retraction_queued_at TEXT,
  retracted_at TEXT,
  PRIMARY KEY(installation_id, item_kind, item_ref)
);
CREATE INDEX IF NOT EXISTS plugin_deliveries_queued_idx
  ON plugin_deliveries(installation_id) WHERE state = 'retraction_queued';

CREATE TABLE IF NOT EXISTS plugin_period_deliveries (
  installation_id TEXT NOT NULL REFERENCES plugin_installations(id),
  dataset TEXT NOT NULL,
  period TEXT NOT NULL,
  group_hash TEXT NOT NULL,
  served_fingerprint TEXT NOT NULL,
  acknowledged_fingerprint TEXT,
  served_at TEXT NOT NULL,
  acknowledged_at TEXT,
  PRIMARY KEY(installation_id, dataset, period, group_hash)
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
  PRIMARY KEY(namespace, record_id)
);
CREATE INDEX IF NOT EXISTS imported_usage_at_idx ON imported_usage(at);

CREATE TABLE IF NOT EXISTS privacy_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  default_state TEXT NOT NULL DEFAULT 'shareable',
  first_export_delay_seconds INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS privacy_rules (
  id TEXT PRIMARY KEY,
  position INTEGER NOT NULL,
  audience TEXT NOT NULL DEFAULT '*',    -- '*' = every exporter; the only value for now
  subject_kind TEXT NOT NULL,            -- conversation or usage
  filter_json TEXT NOT NULL,             -- Query Table where terms over library or usage fields
  outcome TEXT NOT NULL,                 -- private, shareable
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS privacy_rule_history (
  id INTEGER PRIMARY KEY,
  at TEXT NOT NULL,
  rules_json TEXT NOT NULL,              -- the whole ordered list after the change
  default_state TEXT NOT NULL,
  changed_states INTEGER NOT NULL,
  queued_retractions INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS privacy_marks (
  subject_kind TEXT NOT NULL,            -- conversation, workspace, usage
  subject_id TEXT NOT NULL,
  audience TEXT NOT NULL DEFAULT '*',
  state TEXT NOT NULL,
  marked_at TEXT NOT NULL,
  PRIMARY KEY(subject_kind, subject_id, audience)
);

CREATE TABLE IF NOT EXISTS privacy_states (
  subject_kind TEXT NOT NULL,            -- conversation, workspace, usage
  subject_id TEXT NOT NULL,
  audience TEXT NOT NULL DEFAULT '*',
  state TEXT NOT NULL,                   -- private, shareable
  reason_kind TEXT NOT NULL,             -- mark, rule, default, inherited
  reason_ref TEXT,
  settled INTEGER NOT NULL DEFAULT 0,    -- exportable only when settled
  decided_at TEXT NOT NULL,
  PRIMARY KEY(subject_kind, subject_id, audience)
);

CREATE TABLE IF NOT EXISTS export_items (   -- per-item versions, raised on visible change
  subject_kind TEXT NOT NULL,
  subject_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  position INTEGER NOT NULL,             -- export_changes position of the last change
  PRIMARY KEY(subject_kind, subject_id)
);

CREATE TABLE IF NOT EXISTS export_changes (  -- feed behind /changes; pruned when every installation has passed it
  position INTEGER PRIMARY KEY AUTOINCREMENT,
  subject_kind TEXT NOT NULL,            -- conversation, workspace, usage, period
  subject_id TEXT NOT NULL,
  dataset TEXT,
  period TEXT,
  at TEXT NOT NULL
);
```

Writers append to `export_changes` and raise `export_items.version` in the
transactions that change what an exporter could see: indexing, attribution,
privacy decisions, usage imports. Readers never write. A `changes` token is the
installation's last visible position, encrypted with its hash key, so neither
its value nor the gap between two tokens says anything about private activity.

## Invariants and tests

From [plugins](plugins.md#recordkeeping), with the test that pins each. All are
Go tests on `testCatalog`/`testServer` unless marked.

| Invariant | Test |
| --- | --- |
| 1. Only exportable items are served (settled, not private, inside permissions), including through precomputed paths | `TestConnectorServesOnlyExportableItems`: marks, rules, unsettled conversations, a forced fast path; compares every dataset's rows and aggregates with and without a private conversation |
| 2. Every served item gets a delivery row whatever the run's status | `TestFailedRunsStillRecordWhatTheyWereServed` |
| 3. A retraction is queued for every delivery no longer exportable | `TestMarkingSentWorkPrivateQueuesRetractions` |
| 4. A run sends exportable items newer than acknowledged | `TestChangesFollowAcknowledgedVersions` |
| 5. Ledger pruning never touches delivery state | `TestLedgerPruneKeepsDeliveries` |
| 6. Opaque IDs are stable per installation and unrelated across installations | `TestOpaqueIDsDependOnlyOnTheInstallationKey` |
| 7. A mark or rule change commits with the retractions it queues | `TestPrivacyChangeAndRetractionsCommitTogether` |
| No side channels: versions, tokens and counts do not move for private activity | `TestPrivateActivityLeavesTheChangeFeedUnchanged` |
| Limits stop a run: runtime, memory, bytes, requests | `TestSupervisorStopsARunAtEachLimit` (slow) |
| A drop cannot escape, name a real host, or carry an undeclared kind or namespace | `TestDropValidationRefusesMistakes` |
| Re-importing usage never double counts; estimates never count as cost | `TestUsageImportIsIdempotentAndKeepsEstimatesApart` |
| Adoption merges and never deletes | `TestAdoptionReplacesAndKeeps` |
| Uninstalling keeps imported data | `TestUninstallKeepsImports` |
| Contracts and their examples agree (Python) | `contracts/plugins/test_contracts.py` |

## Order of work

Importers first, since the labeling usage import is wanted now and needs no
privacy machinery; exporters after privacy.

1. **Contracts and docs** (this change).
2. **Plugin runtime and transcript importers:** manifests, installations,
   consent, Mac allowance, triggers, supervisor, `plugin.run/import`, drop
   validation and adoption, **Does this look right?**, `pharos plugin
   install|list|test|run|uninstall`. `ssh_pull.py` becomes a catalog plugin.
3. **Usage imports:** `imported_usage`, the usage datasets and views reading
   it, the automation channel, and a labeling ledger importer.
4. **Privacy:** settings, rules, marks, states, settled gating, the Library
   badge, filter and rule editor with previews, inheritance through
   subagents, mirrors and workspaces.
5. **Export datasets and connector reads:** `usage/1`, `library/1`,
   `conversations/1`, grant checks, forced filters (fast paths and fact cubes
   included), redaction, accounting, `export_items` and `export_changes`.
6. **Exporters:** delivery ledger, retractions and restatements, scripted
   exporters, egress views.
7. **Declarative exporter and receiver protocol:** registration, receiver
   client, `declarative-export/1`, the Pythia plugin.
8. **Pharos to Pharos:** `/export` bundles with filtered SQLite records, and a
   bundle importer.

## Hazards

- **Precomputed answers bypass row filters.** Host fast paths inside
  `/metrics` and Machine Time's fact cubes answer counts and sums without
  scanning rows; the connector API must either apply the privacy filter to them
  or not use them. Invariant 1's test forces one.
- **Long reads pin the WAL.** Connector pages must be short read transactions,
  and a cancelled request must not leave a statement open (see the modernc
  cancel fix).
- **Writes on read paths.** All accounting stays in memory until the run ends;
  `TestRequestReadsDoNotWaitForTheCatalogWriter` should cover the connector
  routes.
- **Eject.** A running plugin is a hold; eject stops its process group and
  discards staging, and a declarative run that has not committed acknowledges
  nothing.
- **Clock and time zones.** Periods are in the library's time zone; a change of
  time zone restates every period an installation was sent.
