# Query Table: read and write query link tokens in Go

Assignment for the Query Table repository (`github.com/Pythia-Software/query-table`),
against `main` after 0.9.5. Ship it in the next release (0.9.6) so Pharos
can consume it.

## Problem

`@pythia-software/query-table-core` serializes a `QueryState` into the URL
token that hosts put in query strings: `encodeQuery` emits `2j.` + base64url
JSON or `2d.` + base64url raw DEFLATE, whichever is shorter, after
`compactQuery` and `packQuery` (`packages/core/src/encode.ts`).
`decodeQueryStrict` reads `2j.`, `2d.` and legacy unprefixed tokens.

The Go module has no equivalent. `ValidateQuerySnapshot` inflates a `2d.`
token only to check that it is a JSON object, and `DecodeWireQuery` reads
only the legacy unprefixed form. A Go host therefore cannot build a link to a
filtered view or read the state out of one.

Pharos needs both. Its MCP server is gaining tools that let agents browse the
site and receive links to exact views, such as a tokens dashboard filtered
to `provider = anthropic` and `model_family contains opus`. Pharos must build
those URLs in Go and must parse agent-supplied URLs back into state to
validate them. Its dependency policy forbids a local copy of the token format.

## What to add (backends/go)

- `EncodeQueryLink(q LinkQuery) (string, error)` produces a token that
  `decodeQueryStrict` decodes to the same normalized `QueryState`. It chooses
  `2j.` or `2d.` by length, as `encodeQuery` does, and enforces
  `MAX_QUERY_STATE_BYTES` and `MAX_QUERY_TOKEN_LENGTH`.
- `DecodeQueryLink(token string) (LinkQuery, error)` matches
  `decodeQueryStrict`. It accepts `2j.`, `2d.` and legacy tokens. It rejects
  unsupported versions, bad encodings, oversize input (bounded inflation, as
  in `inflateToken`), invalid set-filter tuples, invalid view references and
  the predicate budget, with error kinds that map to `QueryDecodeError`
  codes.
- `LinkQuery` covers the whole compact wire form: select (with widths and
  display), where (including packed array set filters, `["set", field, mode,
  values, id]`, expanded the way `createSetFilter` does), orderBy, limit,
  offset, aggregations, item details (with the pooled views), fitToScreen
  and grouping. Reuse `WhereTerm` and the existing types where they fit.
  Unknown members must not be silently dropped.
- Normalization parity: Go must reproduce whatever `normalizeQueryState` does
  that changes the encoded form. Encoding a decoded token should reproduce
  the TypeScript encoder's JSON (before compression).

DEFLATE output is not required to be byte-identical to fflate's. The tokens
only have to decode to the same state.

## Proving parity

Add cross-language fixtures (under `schema/fixtures/` or wherever the repo
keeps shared fixtures). Each case holds a `QueryState`, its `encodeQuery`
token, its packed JSON, and for invalid tokens the expected error code.
Cover:
- every compact key;
- set filters (array and scalar targets);
- negated and grouped (`any`) terms;
- relative-time values;
- legacy shapes;
- the size limits;
- each rejection path.

A TypeScript test regenerates the fixtures, or fails when they are stale.
Go tests must:
- decode every token to the expected state;
- re-encode it so that `decodeQueryStrict` returns an equal state (run
  through node in the existing test tooling, or check Go-encoded tokens into
  the fixtures and assert them from the TypeScript side);
- reject every invalid case with the expected code.

## Done when

- `EncodeQueryLink`, `DecodeQueryLink` and `LinkQuery` are exported and
  documented in the Go package docs and README. The CHANGELOG entry lists
  them under 0.9.6.
- The parity fixtures pass in both languages, and `npm run check` and the Go
  tests pass.
- Release 0.9.6 is cut following `RELEASING.md`. All npm packages and the Go
  module share the version.
