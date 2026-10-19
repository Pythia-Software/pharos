# Transcript reader performance

Measured on 2026-10-19 with headless WebKit, the engine of the Mac app's web
view, against a live library on a heavily loaded machine. The conversation is
a Codex root agent with 17 turns, 851 tool calls and 68 sub-agents: 4,020
messages and a 5.6 MB response. Timings are local and noisy; element counts
are exact.

## Findings

Loading was not the problem. Parsing, normalizing and rendering the collapsed
transcript take about 70 ms; the response itself about 200 ms. The cost came
after Open turns, which renders about 1,350 tool rows.

- **Context rail.** Every scroll frame re-measured each of about 940 markers'
  targets and rewrote their geometry, although their positions within the
  transcript do not change with scrolling: only which are in view does. That
  took about 78 ms of script per frame in WebKit. The turn minimap did the same
  for its turns (about 16 ms per frame).
- **Row size.** Each tool row built about 19 elements before it was used,
  including a hidden chip-preview pane (three elements) and an empty details
  body. Timestamp tooltips used `toLocaleString` with options, which builds a
  formatter on every call.

Both rails now measure only when their content, size, filters or open turns
change, and scrolling updates just their highlights and viewport. Rows build
their preview and details body on first use, and timestamps share one formatter.

| WebKit, this conversation | Before | After |
| --- | --- | --- |
| Elements after Open turns | 33,088 | 27,706 |
| Elements after scrolling to the end | 41,180 | 34,222 |
| Open turns, longest stall | 390–410 ms | 310 ms |
| Scroll, rendered history: frames over 50 ms | 8 of 20, about 115 ms each | 0 |
| Scroll, loading more events: frames over 50 ms | 16, 100–315 ms | 11–13, 51–150 ms |
| Close turns | 800–830 ms | 605–610 ms |

`content-visibility: auto` on entries was tried and rejected: placeholder
sizes changed as entries rendered, which re-triggered layout and made WebKit
scrolling slower.

## Next steps

- **Window the transcript.** The remaining scroll stalls are WebKit drawing a
  page of 30,000+ elements. Rendering only turns near the viewport would bound
  this, but Find, the rails and event links currently expect rendered events.
- **Open turns incrementally.** Opening every turn renders all rows at once;
  opening those near the viewport first would remove the 300 ms stall.
- **Send hidden events on demand.** 1,980 of the 4,020 messages (2.5 MB) are
  system events hidden by default; the reader parses them only for token
  badges and the Context rail. Computing those reports in the service would
  roughly halve the response, at the cost of porting `conversationTokenReports`
  (covered by `tests/token-usage-fixtures.json`).
- **Cheaper rail placement.** Placement still measures every marker target
  once per change, about 80 ms in WebKit for a filter toggle.

## Reproducing

Start a dev UI against the service, then run the benchmark with its signed-in
URL for the conversation:

```sh
go run ./cmd/pharos dev-ui --port 8799 --no-open \
  --open-path /work/<workspace>/conversation/<conversation>
npx --prefix .context/browser-tests playwright install webkit
node tools/transcript-bench.cjs --url '<URL printed by dev-ui>'
```

`--engine chromium` uses installed Chrome. The steps avoid the Show filter,
which is a library preference shared with the running app.
