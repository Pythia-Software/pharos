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
- Private is one global state. Private conversations and usage are absent from
  everything an exporter receives, aggregates included; their existence must
  not be inferable.
- Privacy is manual marks, then ordered rules, then a default (which can be
  private). A conversation is exportable once its metadata has settled, and is
  sent again as it changes.
- The egress ledger keeps no copies of data and keeps entries for 180 days.
- Scripted plugins and the declarative exporter share one run contract and one
  connector API; the declarative layer is recommended.
- Opaque IDs and hashes are keyed by a salt the receiver issues per
  organization, so one receiver can recognize a conversation across a person's
  libraries and spot duplicate accounts; two receivers cannot match data.
- Usage-only imports (the labeling case) bring no conversations or text,
  deduplicate by namespace and ID, keep estimates out of cost, and are labelled
  as automation. Uninstalling a plugin never deletes what it imported.

## What to look for

Concrete scenarios beat general concerns. For each finding, give the inputs or
sequence of events and what goes wrong.

1. **Privacy leaks.** Any way an exporter or receiver can learn that private
   conversations or usage exist, or anything about them: counts, gaps in IDs or
   versions, change tokens, timing, restatements, distributions, the
   `threshold` trigger, `resend` answers, errors (`404` versus `403`), the
   consent preview, fast paths and fact cubes, subagents, mirrors, workspaces,
   imported conversations.
2. **Recordkeeping under failure.** Walk the served/acknowledged model and the
   seven invariants through: a crash between adoption and cursor commit; a
   receiver commit whose response is lost; a partial run followed by a privacy
   change; a full re-send; uninstall and reinstall with `keep` and with `new`;
   restoring the library from a backup; copying a library; two Macs opening the
   same library in turn; the ledger's 180-day pruning.
3. **Query restrictions.** How should the connector API decide that a Query
   Table V3 body uses only granted fields and mode? Look at grouping keys,
   where terms, order-by, summaries with expressions, metrics, and
   representative rows. Is `aggregate` versus `rows` well defined for every V3
   shape?
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
