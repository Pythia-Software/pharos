# Query Table: text measurements in grouped aggregate formulas

Pharos Scatter Plots can pair metrics attributed to individual messages. The
message source already exposes classified user-word and character counts, but
raw text length and word count across all message roles need aggregate formula
support in the published Query Table Go backend.

## Released integration and reproduction

All four `@pythia-software/query-table-*` npm packages and the Go backend module
are pinned to 1.0.3. The frontend function catalog declares `LENGTH(text)`,
`TRIM(text)`, `REGEX_REPLACE`, `SPLIT`, and `ARRAY_LENGTH`.

Using the Go `QueryDatasetV3.SourcePlan` / `Execute` API with a text source field
and a grouped summary like this fails:

```json
{
  "keys": ["conversation_id"],
  "summaries": [
    {"id": "characters", "op": "formula", "expression": "SUM(LENGTH([text]))"}
  ]
}
```

Observed diagnostic: `arity: invalid argument count for LENGTH`. In the released
Go `plan.go`, `planBuilder.operation` assumes two arguments for operators other
than its small explicitly listed scalar set; `LENGTH` never reaches a compatible
one-argument implementation. The same limitation blocks composing whitespace
normalization, splitting, and array length into a per-row word count before SUM.
This is separate from numeric source bindings opting into bounded arithmetic.

## Requested upstream behavior

Support the documented scalar text/array functions inside aggregate inputs and
grouped formulas, with the same signatures, types, null handling, Unicode
semantics and errors as the frontend. Preserve the distinction between a missing
text value and a measured empty string. Keep row-level evaluation inside the
source aggregate, rather than counting the characters of an aggregate result.

Add cross-language fixtures for Unicode, empty and missing text, repeated
whitespace, multiple messages per entity, and grouped filters. Verify the same
formula in frontend evaluation and the Go SQLite V3 executor. Publish a release
containing the capability and upgrade all four npm packages and the Go module
together in Pharos.

## Pharos integration after release

Add raw-message word and character measures in `web/src/scatter-metrics.ts`,
sourced from `messages.text`, with message/conversation/workspace identities.
Use normal published formula execution and regenerate schemas and the site map.
Keep the existing classified user-text measurements separately named. Until the
release, these raw text metrics are blocked; no local Query Table patch,
expression evaluator, module replacement or generated-bundle edit is used.
