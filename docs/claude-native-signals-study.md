# Native chat streaming, tool execution and unexplained gaps

Historical v3 study. The [v4 standalone report](claude-estimation-report.md)
describes the current models and the final replay of token and duration estimates.
The results below retain their original scope.

November 12, 2026. Follow-up to the [v2 deployment selection](claude-estimator-deployment.md).
The selected additions are implemented as estimator `claude-output-v3`, manifest
schema 3, revision `2026-11-12.3`. This describes source changes, not a released
binary. The global [manifest](../estimation/assumptions.json) and the
[aggregate report](../estimation/studies/claude-native-signals-2026-11-12.json)
contain the published policy and numerical results. Source transcripts and entity
IDs stay in private local extracts; the production catalog was opened read-only.

Three changes earned inclusion: separate argument generation from execution,
use fully observed nonthinking spans to explain response-window time, and let
explicit visible thinking contribute a conservative text-based estimate. Measured
text throughput is useful, but it does not directly establish thinking-token
throughput or model identity.

## What the clocks measure

A native tool-use block runs from argument generation start to argument generation
stop. Its paired result starts later. The old call-start-to-result span included
both argument generation and the wait after arguments finished. It remains a
valid total span, but should not be labeled execution alone.

The new API exposes these separately:

| Field | Meaning |
| --- | --- |
| `seconds` | Full call-start-to-result-observation span; existing semantics retained |
| `argument_generation_seconds`, `argument_generation_status` | Argument writing, measured from native call start/stop when available |
| `execution_seconds`, `execution_status` | Native argument stop to result observation, or a separately labeled prior |

Each component distinguishes observed, estimated and unavailable. A recorded
zero survives. A missing result or async/user acknowledgment cannot acquire
an execution completion. Conflicting argument/result clocks do not establish
observed components or a usable response window. With an observed total but no
argument stop, the total stays observed and its decomposition stays unavailable.

For paired calls without a result clock, use native argument duration when
available, then add the native-chat execution prior. If argument clocks are also
missing, estimate argument seconds from serialized Unicode character count and
add that same execution prior. This only applies to the manifest's supported
native-chat operations; unknown operations and missing argument bodies keep the
older explicit total-span prior. Coding tool latency is never substituted for
chat runtime latency.

The published argument slope is about **0.003549 seconds per character**. It is
an arithmetic, family-balanced duration fit, rather than the reciprocal of a
median rate relabeled expected duration. The published global post-argument
execution mean is **0.842 seconds**, fitted on 191 calls in 43 conversations.
Operations with at least five calls across three conversations get their own
mean, shrunk toward the global mean with five prior-equivalent conversations.
Sparse supported operations use the global mean. Sensitivity bounds are broad
engineering scenarios, not calibrated coverage probabilities.

The reader now labels execution when a decomposition exists, and explains argument
generation and full call-to-result time in its tooltip. Older undecomposed spans
remain explicitly labeled spans. Execution may still include transport, result
delivery and harness overhead.

## Native chat timing checks

The raw export contains 449 assistant responses. Under this study's explicit
text-rate filter—at least 40 characters, 0.1–600 seconds, and 20–1,000 characters
per second—203 text blocks in 149 responses are usable. Their median is
204 characters/second, with p10–p90 of 152–311. Characters and duration correlate
at 0.993. Counts differ from the proposed survey because the eligibility filters
are explicit here; the central stream-rate observation replicates.

There are 218 usable native call/argument-stop/result triples after excluding
unobservable completions and invalid spans. Argument writing occupies 79.6% of
their total time. Post-argument waiting has median 0.752 seconds and p90
1.760 seconds. This decomposition explains why a large file-writing call can
have a long total span and a short execution wait.

Hold out entire conversations under a stable hash split and a forward-time
split. Parameter fitting uses training conversations; final published parameters
use hash training plus calibration conversations. These exports have been
examined in earlier studies, so the tests are exploratory replication rather
than a new pristine reserve. Comparisons below use exactly the same test calls.

| Total call-to-result WAPE | Conversation hash: 27 calls / 11 conversations | Forward time: 43 calls / 10 conversations |
| --- | ---: | ---: |
| Frozen v2 operation priors | 56.30% | 64.64% |
| Total-span operation means refitted on training only | 42.75% | 82.11% |
| Native argument duration + execution prior | **34.26%** | **14.26%** |
| Argument size + global stream fit + execution prior | **39.71%** | **21.35%** |

The native-argument path's mean absolute errors are about 0.406 and 0.291 seconds.
These are tests masking result clocks; already observed total spans do not
become more accurate by decomposition. Same-response and earlier-conversation
text rates were also tested for argument generation. Their additional gains
were inconsistent across the two splits, so the simpler global argument fit
ships as the size-based fallback.

## Gaps after measured nonthinking work

For an entirely missing thinking trace, start with the native sender-to-final-text
window. Only when every call has a native result clock and every text block has
native start/stop clocks, merge the union of all text-generation and complete
tool spans. Overlapping text or parallel calls count once. The remaining intervals
are unexplained gaps. Remove one second of ordinary overhead from each gap,
clamping each remainder at zero, then apply the fitted arithmetic model.

The published fit is approximately:

```
thinking seconds = 1.5731 + 0.92029 × sum(max(0, gap seconds − 1))
```

It has 56 training-plus-calibration responses from 40 conversations. It predicts
thinking duration, not measured thinking and not an API-request count. A gap can
also contain scheduling, buffering, or unrecorded work. Even zero adjusted gap
does not prove zero thinking. The fitted offset and existing fallbacks preserve
that distinction.

Evaluation masks all thinking blocks and their clocks from predictors, then uses
the union of native thinking clocks as the target. The strict complete-clock
cohort contains 70 responses across 50 conversations. Native clocks and partial
thinking traces still take precedence in the actual app; the gap model only
runs when the entire thinking trace is absent.

| Thinking-duration WAPE | Conversation hash: 14 responses / 10 conversations | Forward time: 16 responses / 10 conversations |
| --- | ---: | ---: |
| Frozen v2 observed-tool-window model | 25.85% | 14.94% |
| V2 feature structure refitted on training only | 24.61% | 17.14% |
| Uncovered time, without per-gap overhead | 12.63% | 5.62% |
| Uncovered time after one-second gap allowance | **8.57%** | **3.10%** |

Feature family and regularization were selected using calibration results on
both partitions. Richer body features and a gap-count coefficient did not beat
the simpler gap-excess model. The samples are small; these percentages cannot
be interpreted as established chat token accuracy. A response lacking any
required native clock retains v2's observed-window, start-only, or body/prior
route. Estimated execution spans never create observed gap coverage.

## Visible thinking is a separate regime

The export has 159 thinking blocks with explicit `thinking_hidden=false` and
real thinking text. A summary is not that text. Missing flags, hidden blocks,
and summary-only blocks cannot trigger this contribution.

The app counts visible thinking characters before clipping and compares its
existing thinking estimate with a text-derived contribution at the published
0.379637 tokens per character. The expected contribution is their maximum,
not their sum. It retains native duration, local-rate provenance and broad
sensitivity; it can increase the token estimate without inventing extra seconds.
`visible_thinking_characters` and `visible_thinking_adjusted` expose the decision.
This is an **estimated contribution floor**, not a mathematical tokenizer bound.

A fitted thinking-text coefficient around 0.81 tokens/character improved one
coding split but worsened the forward-time split. It was rejected. The smaller
published text conversion was frozen before a new coding-family confirmation.

That confirmation reserved 200 previously unused family components, excluding
every previous study and v2's independent reserve. Components link root/helper
conversations and same-account duplicate native API responses. Hash selection
uses counts and positive-label eligibility, not target magnitudes. Of 11,246
examined requests, 4,476 real-text thinking requests across 99 families qualified.
All qualifying requests lacked usable native thinking timers, so the benchmark
compares the explicit unclocked-block fallback against that fallback plus the
conservative text contribution. No coefficients were fitted on these targets.

Thinking-token WAPE fell from **94.49% to 75.82%**. Aggregate underprediction fell
from 25.68% to 7.02%. The contribution changed 677 requests; family-bootstrap
95% intervals for the WAPE change are approximately −22.10 to −15.71 percentage
points. This verifies a clockless coding transfer proxy, not real chat tokens or
the effect on responses whose thinking clocks are already known.

## Why text throughput does not replace the thinking rate

Visible thinking streams at a different, variable rate from ordinary text.
Among 151 paired thinking/text observations, the median thinking/text character
rate ratio is 0.609, with p10–p90 of 0.265–1.304. A single direct substitution
of text throughput would hide this variation and assume equal tokenization.

In a separate masked thinking-duration diagnostic, fitting the thinking/text
ratio reduced error to about 31.7%/30.2%, compared with 43.2%/47.0% for a global
thinking-characters-to-seconds fit and 166.2%/108.1% for the old unclocked block
fallback. It is a useful duration signal. However, these visible-regime duration
tests do not establish hidden thinking-token throughput. V3 therefore keeps the
published/local coding token-rate conversion and does not infer a model or speed
from a stream-rate fingerprint.

## Versioning and verification

The inference hierarchy is now: native thinking intervals and explicit traces;
otherwise fully observed gaps; otherwise v2's native observed-tool window;
otherwise its start-only window; otherwise its supported body model or published
prior. The visible-thinking contribution is applied once to explicit visible
thinking. Reported usage, API costs and measured accounting remain separate.

Manifest schema/version and digest invalidate old local calibration. New scalar
Python/Go parity cases cover the gap equation. Go tests cover overlap, missing
clocks, explicit visibility flags, Unicode, async calls, unsupported operations,
recorded zero execution, conflicting endpoints and reader clipping. The reusable
fitter accepts `observed_gaps` only with its required native gap feature; it still
cannot access transcripts or publish assumptions automatically.

WAPE is total absolute error divided by total target. The different tables have
different targets and populations. None replaces v2's earlier combined-output
masked-coding benchmark with a new all-chat accuracy claim.

Final checks passed: focused estimator/reader tests, `go test -short ./...`,
`go test ./...`, `npm --prefix web run check`, the full JavaScript unit matrix,
both Claude export research browser widths, the fitter unit tests and
`git diff --check`. Each complete JavaScript unit run passed 1,779 tests with
three skips. No production records were written or reindexed.
