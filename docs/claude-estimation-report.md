# Estimating Claude chat tokens and tool timing

November 12, 2026 · Pharos estimator `claude-output-v4` · Manifest revision `2026-11-12.4`

Runtime v4.1 subsequently corrected calibration refresh continuity, native clock
edge cases, visible-thinking lower bounds, block weighting, duplicate tool
accounting and response anchors. The fitted coefficients are unchanged. The
statistics below describe the frozen v4 evaluation, not a new v4.1 validation
run; [runtime corrections](../estimation/README.md#runtime-v41-review-corrections)
describes the differences.

Pharos now has an implementable suite of models for estimating generated tokens,
thinking time and tool-call duration in imported Claude chat conversations. The
suite uses the evidence each response actually preserves and falls back to
published assumptions when a user's library has no suitable coding history.
The implementation is in the app source; this report does not announce a binary release.

On 792 previously unused complete coding turns, reconstructed with the kinds of
evidence a chat export can contain, combined output-token error is **21–28%**,
depending on which clocks and settings remain available. The strongest new
component estimates tokens from fully visible thinking text: its error is
**27.4%**, compared with **75.4%** for the preceding estimator.

These are coding-token validation results. Native chat exports provide useful
timing measurements but no token ground truth. The working assumption is that
coding performance provides a useful prior for chat performance within the same
model class. That assumption enables an estimate; it does not establish an
individual chat's true usage.

## What the app estimates

A **turn** starts with a sender's message, usually the user, and includes the
assistant's intervening tool activity and final answer. One turn can contain many
model requests. A coding API response that emits a tool call is therefore often
only a fragment of a turn.

The app estimates three quantities separately:

- **Generated output tokens:** assistant prose, tool arguments, an empirical
  per-tool output allowance, and thinking tokens. Tool-result text is incoming
  context and is not counted again as generated output.
- **Thinking duration:** observed thinking intervals where available, or a
  prediction from visible text or response timing. A token prediction alone does
  not establish a duration.
- **Tool-call duration:** argument writing plus the wait from completed arguments
  to result observation. That wait includes execution, transport and harness
  overhead; it is not a pure server execution measurement.

Total turn latency is outside the estimate contract. Processed input is
estimated separately, as [its own report](claude-input-estimation.md)
describes; the rest of this report covers generated output and timing.
Reported usage, including reported zero, always takes precedence. Estimates
fill ordinary usage records with a boolean flag and an asterisk in the reader
and analysis tables. Known models receive partial API price equivalents. These
amounts are not subscription bills.

## How the models work

### Generated prose and tool arguments

The published nonthinking conversion is approximately:

```text
tokens = 0.3796 × prose characters
       + 0.3970 × serialized argument bytes
       + 86.61 × tool calls
```

Characters are Unicode characters; argument size is serialized UTF-8 bytes. The
per-call term is an empirical allowance for output absent from rendered text,
without attributing it to a particular provider billing item. On complete turns,
this component has **4.2% error**. On individual API requests its error is **8.9%**;
the counting units and populations differ.

### Thinking tokens and duration

The estimator uses these evidence regimes:

1. **Native thinking clocks.** Merge overlapping thinking intervals and multiply
   duration by the published model/effort/speed rate. Eligible local coding
   recordings can refine that rate. A partly clocked trace retains its observed
   intervals and explicit missing-block assumptions.
2. **Complete visible thinking without clocks.** Every retained thinking block
   must explicitly say `thinking_hidden=false` and contain real thinking text.
   The new token model uses `0.7587 × characters + 5.545 × blocks`. It replaces
   the fixed per-block duration assumption in this supported regime. Summary
   text does not qualify as visible thinking.
3. **No thinking trace.** For complete tool-bearing responses, first try timing
   models: unexplained gaps after fully clocked prose/tools, then time outside
   observed tool spans, then the sender-to-answer window and tool-start
   positions. Convert the predicted thinking seconds using the applicable
   thinking-token rate. When timing is unavailable, try a supported exact
   model/effort/speed body model, then the new provider-wide body prior.
4. **Sparse or unsupported evidence.** Retain explicit summary/block or
   missing-trace assumptions and broad sensitivity bounds. Text-only replies
   still receive estimates, but missing thinking is not interpreted as zero.

The provider-wide body prior predicts thinking tokens from argument size and
tool categories:

```text
thinking tokens = 2,785.16
                + 149.25 × argument kilobytes
                + 132.68 × command calls
                + 1,481.46 × read calls
```

It requires paired calls/results and a final text answer. It works with unknown
model or effort and never infers those identities. Missing argument sizes are
unavailable predictors, not zero-byte observations. Exact body models remain
available for recorded Opus 5.5 high/medium and Fable 5.2 medium settings at
standard speed. These are model names recorded in the source data and manifest.

Visible thinking duration has its own independent model. When the same response
has suitable text-stream clocks, it uses `thinking characters ÷ observed text
characters/second × 1.751`. Otherwise it uses `0.009137 seconds × thinking
characters`. Its support limits differ from the token model's; choosing or
rejecting a duration prediction does not alter the token prediction. Predictions
cannot exceed a trustworthy native response window. Unsupported predictions
remain unavailable or retain a clearly identified prior.

The conservative visible-text contribution from the previous estimator remains
a floor for partial or other unsupported traces. Contributions combine by taking
the maximum where they describe the same thinking, avoiding double counting.

### Tool timing

Native call/result clocks establish the full observed span. A tool-use block's
stop timestamp marks the end of argument writing; it does not establish the end
of execution. If clocks are missing for a completed supported tool call, the app
predicts argument-writing time from size and adds a published execution/wait
prior. The global expected wait is about **0.84 seconds**, with specific priors
for supported tools such as web search and fetch.

Unpaired calls, asynchronous launches and user interactions do not receive
invented completion times. Overlapping spans are combined as a union when
explaining elapsed response time. Tool durations are never simply added together
to claim a turn's elapsed latency, and sub-agent acknowledgments do not establish
a sub-agent's completion or its hidden token usage.

## Performance on complete turns

All percentages below use **weighted absolute percentage error (WAPE)**:
sum of absolute prediction errors divided by sum of actual values. A 25% WAPE
means 25 tokens of absolute error for every 100 actual tokens across the cohort.
It does not mean each individual prediction is within 25%. Signed bias measures
net overprediction or underprediction, which can cancel across turns.

The same **792 complete turns from 394 independent conversation families** were
replayed with different evidence withheld. A family keeps root sessions, helpers
and connected duplicate API recordings together. These families were excluded
from fitting and candidate selection. The previous version is v3.

| Available evidence | v3 output error | v4 output error | v4 net bias | 95% interval for v4 cohort error |
| --- | ---: | ---: | ---: | ---: |
| Sender/final event window and tool-result clocks | 24.2% | **21.3%** | +12.1% | 18.9–23.8% |
| Same window and tool starts, without tool ends | 31.2% | **28.4%** | −22.1% | 25.7–31.4% |
| No usable timing; recorded settings retained | 39.6% | **25.7%** | +7.5% | 23.5–27.8% |
| No timing; model/effort/speed withheld | 43.9% | **27.0%** | +10.0% | 24.7–29.2% |
| Retained thinking text; native thinking clocks withheld | 31.2% | **24.1%** | approximately 0% | 21.9–26.4% |

The intervals resample whole families and describe uncertainty in this cohort's
error. They are not coverage intervals for individual chat estimates. In the
first four scenarios, thinking-token error alone is respectively **49.5%, 62.4%,
57.9% and 60.8%**. Combined output is easier to predict because observable prose
and arguments make up a substantial share.

The start-only window route performs worse here than the no-timing body route.
Additional evidence does not guarantee a better transferred model. The routing
hierarchy was selected before this test and remains fixed; these test results
were not used to choose a new winner or retune coefficients.

### Performance of individual token models

| Model or component | Validation unit and support | Estimated error |
| --- | --- | --- |
| Prose/argument/per-call conversion | 792 fresh complete turns; 394 families | **4.2%** nonthinking-token WAPE; 486 tokens mean absolute error per turn |
| Complete visible, unclocked thinking | 10,796 fresh API requests; 227 families; settings withheld | **27.4%** thinking WAPE versus 75.4% in v3; 194 tokens mean absolute error; +0.1% bias |
| Same visible-thinking regime, combined output | Same requests | **16.7%** output WAPE versus 43.4% in v3; 208 tokens mean absolute error |
| Provider-wide body prior, no settings | 778 fresh complete turns; 394 families that pass its domain gates | **26.9%** combined-output WAPE; **60.8%** thinking WAPE; +10.1% output bias |
| Opus 5.5 high exact body model | 218 fresh complete turns; 85 families | **27.5%** combined-output WAPE; **69.2%** thinking WAPE; +8.3% output bias |
| Opus 5.5 medium exact body model | 96 fresh complete turns; 46 families | **26.4%** combined-output WAPE; **64.8%** thinking WAPE; +12.6% output bias |
| Fable 5.2 medium exact body model | No matching cases in the fresh reserve | Retained prior selection; no new independent error estimate |
| Native thinking interval × published coding rate | 1,465 deduplicated requests; 22 previously examined families | **29.3%** thinking WAPE; 15.7% combined output; internal replay only |

The visible-thinking model's family-bootstrap interval is **26.1–28.9%** for
thinking WAPE and **15.8–17.9%** for combined output. Its token fit used 6,154
earlier requests from 126 families. The provider-wide body fit used 3,083 earlier
turns from 1,380 families. Grouping the visible fit by model and effort did not
beat the global candidate on calibration data, so the shipped visible fit is
global; model/effort still select rates and exact body models elsewhere.

**Sparse fragments remain a weak regime.** Replaying all 28,366 eligible fresh
API requests, including incomplete tool-response fragments, gives **51.4%
combined-output WAPE**, versus 67.6% for v3. Thinking alone has **113.0%** WAPE;
error can exceed 100% when assumptions overpredict small true counts. This
stress test is neither a complete-turn benchmark nor a representative mixture
of chat exports. It demonstrates why the 21–28% complete-turn result cannot be
advertised as universal accuracy. Plain text with no thinking trace and no tool
evidence continues to use a broad assumption rather than a validated precise fit.

## Performance of duration models in native chat

These tests use actual chat clocks as targets and hide the clock being predicted.
They do not validate thinking-token counts. The conversations were already
examined during the earlier signal survey; their frozen family-hash holdouts
provide useful diagnostics with weaker independence than the fresh coding reserve.

| Duration model and available evidence | Holdout support | WAPE | Mean absolute error |
| --- | --- | ---: | ---: |
| Hidden thinking from fully clocked nonthinking spans and adjusted gaps | 14 responses; 10 conversations | **8.3%** | 1.43 s |
| Hidden thinking from response window and observed tool spans | 8 responses; 7 conversations | **12.0%** | 1.63 s |
| Hidden thinking from response window and tool starts only | 18 responses; 9 conversations | **37.6%** | 29.28 s |
| Visible unclocked thinking from text size/stream rate | 18 responses; 11 conversations | **30.4%** | 2.74 s |
| Summary-only duration assumption | 36 responses; 18 conversations | **55.9%** | 15.30 s |
| Post-argument execution/result wait | 27 calls; 11 conversations | **40.3%** | 0.39 s |
| Argument-writing duration from size, without clocks | Same 27 calls | **82.3%** | 0.18 s |
| Full tool span with native argument clock and estimated execution | Same 27 calls | **32.8%** | 0.39 s |
| Full tool span with size-based arguments and estimated execution | Same 27 calls | **39.3%** | 0.47 s |

Seventeen of the 18 visible-duration cases use a same-response measured text rate;
one uses the character-duration fallback. The preceding block-duration assumption
has 160.3% WAPE on those cases. The new result is encouraging but small: its
family-bootstrap interval is approximately 17.7–77.6%. Argument-writing error is
large proportionally because many phases last fractions of a second; the small
absolute error does not eliminate that weakness.

Coding records do not preserve equivalent native chat text-stream intervals.
Consequently, the full-turn coding replay deliberately cannot activate the gap
route. Its 8.3% duration result must not be presented as a verified token-error
result. Component errors must not be added or multiplied to manufacture a single
overall percentage.

## Model limits and operation in the app

The global [assumptions manifest](../estimation/assumptions.json) contains the
coefficients, selectors, support counts, tool categories and domain limits. It
is bundled with the app and exposed through `GET /api/estimation/assumptions`.
Libraries with chat alone can use every published fallback. Libraries with coding
history can refine eligible thinking rates during Tools maintenance; page reads
do not scan global donor transcripts. Local calibration never rewrites the
published manifest or learns from the app's own chat estimates.

Existing imports also receive a resumable stored backfill through Tools
processing. Predictions fill ordinary request/session/hourly usage and tool
duration records with explicit estimate flags and provenance. Ordinary views
mark estimates with `*`; known models also receive partial cost through
the standard price book. Estimated input carries an assumed cache division. Any
authoritative usage, including zero, excludes the whole conversation. A model update schedules the backfill even without new source indexing.
The [shipping and backfill guide](../estimation/README.md#shipping-and-backfilling-existing-imports)
describes automatic scheduling, cancellation/resume and completion status.

| New model | Runtime support limits |
| --- | --- |
| Complete visible thinking tokens | No usable thinking clocks; all thinking explicitly visible; up to 111,292 characters and 2 blocks |
| Visible thinking duration | Complete visible trace without thinking clocks; up to 25,359 characters and 600 predicted seconds; suitable text clocks take precedence over character-only duration |
| Provider-wide body prior | Complete paired tool body ending in text; 1–247 calls; up to 52,321 prose characters; required argument size available |

The remaining models retain their own published domains. Unsupported images,
attachments, incomplete tool bodies and incompatible settings prevent whole-body
fits from applying. The UI retains provenance, availability, model identifiers,
manifest digest and broad lower/upper sensitivity values. These sensitivity
values are scenarios, not calibrated confidence bounds. Missing duration remains
distinct from measured zero.

Conversation position, retained history, result volume, compaction and sub-agent
structure were investigated in the [context study](claude-context-estimation-study.md).
They improved processed-input prediction, but did not consistently improve
generated-output prediction under export-like evidence. They are therefore not
blanket output multipliers. Tool kinds and argument size enter supported body
models; result timestamps and call positions enter timing models. Child-agent
usage is never invented by multiplying the parent's estimate.

## Validation and reproducibility

Point coefficients were selected on earlier training/calibration data and frozen
before evaluating 407 previously unused connected coding families containing 662
conversations. The reserve contains thinking-active families and is not a random
sample of all chats. After deduplication it contains 28,894 coding requests;
28,366 have retained, known, internally consistent token labels. The complete-turn
subset requires closed turns with complete self-request token sums. It does not
automatically add separately recorded helper usage.

The evaluation reconstructs export-shaped blocks from scalar character/byte
counts and available clocks, then calls the actual Go parser, evidence router
and estimator. Synthetic strings preserve feature sizes; they are not new token
ground truth. Prediction inputs exclude target labels. Request fragments do not
receive fabricated future tool results or final answers. Native timer records
are deduplicated. No text-stream clocks are manufactured for coding turns.
Coding response windows use retained sender/final response event times; they are
proxies for chat windows, not verified request-send or text-stream boundaries.

The [aggregate evaluation](../estimation/studies/claude-shipping-2026-11-12.json)
records support, error, bias, route counts, family-bootstrap intervals and the
frozen manifest digest. Private scalar extracts, family identities and labels
remain outside tracked source. The production catalog was read-only.

The [estimation developer guide](../estimation/README.md) describes the scalar
fitter and manual Go replay. The fitter exports reviewable manifest entries and
keeps test targets out of fitting; updating the manifest remains an explicit
reviewed source change. Future comparisons should freeze candidates before using
new families, retain separate request/turn units and report weak fallback regimes
alongside the best-supported results.

Validation passed for the full and fast Go suites, focused estimator/reader
checks, all three Python fitter tests, the web checks and full JavaScript
matrix (1,779 passed; three skipped in each run), and both Claude export browser
cases at 1440 and 320 pixels. The final production-estimator replay passed
89,470 version/evidence cases, including repeated source responses under
different masks. `git diff --check` passed. The aggregate artifact records the
manifest digest and implementation file digests for this evaluation.
