# Processed input for Claude chats

November 13, 2026. Estimator `claude-usage-v5`, manifest revision `2026-11-13.1`.
This adds a processed-input estimate beside the
[generated-output estimate](claude-estimation-report.md) for imported Claude
chats without reported usage. It follows the design the
[context study](claude-context-estimation-study.md) recommended. Aggregate
results are in
[`claude-input-2026-11-13.json`](../estimation/studies/claude-input-2026-11-13.json).

## Why output alone misleads

A chat export records what was said, not what the model read. A response that
searches the web three times is four model requests, and each one reads the
whole conversation so far, including every earlier search result. Output-only
estimates leave all of that out: a first turn with three searches and 93,000
characters of results showed about 5,000 tokens, where the requests read about
79,000.

**Processed input** is the sum, over model requests, of the context each one
read. It includes repeated and cached input. It is not context size, and it is
not the number of new tokens.

## The estimate

For each response on the selected branch, in source order:

1. **Requests.** A request starts at the response's first assistant block and
   after each run of tool results. Calls with no result between them share one
   request.
2. **Context.** Each request reads everything retained before it:
   - the person's text, injected prompts and extracted attachment text;
   - earlier assistant text and tool arguments;
   - tool-result text;
   - the thinking each earlier request is estimated to have generated, and the
     published per-call output allowance.
3. **Input.** The response's input is the sum of its requests' contexts.

Visible characters convert at one published rate,
`input.context_tokens_per_character`. Thinking and the per-call allowance come
from the output estimator, so generated tokens are counted once as output and
again each time a later request reads them.

Input, output and the cache division are stored separately. Tool-result size
moves input only.

### Sensitivity range

| Bound | Rate | Hidden overhead per request | Thinking kept in context | Context cap |
| --- | --- | --- | --- | --- |
| Lower | 0.28 | none | none | 200,000 tokens |
| Expected | 0.3907 | none | all | none |
| Upper | 0.47 | 51,000 tokens | all, at its upper estimate | none |

The expected value counts visible history only. A chat's system prompt and
tool definitions are not in an export, so it is a floor on that part. The
range is sensitivity, not a confidence interval.

### Cache division

Input is divided into cache reads and cache writes as a pricing scenario under
standard prompt caching: a request reads the previous request's prompt and
writes what was added since. When more than `input.cache_ttl_seconds` (five
minutes) passed since the previous response ended, or the gap is unknown, the
whole prompt is written. No estimated token is called uncached. The division
has less support than the total and should be read as an assumption.

## Evidence

The rates come from recorded input of root Claude coding conversations: 5,843
conversations and 197,521 recorded requests, split by family 60/20/20.

- **Result rate.** Recorded input grows by 0.39 tokens per character of tool
  result (median single-result step 0.36, interquartile range 0.20 to 0.47).
  Supported model families range from 0.25 to 0.46.
- **Truncation.** Coding results past about 30,000 characters add no input,
  because those harnesses truncate them. An untruncated-size fit put the result
  rate near zero until this was separated. Chat results are counted whole.
- **Thinking retention.** Thinking from earlier user turns keeps a positive
  input coefficient near this turn's in every supported family but one.
- **Masked boundaries.** With request boundaries hidden and turns rebuilt as an
  export would show them, the fitted history model has 20.6% WAPE on held-out
  families and 16.3% on the forward-time set. Mean input per inferred request
  has 70.5%.
- **Published rule.** Replaying the simpler published rule gives 25.3% and
  16.0% once coding's hidden per-request overhead is added. Visible history
  alone understates recorded coding input by about 40%.
- **Cache.** The division rule gives a 99% read share against 97.7% recorded.

## Limits

- Chat system prompts, tool definitions, memories and images are not counted in
  the expected value. Responses with images or files without extracted text are
  flagged.
- Coding labels cannot validate chat retention, context editing or caching.
  A long chat can exceed a model's window; only the lower bound caps it.
- An export keeps one tool-result ordering. Requests are inferred, and coding
  turns show about 6% more inferred than recorded requests.
- Costs from estimates stay partial API price equivalents, not bills.
- The fit and its tests share one extract. Take a fresh extract before changing
  a coefficient.

## Storage and readers

Estimated rows in `model_requests`, `agent_session_usage` and `agent_sessions`
carry `input_tokens`, `cache_read_input_tokens` and
`cache_creation_input_tokens` with `is_estimate=1` and
`input_tokens_available=1`; `total_tokens` is input plus output. Their
provenance scope is `input_and_output`. Readers that show estimates mark them
with `*` and now show input where the row has it. Analyses of measured
requests and context still read authoritative rows only.

## Reproduction

Private scalar extracts and scripts stay under `.context/input-estimation/`:
`extract.py` reads a library copy read-only and keeps lengths and recorded
counts; `delta2.py` and `think.py` fit growth and retention; `bench2.py` and
`bench3.py` score masked-boundary turns. No transcript text, command or path
is extracted.
