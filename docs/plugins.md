# Importer and exporter plugins

Design, 2026-11-02, revised after the first design review. Nothing in the
service is built yet. The public contracts
(JSON Schemas, OpenAPI and checked examples) are in
[`contracts/plugins/`](../contracts/plugins/), the service's side is planned in
[plugins implementation](plugins-implementation.md), and a receiver's side in
[plugin receiver](plugin-receiver.md). A Python helper and an SSH reference
importer that follow the run contract are in [`importers/`](../importers/).
Where this document and a schema disagree, the schema is the contract.

## Goal

Let users bring their own code to move data into and out of a library:

- **Importers** bring transcripts from machines Pharos does not run on (Linux
  and Windows boxes over SSH, buckets that laptops push to, one-off exports).
- **Exporters** send usage stats, conversation metadata or whole conversations
  to a destination: a company's AI-usage service such as Pythia's, a manager's
  dashboard, a public productivity page, a bucket another Pharos imports from.

Both are plugins. A plugin asks for a fixed set of permissions; the person
installing it says yes or no. It runs on triggers, under limits Pharos
enforces, and every run is recorded. Pharos starts every transfer in both
directions; nothing listens for incoming connections.

The person whose library it is stays in control: they install, pause and remove
plugins, mark work private, and see what each exporter sent. An
organization can point people at a plugin but cannot install, require or lock
one. A managed company edition that preconfigures and locks plugins is a
possible later product, not part of this design.

## Trust model

Plugin code is assumed to be **non-hostile but fallible**: a misbehaving child,
not an attacker. Pharos guards against a plugin that degrades the Mac (runs too
long, uses too much memory, disk or I/O, hammers the API, runs too often) or
makes mistakes (malformed output, asking for data it was not granted, sending
twice). It does not sandbox: a script runs as the user and could read files
directly. Permissions govern everything that passes through Pharos, which is all
a well-behaved plugin needs.

- Limits are enforced: Pharos stops a run that exceeds one.
- Errors are specific. A request for a field outside the grant is refused with
  a message naming the field, not silently trimmed, so the author (often an
  LLM) can fix the script.
- Mistakes are bounded: importers cannot delete history; exporters see only
  their grant, redacted by the server; deliveries carry `run_id` so receivers
  can drop retries; every run's reads are recorded.

## Plugins

A plugin is a manifest ([`plugin-manifest.schema.json`](../contracts/plugins/plugin-manifest.schema.json))
plus, for scripted plugins, a script bundle:

<!-- contract: plugin-manifest.schema.json -->
```json
{
  "plugin": "pharos-plugin/1",
  "id": "software.pythia.usage",
  "name": "Pythia AI usage",
  "publisher": "Pythia Software",
  "version": "1.4.0",
  "direction": "export",
  "description": "Shares daily AI usage and work-item summaries with your company's Pythia workspace. No message text.",
  "homepage": "https://pythia.software/pharos",
  "permissions": {
    "read": {
      "datasets": {
        "usage/1": {
          "fields": [
            "at",
            "provider",
            "model",
            "repository_name",
            "total_tokens",
            "cost_usd"
          ],
          "mode": "aggregate"
        },
        "library/1": {
          "fields": [
            "id",
            "activity_at",
            "repository_name",
            "cost_usd",
            "outcome",
            "conversation_count"
          ],
          "mode": "rows"
        }
      },
      "text": false
    },
    "redaction": {
      "preset": "metadata",
      "hash": [
        "repository_name"
      ]
    },
    "destinations": [
      "https://ingest.pythia.software"
    ]
  },
  "triggers": [
    {
      "after_index": {
        "min_interval": "1h"
      }
    },
    {
      "schedule": {
        "every": "24h"
      }
    }
  ],
  "guards": {
    "max_runs_per_day": 30
  },
  "limits": {
    "max_runtime": "10m",
    "max_requests": 500,
    "max_served_bytes": "50MiB",
    "max_bytes": "1MiB"
  },
  "registration": {
    "url": "https://app.pythia.software/pharos/authorize",
    "token_url": "https://app.pythia.software/pharos/token"
  },
  "supports": [
    "retract"
  ],
  "identity": {
    "on_reinstall": "keep"
  },
  "run": {
    "builtin": "declarative-export/1",
    "receiver": {
      "url": "https://ingest.pythia.software/pharos"
    },
    "send": [
      {
        "name": "daily-usage",
        "dataset": "usage/1",
        "incremental": "changes",
        "query": {
          "query": "export-query/1",
          "group": {
            "period": {
              "field": "at",
              "interval": "day"
            },
            "keys": [
              "provider",
              "model",
              "repository_name"
            ],
            "summaries": [
              {
                "id": "total_tokens",
                "op": "sum",
                "field": "total_tokens"
              },
              {
                "id": "cost_usd",
                "op": "sum",
                "field": "cost_usd"
              }
            ]
          }
        }
      },
      {
        "name": "work-items",
        "dataset": "library/1",
        "incremental": "changes",
        "query": {
          "query": "export-query/1",
          "select": [
            "id",
            "activity_at",
            "repository_name",
            "cost_usd",
            "outcome",
            "conversation_count"
          ],
          "order_by": [
            {
              "field": "activity_at",
              "dir": "desc"
            }
          ]
        }
      }
    ]
  },
  "update_url": "https://plugins.pythia.software/usage/manifest.json"
}
```

- `permissions` is static. Installing grants all of it; declining installs
  nothing. There is no partial grant, so what the publisher built against is
  exactly what runs. An exporter's permissions say what it may `read` (export
  datasets and their fields, text, bundles) and how it is redacted; an
  importer's say what it may `produce` (drop kinds and usage namespaces) and
  where it `fetches_from`. The permissions hash is taken over the canonical
  JSON (RFC 8785) of the permissions with every default filled in, so writing
  a default out never asks for consent again.
- `destinations` declares where data goes, shown at consent. Pharos cannot
  enforce it for a script (see the trust model), but a declarative exporter's
  deliveries go only there. The consent screen names the manifest's domain as
  the publisher; bundles are not signed for now.
- `registration` is optional: a receiver that needs to know who is sending
  runs its own sign-up there (below).
- An importer's manifest declares instead the source kinds it produces, where
  it fetches from (for example "alice@lab-box over SSH", or a bucket), and a
  JSON Schema for its `params`, which Pharos renders as the install form. Importers install through the same consent: nothing enters a
  library from a place the person did not agree to, and every imported
  conversation names the plugin and machine it came from.
- `retract` in `supports` declares that the plugin can ask its receiver to
  delete what it sent (see [Retraction](#retraction)). The consent screen says
  whether it can.

Plugins come in two layers that share one socket:

- **Scripted:** any program that follows the [run contract](#runs). It reads
  through the [connector API](#connector-api) (exporters) or drops files
  (importers). A user can point Pharos at their own script without a manifest
  from anyone else; **Write your own** drafts one around it.
- **Declarative** (recommended): a manifest whose `run` is
  `{"builtin": "declarative-export/1"}` plus [export queries](#export-queries)
  and a receiver. Every field its queries name must be in its read
  permissions; Pharos checks that when it reads the manifest. Pharos's own binary is the program: it receives the same request
  and token and goes through the same API, limits and accounting as any script.
  It needs no interpreter on the Mac (macOS ships no Python without the Command
  Line Tools), the consent screen describes exactly what runs, and deliveries
  can only go to `destinations`.

The catalog and the consent screen nudge towards declarative plugins; scripted
ones are for anything they cannot express. Both use the public contracts below,
and first-party plugins, Pythia's included, use them like anyone else's.

## Installing

1. **Open an install link.** An employer, a dashboard or a plugin catalog links
   to `https://…/manifest.json` through a `pharos://install?manifest=…` link, or
   the user picks a manifest or folder in Settings. A user's own script becomes
   a local plugin through **Write your own**, which drafts the manifest.
2. **Consent.** Pharos fetches the manifest and bundle and shows one screen:
   who publishes it and where it came from (the manifest's domain or the local
   path); in plain language, what it reads and where it sends it, and how
   often; what stays out (private work items, text when `text` is false,
   pseudonymised fields, described as "this receiver can check guesses"); the
   finest grouping its aggregates allow ("by day, model and repository"); the
   library's export delay; and, for an exporter, a summary of what it would
   send today, computed from its permissions (datasets, rows, date range).
   **Yes** or **No**.
3. **Register,** when the manifest asks: Pharos opens `registration.url` in the
   browser with a one-time code and a loopback callback. The receiver signs the
   person in on its side and returns a credential. Declining here uninstalls
   the plugin.
4. **Allow on this Mac.** Scripted plugins run code here, so each Mac a library
   is opened on must allow them once (below). On the installing Mac this is
   part of the consent.

Uninstalling a plugin that supports `retract` offers to ask the receiver to
delete everything it was sent, as a last run, before the plugin is removed.

Uninstalling never deletes anything from the library. What an importer brought
in (captured transcripts and usage records) stays as history, still attributed
to the plugin and runs that imported it, as a captured file is never deleted.
Reinstalling the same plugin continues deduplicating against those records;
a usage namespace belongs to the plugin that declared it.

**Changes need acknowledgement.** The consent covers the exact manifest and
bundle (by hash). An update fetched from `update_url` with the same permissions
asks to update; one with different permissions shows the difference and asks
for consent again, and the plugin stays paused on its previous version until
then. A local plugin whose source file changed is paused with a diff in the same
way.

## Where things live

Plugins belong to the **library** and move with it, as on an SSD:

- The catalog holds each plugin's manifest, approved bundle hash, consent,
  credentials from registration, cursor, trigger state, run history and egress
  ledger. Credentials follow the cloud backups' precedent of restricted
  credentials stored in the library's database.
- Approved bundles are copied into `<library>/plugins/<id>/<hash>/` and run from
  there.

A plugin runs on whichever Mac has the library open. What is Mac-specific is
**permission to run code there**: a library opened on a Mac for the first time
lists its scripted plugins under **Plugins to allow on this Mac**, each with
its interpreter (found or chosen) and any Mac-specific settings an importer
needs, such as an SSH agent socket. Until allowed, a plugin is paused on that
Mac and its triggers wait; a schedule catches up once when it runs again.
Declarative plugins run no code and need no per-Mac allowance.

## Triggers

A trigger decides when a run starts. What a run sends is its cursor's job.

| Trigger | Fires |
| --- | --- |
| `schedule` | every interval, or at times of day, with one catch-up after downtime |
| `after_index` | when an index of this library finishes |
| `threshold` | after an index, once N new or changed conversations (or N tokens) have arrived since the last acknowledged run |
| `manual` | from Settings or `pharos plugin run` |

Guards apply to every trigger: minimum interval, maximum runs a day, quiet
hours, on power only, and deferral while the Mac is busy (the same idle signals
heavy work uses). An importer followed by an index, then an exporter, falls
out of `after_index`. Other triggers can come later.

## Limits

| Area | Limits |
| --- | --- |
| Process | wall time, CPU time, resident memory (sampled; over the limit stops the run), bytes written to staging, utility I/O tier and lowered priority, process group stopped as a whole |
| Connector API | requests, rows and bytes served per run; one request at a time; per-request time and page-size caps; `429` with `Retry-After` near a limit, token revoked past it |
| Schedule | minimum interval, runs a day, two plugins running at once |
| Failure | retries after 1, 6 and 24 hours; paused after five consecutive failures, with a notice |

A run stopped by a limit records which one. The manifest sets limits within
app-wide ceilings; the user can lower them.

## Runs

Pharos runs a scripted plugin's `interpreter` with its `entry` (or its
`executable`), then any `args`, then the request file's path, as an argument
vector (no shell), in a private working directory, with stdin closed
and a minimal environment (`HOME`, `USER`, `LANG`, `TMPDIR`, a plain `PATH`,
`SSH_AUTH_SOCK` when launchd provides one, the plugin's Mac settings, and the
variables below). The last 1 MiB of stdout and stderr is the run's log.

Request, contract `pharos-run/1`
([`run-request.schema.json`](../contracts/plugins/run-request.schema.json)):

<!-- contract: run-request.schema.json -->
```json
{
  "contract": "pharos-run/1",
  "direction": "export",
  "plugin": {
    "id": "dev.example.public-dashboard",
    "version": "2.0.1"
  },
  "run_id": "run_20261102T060000Z_4f2a",
  "started_at": "2026-11-02T06:00:00Z",
  "deadline": "2026-11-02T06:02:00Z",
  "dry_run": false,
  "trigger": {
    "kind": "schedule"
  },
  "limits": {
    "max_bytes": 1048576,
    "max_requests": 50,
    "max_rows": 100000,
    "max_served_bytes": 10485760
  },
  "api": {
    "url": "http://127.0.0.1:53817/connector/v1",
    "token": "pht_5JzQm2v8Lr4Xc9Wn7Ty3Kd6Hs1Pa0Bf8Ge2Uj4Ni"
  },
  "output_dir": "/Volumes/euclid/Pharos/staging/plugins/dev.example.public-dashboard/run_20261102T060000Z_4f2a/out",
  "cursor": {
    "changes": "chg_0001f3a9"
  },
  "params": {},
  "library_instance": "inst_7Hq2Lx9Wv4",
  "as_of": "2026-10-30T06:00:00Z"
}
```

`PHAROS_REQUEST`, `PHAROS_API` and `PHAROS_TOKEN` carry the same values.
Registration credentials are read through the API (`GET /connector/v1/credential`),
not put in the environment. `cursor` is the plugin's own value from its last
acknowledged run. `dry_run` asks for everything except delivery; `pharos plugin
test` and previews use it.

Result, `output_dir/result.json`
([`run-result.schema.json`](../contracts/plugins/run-result.schema.json)):

<!-- contract: run-result.schema.json -->
```json
{
  "contract": "pharos-run/1",
  "status": "complete",
  "message": "52 weeks of totals published",
  "cursor": {
    "changes": "chg_0001f3b0"
  },
  "delivered": {
    "periods": 104,
    "bytes": 18211
  },
  "receipt": {
    "commit": "5d1e2c9"
  },
  "transferred_bytes": 20480,
  "warnings": []
}
```

`status` is `complete`, `partial` (stopped at a limit, consistent with `cursor`,
run again soon) or `failed` (nothing acknowledged, cursor stays). `complete` and
`partial` acknowledge: the cursor advances with them. Importers add `machines`
and `drops` (below).

## Connector API

The service serves `/connector/v1` on loopback. Every request carries the run's
bearer token, valid only until the run ends, which identifies the plugin, run
and approved permissions. Nothing else (no UI cookie, no app token) works there.

```text
GET  /connector/v1/grant                          what this run may do, its cutoff and usage
GET  /connector/v1/credential                     a short-lived receiver access token, if registered
POST /connector/v1/query/{dataset}                an export query: rows or groups
GET  /connector/v1/items/{dataset}?ids=           rows by opaque ID, for example those /changes lists
GET  /connector/v1/changes?since=&limit=          what to send again, restate or retract
GET  /connector/v1/conversations/{id}/messages    a conversation's messages, when the grant allows text
GET  /connector/v1/export?since=&cursor=          pharos-drop/1 bundles, for another Pharos
POST /connector/v1/resend                         record that the receiver asked for everything
```

[`connector-api.openapi.json`](../contracts/plugins/connector-api.openapi.json)
specifies each endpoint and its errors. Every endpoint serves exactly one
dataset, and everything an exporter receives is keyed by dataset and opaque ID.

### Export queries

Plugins query in `export-query/1`
([`export-query.schema.json`](../contracts/plugins/export-query.schema.json)):
filters, then either a selection of fields (rows) or a period, group keys and
summaries (aggregates), with ordering and a limit. Its semantics are Query
Table's (the same operators, calendar periods, summary operations and
OR-of-AND filters), so Pharos compiles it to Query Table directly. Its shape
belongs to the plugin contract, the way an API keeps its own schema rather
than exposing its database's: a consented manifest's queries keep their
meaning whatever Query Table release Pharos uses, and checking a query against
a grant is mechanical. Every field it names must be granted; its group keys
and period must be ones the dataset allows (listed in `/grant`); aggregate mode
allows no selection.

### Export datasets

Queries run over **export datasets**: versioned public subsets, each mapped
onto fields of the UI's [schemas](../schemas) and defined field by field in
[plugins implementation](plugins-implementation.md#export-datasets).

| Dataset | Rows |
| --- | --- |
| `usage/1` | conversation usage: hour, provider, model, repository, tokens by category, cost |
| `automation-usage/1` | usage imported without conversations, such as labeling runs |
| `library/1` | work items |
| `conversations/1` | conversations, each in a work item |

The UI's schemas change with the UI; a published dataset version never
changes. A new field, or a new value that changes what a field covers (such as
a new kind of channel), means a new dataset version, which a plugin adopts with
a new manifest version and consent. That is why imported automation usage is a
dataset of its own: adding it to `usage/1` would silently change what every
installed exporter sums.

### Enforcement

- **Checks run in a fixed order:** the token, then the grant (dataset, fields,
  mode, group keys, text, export), then whether the item exists. A refusal
  names what was refused, and a `404` never distinguishes a private item from
  an absent one.
- **Forced filters** are added to every query: private work items and usage
  removed (below), the run's cutoff (`as_of`, see [export delay](#export-delay)),
  and any repository, machine or time restriction the manifest declares.
  Permissions have no time window unless the manifest sets one, and the consent
  screen says "including all past history" when it does not.
- **Redaction** is applied to responses, so what the script sees is what the
  consent screen said. Filters, group keys and ordering on a redacted field see
  the redacted value, never the original.
- **Counting** happens per run in memory and is written to the run record at
  the end: requests, rows, bytes, and the items served.
- **Reads never write and never hold the catalog.** Pages are keyset, short
  read transactions; a slow query hits its time cap rather than stalling
  indexing.
- **Change tokens** are opaque and do not move when only private things
  changed. The feed keeps 30 days; an older token gets `cursor_expired`, and the
  plugin starts again without one, which sends everything.

Importers get a token too, with no read scopes by default, so the contract is
the same in both directions.

### Egress ledger

For every exporter run Pharos records what it served: endpoints, rows, bytes,
item IDs, and the permissions and redaction in force. It never keeps copies of
the data. Ledger entries are kept for 180 days; what each receiver may still
hold is kept separately for as long as it matters (see
[Recordkeeping](#recordkeeping)). Settings shows, per plugin, what left and
when, and per work item, which plugins saw it.

Counting during a run stays in memory, because requests never write. The run's
record, its ledger entries and its delivery state commit in one transaction
when the run ends.

## Redaction

A manifest's redaction is a preset plus field overrides:

| Preset | Contents |
| --- | --- |
| `stats` | aggregates only; titles, paths, URLs, text and tool output absent |
| `metadata` | rows without message text or tool output; titles optional |
| `text-scrubbed` | text with secrets (keys, tokens, private keys, passwords in URLs) and optional patterns removed |
| `full` | everything the permitted fields allow |

Overrides per field: drop, hash, coarsen (time to the day or week), truncate,
or replace paths with repository-relative ones. Presets must agree with the
permissions: `stats` allows only aggregates and no text, `metadata` no text,
and `text-scrubbed` needs text.

Hashes and opaque IDs are keyed with a salt the receiver issues at
registration, one per organization, so one receiver sees the same value for the
same conversation or repository from all its members' libraries (and can count
a conversation once, or notice two accounts sharing conversations), while two
receivers cannot match each other's data. A plugin without registration uses a
key of its own installation. Hashing is **pseudonymisation**, not hiding: the
receiver knows its salt, so it can check a guess such as `acme/secret`. The
consent screen says "pseudonymised: this receiver can check guesses", never
"hidden".

## Privacy

### Private work items

Privacy is set on **work items**. A work item and everything in it (its
conversations, subagents and delegated runs, and the aliases and mirrors of
each, such as a Conductor wrapper and its native session) are private or
shareable together. One unit keeps the meaning simple: making something
private hides that whole piece of work from every exporter. Usage imported
without conversations is marked the same way, by rules over its fields or by
namespace and job.

A private work item does not exist as far as any exporter can tell. It is
absent from rows, messages, changes and exports, and from every aggregate:
totals, counts, averages and distributions are computed as if it had never
been indexed, so everything an exporter receives agrees with the rows it could
fetch, and nothing reveals that private work exists. (Ask Pharos already has
private chats, which history snapshots exclude; the two should share one
meaning and one mark.)

- **Marking:** a row action and a bulk action on any Library selection, the
  work item's page, the [leaving soon](#export-delay) view, and
  [privacy rules](#privacy-rules).
- **No side channels:** IDs exposed to exporters are opaque, so gaps reveal
  nothing; versions are the time Pharos served an item, not a catalog counter;
  a `changes` token does not move for private activity; usage attributed to a
  private work item is left out, not reassigned; a `404` never distinguishes
  private from absent.
- **Precomputed answers:** host fast paths and fact cubes that answer counts and
  sums without row scans must exclude private work items too, or the connector
  API must not use them.
- **Exports:** Claude and Codex transcripts are one file per session, so a
  bundle simply leaves private ones out. A SQLite source holds many
  conversations in one file, so its bundles carry filtered
  [records/v1](sqlite-records.md) rows instead of the raw snapshot.
- **Moving between work items:** a conversation attributed to another work
  item later takes that work item's state.
- **Marking something already sent** removes it from later deliveries and
  queues a [retraction](#retraction) for every receiver that may hold it.

### Privacy rules

People decide what is shareable in broad strokes first and exceptions second,
so privacy is a default, an ordered list of rules, and manual marks:

1. **Manual marks** on a work item win over everything.
2. **Rules**, in order; the first that matches decides. A rule is a Library
   filter over work items (the same Query Table filter language, and any saved
   Library view can become one) plus an outcome, **private** or **shareable**,
   for example "repository is acme/secret → private", "machine is Personal
   laptop → private", "repository owner is acme → shareable". Rules over
   automation usage filter its fields ("automation job is titles-v3 →
   private").
3. **The default** for everything else: **shareable** or **private**. Someone
   who wants to share only what they choose sets the default to private and
   writes shareable rules.

Privacy is one global state: private means private from every exporter. The
records carry an audience (always "every exporter" for now) on rules, marks and
effective states, so per-plugin privacy can be added later without migrating
them.

Each work item records its effective state and why ("rule 3: repository is
acme/secret", "marked by you on Oct 28", "default"), shown on its badge and
filterable in the Library. Editing rules previews the effect before saving:
how many work items change state, which of those any receiver may hold, and
therefore what would be retracted. Rule changes are kept in a history.

Work items change after they are first seen: repositories are attributed,
titles assigned and conversations linked later in indexing. A rule that
depends on those must not miss its chance, so **a work item is not exportable
until its privacy is decided on settled metadata**: after the index pass that
attributes it completes.

### Export delay

Nothing is exported until it is older than the library's **export delay**:
three days by default, adjustable in Settings (zero included). Each run sees
the library as it stood at its cutoff, `as_of`, the run's start minus the
delay:

- usage and automation usage up to `as_of`;
- work items and conversations once their first activity is before `as_of`,
  with totals over their activity up to `as_of`, and messages created up to
  `as_of`;
- current metadata (titles, repositories, outcomes) of those items.

Privacy is always current. Someone still working on something private does not
need to scramble: marking the work item private at any time before its
activity reaches the cutoff means it is never sent. A work item that runs for
days is exported a few days behind and kept current as its activity ages past
the cutoff. Retractions are never delayed.

**Leaving soon** shows what will cross the cutoff in the next days, per
exporter and grouped by day ("Thursday: 4 work items and $31 of usage, to
Pythia AI usage"), with **Make private** beside each work item. It is the
Library filtered to work items whose activity falls within the next days'
cutoffs. Shortening the delay previews what would then be sent at once.

Every batch carries `as_of`, so a receiver knows how current its data is.

### Retraction

When something a receiver may hold leaves export scope (its work item becomes
private, the plugin's permissions narrow on update, or the plugin is
uninstalled with **ask to delete**), Pharos queues a retraction for that
receiver. The `changes` feed returns retractions beside changes:

<!-- contract: connector.schema.json#/$defs/changes -->
```json
{
  "changes": [
    {
      "dataset": "library/1",
      "id": "w_9tq3LmA2"
    }
  ],
  "restate": [
    {
      "dataset": "usage/1",
      "from": "2026-10-21T06:00:00Z",
      "to": "2026-10-22T06:00:00Z"
    }
  ],
  "retract": [
    {
      "dataset": "library/1",
      "id": "w_2bH7xPq1",
      "version": 1793718000000003
    }
  ],
  "next": "chg_0001f3b0",
  "more": false
}
```

- Each retraction names the dataset and the opaque ID the receiver was given,
  so it names only what the receiver already has.
- Aggregates cannot be retracted item by item. A time range whose totals
  included the work item is **restated**: every period of every series that
  overlaps it is sent again, complete and computed without it. A period
  replaces all the groups the receiver holds for it, so a group that existed
  only because of the private work disappears rather than lingering at zero.
  A restatement shows only that totals the receiver already had changed, which
  also happens for ordinary reasons (re-indexing, price updates).
- A retraction stays queued until a run that received it is acknowledged, and
  is retried like any delivery. A plugin without `retract` support still
  restates aggregates and stops sending the work item; the consent screen and
  the plugin's page say that it cannot ask for deletion.

## Registration and sender identity

A receiver that collects data from many people needs to know who sent each
delivery. The receiver owns that: its `registration.url` signs the person in
(or accepts an invitation) and returns a credential that the plugin presents on
delivery. Each installation also has a random sender ID, stable for that library
and plugin, available to the plugin through `/grant`. Pharos sends no names; the
receiver knows who registered.

Registration is OAuth for native apps (RFC 8252) with PKCE: Pharos opens
`registration.url` with a loopback redirect, and exchanges the code at
`registration.token_url` for a refresh token and the organization's salt, both
stored with the plugin in the library. Runs get only short-lived access tokens,
from `GET /connector/v1/credential` (scripted) or directly (declarative).

The manifest's `identity.on_reinstall` decides what reinstalling the plugin in
the same library reuses. `keep` reuses the sender ID and, for a plugin without
registration, the installation key behind its opaque IDs and hashes; Pharos
keeps both after an uninstall for that purpose. `new` starts with fresh ones.
With registration, opaque IDs and hashes always follow the organization's salt,
so the receiver can match items either way, and `new` changes only the sender
ID.

## Restored and copied libraries

A library's writable instance changes when it is restored from a backup or
copied. Either way its delivery state may be behind what receivers hold: a
restored library has forgotten what it served after the backup, and a copy
shares an installation with the library it came from.

So a plugin in a restored or copied library starts paused, saying so: **This
library was restored or copied. Resume sending from it?** Resuming takes over.
The hello names the new instance and the one it replaces; the receiver answers
`everything`; Pharos sends everything the permissions allow and commits with
`replace_all`, which retracts whatever the receiver holds from this
installation that the run did not send. The receiver then refuses the old
instance (`409 superseded`), so a copy still running on another Mac stops
instead of alternating with this one. Versions are the time an item was served,
so everything sent after a restore is newer than what came before it.

## Importers

An importer's script drops native transcript files into `output_dir` and
declares them in `result.json`. Pharos validates the drop and adopts it as
captures of new hosts. The script never learns what the library holds; it gets
back only its own cursor. A partial run's result:

<!-- contract: run-result.schema.json -->
```json
{
  "contract": "pharos-run/1",
  "status": "partial",
  "message": "312 file(s), 1.9 GB from alice@lab-box; stopped because the byte budget is spent",
  "cursor": {
    "version": 1,
    "destination": "alice@lab-box",
    "sources": {
      "claude": {
        "since": 1793120411,
        "after": "-home-alice-src-api/7a1c.jsonl",
        "databases": {}
      },
      "codex": {
        "since": 1793116800,
        "databases": {
          "state_5.sqlite": 1793120002
        }
      }
    }
  },
  "transferred_bytes": 2040109056,
  "machines": [
    {
      "key": "alice@lab-box",
      "label": "lab-box (alice)",
      "os": "linux",
      "machine_id": "4b2d9c0e8f7a41d3b6c5e2f1a0987654"
    }
  ],
  "drops": [
    {
      "dir": "claude",
      "kind": "claude",
      "machine": "alice@lab-box",
      "source": "claude",
      "root": "/home/alice/.claude/projects"
    },
    {
      "dir": "codex",
      "kind": "codex",
      "machine": "alice@lab-box",
      "source": "codex",
      "root": "/home/alice/.codex"
    }
  ],
  "warnings": [
    "codex: sqlite3 is not installed on lab-box; state_5.sqlite skipped"
  ]
}
```

- A drop mirrors the source's layout under `root`: `claude` takes `**/*.jsonl`
  and `*/sessions-index.json` under `~/.claude/projects`; `codex` takes
  `sessions/` and `archived_sessions/` JSONL, `session_index.jsonl`, and
  consistent `state*.sqlite` snapshots under `~/.codex`; `canonical` takes
  [canonical JSON](canonical-export.md); `chatgpt-export` takes
  `conversations.json`. Other kinds are Mac-only and refused.
- Validation: regular files only, no links or `..`, every file in a declared
  drop and wanted by its kind, SQLite files intact and without WAL, and the
  kind's adapter must parse the drop.
- Pharos derives each machine's host ID from the plugin installation and the
  machine `key`, so a script cannot write into an existing Mac's captures.
- Adoption merges: a dropped file replaces its captured copy (a rewrite keeps
  the previous one in `history/`); a missing file is left alone. Files become
  durable first; then the run record and cursor commit together.

**Does this look right?** An importer's first run, and the first run after any
change, is held in staging. Pharos shows what came in: each machine and source,
conversation counts (or, for usage, records, tokens and cost by model), date
range, a few titles, and warnings. **Keep** adopts it
and starts the triggers; **Discard** drops it, leaves the cursor, and offers the
run's log.

### Usage imports

Some usage has no conversation worth keeping. `pharos labeling run` sends
hundreds of thousands of headless `claude -p` and `agy` requests that
deliberately leave no transcripts; their text is worthless, but their tokens
and cost are real spend. The labeling runner's own ledger
(`dataset.sqlite3`'s `usage`, `attempts` and `jobs` tables) is the authoritative
record. An importer for it brings **usage only**: no conversations, messages or
text enter the library.

Such a drop has kind `usage-records/1`: JSON Lines, one record
([`usage-record.schema.json`](../contracts/plugins/usage-record.schema.json))
per request and model, shown here pretty-printed:

<!-- contract: usage-record.schema.json -->
```json
{
  "id": "att_01JC7Q5H2M",
  "at": "2026-10-28T14:03:11Z",
  "ended_at": "2026-10-28T14:03:19Z",
  "provider": "anthropic",
  "route": "claude",
  "model": "claude-haiku-4-5",
  "effort": "low",
  "harness": "claude-code",
  "harness_version": "2.1.4",
  "tokens": {
    "uncached_input": 1840,
    "cache_read": 22000,
    "cache_write_5m": 0,
    "output": 310,
    "reasoning": 0
  },
  "cost": {
    "usd": 0.0041,
    "status": "priced"
  },
  "estimate": {
    "usd": 0.0045,
    "basis": "list price"
  },
  "duration_ms": 8120,
  "outcome": "ok",
  "automation": {
    "kind": "labeling",
    "job": "titles-v3",
    "dataset": "full-20261027",
    "role": "primary"
  }
}
```

- **Deduplicated by `(plugin, namespace, id)`.** The manifest declares the
  namespace (`pharos-labeling`), which belongs to that plugin: the same attempt
  arriving from a dataset, its study copy and a restored archive is one record
  however many runs bring it, and no other plugin can overwrite it. A record sent again replaces the stored one, which makes re-running
  over a growing or resumed ledger safe.
- **Cost and estimates stay apart.** `cost.usd` counts as spend only when
  `status` is `priced` or `assumed`; other statuses show as unpriced usage.
  `estimate` is exposure, never cost. Failed attempts with usage still count.
  Pharos also prices the tokens itself, for "cost at today's prices".
- **Marked as automation.** In Pharos's own usage views the records have their
  own channel, **Automated: labeling**, with their job and dataset as fields,
  so they never read as the person's own work or as a conversation. They join
  no work item and appear in no Library list. Exporters see them only through
  `automation-usage/1`, which a manifest must ask for.
- **The importer reads without disturbing.** Ledgers are written during runs
  (WAL), so the importer reads briefly or from a snapshot and never writes a
  dataset. Its parameters name dataset folders; nothing about them is committed
  anywhere but the library.

Pharos stores these as their own usage table alongside session usage, since
session usage belongs to a conversation; the usage views read both.
Other automation that leaves no transcripts (CI runs, batch jobs) can use the
same kind.

### Cursors

The usual cursor is a per-source timestamp from the remote clock at the start
of the run, minus an overlap. A partial run resumes after the last (mtime, path)
it sent, since more files can share an mtime than fit in a run. A SQLite
database needs its own stamp, because reading it can touch its WAL. The helper
and `ssh_pull.py` do both.

## Exporters

Common cadences are a trigger plus a cursor:

| Cadence | Trigger | Cursor |
| --- | --- | --- |
| everything once a day | `schedule` daily | none; send everything again |
| new things after each index | `after_index` | `changes` token |
| wait for 10 new conversations | `threshold` 10 | `changes` token |
| stats changed since the last acknowledgement | `after_index` or `schedule` | `changes` token; restated periods are sent whole |

Payloads use `pharos-report/1`
([`report.schema.json`](../contracts/plugins/report.schema.json)): an envelope
(sender ID, library instance, plugin, permissions hash, `run_id`, batch number,
`as_of`, generated at) around row upserts, whole aggregate periods and
retractions, each naming its export dataset. A period is keyed by its series
(the send's name in the manifest), the period's start and its group, and an
entry holds every group of one series and period. A receiver can drop a retried
batch by `run_id` and batch number.

### Asking the receiver what it needs

A receiver never contacts Pharos, except through the registration flow at
setup. It only answers Pharos's requests. So every exporter run starts by
asking the receiver what it needs, and the answer can be **everything**: after
the receiver lost data, restored a backup, changed its schema, or is
backfilling a new member, optionally from a date it names. Pharos then sends
everything the permissions allow, over as many partial runs as the limits
require. The plugin's run history shows such a run as a full re-send, with the
receiver's reason. It needs no new consent, since it sends nothing the
permissions did not already allow.

The declarative runner speaks the [receiver protocol](#receiver-protocol)
itself. A scripted plugin talks to its receiver however it likes, and reports
a full re-send through `POST /connector/v1/resend` with the reason, which resets
what Pharos counts as acknowledged for that plugin and is recorded the same way.

## Receiver protocol

`pharos-receiver/1` is the public contract the declarative runner speaks, over
HTTPS, with an access token from the registration credential
([`receiver-api.openapi.json`](../contracts/plugins/receiver-api.openapi.json)).
Pythia's service implements it; anyone can, and
[plugin receiver](plugin-receiver.md) sketches the receiver's side. Each run:

1. `POST /v1/hello` with the sender ID, library instance, plugin version,
   permissions hash, dataset versions, `as_of`, and the receipt of the last run
   the receiver committed. The receiver answers with what it needs: `changes`;
   `everything` with a reason and optionally a `from` date; or `resend` with
   opaque IDs and periods. It also gives the dataset versions it accepts and its
   batch size limit. A receipt the receiver does not recognize means it lost
   what came after, and it answers `everything`.
2. `POST /v1/batches`, one or more, each with `Idempotency-Key: <run_id>/<n>`
   and the body's SHA-256, carrying upserts, whole periods and retractions as
   `pharos-report/1`. The same key with a different body is refused, so a key
   always means one body.
3. `POST /v1/commit` with the run ID and batch count (and `replace_all` after a
   [takeover](#restored-and-copied-libraries)). The receiver applies the run's
   batches together and returns a receipt. Only a committed run is
   acknowledged; a run that ends without a commit counts as served, not
   acknowledged.

A run that fails is retried as a new run with a new `run_id`; receivers discard
staged batches that are never committed. `401` or `403` from the receiver (the
person disconnected on the receiver's side, or their access was revoked) pauses
the plugin with a notice, and `409 superseded` pauses a library that another
instance took over from. Uninstalling sends `DELETE /v1/registration`, after the
optional request to delete everything. The protocol has no endpoint through
which a receiver could ask for more than the permissions allow: those change
only with a new plugin version and new consent.

## Recordkeeping

The records have to answer, for any conversation or plugin:

- where a conversation came from: which Mac or importer, machine, run and file
  revisions, and when;
- which receivers may hold it, which version, since when, and what was
  retracted;
- what a plugin sent in each run, under which permissions;
- why a work item is private or shareable;
- what the next run needs to send.

Two principles keep this tractable:

- **"May hold" and "acknowledged" are kept apart.** Pharos knows what it served
  a run, not what the script delivered. So for each receiver and item it keeps
  both: *served*, what the receiver may hold, and *acknowledged*, what a
  successful run confirmed. Retraction uses served (retracting something never
  received is harmless); incremental sending uses acknowledged (sending
  something again is harmless). Each errs on the safe side for its purpose.
- **Events expire; state does not.** The ledger is an event log pruned at 180
  days. Delivery state is one row per receiver and item, kept until it is
  retracted or the plugin is removed, so pruning never makes Pharos forget what a
  receiver may hold.

A sketch of the tables:

| Table | One row per | Holds |
| --- | --- | --- |
| `plugin_installations` | installed plugin, kept after uninstall | manifest, consented version and bundle hash, permissions hash, credential, sender ID, hash key, state, last receiver receipt |
| `plugin_consents` | consent or acknowledgement | version, permissions hash, time, the difference shown |
| `plugin_runs` | run | trigger, Mac, start and end, status, limit reached, counts, cursor before and after, permissions hash |
| `plugin_ledger` | item served or retracted in a run (180 days) | run, dataset, item, version, fields hash |
| `plugin_deliveries` | plugin × dataset × item | opaque ID, served version and time, acknowledged version and time, state (held, retraction queued, retracted) |
| `plugin_period_deliveries` | plugin × series × period | served and acknowledged fingerprints, for restatements |
| `plugin_imports` | file revision adopted from a run | run, machine host ID, source, original path, SHA-256, adopted at |
| `imported_usage` | usage record by plugin, namespace and ID | time, provider, route, model, effort, tokens, cost and status, estimate, outcome, automation labels, plugin and run that last wrote it |
| `privacy_rules`, `privacy_rule_history` | rule, change | order, filter, outcome, audience |
| `privacy_marks` | manual mark | work item or usage, state, audience, time |
| `privacy_states` | work item or usage record × audience | effective state, reason (mark, rule, default), decided at, settled; audience is "every exporter" for now |

An item's version is the time Pharos served that state of it, as a clock that
never goes back for the installation: later deliveries always win, including
after a restore, and the number says nothing about other items. Change tokens
are opaque. Imports extend the
existing evidence model: a conversation's copies already name host, source and
origin, and `plugin_imports` adds the plugin and run for each adopted file
revision, so "from lab-box through the SSH importer, Oct 28" is a join. A
conversation captured both natively and through an importer stays one
conversation with two copies, deduplicated by native ID as today. Privacy rules
apply to imported conversations like any other. Full definitions, with the
tables behind the change feed, are in
[plugins implementation](plugins-implementation.md#catalog-tables).

Invariants, each a test target:

1. An item is served only if it is exportable in the read snapshot: settled,
   not private, and inside the permissions.
2. Every item served gets a delivery row when its run ends, whatever the
   status: a failed script may have sent it anyway.
3. Retraction is queued for every delivery whose item is no longer exportable
   and is not yet retracted.
4. A run sends exportable items whose version is newer than the acknowledged
   one.
5. Pruning the ledger never touches delivery state.
6. Opaque IDs are stable per receiver key and unrelated across receivers.
7. A privacy mark or rule change commits together with the retractions it
   queues.
8. Nothing with activity after a run's `as_of` is served, except retractions.
9. Versions served for an installation only increase, across restores too.
10. A restored or copied library sends nothing until the person resumes it,
    and its first run then replaces everything the receiver holds.

## Pharos to Pharos

Pharos never listens, so two libraries meet through something both can reach:
an exporter on one writes `pharos-drop/1` bundles (from `/export`, private
conversations already left out) to a bucket, shared folder or service; an
importer on the other pulls them, and they go through the same validation and
adoption as any drop.

Each library exports only conversations its own Macs captured unless the
manifest asks to forward imported ones, so two libraries feeding each other do
not loop. Captures already record their origin host.

## Catalog

Plugins listed in the app: Pythia AI usage, HTTPS JSON webhook, folder or
bucket writer, public dashboard (static JSON to a Git repository), Pharos
bundle exporter and importer (bucket or folder), SSH importer
(`importers/ssh_pull.py`). **Write your own** offers the contracts, the helper,
a copyable prompt for a local coding agent, and `pharos plugin test`, which runs
the plugin with `dry_run`, checks its output and API use against its
permissions, and prints what it read, what it would have sent or dropped, and
its cursor (`--twice` runs again from that cursor).

## Open questions

None outstanding. The cloud receiver's side is sketched in
[plugin receiver](plugin-receiver.md).
