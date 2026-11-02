# Risky tool-call study

An offline research prototype for the risky-tool-calls handoff. It has no service,
UI, schema, indexing, release or labeling-runner integration. It never executes
catalog commands or invokes a paid model. Python 3.9+ and its standard library
are sufficient.

```sh
python3 tools/risky_tools/study.py scan \
  --catalog '/path/to/checkpointed/backup/catalog.sqlite3' --snapshot-copy \
  --out ~/pharos-labeling/studies/NIGHTJAR/weekly \
  --start 2026-10-21 --end 2026-10-28 --timezone America/Denver

# After manually reviewing the samples:
python3 tools/risky_tools/study.py report \
  --study ~/pharos-labeling/studies/NIGHTJAR/weekly \
  --labels ~/pharos-labeling/studies/NIGHTJAR/weekly/labels.jsonl

python3 -m unittest discover -s tools -p test_risky_tools.py
```

Dates are local, start inclusive and end exclusive. Use a new, empty output
directory for each scan; re-rendering only requires `report`. All study data,
annotations and results must stay outside git. The scanner enforces that boundary,
opens the checkpointed backup with `mode=ro&immutable=1` and `query_only`, refuses
library locks and nonempty WAL/SHM sidecars, and compares source fingerprints
before publishing results. It never initializes or migrates a catalog. Outputs
are private (directory 0700, new files 0600). Retained strings have best-effort
secret redaction; they still contain private conversation content.

The snapshot must contain the tool-call ledger, split commands, workspaces,
repositories, messages and mirror-workspace table. An unavailable ledger is an
error, not a zero. Calls without a usable start time are outside the dated
population. The mirror table is used as stored; its freshness is not recomputed.
`mcp_calls` records requests to Pharos itself and is not the agent-action source.
Agent MCP tools are surveyed through `tool_calls.mcp_server`.

## Rules and interpretation

`rules.json` versions the risk taxonomy, severity and reversibility notes;
`study.py` contains the executable predicates. Both hashes are recorded. Rules
operate on existing `tool_commands` segments, not keyword searches through whole
shell calls. Quoted text and heredoc bodies remain data according to the ledger's
splitter. The prototype never expands variables, aliases, substitutions, functions,
scripts or nested interpreter code. Clipped segments are excluded and counted as
coverage gaps. Polling/wait calls that repeat earlier command executions do not
count as new executions.

Kinds cover deletion, history/working-tree rewrites, database mutations,
installation/system changes, publication/sending, credential operations, process
signals, permissions, provisioning/spending, and writes outside repositories.
Routine pushes and PR updates have their own severity. Only recognized dry-run
options and literal scratch-only deletion/write/permission targets lower severity.
A worktree is not assumed disposable merely because it is a worktree. Rebase
rules do not claim a branch is shared. Local package installs and arbitrary
migration/deployment scripts remain outside this first rule set.

Each kind counts distinct calls. A call's highest effective severity determines
its severity bucket; kinds overlap. Repository counts use the **workspace's
repository**, not a claim that every compound command stayed in that checkout.
An `ok` result is only reported success, not independently verified effects.
A failed compound call can have partial effects; its successful earlier segments
remain visible. A segment's presence does not prove a conditional branch ran.
Missing results remain unknown. A matched call is not a finding of misconduct.

Authorization defaults to `unknown`. The nearest prior selected user-role message
is supplied for manual inspection; it may contain harness or delegated text. It
never automatically grants authorization. Reviewers can mark an action explicitly
requested with the original message ID and request text. This does not lower its
risk severity, and permission for a merge is not permission for a force push.

## Outputs and evaluation

- `manifest.json`: source fingerprint, code/rule hashes, dated population,
  kind/repository/severity/outcome counts and coverage gaps.
- `survey.json`: frequent primary program/subcommand pairs, all parsed segment
  pairs and agent MCP tool identities.
- `matches.jsonl`: all candidate calls, matched segments, rules, targets, outcome
  evidence, reversibility notes and reader paths.
- `review.jsonl`: up to 300 uniformly sampled matched calls and 300 uniformly
  sampled calls of any kind. Lowest SHA-256 ranks of seed plus call ID make each
  sample reproducible and independent of database row order. Overlap appears once
  with both cohort names. `retained_input` holds the full original call input,
  which can include multiple nested calls; inspect it alongside the ledger.
- `evaluation.json`: call precision, recall among sampled positives, exact kind
  agreement, Wilson intervals, completeness and missed call IDs.
- `report.html`: self-contained, filterable weekly counts and every candidate call,
  linking to `/work/<workspace>?conversation=<id>&message=<call_message_id>`.
  Set the Pharos origin at the top of the drill-down to the running service.

Copy the sample records to `labels.jsonl` and fill `review.risk_kinds` with a
list of actual kinds (an empty list means no identifiable action in scope).
`null` remains unreviewed and is excluded from evaluation. Set
`review.authorization` to `unknown` or `explicitly_requested`; the latter requires
nonempty `authorization_evidence`. Labels must reference unique sampled call IDs.
Labels are separate from the detector's predictions. No labels are filled by the
scanner. Supplemental labels for calls outside the sample are rejected.

Precision asks whether a matched call contains any action in scope; exact kind
agreement separately catches incorrect categories. Recall uses all manually
identified positives in the random sample, including routine publication and
scratch deletion. Neither metric verifies side effects. Opaque scripts can hide
additional actions, so a retained-input audit only measures observable-action
recall. A sparse random sample can leave very wide intervals. Same-agent reviews
and samples used to refine rules are **calibration**, not independent held-out
quality evidence; Wilson intervals do not cover tuning or reviewer bias. A future
paid run needs a separate tool-free judge and explicit approval before a large run.

Read the external study's `REPORT.md` for its results, annotation provenance,
missed cases and the decision about further research. Product integration waits
for the user's review of that output.

## Subject-focused second experiment

```sh
python3 tools/risky_tools/enrich.py \
  --study ~/pharos-labeling/studies/NIGHTJAR/result \
  --out ~/pharos-labeling/studies/NIGHTJAR/subjects-v2
```

This is an enrichment experiment over the first study's candidates and known
random-audit misses, **not** a new population-wide detector. The first run reads
the same immutable snapshot, checks its fingerprint against the original study,
and caches redacted source calls outside git. Subsequent runs use that cache.
Use a new output directory to start a different cohort. It does not run a model,
execute any archived command, or inspect the present filesystem for historical
facts. A Go toolchain is needed to build a temporary stdlib-only wrapper around
the exact `internal/archive/shell_command.go`; no product API is changed.

New artifacts:

- `actions.jsonl`: action, typed targets, copy source/destination, cwd provenance,
  Git repository/ref/lease, API resource, SQL table/predicate, permission mode,
  process selector, result attribution, and explicit uncertainty.
- `episodes.jsonl`: resource + conversation associations within six hours, plus
  credential-destination/permission links and rebase-before-force-push patterns.
  Association is not causality. Single-call associations remain visible.
- `evidence.jsonl`: original reader links, first-pass hits, result excerpts,
  unhandled cases and extraction gaps.
- `summary.json`: cohort boundary, overlapping mention/action counts and source
  hashes. `sources.jsonl` retains the full redacted inputs for reproduction;
  `context.jsonl` is a ledger cache reserved for future lifecycle work, not used
  as proof of creation, cleanup or process ownership in this version.
- `briefing.html` and `annotatable.html`: six analyst-selected case studies,
  filterable target/parent/scope/role explorer, related actions, and coverage.
  `annotations.py` reuses the application's existing feedback UI. Notes are
  local to the browser origin/path; copy them before moving the report.

Literal variable bindings stay within a single invocation. Tool `workdir`,
`cd`, and `git -C` take precedence over the workspace fallback. `cd` success is
an assumption; HOME initially comes from the recorded workspace user path.
Shell substitutions, loops/functions, unknown variables and glob expansion are
not evaluated. Unresolved values and process selectors are scoped to the source
call to avoid treating a reused PID/variable as one global resource. Git refs
are qualified by the effective repository context. Path identity is lexical;
symlinks, historical repository roots/remotes and machine identity are not proven.
A temporary or `.context` path is not automatically considered disposable.

A narrow Python AST experiment recognizes literal `Path` reads/writes/chmod and
a direct SHA-256 payload, including the audited API-key-hash example. It is not
whole-program analysis: imports, monkey-patching, aliases, custom functions,
control flow and arbitrary transformations are not validated. Hashing does not
establish cryptographic suitability or absence of disclosure elsewhere.

Exact matching full commands in result `details.commands` can attribute an
invocation exit code. Otherwise outer completion/running/error is shown without
claiming target effects. No rows/files changed, secret disclosure, backup,
process ownership, cloud charge, remote audience, or authorization is inferred.
Read-only help is excluded. An API PATCH is de-escalated to PR metadata only for
recognized title/body fields; merges and generic mutations remain separate.

The briefing copy is analyst interpretation, not generated independent ground
truth. Utility still needs a human assessment of target correctness, outcome
correctness, grouping, attention priority and consequential omissions. Product
integration remains deferred.

## Deterministic investigation queue

```sh
python3 tools/risky_tools/cases.py \
  --study ~/pharos-labeling/studies/NIGHTJAR/subjects-v2 \
  --out ~/pharos-labeling/studies/NIGHTJAR/cases-v3

python3 tools/risky_tools/serve.py \
  ~/pharos-labeling/studies/NIGHTJAR/cases-v3/annotatable.html --port 56393
```

The third experiment implements automatic case selection and prompt preparation.
It does **not** run an investigator or any model. Its cards have template-written
labels, questions and reasons, with source facts filled by code. No example call
IDs, manual review labels or hand-written case narratives select the queue. The
input cohort still consists of the original candidates plus five known audit
misses; the queue is not a new detector over all 80,759 population calls.

`case_rules.json` versions the explicit triggers, two priority queues, six-hour
association window, packet byte limit and audit sizes. `cases.py` applies the
predicates to structured action records. Stronger consequence/boundary triggers
enter **investigate**; ambiguous changes enter **review**. Known help and recognized
dry runs do not create cases. Temporary location, raw PID, ordinary push and PR
metadata activity can be deferred, without being called safe or authorized.
Resource identities retain checkout/conversation context. Unresolved destinations
are scoped to a call. Sibling nonrecursive file deletions may share a parent;
recursive targets retain exact identities. Complex uncertain calls combine three
or more consequential families into one case instead of many separate alerts.

The default presentation alternates primary rule groups and orders each group
chronologically, so repeated copies do not fill the first page. This changes only
display order, not selection, and is not a risk score. The same inputs and rules
produce the same case IDs, decisions, audit samples and evidence packets.

Outputs (private, outside git):

- `cases.jsonl`: selected cases, priority, named reasons/questions, subjects,
  triggering actions, associated calls, explicit unknowns and **unrun** status.
- `decisions.jsonl`: every input action's selection/defer decision and reason;
  related evidence membership is recorded separately from triggering a case.
- `packets/case-*.json` and `.txt`: bounded evidence plus a ready-to-use read-only
  investigation prompt for every selected case. The case list is authoritative
  if reusing an output directory after rule changes; use a fresh directory when
  starting a different experiment rather than treating old packet files as cases.
- `audit.jsonl`: up to 20 reproducibly sampled calls without a selection trigger
  and 20 from the earlier population-random sample outside the subject cohort.
  Review labels remain empty. Samples diagnose filtering and upstream omissions;
  they do not establish recall without review.
- `manifest.json`: scope, counts, rules/code/input hashes, source fingerprint,
  sample sizes, display-order policy and zero investigations run.
- `investigation-output-contract.json`: example shape for a later investigator's
  disposition, evidence-linked claims, authorization evidence and unknowns. This
  is a response contract, not a JSON Schema or a validated model result.
- `queue.html` and `annotatable.html`: filterable queue, source evidence,
  context, prompts, JSON downloads, deferred audit and rule explanations.

Context reads use only the original immutable snapshot and verify its fingerprint.
Packets include nearby conversation excerpts, candidate user-role request context,
source attribution spans, and bounded exact-literal path-search leads in the cached
ledger. A ledger mention may be quoted data; it is not proof of creation, use or
cleanup. Span coordinates refer to original full text, not redacted excerpts.
Neither nearby user-role text nor missing text establishes authorization or its
absence. No present filesystem inspection or live library query occurs.

The first triggering call is included before optional material can fill the
64,000-byte formatted-JSON budget. Text fields mark clipping; omission groups
record counts and example IDs. Full action/call IDs remain in the case for later
archive lookup. The prompt requires citations, separates observations/inferences,
checks benign explanations and prohibits executing archived commands, changing
resources, contacting services or querying the live library. Copy/download actions
only prepare local material; they cannot launch an investigation or transmit it.

The one-file loopback server exposes only the report and favicon, validates Host,
has no mutation API and keeps the browser origin stable across restarts. Earlier
prototype files and their annotation storage are kept separate. Research filters
and annotations are standalone report controls, not additions to the product's
view contract, routes, dependency bundles or site map.
