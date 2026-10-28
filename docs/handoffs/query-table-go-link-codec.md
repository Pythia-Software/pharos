# Query Table: read and write query link tokens in Go

A standalone assignment for the Query Table repository
(`github.com/Pythia-Software/query-table`), against `main` after 0.9.5. Pharos
needs it in a published release before it can ship the link tools described
below. It has been written to be sent to the Query Table team as is.

## Summary

The TypeScript core serializes a `QueryState` into the URL token hosts put in
query strings, and reads it back. The Go module cannot do either. Please add
a Go codec that matches `encodeQuery` and `decodeQueryStrict`, prove parity
with fixtures shared by both languages, and release it in the next version
with all packages and the Go module.

A reference implementation exists (see "Reference implementation" at the end).
It is offered as evidence of feasibility and as a source of test cases, not as
a required design.

## Why Pharos needs it

Pharos is gaining two MCP tools, `browse_site` and `site_link`. They let an
agent browse Pharos's views and receive a link to exactly the view it wants,
for example a tokens dashboard for `provider = claude` and
`model_family contains opus` with a 7-day rolling mean. The Go service must:

- **build** those URLs. A dashboard's filters travel as a query token in
  `analyze_filters`. Every prebuilt card's link also carries the card's full
  source query in `q_<dataset>`: 101 of 119 cards use `select`, `orderBy` and
  `grouping`, and 86 use `aggregations`. A where-only encoder is therefore not
  enough.
- **parse** URLs an agent supplies, often copied from the browser. Pharos
  accepts an agent's URL only after decoding it, checking it against what the
  page can show, and rebuilding it canonically. A decoder that is laxer than
  the browser would let Pharos approve links the page then refuses to open.
  One that is stricter would reject links the browser wrote.
- **compare** two links by state, not by bytes, because DEFLATE output differs
  between implementations.

Pharos's dependency policy forbids a local copy of the token format, so this
has to come from Query Table.

## Current state (0.9.5)

- `packages/core/src/encode.ts`:
  - `encodeQuery` runs `normalizeQueryState`, then `compactQuery` and
    `packQuery`. It emits `2j.` + base64url JSON or `2d.` + base64url raw
    DEFLATE (fflate `deflateSync`), whichever is shorter.
  - `decodeQueryStrict` reads `2j.`, `2d.` and legacy unprefixed base64 JSON.
    It inflates in 256-byte chunks with a running size check, enforces the
    100-predicate budget, and normalizes.
- Go:
  - `ValidateQuerySnapshot` (`backends/go/query_snapshots.go`) inflates a
    `2d.` token only to check that it is a JSON object.
  - `DecodeWireQuery` (`backends/go/query.go`) reads only the legacy
    unprefixed form.
  - There is no encoder, and no typed decode of `2j.` or `2d.`.

## Requirements

### API (`backends/go`)

Names are suggestions. Keep or change them, but document the result.

- `EncodeQueryLink(q LinkQuery) (string, error)`:
  - Produces a token that `decodeQueryStrict` reads as the same normalized
    `QueryState`.
  - Picks `2j.` or `2d.` by length, as `encodeQuery` does.
  - Enforces `MAX_QUERY_STATE_BYTES` (4 MiB of UTF-8 JSON) and
    `MAX_QUERY_TOKEN_LENGTH` (2 Mi UTF-16 units).
- `DecodeQueryLink(token string) (LinkQuery, error)`:
  - Behaves like `decodeQueryStrict` and returns the normalized state.
  - `""` is the empty query.
- Errors are typed with codes `invalid`, `too_large` and
  `unsupported_version`, matching `QueryDecodeError.code`. Messages should
  match the TypeScript ones where they exist.
- `LinkQuery` covers the whole compact wire form, and nothing the
  TypeScript state keeps may be silently dropped:
  - select, with width and display;
  - where, including packed array set filters `["set", field, mode, values, id]`
    expanded as `createSetFilter` does, and scalar set filters as marked terms;
  - orderBy, limit and offset;
  - aggregations;
  - item details, including the pooled `views: {pool, refs}` form;
  - fitToScreen and grouping.

  Reuse `WhereTerm`, `OrderBy` and the other existing wire types where they
  fit. Members whose shape is owned by TypeScript can be carried as
  normalized JSON if typing them would lose members. The existing `AggSpec`
  lacks label, layout, window, share and `unsupportedOp`, for instance.
- Set-filter metadata on `WhereTerm` (id, mode, values) must survive a
  decode and re-encode. The SQL compilers can ignore it.

### Parity contract

- **Decode:** for every token, `DecodeQueryLink` either returns a state equal
  to `decodeQueryStrict`'s, or fails with the same error code.
- **Encode:** the JSON before compression should be byte-identical to
  `encodeQuery`'s for the same normalized state. That is the simplest
  testable definition of "the same link". DEFLATE bytes do not need to
  match. Near a length tie, Go may therefore pick the other prefix; say so in
  the docs.
- **Normalization:** Go must reproduce everything in `normalizeQueryState`
  that changes the encoded form: clamping, defaults, dropped members, member
  order and diagnostics. If the team prefers to make normalization a
  language-neutral specification first, or to simplify the TypeScript side so
  that less needs mirroring, that is welcome. Please tell Pharos.
- **Strict encode (recommended):** `encodeQuery` silently drops some input,
  such as an empty OR group, which matches nothing, or an invalid metric. The
  Go encoder should reject anything at top level that a strict decode would
  reject or drop, so a host never builds a link that widens a filter. Filters
  nested inside metrics, grouping and link patches can be normalized as in
  TypeScript.

### Robustness

- Every limit applies before work is done:
  - token length;
  - inflation bounded by the expanded size, not trusting the input;
  - JSON size;
  - the predicate budget.
- Decoding must be close to linear in token size. A 2 MB token is an attacker
  input wherever a host decodes URLs it did not write. A fuzz target is
  expected.

## Pitfalls found by the reference implementation

These came up while building and reviewing the reference port. Each one is a
fixture case there.

1. **JavaScript object key order.** `JSON.stringify` writes integer-like keys
   (`"0"`, `"1"`, …) first, in numeric order, and the other keys in insertion
   order. Byte-identical packing needs this. A naive ordered-map insert that
   keeps index keys sorted is O(n²): a 900 KB token of `{"0":0,"1":0,…}` took
   about 3 minutes to decode in the first review. Collect index keys
   unsorted, and sort them once when the keys are read.
2. **UTF-16 lengths.** Length limits on labels, ids and values count UTF-16
   code units. Lone surrogates survive `JSON.parse`, and truncating a
   diagnostic can split a surrogate pair. Go strings are bytes, so measure the
   way JavaScript does.
3. **Numbers.** Use `Math.round` semantics (half away from zero, with
   negative halves rounding up), clamp to safe integers, and handle
   `Infinity` and non-finite input as normalization does.
4. **`String()` coercion.** Several checks convert a value with `String(x)`.
   Arrays join recursively, and plain objects become `"[object Object]"`.
   An object with its own `toString` key, from `JSON.parse`, makes `String()`
   throw. TypeScript reports that as `invalid` unless it happens inside a
   `try`, as with property views. Deeply nested arrays overflow V8's stack at
   about 2,600 levels, depending on the engine. The reference port rejects
   past 1,000 levels, erring on the strict side.
5. **Normalization is not idempotent** for some invalid metrics. An
   unsupported op moves `diagnostics` ahead of `unsupportedOp`, and a sort
   term with an array `dir` is dropped only on the second pass. Fixtures
   should record the packed JSON of re-encoding the decoded state, not assume
   that one pass is a fixed point.
6. **Text decoding.**
   - Invalid UTF-8 inside a `2j.` token becomes U+FFFD, as with Node's
     `Buffer`.
   - A byte-order mark is stripped in a `2d.` token, as `TextDecoder`
     does.
   - A byte-order mark at the start of a `2j.` token is rejected by Node but
     accepted by browsers. The TypeScript core already differs by engine
     here; the team should pick one behaviour.
7. **Possible TypeScript bug.** `decodeQueryStrict` keeps a set filter whose
   `mode` is not a string, for example `["any"]`, but `encodeQuery` then
   throws on that state. The reference port rejects it on decode. Please
   decide which is intended and make both languages agree.
8. **Options added to TypeScript later.** Unless something fails, Go would
   silently drop a new metric option. The reference added a TypeScript test
   that reads the option tables in `metricNormalize.ts` and fails when a
   fixture doesn't cover an option. It matches by name only. A sturdier
   guard, such as an exported list of option keys, would be better.

## Proving parity

- **Shared fixtures,** for example `schema/fixtures/query-links.json`,
  generated from a case list by a TypeScript test that fails when the file
  is stale. Each valid case holds the input state or a hand-made token, the
  `encodeQuery` token, the normalized state, and the re-encoded packed JSON.
  Each invalid case holds the token or state and the expected error code.
- **Coverage:**
  - every compact key, and every metric option;
  - array and scalar set filters, and negated and grouped (`any`) terms;
  - relative-time values and legacy shapes;
  - pooled item-detail views, and grouping limits (summaries, representative
    terms, params, link columns in patches);
  - UTF-16 limits with astral characters;
  - both size limits, and every rejection path;
  - the pitfalls above.

  Large inputs can be small generator entries that both languages expand the
  same way.
- **Go to TypeScript:** Go writes the tokens it encodes to a second fixture
  file, for example `query-links-go.json`, and fails when that file is stale.
  The TypeScript test runs those tokens through `decodeQueryStrict` and checks
  the state.
- **Differential fuzzing (recommended before release):** mutate payloads and
  compare Go with the built TypeScript package on state, error code and
  re-encoded JSON. The reference ran about 150,000 mutated payloads with no
  mismatches. Keep a Go fuzz target (`FuzzDecodeQueryLink`) in the repo.
- **Performance regression test:** decode and re-encode a token with about
  200,000 index keys within a generous limit. The reference does this in
  about 0.25 s at worst.

## Release

- Ship it in the next release (0.9.6 unless the team prefers otherwise),
  following `RELEASING.md`, with every npm package and the Go module at the
  same version.
- Note: `main` already holds unreleased changes (#143 touch-target
  enlargement, #144 metric windows and shares). They would ship in the same
  version.
- Document the new API in the Go package comment, the README's Go section
  and the CHANGELOG.

## Done when

- The exported Go API is documented, and the parity fixtures pass in both
  directions.
- The fuzz target and the performance test are in the repo, and
  `npm run check`, `npm run release:check`, `go test ./...` and `go vet ./...`
  pass.
- The decisions on pitfalls 6 and 7 are recorded in the CHANGELOG or the
  docs.
- The release is published, so Pharos can pin it with `GOWORK=off`.

## Reference implementation

Pharos built and reviewed a port in a local Query Table worktree, branch
`gbdubs/go-query-link-codec`, three commits on 0.9.5's `main`. It is not
pushed. It can be sent as a patch on request.

It comprises:
- `backends/go/query_links.go`: the API and tokens;
- `query_links_json.go`: an ordered JSON value with JavaScript semantics;
- `query_links_normalize.go`: a port of `normalizeQueryState`, about 1,300
  lines;
- `query_links_test.go`;
- `packages/core/test/queryLinkCases.ts`;
- `queryLinkFixtures.test.ts`;
- the two fixture files.

That is about 9,000 added lines, most of them fixtures. Two independent
review rounds found pitfalls 1, 3, 4 and 8 and the empty OR group; all are
fixed there. Use it, adapt it or replace it. What Pharos depends on is the
API and the parity contract above.
