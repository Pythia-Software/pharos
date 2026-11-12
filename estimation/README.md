# Published usage estimation assumptions

`assumptions.json` is the global estimation manifest bundled with Pharos, much
like `pricing/cost_changes.json`. It supplies estimates even when a library has
no coding history. It contains reviewed assumptions and descriptive study
priors, not provider-reported consumption. The authenticated
`GET /api/estimation/assumptions` endpoint exposes this exact manifest, its
digest and the estimator version. It needs no network service or local model.

Estimator `claude-output-v4.1` (manifest schema 4) estimates generated output for Claude.ai exported
responses without reported usage, and durations of completed tool calls whose
native completion clocks are missing. The reader aggregates response estimates
within its existing turns. Input/cache tokens and total turn latency remain unavailable.
For a recorded, priced model, estimated output also contributes a partial API
price equivalent; it is not a full-request cost or subscription charge.

## Shipping and backfilling existing imports

Opening the updated app adds provenance columns to the existing accounting tables and enrolls
existing Claude.ai conversations. The estimator version, manifest digest and
storage version participate in the existing Tools derivation version, so a
library with nothing new to index still schedules the refresh after an update.
Tools processing first refreshes eligible local coding rates, then backfills
chat estimates. It respects the existing background schedule; **Run now** for
Tools also runs the pass. No source reimport or forced reindex is required.

The backfill streams one conversation's retained messages through the same
estimator as the reader, before clipping. It keeps only scalar predictions,
commits one conversation at a time and resumes after interruption. Publication
checks the conversation's input revision and current calibration identity in
the writer transaction. Concurrent changes leave the conversation pending.
Completed conversations are not repeated when a pass resumes.

Predictions fill the existing `model_requests`, `agent_session_usage`, and
`agent_sessions` records with `is_estimate=true`. Missing tool durations use
`tool_calls.duration_ms` with `duration_is_estimate=true`; no end timestamp is
invented. `input_tokens_available=false` preserves missing input/cache data at
read boundaries. `usage_estimate_json` and `duration_estimate_json` retain model,
manifest, and sensitivity provenance. There are no separate estimate collections.

The existing Usage, Library and Tool Calls views consume these
rows. Estimated numbers display `*`; grouped rows retain the flag when any
contribution is estimated. Query metrics indicate when their selected population
includes estimates. Known models use ordinary historical output pricing, marked
partial; unknown models remain unpriced. Reasoning is a subset of output and is
never counted again. Sensitivity ranges are not confidence intervals, and
overlapping tool spans cannot be summed into turn latency.

Only conversations with no authoritative usage qualify. A single reported
request, hourly usage row, reported session or exported usage record excludes
the entire conversation, including a reported zero. Later source reports clear
predictions transactionally; predictions cannot become calibration labels or
preserved source usage.

Source/selection corrections, deletions and harness changes invalidate the
affected conversation. A new manifest or changed local rate groups invalidate
the applicable stored results. The tables withhold obsolete predictions until
they are rebuilt; readers continue to compute from current source evidence.
Repeated daily calibration with identical groups does not rebuild every chat.
Empty, unsupported and fully reported conversations still receive a completion
stamp, preventing endless retries. A reported zero is never replaced.

`GET /api/estimation/status` reports tracked, current and pending conversation
counts, current reported/unsupported response counts, and the active estimator
identity. A partial backfill is therefore
distinct from an empty estimate population. `usage_estimation_inputs` stores
revision and completion bookkeeping only; predictions live in canonical ledgers. Deleting
a conversation removes its derived estimates. Query-cache invalidation follows
the per-conversation publication transaction.

Before a release, run focused estimation/reader/backfill tests, the fast and full
Go suites, web checks, the full JavaScript matrix and the focused query/Claude
export browser checks. The [model evaluation report](../docs/claude-estimation-report.md)
describes the frozen coefficients; storage and scheduling do not change those
point predictions. Publishing a binary uses the normal release workflow.

The November 12 rollout passed focused upgrade/backfill/reader tests, the fast
and full Go suites, web checks, the full JavaScript matrix, URL-control checks,
and four focused browser cases covering the Claude reader and estimate table
at desktop and narrow widths. No binary was published or live catalog modified
during this source-readiness pass.

## Editing the manifest

Increment `revision` and `published_at` when changing assumptions. Keep
`version` for the JSON schema; changing field semantics requires a parser and
estimator version change. The strict Go parser rejects unknown fields,
duplicates, invalid ranges, missing profiles and missing fallback rules.

- `profiles` contains arithmetic expected values and lower/upper sensitivity
  values. Units are encoded in field names. These are not calibrated chat
  confidence intervals. Expected values must not be substituted with medians.
- `thinking_tokens_per_second` applies to the union of a response's positive
  native thinking intervals. Local calibration covers 100 ms through 600 seconds;
  positive spans outside that domain retain native time and use published rates.
  Zero-length, reversed and missing block clocks use the explicit fallback. If a thinking block lacks
  usable clocks,
  `seconds_per_summary` or `seconds_per_thinking_block` supplies a duration
  scenario. `thinking_tokens_without_trace` is a separate assumption when no
  thinking block was exported and a fitted response model cannot apply. An
  absent trace does not establish zero usage. Each connected native interval
  selects its own local duration band; partial traces never receive an extra
  whole-response prediction.
- `text_tokens_per_character` applies to Unicode character counts of assistant
  text. `argument_tokens_per_byte` applies to serialized UTF-8 tool inputs.
  `tool_call_output_tokens` adds an empirical per-call unrendered-output
  allowance, without identifying it as a specific billing item. Tool results
  and thinking summaries are not added as newly generated visible response tokens.
- `visible_thinking_tokens_per_character` supplies a conservative estimated
  contribution only for explicit `thinking_hidden=false` with real thinking
  text. Take the maximum of this contribution and the existing thinking estimate,
  never their sum. It does not alter native duration or establish a tokenizer
  lower bound. Missing/hidden/summary-only text does not qualify.
- `visible_thinking_models` supplies direct token predictions for complete,
  explicitly visible traces without usable thinking clocks. The current global
  fit uses character and block counts, within its published domain. It replaces
  the block-duration token fallback in that regime; it is not added to that
  fallback. The conservative visible-text contribution still acts as a floor.
- `visible_thinking_duration` separately predicts seconds for complete visible
  unclocked traces. Eligible same-response text clocks supply an observed
  characters/second rate; otherwise it uses seconds/character. Its support gates
  are independent of the token model. A duration prediction never changes a
  separately selected token prediction. Reject predictions beyond its training
  duration domain or a trustworthy native response window.
- `models` chooses a profile by exact model, effort and speed. Precedence is
  exact model/effort, model with wildcard effort, provider with exact effort,
  then provider fallback. Within each pair, exact speed precedes wildcard
  speed. Missing effort is not medium or high. Unknown models do not acquire
  a neighboring model's identity. Explicit wildcard rows document assumptions.
- Each model rule records its basis and notes. `coding-study` rules also
  require donor request/family counts. The first study priors use arithmetic
  means of family mean rates, restricted to groups with at least ten families;
  other explicit model/effort rows use broad engineering assumptions.
- Tool priors match exact runtime and tool name. Their `seconds` describe
  execution through result observation, including possible transport/harness
  overhead. `completion_unobservable_tools` protects async launches and user
  interaction from receiving spurious completion estimates.
- `response_models` contains nonnegative arithmetic models for complete,
  tool-bearing responses with no thinking trace. `observed_gaps` first uses the
  time uncovered by the union of fully clocked text and tool spans, after a
  per-gap overhead allowance. `observed_window` predicts
  seconds using native residual time after overlapping tool intervals;
  `window` uses native elapsed time and tool-start positions; `body` predicts
  tokens only with exact recorded model/effort/speed. `body_prior` then supplies
  an explicitly published provider-wide token fallback, even without settings.
  Resolution tries those evidence levels in that order. Window models use chat duration donors;
  body models use coding usage donors. Unavailable predictors, unsupported
  settings and out-of-domain shapes retain the explicit prior. Their lower and
  upper multipliers describe sensitivity, not coverage probabilities. Missing
  argument payloads leave argument size unavailable, so models requiring that
  feature cannot treat missing bytes as measured zero.
- `response_window` bounds clock skew and elapsed duration. Both endpoints must
  be native: sender `created_at` and final text `stop_timestamp`. Envelope gaps
  and imputed tool completions cannot masquerade as these measurements.
  `tool_categories` maps export tool aliases into the reviewed model features.
- `tool_timing` separates argument generation from post-argument execution. Native
  argument clocks take precedence. Its arithmetic seconds/character fit and
  execution priors only apply to `supported_tools`; unknown operations or missing
  bodies retain total-span priors. Execution priors are native-chat observations,
  including transport/harness overhead. Text stream rates never identify models
  or silently replace thinking throughput.

The manifest is the editable source of policy; do not add model-specific ratios
to reader code, generated frontend bundles or database migrations. To add a
model, add reviewed rules and profiles here, then run
`go test -run TestUsageEstimation ./internal/archive` and the fast Go tier.
No frontend rebuild is required for this manifest because the Go package embeds
it directly.

## Local refinement and refresh

The existing Tools maintenance stage performs a bounded calibration pass after
request evidence is current. It examines at most 50,000 recent candidate coding
requests, over a 90-day window under the initial policy. Donors require a native
Claude Code recording, model and effort, explicit speed/tier provenance,
positive reported thinking tokens, and a 100-ms to 600-second native
`thinkingDurationMs` timer. Native API response IDs deduplicate repeated copies
within an account; parent links keep helpers in their root family. Conductor
mirrors and chat estimates never become coding training labels.

Rates are fitted independently by model, effort, speed and duration band
(under 1, 3, 10, 30, 100 and up to 600 seconds). A local group needs 50 requests
and ten independent linked families. Its arithmetic family-balanced mean is
shrunk toward the published rate with twenty prior-equivalent families. Local
data can refine the expected value, but cannot narrow away published sensitivity.
The timer cohort conditions on positive thinking; it is not a zero-thinking
classifier. Tool priors currently stay published rather than transferring
incompatible coding tool implementations into chat.

The saved calibration is a small, derived document in catalog `meta`, keyed by
estimator version, manifest digest, source generation and build time. It is
refined at most once per day for ordinary coding growth. Readers retain the last
compatible snapshot until its replacement is published; an unrelated ingest
cannot flip between published and local rates. Corrected/deleted timers or
ancestry changes request an immediate refinement at the next Tools pass without
withholding the old snapshot. A generation guard prevents publishing a fit
computed across an intervening index update. Old-manifest, expired or malformed
calibrations fall back to bundled assumptions. Optional calibration failure is
reported by `calibration_error` in estimation status and does not fail Tools or
block backfill with compatible/published assumptions. Recent donors use a
partial timestamp index rather than a library-wide sort. Local refinements do
not modify or publish the global manifest.

Reader inference extracts scalar features before source-body clipping and
computes results from current preserved evidence. It retains no second transcript
copy and never scans global donors on a page request. Source/branch/settings
corrections therefore take effect on the next conversation read. Cached chat
model selections remain observations; they are not automatically assigned to
historical turns. Explicit model/effort in an exported message can select an
exact manifest/local group.

Older indexed native blocks can supply estimates without a message envelope
or a reindex. Those results use `evidence_scope: retained_content_blocks` and
leave unavailable settings unreported. Explicit adapter-owned usage records
still take precedence, including a reported zero. Newer preserved envelopes
use `evidence_scope: native_message_envelope`.

## Result contract

Conversation responses expose sensitivity diagnostics in `usage_estimates`.
Stored output predictions also enter ordinary `token_usage` and per-message
`usage`, with `token_usage_is_estimate` / `usage_is_estimate` booleans.
It includes estimator/manifest versions, sensitivity bounds, basis, fallback
level, model/effort/speed provenance, observed/missing thinking-block counts,
and any local support/weight. Per-tool `duration_estimate` distinguishes
`observed`, `estimated` and `unavailable`. Only the native call and paired result
clocks establish an observed span; old catalog envelope timestamps and a tool
argument block's stop do not.

Responses also expose `response_model`, its unit and donor support, when one
applies. `thinking_duration_model` identifies an independently selected visible
duration model. `response_window` reports native elapsed seconds, start coverage and
observed completion coverage. `tool_covered_seconds` and `residual_seconds` are
omitted unless all tool spans are observed. Check `thinking_seconds_available`:
a body/token prediction does not establish a duration even though the numeric
placeholder remains zero. Missing duration is not measured zero.

When all text spans are also native, window evidence adds
`nonthinking_covered_seconds`, `unexplained_seconds`, `gap_adjusted_seconds` and
`gap_count`. These are model features, not direct thinking or request counts.
Per-tool `argument_generation_seconds` and `execution_seconds` have independent
observed/estimated/unavailable status fields. `seconds` retains full call-start
to result-observation semantics. An observed total without an argument stop does
not establish a measured decomposition.

The scope is selected-branch responses in conversations without any reported
usage. A reported zero is still reported usage and is never overwritten. The
input-context rail uses measurements only; consumption totals and partial output
pricing include explicitly flagged predictions. Sensitivity bounds sum without an independence assumption and are not
presented as an 80% or 90% confidence interval. Parallel tool durations are
not added into a claimed turn elapsed time.

See [the current standalone report](../docs/claude-estimation-report.md) for v4's
complete-turn and component validation, including weak fallback regimes. See
[the native-signals study](../docs/claude-native-signals-study.md) and
[the v2 deployment selection](../docs/claude-estimator-deployment.md) for
the evidence hierarchy, independent masked-coding evaluation, native chat timing
checks and rejected factors. Historical study reports in `studies/` are evidence;
only reviewed coefficients in `assumptions.json` are runtime policy. Position
and accumulated context/result volume are not output-token multipliers.

## Fitting future response models

`tools/fit_usage_estimation.py` needs Python 3, NumPy and SciPy. It reads an
explicit scalar-only version-1 dataset; it never accesses a catalog. Keep all
root/helper and same-account duplicate-API-connected records in one family
before assigning `train`, `calibration` and `test` splits. Use opaque private
family labels; keep the dataset outside tracked files. For example, one row is:

```json
{"family":"private-family-1","split":"train","target":1200,
 "baseline":400,"features":{"tools":2,"visible_kchars":0.8,
 "argument_kbytes":0.12,"tool_read":1,"tool_web":1}}
```

The input wrapper is `{"version":1,"rows":[...]}`. Targets and features must be
finite nonnegative numbers. Response models require `tools` and `visible_kchars`
for domain bounds, even when they have no coefficient. All selected features must
be present; unavailable is not zero. Window rows also need `elapsed_minutes`
and native thinking-second targets within the same window. Only scalar feature
names from the Go feature contract are accepted; raw text and identifiers other
than the private family key are rejected. Include zero-thinking targets where
reported; do not turn unknown labels into zero.

Example candidate fit (choose features and regularization with calibration data,
then freeze before inspecting test results):

```sh
python3 tools/fit_usage_estimation.py \
  --input .context/scalar-rows.json \
  --output .context/candidate.json --report .context/candidate-report.json \
  --id reviewed-body-candidate --features visible_kchars,argument_kbytes,tools,tool_read \
  --unit tokens --evidence body --basis coding-study \
  --model claude-opus-5-5 --effort high --speed standard --alpha 1
```

The fitter balances families, uses nonnegative ridge regression and requires at
least 20 training responses from five families. Optional `--prior-model` supplies
a compatible reviewed parent candidate; its penalty and shrinkage apply before
export. Optional `--calibrate-mean` uses only calibration targets for a bounded
mean correction. `--refit-calibration` refits on train plus calibration with that
correction fixed. The report evaluates the final exported coefficients on test
rows; test labels never affect the candidate. Calibration metrics are labeled
as pre-refit and cannot serve as independent test evidence after refitting.

For timing fits, use `--unit seconds --evidence window` (or `observed_window`
or `observed_gaps`, which requires `gap_excess_minutes` from complete native
text/tool clock coverage)
and `--basis chat-duration-study`. `--evidence body` token fits require exact
normalized model/effort/speed selectors; `--evidence body_prior` permits explicit
wildcards for the provider fallback. For a complete visible/unclocked token fit,
use `--evidence visible_unclocked --unit tokens --basis coding-study` and exactly
`--features thinking_kchars,thinking_blocks`. Those rows require positive thinking
characters and an integral positive block count instead of tool/prose features.
This fit has a per-block term and no free intercept; it exports a
`visible_thinking_models` entry rather than a `response_models` entry. Review runtime/domain compatibility, family and
forward-time performance, signed bias, support and sensitivity before copying a
candidate into the manifest. Reject unsupported shape or transfer regimes even
when a fit looks numerically strong. Re-run the scalar parity fixtures and Go
tests, bump the manifest revision, and retain an aggregate study report. This
tool does not replace the broader candidate-selection or independent-reserve
protocol and never updates the app's assumptions automatically.

## Replaying the production estimator

The opt-in `TestUsageEstimationScalarReplay` probe reconstructs export-shaped
blocks from scalar feature counts and calls the production Go observation,
routing and estimation path. It opens no catalog. Its strict JSONL input allows
evidence only; keep target labels in a separate private file. Input rows specify
an opaque case `id`, an absolute `manifest` path, optional recorded
`model`/`effort`/`speed` and `speed_recorded`, optional `sender_created`, and
`blocks`. Block fields are `kind`, `call`, `tool`, `start`, `stop`, `characters`,
`thinking_hidden`, `summaries`, `argument_bytes`, `argument_characters`,
`input_known` and `delegation`. Clocks are native timestamps only. A known
argument includes its JSON serialization overhead in the scalar sizes.

```sh
PHAROS_ESTIMATION_REPLAY_INPUT="$PWD/.context/cases.jsonl" \
PHAROS_ESTIMATION_REPLAY_OUTPUT="$PWD/.context/predictions.jsonl" \
go test -tags probe -run '^TestUsageEstimationScalarReplay$' ./internal/archive
```

Do not manufacture future tool results to complete an individual API fragment,
or fabricate text-stream clocks from coding event timestamps. Only complete
turns can validate a complete-response body model. Group duplicates and helpers
before creating splits, freeze coefficients before extracting fresh targets,
and publish aggregate error/support without private identities. The current
[aggregate replay results](studies/claude-shipping-2026-11-12.json) bind to the
frozen manifest digest and distinguish fresh coding validation from previously
examined native-chat timing holdouts.


## Runtime v4.1 review corrections

The fitted coefficients and manifest digest remain those of v4. Runtime fixes
preserve positive native thinking spans below 100 ms and above 600 s, using
published rates outside the local calibration domain. A native observation span
is not proof of uninterrupted generation. Zero/reversed clocks still use missing
clock assumptions. Visible thinking raises the sensitivity lower bound; missing
blocks select duration bands individually; repeated tool IDs contribute argument
output once; response anchors select the earliest known source order with a
stable ID tie-break. These edge-case fixes are covered by runtime tests; the
historical v4 study tables have not been relabeled as a new v4.1 validation run.

## Consumption and measured-analysis readers

Predictions share the ordinary accounting ledgers, with explicit provenance.
The estimate storage version is `chat-estimates-v4-response-dates`; upgrading
enrolls existing imported chats for a resumable refresh. The independent
`usage_estimation_derivation_version` marker prevents an unrelated Tools
derivation update from deleting estimates. When the estimator identity changes,
clearing predictions also increments their input revisions so they rebuild.

A response's `usage_at` is independent of its stable message anchor. Accounting
uses the native response `created_at` when valid, otherwise the earliest dated
retained assistant event in that response. An unclocked first tool therefore
cannot erase the date of a later reply. If no response date survives, storage
retains its existing anchor/conversation-start fallback. These accounting dates
do not supply missing generation or tool-execution clocks. The storage upgrade
repairs existing estimate dates through ordinary Tools backfill without reimport.

| Reader | Policy |
| --- | --- |
| Usage, Library, transcript reader, token dashboards and previews | Include predictions, mark affected values with `*`, preserve missing input/cache and partial output cost. Count-only groups need no estimate reduction. |
| Home/Quick Stats, recent conversations, menu bar and pricing health | Include estimates with window-specific provenance; costs remain partial. Unpriced amounts do not become zero. |
| MCP metrics | `is_estimate` travels with values, evidence, row details and aggregates. Default `complete` quality excludes partial predictions; `available` includes qualified values. Entirely missing input/cache is unavailable, and a mixed sum is partial. |
| Model Requests/thinking, Session Shape/context, Findings, TL1, instruction/Ask signals, delegation/execution accounting and usage reconciliation | Use authoritative rows only. One estimated response can contain several actual model requests, so it cannot count as one measured request or supply measured context. |
| Model/effort repair, speed fitting and attribution replay | Never use predicted tokens as recorded evidence or calibration labels. |
| Palette discovery, repricing, raw Workspace payloads and library transfer | Preserve predictions and their flags. These storage/discovery paths do not relabel them as recorded measurements. |

`UsageHistory` counts conversation activity rather than token consumption and
does not need a usage-estimate flag. Canonical token columns remain NOT NULL for
compatibility with accounting writers. Their zero placeholders are not measured
input: raw consumers must honor `input_tokens_available`; public usage readers
expose unavailable input/cache as null. This review did not replace those columns
with nullable storage or measure production-size upgrade/query throughput.
