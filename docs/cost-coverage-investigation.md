# Unresolved model identity and cost coverage

## Result

The November 10, 2026 investigation found no evidence-supported new price or
model allocation in the five cohorts below. They retain **84,029,462 unpriced
tokens**. No pricing entries, aliases, parser behavior, attribution versions or
stored usage were changed. Newly supported API-equivalent cost is $0 from new
pricing and $0 from new attribution; the unknown cost is still unknown.

The selected Euclid library had 59,370 agent sessions and 88,620 usage buckets,
two more of each than the assignment's earlier snapshot. All five cohort token
totals reproduced exactly. These counts include stored source/mirror records;
they are not a deduplicated provider bill.
Agent-session totals and ledger totals both sum to 224,295,669,495 tokens.

| Source / recorded model | Conversations | Usage buckets | Unpriced tokens | Classification |
| --- | ---: | ---: | ---: | --- |
| Codex / `codex-auto-review` | 21 | 54 | 44,065,335 | Recorded reviewer identity; concrete API model/rate mapping unestablished |
| TL1 / `Unknown model` | 20 | 20 | 18,227,097 | Model absent from retained usage, transcripts and available attempt metadata |
| Conductor / `<synthetic>` | 20 | 41 | 14,577,591 | Ambiguous run/task remainder; insufficient per-model consumption evidence |
| Cursor / `Unknown model` | 96 | 106 | 7,158,633 | Model absent from available composer/bubble metadata; three incomplete histories |
| OpenCode / `Unknown model` | 1 | 1 | 806 | Session remainder without a recorded session model |

No supported mapping, recoverable discarded model metadata, or demonstrated
pricing-history omission was found in these candidates. This is a finding about
the inspected records and sources, not proof that every future gap is
unrecoverable.

## Evidence and limits

### Codex reviewer identity

All 21 conversations retain `codex-auto-review` on their messages. Recorded
Codex versions range from 0.163.0 through 0.167.0. Their 54 request buckets span
October 23 through November 9 UTC. The assignment's exemplar retains the two
294,051- and 265,066-token buckets and categorized response usage, including
response/turn/thread identities. Neither those usage records nor the retained
model settings establish a priced API sibling.

The original files of all 21 conversations are unavailable at their recorded
paths, and no matching file entries were found in the two available hosts'
current Codex capture manifests. Retained catalog messages are therefore the
available evidence for this cohort; no native-file reparse was possible.

[OpenAI's Auto-review documentation](https://learn.chatgpt.com/docs/sandboxing/auto-review)
describes a separate reviewer agent. The official
[Codex changelog](https://learn.chatgpt.com/docs/changelog?date=2026-09-10&index=2)
also records reviewer model metadata and override changes. This establishes a
reviewer role, but does not establish the concrete model behind these historical
requests. The checked [API pricing page](https://developers.openai.com/api/docs/pricing)
does not supply a dated price or alias for `codex-auto-review`.

Inference: the recorded label cannot safely be treated as the parent's model,
as a similarly named API model, or as a stable alias across client versions.
Whether its backing model stayed constant is unresolved. A future mapping needs
provider evidence that identifies the executed model or its rate and effective
interval; a current default alone cannot price historical review calls.

### TL1

All 20 conversations are Codex exec transcripts. Their retained
`turn.completed.usage` includes input, cached input, output and reasoning
counters, but no model identity. All 20 matching retained transcript captures
were read through the existing capture reader. They supply no additional model
field. Available matching attempt rows likewise have no model value; older
snapshot schemas need missing-column handling rather than treating absence as
a query failure.

The originals no longer exist at their recorded paths. An executor named
`codex`, an attempt's date, or a present-day configuration is insufficient to
identify the model used for that attempt. Keep the 18,227,097 categorized tokens
and their unknown model. No token repair or guessed alias is warranted.

### Conductor

The 41 synthetic buckets consist of 19 `run-remainder` buckets
(13,459,834 tokens) and 22 `task-remainder` buckets (1,117,757 tokens).
The retained conversations contain named request usage and original structured
SDK result envelopes. Sampled results carry aggregate categorized usage while
their `modelUsage` entries name multiple models with only `contextWindow`
values. Those entries establish possible identities, not consumption shares.
Task progress records can supply a total without input/output categories.

The existing replay leaves the cohort unresolved after subtracting retained
requests. Replacing a conversation's summary label cannot allocate this
remainder. Preserve its scope and unknown model/categories. A repair would need
reconcilable per-model category counters or exact request evidence for the
uncovered work; aggregate result cost does not provide that allocation.

### Cursor

Expanded the original three-history sample to all 96 affected conversations in
the available native capture. The existing Cursor thread reader found no
`modelConfig` and no bubble `modelInfo` in any of them. Of these histories, 93
are complete according to the reader and three have missing history/chunks.
The catalog preserves the usage categories but has no message model for this
cohort.

For the 93 complete histories this is source-absent metadata in the available
snapshot. The other three also have a source-coverage limitation: missing
chunks could have held additional evidence. Do not extrapolate this result to
all Cursor histories, or infer a model from prose, dates or token amounts.

### OpenCode

The captured session has a null session model. Its two assistant records name
`big-pickle`; those requests already retain their identities and accounting.
The remaining usage is the adapter's `session_residual`: 101 base input tokens,
384 cache-read tokens and 321 output tokens, including 314 reasoning tokens.
Reasoning is a subset of output, so the remainder is 806 tokens, not 1,120.
The ledger currently labels that row `request`; the retained
`source: session_residual` marker establishes its aggregate origin, not an
additional proved request. That scope distinction must survive any later repair.

Neither the null session setting nor the named visible requests identifies the
model of the extra session usage (for example, an unrecorded side call).
Assigning their model to that remainder would be an assumption. Preserve it
without changing the source's session/request boundary.

## Partial category coverage

The historical price book reports 1,028 partially priced buckets containing
4,731,301,367 tokens. Only **57,496,630 tokens** in those buckets are uncovered;
all are uncategorized remainders. No positive token quantity in these partial
buckets is missing a category rate. The remaining **4,673,804,737 tokens** keep
their category-based price. The full bucket total is not lost cost.

The inventory also reports 159 sessions with usage but no price and 20 with
both priced and unpriced buckets. Those are agent-session classifications, not
extra cohorts to add to the bucket totals above. Assumed prices remain separate
from confirmed prices. Reported source costs, subscription charges and API
list-price equivalents remain different measures.

## Reproduction and validation

The existing `TestUsageCostAudit` and `TestUsageCostAuditReplay` use SQLite
`mode=ro`, a read transaction and the historical `priceBook.cost` implementation,
including local usage day and dated alias intervals. They do not initialize,
migrate or repair the selected library. See
[the audit instructions](token-cost-evidence-audit.md) for their environment
variables and commands.

Before/after audit artifacts, retained/native metadata projections, reproduction
scripts and logs are private under `.context/cost-coverage/`. Native captures
were opened with `leaseCapturedVersion`, then inspected with JSONL reads, the
existing Cursor reader and read-only SQLite queries. No home-directory scan or
library repair ran. No disposable attribution replay is needed because no
attribution change is proposed.

The ordinary `-tags probe` build is blocked by obsolete `gitCheckout` and
`inferLinks` calls in the unrelated `outcome_stages_probe_test.go` (assignment
06). These audit runs used a temporary, compile-only Go overlay adapting those
calls. The unrelated probes were not executed; no tracked file was excluded or
patched to hide the blocker. Remove the overlay after assignment 06 fixes the
normal probe build.

Before/after inventories have identical coverage projections for all 8,478
findings, including the 1,207 pricing-gap sessions. Both replays classified all
1,207 as `unchanged-or-unresolved`, with identical per-session token and price
results after sorting the model buckets. Added cases in the existing pricing and
token usage tests preserve unmapped review labels, partial category costs, multiple
context-only model entries and the unknown remainder beside known requests.
Existing dated-alias and attribution-preservation tests remain applicable.
Focused tests and `go test -short ./...` passed. Pricing JSON and frontend
sources did not change.

Any later price/alias candidate must follow `pricing/README.md`: cite primary
evidence, preserve effective dates, add proposed rows, and obtain the prescribed
approval before confirmation. Attribution recovery needs a separate bounded,
idempotent disposable replay coordinated with `RepairUsageAttribution`.
