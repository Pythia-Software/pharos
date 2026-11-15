# Claude output estimator selected for the app

Historical v2 selection. See the [v4 standalone report](claude-estimation-report.md)
for the current implementation and final performance estimates.
The subsequent [native-signals study](claude-native-signals-study.md)
documents v3's argument/execution separation, observed-gap route and explicit
visible-thinking contribution. The v2 results below retain their original scope.

November 12, 2026. Estimator `claude-output-v2`, manifest schema 2, revision
`2026-11-12.2`. This completes the selection exercise following the
[source survey](claude-chat-estimation.md),
[context/tool ablation](claude-context-estimation-study.md) and
[latency study](claude-latency-estimation-study.md). The implementation and
published assumptions are in the app source; this document does not describe a
released binary or a production catalog migration.

The selected system estimates **generated output per assistant response** and
**execution through result observation per tool call**. The reader sums responses
within its existing turns. It works without local coding history. It does not
populate reported usage, input-token totals, API costs or complete turn latency.

## Evidence hierarchy

Reported usage, including a reported zero, takes precedence. For unreported
Claude.ai export responses:

1. **Native thinking clocks:** merge overlapping intervals and convert seconds
   to thinking tokens with the published model/effort/speed rate. Compatible
   local coding evidence can refine that rate. Apply duration bands separately
   to each connected interval, rather than to the sum of unrelated intervals.
2. **Partial traces and summaries:** keep the native contribution and add the
   explicit published duration for each unclocked thinking block: seven seconds
   per summary, or eight seconds for a block without summaries. Do not overlay a
   complete-response model on these contributions.
3. **Entire thinking trace absent, native response window available:** predict
   thinking seconds from a chat-duration fit. Prefer the model using residual
   time after the union of observed tool spans. When ends are unavailable, use
   elapsed time and the position of the first tool start, alongside supported
   body features. Convert those predicted seconds with the separately resolved
   coding model/effort rate or published provider fallback.
4. **Window unavailable, complete tool-bearing body available:** use a published
   coding-derived token model only for its exact recorded model, effort and speed.
   Three cells have sufficient support: Opus 5.5 high/standard, Opus 5.5
   medium/standard, and Fable 5.2 medium/standard. An assumed standard speed does
   not establish an exact recorded match.
5. **Remaining cases:** use the explicit published missing-trace token prior.
   This includes plain text replies, unsupported settings, incomplete bodies and
   out-of-domain inputs. A missing trace does not mean zero thinking tokens.

All coefficients, support counts, sensitivity multipliers, tool categories and
domain limits live in [the global manifest](../estimation/assumptions.json).
Local calibration refines rates, not response-model coefficients. It remains in
the user's library and is refreshed by Tools maintenance. No network model or
training pass runs when opening a conversation.

## Clocks and tool accounting

The response window starts at the **native sender message's `created_at`** and
ends at the **native final text block's `stop_timestamp`**. An assistant envelope,
catalog timestamp, `updated_at` or later selected-model observation cannot supply
either endpoint. A maximum 250-ms clock-domain skew is allowed relative to the
original sender timestamp; the maximum window is 30 minutes.

The complete-body path requires retained source order, a nonempty text ending,
unique tool IDs and paired results in order. Explicit image/document context or
sender attachments disable the fitted whole-response route. Missing features
are unavailable, never filled with zero. Predictions outside the published
domain or with predicted thinking seconds exceeding the window fall back.

A tool's observed span runs from its native call start to its paired result
observation. Overlapping spans count once when constructing residual time.
Residual time is only available when **every** call has both native endpoints.
With start-only evidence, typical tool durations are not subtracted and labeled
as observed time. The residual contains text generation, scheduling and other
overhead as well as thinking; the regression predicts the thinking portion.

For individual calls without clocks, retain runtime-specific chat operation
priors. Coding shell/read timings are not transferred into chat tool durations.
Async launches and user-interaction acknowledgments do not establish completion;
they disable response-window inference and retain unavailable completion status.
Their presence can still contribute body/category features to an otherwise
supported coding token model. Helpers' hidden requests and tokens are not added
to the parent's output. An agent call is a feature, not a measurement of the
child's consumption.

## Nonthinking output conversion

Add three distinct components to thinking tokens:

| Component | Published expected conversion |
| --- | ---: |
| Assistant text | 0.379637 tokens per Unicode character |
| Serialized tool arguments | 0.396984 tokens per UTF-8 byte |
| Tool-bearing output allowance | 86.609457 tokens per call |

The text conversion was fitted on 3,180 pure-text coding requests. Arguments and
the per-call allowance were fitted on 84,276 tool-bearing requests after fixing
that text rate. This isolates text conversion from tokens associated with tool
calls that are absent from the rendered text. The allowance is empirical; it is
not identified as a particular provider billing item. Tool result bodies are
consumed input, not newly generated output. Summaries are evidence about hidden
thinking, not additional generated thinking text.

An initial whole-turn two-rate fit confounded tool-associated output with text
length and produced an implausibly high text rate. It was rejected and replaced
with the isolated fit before inspecting the independent reserve results.

## Selection and independent evaluation

Candidates used nonnegative arithmetic regression with family balancing,
regularization, optional validation-only mean correction and supported-setting
shrinkage. Feature sets and regularization were compared on calibration evidence
under both family hash and forward-time splits. Existing study test sets had
already been examined and are described as **internal tests**, not pristine
held-out evidence. True request counts, usage labels and unavailable tool ends
were excluded from predictors.

For the final check, reserve 800 previously unused connected families, linking
root/helper conversations and same-account duplicate native API responses.
Exclude every component overlapping a conversation in the previous studies.
Eligibility required at least three requests and two positive-thinking labels;
stable hashes selected families without ranking their target magnitudes. Thus
this is a selected, thinking-active cohort rather than a representative sample
of all chats. Keep reported zero-thinking targets in its eligible turns.

Freeze coefficients and routing before reading reserve targets. The frozen
package digest was
`855a998dafae494833eb87691a9c2ec7de6f0b57ba850187199f9af7f7327c11`.
The reserve yielded **1,531 complete turns from 783 families**. Coding records
were masked into export-shaped evidence. Native coding windows are a proxy for
the missing chat measurements; these are not true chat token-accuracy results.

WAPE is total absolute error divided by total reported target tokens. Signed bias
is the signed aggregate error divided by that same total. These are not average
per-turn percentage errors or confidence in an individual estimate.

| Evidence available in the masked benchmark | Thinking-token WAPE | Combined generated-output WAPE | Combined signed bias |
| --- | ---: | ---: | ---: |
| Previous missing-trace and text assumptions | 93.87% | 68.96% | −68.88% |
| Exact supported body model, otherwise prior | 86.14% | 43.38% | −34.75% |
| Native window and tool starts | 70.07% | 35.26% | −30.63% |
| Native window and observed tool ends | 44.75% | 21.75% | +5.19% |

Each candidate row includes the audited nonthinking conversion. These are
separate evidence-availability scenarios, not a claim that every export gets the
best row. The body model applies to 536 turns; within that supported subset,
thinking WAPE is 62.30%, versus 94.97% for its prior. The other 995 turns keep
their prior. Nonthinking output alone improves from 45.34% to 4.54% WAPE on the
reserve, with −1.14% signed bias. Start-only thinking remains substantially
underpredicted; observed-end thinking has +11.57% bias. Broad sensitivity ranges
remain necessary and are not calibrated chat confidence intervals.

Other checks support component choices, on different populations:

- **Native duration bands:** calibrating per connected interval reduces internal
  thinking WAPE from 26.47% to 21.37% on 448 family-split responses, and from
  28.34% to 23.50% on 218 forward-time responses.
- **Native chat thinking duration:** the start-only fit has 86 eligible responses
  from 56 conversations. Internal test WAPE is 34.16% and 49.71%, versus prior
  errors of 93.34% and 81.73%. Each test has only 18 responses. The observed-end
  fit has 71 responses from 50 conversations; test WAPE is 8.96% and 12.08%,
  versus 63.94% and 82.75%, on only eight and 16 responses. These measure
  recorded thinking seconds, not tokens; small samples limit precision.
- **Chat tool durations:** 202 native call/result pairs across 51 families
  support operation-specific means. Internal test WAPE is about 50%/57%, versus
  131%/113% for a generic three-second prior, on 18/37 calls. Retain the existing
  published chat operation means; these small tests do not justify a larger fit.

The sanitized [aggregate report](../estimation/studies/claude-deployment-2026-11-12.json)
records the protocol and results without transcripts or source entity IDs.

## Factors retained and rejected

Elapsed time, early tool position, observed residual time, argument size and
selected tool categories provide useful signal in the supported routes.
Tool-category coefficients describe associations, not causal latency charges.

Conversation position, accumulated context/result volume and helper activity
were tested. They help explain processed input but did not consistently improve
the deployable output model across family and time tests. Hidden compaction and
model-request boundaries prevent reliable processed-input reconstruction here.
Do not add a position multiplier to output tokens or claim that unseen context
was counted. Rich tool-result payloads can matter to future input estimation,
but their size does not directly become generated output in this version.

A new summary-duration coefficient of about eight seconds and a purely
body-based chat-duration model improved some internal tests but worsened the
forward-time tests. Neither is promoted. Subtracting imputed tool durations also
failed to provide consistent gains. Explicit native durations and source-specific
window models are the supported timing routes.

## Updating and verifying the pipeline

The [manifest guide](../estimation/README.md) describes the scalar dataset and
`tools/fit_usage_estimation.py`. Future updates should keep family/duplicate
components together, freeze a new unseen reserve, and test the final coefficients
before publishing them. The fitter accepts explicit scalar rows, produces a
reviewable candidate and aggregate report, and never opens a catalog, rewrites
the bundled manifest or publishes user data automatically.

Go tests cover source clocks, overlap unions, partial traces, async calls,
unsupported settings, missing and malformed evidence, local invalidation, and
Python/Go scalar prediction parity. Reader integration covers clipping, Unicode,
published-only inference, native precedence and isolation from reported usage.
Real chat token truth remains inaccessible; all comparisons preserve the explicit
coding-to-chat transfer assumption rather than treating it as established.

Final app checks passed: focused estimator/reader/settings tests,
`go test -short ./...`, `go test ./...`, `npm --prefix web run check`,
`npm --prefix web run test:full`, both Claude export research browser widths,
the two fitter tests, and `git diff --check`. Both JavaScript unit runs passed
1,779 tests with three skips.

The broader `python3 -m unittest discover -s tools` run has one pre-existing
failure among 161 tests (158 pass, two skip): the risk-analysis splitter helper
compiles `shell_command.go` alone, omitting `authorshipOutputFile` and
`authorshipShellSyntax`. Those helper/parser files are unchanged from
`origin/master`. The new fitter tests pass within that run as well.
