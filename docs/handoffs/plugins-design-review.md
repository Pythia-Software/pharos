# Review the plugin design and contracts

## Assignment

Review the design, public contracts and implementation plan for importer and
exporter plugins **before any of it is built**, and report what is wrong,
missing or inconsistent. Do not implement anything, and do not edit the design
documents: the findings go back to the person who owns the design, who decides
what to change.

The work is in five places:

1. [`docs/plugins.md`](../plugins.md): the design. Start here.
2. [`contracts/plugins/`](../../contracts/plugins/): JSON Schemas, two OpenAPI
   3.1 specs and checked examples; its README says what each is and which rules
   only Pharos can check.
3. [`docs/plugins-implementation.md`](../plugins-implementation.md): Go
   interfaces, catalog tables, export datasets, operation kinds, invariants and
   their tests, order of work.
4. [`docs/plugin-receiver.md`](../plugin-receiver.md): what a cloud receiver
   (Pythia's first) must do.
5. [`importers/`](../../importers/): a Python helper and an SSH reference
   importer that already follow the run contract, with an end-to-end test.

Background worth knowing: [background work](../background-work.md) (the
operation contract runs must fit), [configuration: capture](../configuration.md#capture)
and [indexing captures](../configuration.md#indexing-captures) (what importers
feed), [channel and machine](../channel-and-machine.md), and
[`CLAUDE.md`](../../CLAUDE.md). Requests to the service must never write to the
catalog ("requests never write"), and reads must not hold long transactions.

## Decided; do not reopen

The owner made these choices. Report one only if it causes a concrete defect,
and say which.

- Plugin code is non-hostile but fallible ("a misbehaving child"): Pharos
  limits resources and makes mistakes loud and bounded; it does not sandbox.
- The person whose library it is controls everything. No organization can
  require, install or lock a plugin.
- A plugin asks for a fixed set of permissions; the answer is yes or no. Any
  change of permissions needs new consent; any change of code needs
  acknowledgement.
- Plugins belong to the library and travel with it; running a scripted plugin
  on a Mac needs a one-time allowance there. Credentials are stored in the
  library's database.
- Pharos initiates every transfer. Receivers contact Pharos only during
  registration, and may answer a hello with "everything", from any date.
- Private is one global state, set on **work items**: a work item and
  everything in it are private or shareable together, for simplicity of
  meaning. Private work and usage are absent from everything an exporter
  receives, aggregates included; their existence must not be inferable.
- Privacy is manual marks, then ordered rules, then a default (which can be
  private). A work item is exportable once its metadata has settled, and is
  sent again as it changes.
- Exports run behind a configurable **export delay** (three days by default):
  each run sees the library as of its cutoff, so people can mark work private
  before it leaves, helped by a **Leaving soon** view.
- The public query language is our own, versioned `export-query/1`, following
  Query Table's semantics and compiled to it, so the database's contract never
  becomes the API's.
- Imported automation usage is its own dataset, `automation-usage/1`.
- The egress ledger keeps no copies of data and keeps entries for 180 days.
- Scripted plugins and the declarative exporter share one run contract and one
  connector API; the declarative layer is recommended.
- Opaque IDs and hashes are keyed by a salt the receiver issues per
  organization, so one receiver can recognize a conversation across a person's
  libraries and spot duplicate accounts; two receivers cannot match data.
- Usage-only imports (the labeling case) bring no conversations or text,
  deduplicate by namespace and ID, keep estimates out of cost, and are labelled
  as automation. Uninstalling a plugin never deletes what it imported.

## Round one

The first review (of `5bd16c91`) found the following; each was resolved as
shown. Check the resolutions as part of this review.

| # | Finding | Resolution |
| --- | --- | --- |
| 1 | The public contract embedded Query Table V3 | `export-query/1` (`contracts/plugins/export-query.schema.json`), checked field by field and compiled to Query Table |
| 2 | Versions and receipts did not survive restore or copy | Versions are a serve-time clock that only moves forward; hello carries `library_instance`; restored or copied libraries pause, then take over with `replaces_instance` and `replace_all`; receivers refuse the old instance with `409 superseded` |
| 3 | Work items and conversations were muddled; retractions had no dataset | Everything keyed by `(dataset, opaque id)`; `/items/{dataset}` replaces `/conversations`; each endpoint serves one dataset |
| 4 | Work item totals leaked a private conversation | Privacy is per work item, so totals never mix private and shareable activity |
| 5 | Periods had no series and could not shed groups | Periods are keyed by `(series, period)` and carry all their groups; restatements are time ranges |
| 6 | Salted hashes hide nothing from their receiver; `on_reinstall: new` was undefined | Consent says "pseudonymised: this receiver can check guesses"; `on_reinstall` defined per registration |
| 7 | A paused installation blocked feed pruning | The feed keeps 30 days; older tokens get `410 cursor_expired` and resync |
| 8 | Usage imports changed what `usage/1` means | `automation-usage/1`; a field's set of values is part of a dataset version |
| 9 | Retrying under one idempotency key with new content | Retries get a new `run_id`; batches carry `Pharos-Batch-SHA256`; mismatch is `409 batch_conflict`; staging expires after 24 hours |
| 10 | Usage namespaces had no owner | Namespaces belong to the declaring plugin; records keyed by `(plugin, namespace, id)` |
| 11–17 | Interpreter, permissions hash, duplicated declarative fields, preset checks, aggregate granularity, 403/404 order, a missing README rule | `interpreter`/`entry`; RFC 8785 hash with defaults; parse-time subset check; presets checked in the schema; per-dataset group keys shown at consent; check order in the OpenAPI; README updated |

## Round two

The second review (of `608f8702`) found the following.

| # | Finding | Resolution |
| --- | --- | --- |
| 1 | Nothing said how Pharos detects a restored or copied library | `library_instance` is the existing library descriptor's instance (`pharos-library.json`): a copied folder is refused until **Recover library** (also how backups restore) mints a new one, and one drive opened on several Macs keeps it. `checkPluginInstances` pauses installations from another instance. A block-for-block drive clone, which the descriptor cannot see, is caught by receipts: `409 forked` |
| 2 | `replace_all` assumed a takeover fits in one run | A takeover (`tko_…`) spans runs: every hello and commit names it, the run that finishes commits `complete`, and only then does the receiver retract what no run of it sent |
| 3 | `resend` still used bare IDs and `{dataset, period}` | `items: [{dataset, id}]`, `periods: [{series, period}]` with an RFC 3339 start |
| 4 | Aggregate mode could return row values through `min`/`max` and sparse groups | Fields have types, listed in `/grant`; in aggregate mode text and list fields are only allowed group keys, `min`/`max` take numbers and times, `sum`/`avg` numbers. A minimum group size is an open question for the owner |
| 5 | After a restore the version clock rested on the wall clock | Hello answers carry the receiver's `max_version`; the clock never goes below it |
| 6 | "Work item" was ambiguous in the catalog | Defined once: a `workspaces` row, not the `work_items` table |
| 7 | Queries over redacted fields need a performance note | Added to Hazards |
| 8 | `/items?ids=` would hit URL limits | `POST /items/{dataset}` with an `items_request` body |
| Q | Default freshness | The three-day default stays; hello now carries `export_delay_seconds` beside `as_of`, so receivers can explain the gap |

Also changed: a commit whose answer was lost is recovered through hello
(`unconfirmed_run`, answered with its `receipt`) instead of being sent again.

## What to look for

Concrete scenarios beat general concerns. For each finding, give the inputs or
sequence of events and what goes wrong.

1. **Privacy leaks.** Any way an exporter or receiver can learn that private
   work or usage exists, or anything about them: counts, gaps in IDs or
   versions, change tokens, timing, restatements, distributions, the
   `threshold` trigger, `resend` answers, errors (`404` versus `403`), the
   consent preview, fast paths and fact cubes, subagents, mirrors, workspaces,
   imported conversations.
2. **Recordkeeping under failure.** Walk the served/acknowledged model and the
   ten invariants through: a crash between adoption and cursor commit; a
   receiver commit whose response is lost; a partial run followed by a privacy
   change; a full re-send; uninstall and reinstall with `keep` and with `new`;
   restoring the library from a backup; copying a library; two Macs opening the
   same library in turn; a takeover interrupted by a privacy change or a second
   restore; the ledger's 180-day pruning.
3. **The export delay and the export query.** Whether `as_of` holds
   everywhere (metadata, messages, totals, change visibility, restatements,
   a change of the delay itself), and whether `export-query/1` is complete
   enough for Pythia's needs while staying checkable field by field.
4. **Contract gaps.** Places where the docs, schemas, OpenAPI and examples
   disagree, a field the design needs that no schema has (or the reverse),
   rules that only Pharos can check and are not listed in the contracts'
   README, and versioning that would force a new major version for an ordinary
   change.
5. **Idempotency end to end.** Repeated runs, replayed batches, an importer
   re-sending files and usage records, a receiver applying a run twice, an
   out-of-order commit.
6. **Fit with existing code.** The proposed operation kinds and holds against
   [background work](../background-work.md); adoption against the capture
   manifest and `IndexCapture`; `imported_usage` against how the usage dataset
   and its precomputed paths are built; `conversations/1`, which needs a new
   projection.
7. **Load on the library.** Growth of `export_changes`, `plugin_deliveries` and
   the ledger for a library with a million conversations and several exporters;
   the cost of the privacy filter on every connector query; the effect of a
   large backfill on indexing and the writer.
8. **Consent truthfulness.** Whether the consent screen, as designed, would
   describe everything a plugin can then do, including what a scripted plugin
   can do outside Pharos.

## Deliverable

A list of findings, most severe first, each with: severity (blocker, major,
minor), the file and line, the scenario, and a suggested change. Put questions
that need the owner's decision in a separate list. Say what you checked and
found sound, briefly, so the owner knows what was covered.

## Checks

```sh
python3 -m unittest discover -s contracts/plugins   # schemas, examples, OpenAPI references, doc examples
python3 -m unittest discover -s importers           # SSH importer end to end, results checked against the schemas
```

Both need `jsonschema` 4.18 or later.
