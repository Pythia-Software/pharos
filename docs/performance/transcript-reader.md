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
(cd web && npx playwright-core install webkit)
node tools/transcript-bench.cjs --url '<URL printed by dev-ui>'
```

`--engine chromium` uses installed Chrome. The steps avoid the Show filter,
which is a library preference shared with the running app.

## Larger conversation, 2026-10-22

The workspace conversation used for this pass has 33 turns and 16,569
normalized events. Chrome profiling against the live service found two
additional costs: delegation links searched the entire transcript once per
child session on every newly rendered page, and the two rails alternated
geometry reads and style writes. Even reading `scrollTop` after the other
rail's writes could flush layout.

The reader now reports the newly inserted roots with each materialization.
Context targets and child links scan those roots, and completed links leave
the pending set. Both rails measure before either writes, retain their
geometry between layout changes, and derive scrolling positions from the
scroll offset. Pagination re-observes its remaining control instead of
synchronously measuring it after each page. Token counts share a number
formatter.

Each turn initially renders 50 events; subsequent pages still contain 150.
This reduces the work multiplied across every turn by **Open turns**, while
Find, context jumps, failures and message links retain access to all events.
Deep links also initialize delegation analysis with the selected conversation,
eliminating an initial request for the workspace alone (186 KB here).

One controlled Chrome run, comparing baseline `b921c62` with this change on
the same service and URL:

| Interaction | Before | After |
| --- | --- | --- |
| Elements after Open turns | 75,795 | 34,554 |
| Open turns, longest animation-frame gap | 933 ms | 250 ms |
| Open turns, action through two animation frames | 1.36 s | 0.88 s |
| Twenty scroll steps, loading more events | 10.03 s | 7.93 s |
| Twenty scroll steps through already rendered history | 3.32 s | 2.66 s |
| Close turns | 852 ms | 638 ms |

Scroll timings include browser input delivery and two animation frames per
step. The after run materializes less history (92,303 elements after scrolling,
versus 131,686), so these represent the same interactions rather than identical
amounts of rendered content. Service load and timings varied substantially;
these are local measurements, not latency guarantees. Large-history painting
still causes scroll stalls, and WebKit remains slow on this conversation.
Full transcript windowing remains the next larger improvement.

Browser checks cover pagination by scrolling and context jumps, child links
on newly rendered pages, rail navigation after resizing and opening/closing
turns, and a single delegation request on a conversation deep link. These run
in both Chrome and WebKit.

## Toggling turns on a long page, 2026-11-02

The conversation for this pass is a Codex workspace conversation with 46 turns
and 4,239 messages. Opening or closing a turn was slow even though a turn
holds little content. The toggle itself was cheap. The cost came from page-wide
work that every small DOM change triggered, and it grew with the page.

- **`:has()` on `<main>`.** Two app-shell rules placed the sidebar with
  `main:has(...)`. In Chrome, the rule that styled `main>.view` marked every
  descendant of `<main>` invalid, so each attribute change anywhere on the page
  restyled about 14,000 elements, five to ten times per toggle. In WebKit, the
  `#appNavigationRail:not([hidden])` argument made every `hidden` change in
  `<main>` run `:has()` invalidation synchronously: the Context rail's marker
  writes took 25 ms a frame. The navigation now writes `data-app-sidebar` on
  `<main>`, and both rules select that attribute.
- **`:has(.xml-chip[open])`.** In WebKit, every `open` change on the page paid
  for this rule: Close turns spent 400 ms just setting `open` on 46 turns. Chip
  rows now keep a `chip-open` class from a capturing `toggle` listener.
- **Animated `box-shadow`.** The drive and status pulses animated `box-shadow`,
  which WebKit can't run on the compositor. Each frame repainted and walked
  every compositing layer on the page. With every turn open, the idle web
  process used about 15% CPU; in the app after a long session, about half its
  main thread. The pulses now animate opacity, with the ring on `::after`.
  Indeterminate progress bars use `transform` instead of `margin-left` or
  `left`.
- **URL churn.** Each disclosure wrote `reader_expanded` and `reader_collapsed`
  separately. Each write recorded a visit, which parses the history, posts it
  to Ask and rebuilds the bookmark star. Each write also re-scanned the
  document for classic disclosures. Both fields are now written together,
  visits are recorded once per burst, and classic disclosures restore only when
  their own parameter changes. The reader's disclosure observer now scans only
  added content.

Measured against the live service before and after the change. Times are
until three quiet frames, so each includes about 50 ms of idle frames:

| Interaction (all turns open) | Before | After |
| --- | --- | --- |
| Chrome, one toggle | 97–127 ms | under one frame |
| Chrome, Open turns / Close turns | 684 / 521 ms | 248 / 73 ms |
| Headless WebKit, one toggle | 130–149 ms | 54–61 ms |
| Headless WebKit, Close turns / reopen | 1,055 / 600 ms | 143 / 214 ms |
| WKWebView (app engine), one toggle | 263–272 ms | 99–124 ms |
| WKWebView, idle CPU | ~19% | ~1–2% |

Headless WebKit lacks the app's compositor, so it shows neither the idle cost
nor the per-frame compositing walk. Use a WKWebView in a real window, or
`sample` the app's WebContent process, for those.

Keep `:has()` off ancestors of large content, such as `main`, `.view` and the
transcript, and keep infinite animations to `opacity`, `transform` and
`filter`.

### Every animation on the site

The same cost applied to every repeating animation of a property WebKit can't
composite. Each one that ran made every frame update rendering and walk the
page's compositing layers, so a small indicator cost more on a longer page.

- **Header beacon sweep** (runs during every sync). This animated a
  registered custom property that drove a `clip-path` and the gradient angle.
  It now uses `transform` and `opacity` keyframes generated every 15°. The lit
  name is revealed by two opposite translations, which cancel exactly, and the
  lamp and halo are overlays with their own opacity. The static pose is the
  same.
- **Ask wait skeleton** animated `background-position`. A wider `::before`
  now slides with `transform`.
- **Ask working trace** still animates `stroke-dashoffset` on the glow's
  outline. It runs only while Ask answers, and costs 2–4% CPU with every turn
  open. A dash moving along a path has no compositor
  equivalent.
- **Ask prompt ideas** counted down by rewriting their label four times a
  second on every page, even while hidden. They now write only changes.
- `token-flash` is a single 2-second highlight and stays as it is.

In WKWebView, with every turn of the same conversation open, the web process
used about 13% CPU during the sweep. It now uses 1–2%, the same as with nothing
animating. The skeleton uses 2–3%.
