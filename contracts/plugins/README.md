# Plugin contracts

The public interfaces between Pharos, plugins and receivers, as described in
[docs/plugins.md](../../docs/plugins.md). Nothing here is implemented in the
service yet. Where the docs and a schema disagree, the schema is the contract.

| Contract | File | Written by | Read by |
| --- | --- | --- | --- |
| `pharos-plugin/1` manifest | [plugin-manifest.schema.json](plugin-manifest.schema.json) | plugin publisher | Pharos (install, consent, runs) |
| `pharos-run/1` request | [run-request.schema.json](run-request.schema.json) | Pharos | plugin, per run |
| `pharos-run/1` result | [run-result.schema.json](run-result.schema.json) | plugin, per run | Pharos |
| `usage-records/1` | [usage-record.schema.json](usage-record.schema.json) | usage importer, one JSON line per record | Pharos |
| `pharos-drop/1` bundle | [drop-bundle.schema.json](drop-bundle.schema.json) | Pharos (`/export`) | importer on another library |
| `connector/v1` API | [connector-api.openapi.json](connector-api.openapi.json), bodies in [connector.schema.json](connector.schema.json) | Pharos, on loopback during a run | plugin |
| `pharos-report/1` batch | [report.schema.json](report.schema.json) | exporter | receiver |
| `pharos-receiver/1` protocol | [receiver-api.openapi.json](receiver-api.openapi.json), bodies in [receiver.schema.json](receiver.schema.json) | receiver | Pharos's declarative exporter, or a scripted one |

[common.schema.json](common.schema.json) holds shared definitions: IDs,
timestamps, durations, byte sizes, machines, drops, tokens and the error body.
[examples/](examples/) has at least one valid document for each, and
`test_contracts.py` checks them all.

## Rules the schemas encode

- **Exporters read; importers produce.** An export manifest has
  `permissions.read` and no `produce`; an import manifest the reverse, and only
  scripted runs. A declarative exporter must declare `destinations`.
- **Datasets are versioned** (`usage/1`). A published dataset version never
  changes; new fields mean a new version, a new manifest version and new
  consent.
- **Usage drops name a namespace and no machine root**; transcript drops name a
  machine and its original `root`. Drop folders cannot climb out of
  `output_dir`.
- **Spend and estimates are different fields.** A usage record's `cost.usd` is
  required when its status is `priced` or `assumed`; `estimate` is never cost.
- **A receiver can ask for changes, everything, or particular items again,**
  and nothing else.

Some rules span documents and are checked by Pharos rather than by the schemas:
every drop's `machine` is declared in `machines`; drops do not share a folder or
a machine and source; a usage drop's namespace is in the manifest's
`produce.namespaces`; a run's queries use only granted fields.

## Versioning

- Each contract carries its version in its `contract` or `plugin` value.
- Within a version, Pharos may add **optional** request fields; plugins ignore
  fields they do not know. A run request has no `additionalProperties: false`
  for that reason.
- Pharos is strict about what plugins write: results, manifests and usage
  records refuse unknown fields, so a plugin's mistake is reported, not
  ignored.
- Anything else is a new version, supported beside the old one while plugins
  move.

The `$id` URLs identify the schemas; they are not served anywhere yet.

## Checking

```sh
python3 -m unittest discover -s contracts/plugins   # schemas, examples, OpenAPI references, doc examples
python3 -m unittest discover -s importers           # the SSH reference importer, end to end
```

Both need `jsonschema` (4.18 or later) and skip their schema checks without
it. A JSON example in `docs/plugins.md` or `docs/plugin-receiver.md` preceded by
`<!-- contract: FILE[#/$defs/NAME] -->` is validated too.
