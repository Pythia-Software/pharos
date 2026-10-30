> Sanitized copy of ~/pharos-labeling/research/tracks/F-audit/REPORT.md (archived in full on euclid at "Pharos Labeling/2026-10-28-student-research"). Verbatim conversation text was removed.

# Track F: qualitative error audit of the best student (`c-e32-512-all`) against y3

2026-10-28, track F. One annotator (Claude) judged by hand every label the student and y3 disagree on, in 73 items.
Predictions: `tracks/C-finetune/preds/c-e32-512-all.npz`, using `rlh.apply_thresholds` with the saved dev-tuned
thresholds (these include closure). Targets come from `rlh.target_matrix`. Teacher evidence quotes come from
`item_results` (read-only). Scripts: `scripts/sample.py` (sampling, seed 20261028), `render.py`, `tally.py`,
`credit.py`, `credit_llm.py`, `drops.py`. Per-label verdicts with notes: `out/verdicts.txt`, `out/judged.json`.
Nothing was tuned on any of it.

## Answer
- **The student's disagreements with y3 split cleanly by direction.**
  - **When y3 has a label the student lacks, the student is wrong. y3 was right in 74 of 74 judged decisions.**
    All 74 were unanimous 3-of-3 teacher labels, clearly supported by the text (95% CI for the rate ≥ 0.95).
    These are real model errors.
  - **When the student has a label y3 lacks, the student is usually right.** Of 38 decisions: student correct 27,
    both defensible 10, y3 correct 1. Here y3 is too strict: a teacher left the label out, hedged at a parent,
    returned nothing, or gave a quote that failed evidence validation.
- **Only about 9% of disputes are real taxonomy ambiguity** (10 of 112 decisions). They sit at two boundaries:
  intent / plan / progress, and investigate / information.
- **Error volume:** misses (FN) are the majority of the student's errors. Over all of gold3_test the student has
  1,115 FN and 1,232 FP; in fresh AIUR it has 299 FN and 169 FP. Most FPs are not errors (see above).
- **Business implication.** The two directions need different fixes:
  - **The FP side needs a cheap label-construction fix, with no modelling and no spend.** Score against an
    abstain-aware target, or credit 2-of-3 labels (below), and fix quote validation.
  - **The FN side needs modelling effort.** The work is leaf thresholds, longer input and context for long
    prompts and short replies, and above all fresh-template adaptation for AIUR.
  - **Taxonomy clarification is worth doing for two boundaries, but it is not the main lever.**
- **Calibration warning for the "student matches Haiku" claim.** Give every predictor credit for its labels that
  one of the other two teachers also asserted. Under that credit the LLMs gain more than the student (table
  below), because the LLMs' extra labels are mostly defensible. On gold3_test the student then trails each LLM by
  0.03–0.05, and in fresh AIUR by 0.06–0.14.

## 1. Tally

Sample A: 48 items from gold3_dev where student ≠ y3, 12 per cohort. Sample B: 25 items from the fresh AIUR cell of
gold3_test where student ≠ y3 (170 of the cell's 250 items disagree).

Verdict codes: **S** = student correct, **Y** = y3 correct, **B** = both defensible (ambiguous boundary),
**W** = both wrong (never assigned). There are two counting levels:
- **Decision level** counts one judgment per label I decided. When a leaf is missed, its missed parents are not
  counted again.
- **Closure level** counts every disputed label, ancestors included, the way Jaccard counts them.

| slice | decisions | S | Y | B | closure-level labels (S / Y / B) |
|---|---|---|---|---|---|
| **all** | 112 | 27 | 75 | 10 | 170 (48 / 108 / 14) |
| y3 has it, student lacks it (FN vs y3) | 74 | 0 | **74** | 0 | 107 (0 / 107 / 0) |
| student has it, y3 lacks it (FP vs y3) | 38 | **27** | 1 | 10 | 63 (48 / 1 / 14) |
| sample A (gold3_dev) | 65 | 19 | 39 | 7 | 102 (34 / 57 / 11) |
|  · agent_reply | 15 | 4 | 8 | 3 | 20 (7 / 9 / 4) |
|  · human | 19 | 0 | 16 | 3 | 32 (0 / 27 / 5) |
|  · agent_in_user_role | 15 | 7 | 7 | 1 | 20 (11 / 7 / 2) |
|  · delegation | 16 | 8 | 8 | 0 | 30 (16 / 14 / 0) |
| sample B (fresh AIUR, gold3_test) | 47 | 8 | 36 | 3 | 68 (14 / 51 / 3) |

How the verdict depends on the number of teachers that asserted the disputed label (decision level):

| teacher votes | S | Y | B |
|---|---|---|---|
| 3 (y3 has it) | 0 | 74 | 0 |
| 2 | 21 | 0 | 4 |
| 1 | 6 | 0 | 5 |
| 0 | 0 | 1 | 1 |

Most-disputed labels (decision level):

| label | n | S | Y | B | note |
|---|---|---|---|---|---|
| P.request.work.implement | 16 | 4 | 11 | 1 | Misses are mostly just under the 0.62 threshold on fresh templates. Correct FPs are where Haiku hedged at P.request.work. |
| P.frame.scope | 14 | 1 | 13 | 0 | Almost always a real miss: scope sentences inside long prompts. |
| P.frame.constraint | 11 | 5 | 6 | 0 | |
| P.frame.criteria | 11 | 1 | 9 | 1 | Rating rubrics and "must produce X" sentences are missed. |
| C.report.finding | 10 | 6 | 4 | 0 | FPs are mostly sub-agent results where Haiku returned nothing. |
| P.request.work.revise | 8 | 0 | 8 | 0 | TL1 bucket-fixing and repair prompts [prompt wording omitted], and relayed review feedback. |
| P.request.work.verify | 6 | 3 | 3 | 0 | |
| C.report.progress | 5 | 0 | 3 | 2 | intent / progress boundary |
| P.request.work.investigate, P.request.work, P.request.work.review | 4 each | | | | |

Error-type tally (decision level):

| type | n | S | Y | B |
|---|---|---|---|---|
| y3 too strict (a teacher omitted, hedged, returned nothing, or had its quote rejected) | 26 | 25 | 0 | 1 |
| plain miss of a clearly stated function | 33 | 0 | 33 | 0 |
| near-threshold miss on a repeated fresh template (p within ~0.15 of threshold) | 12 | 0 | 12 | 0 |
| long-chunk dilution (instruction buried after code, JSON or a quoted report) | 9 | 0 | 9 | 0 |
| space-stripped orchestrator prompts | 6 | 0 | 6 | 0 |
| missing context dependency (answer / option to a prior question) | 4 | 0 | 4 | 0 |
| broad parent vs leaf (teachers split leaves, so y3 keeps the parent; student predicts nothing) | 4 | 0 | 4 | 0 |
| relayed review feedback treated as inert | 3 | 0 | 3 | 0 |
| intent vs plan vs progress | 6 | 0 | 3 | 3 |
| investigate vs information | 3 | 0 | 0 | 3 |
| other sibling / frame boundary, acknowledgment-as-accept, over-prediction | 6 | 2 | 1 | 3 |

How the 25 "y3 too strict" decisions break down:
- 8: one teacher simply left the label out.
- 6: a teacher's quote was rejected (`not_found`), because it contained JSON-escaped `\n` or `\"`, or markdown.
- 5: Haiku (once Flash) returned the parent plus an uncertain child.
- 5: Haiku returned an empty set, mostly with need_context.
- 1: Flash read a delegation prompt as the sender's own plan.

**Confidence.** 71 decisions high, 40 medium, 1 low. There is a known anchoring risk: I saw the teachers' labels and
quotes before judging. That makes agreeing with a unanimous trio easy. Still, every one of the 74 FN verdicts rests
on an explicit sentence I could point to. I did not look for labels that neither side had, so "both wrong" is
under-sampled, not shown to be absent.

**Corpus-level check of the y3-strictness finding (all of gold3_dev + gold3_test).**
- **2-vote labels:** there are 3,668, against 12,727 y3 positives. Haiku is the dissenting teacher in 46% of them.
- **Abstention:** in 34% of 2-vote labels, the dissenting teacher abstained rather than rejected: the label was
  masked (uncertain, unsupported, or a descendant of a returned parent).
- **Among the student's FPs vs y3, the share that two teachers asserted** is 72% on dev and 64% on test. By label:
  C.deliberation.plan 0.93, C.report 0.89, P.frame.scope 0.77, investigate 0.77, C.report.finding 0.68.
  C.report.verification is the lowest at 0.46.

**Credited Jaccard (`scripts/credit_llm.py`).** Each predictor is scored against the other two teachers'
intersection, plus credit for any of its labels that at least one of those two asserted. The same rule is applied to
LLMs and the student.

| set | Haiku: LOO → credited | student vs F∩A: → credited | Flash: LOO → credited | student vs H∩A | Argon: LOO → credited | student vs H∩F |
|---|---|---|---|---|---|---|
| gold3_test | 0.757 → 0.862 | 0.757 → 0.819 | 0.742 → 0.866 | 0.773 → 0.838 | 0.728 → 0.884 | 0.770 → 0.854 |
| fresh AIUR (n=250) | 0.674 → 0.793 | 0.598 → 0.649 | 0.634 → 0.824 | 0.655 → 0.718 | 0.598 → 0.818 | 0.668 → 0.756 |

The student vs y3 on gold3_test is 0.774. With its 2-vote labels credited it is **0.861 raw / 0.877 weighted**.
Scoring against an abstain-aware intersection, with no retraining, gives 0.788.

## 2. Top error patterns (examples truncated)

**P1. Student misses explicit frames and leaves on unanimous labels (real errors; 33 plain + 12 near-threshold).**
The student often predicts the parent but not the leaf.
- dev human: [example: a question asking whether changes are also needed in another project, and if so requesting
  a handoff; text omitted]. The student gave P.request.input but missed .information and .work.implement.
- dev delegation: [example: a delegation stating a success criterion that a generator script must reproduce a
  reference output; text omitted]. P.frame.criteria
  p=0.12, missed. Every teacher has it.

**P2. Short human replies whose function depends on the previous message (context dependency).**
- [example: a short human reply choosing to pause; text omitted] The prior message offered [a choice between
  continuing with follow-up work and pausing; text omitted]. The student predicted only C: P.control.pause had p=0.00 and C.input.option p=0.07.
- [example: a human reply answering an agent's design question; text omitted]. C.input.answer
  p=0.12.

**P3. Long-chunk dilution: the instruction comes after code, JSON or a quoted report.** The 512-token window with a
70/30 head/tail split loses it.
- fresh AIUR chunk 2/4: about 5k characters of retrieval-context JSON [template heading omitted] and a Python build recipe,
  then [a one-sentence repair instruction with a no-tools constraint; text omitted]. The student predicted nothing:
  revise p=0.02, P p=0.11.
- dev delegation chunk 1/2: a sub-agent's tool-result JSON ending in [a takeaways section reporting a coding finding; text omitted].
  C.report.finding p=0.05.

**P4. y3 too strict: the student is right and one teacher dropped the label (25 decisions).**
- dev AIUR, a workbook-description template [template name omitted] (3 of 12 sampled AIUR items): [example: a prompt
  asking for a one-line description in a fixed JSON output format; text omitted]. Flash and Argon gave implement. Haiku gave P.request.work with implement uncertain,
  so y3 stops at the parent. The student's implement is right.
- fresh AIUR, the TL1 review template: [example: a prompt asking for a review of a candidate's change against a
  review-guidelines file; text and path omitted]. Flash's review and constraint quotes were rejected as
  `not_found` because they contained an escaped `\n`. The student's review and constraint are right.
- dev delegation: [example: a delegation asking a sub-agent to search a codebase and list what it finds; text omitted]. Flash labeled it as the sender's own
  C.deliberation.plan, so y3 is empty. The student's investigate is right.

**P5. Relayed material in AIUR: the student treats it as inert.** In this student, the expected failure mode
("quoted material read as the author's act") hardly appears. The opposite does. In TL1 resume and handoff prompts,
the orchestrator relays reviewer findings under a feedback heading [heading text omitted] and asks for them to be fixed. All three teachers
read this as P.request.work.revise or P.frame.constraint. The student predicts nothing.
- [example: a resume prompt relaying a numbered reviewer finding, followed by headless-operation constraints; text
  omitted] The student gave an empty set.
  y3 = P.frame.constraint (plus parents).

**P6. Genuinely ambiguous boundaries (10 decisions, both defensible).**
- intent / plan / progress:
  - [example: an agent announcing what it will explore next; text omitted]: Haiku and Flash gave plan, Argon gave intent.
  - [example: an agent's gerund status line about updating a test and adding tests; text omitted]: Haiku and Flash gave intent, Argon gave
    progress.
  - [example: an agent's present-progressive status line about checking configuration and tests; text omitted]: plan vs progress.
- investigate / information: [example: a human yes/no question asking whether a design alternative could avoid
  polling; text omitted] (Haiku and Argon gave information, Flash investigate). The taxonomy's own boundary note says
  "keep the broad parent".
- acknowledgment as accept: [example: a short acknowledgment approving a delivered change; text omitted]

## 3. Fresh AIUR cell (sample B): what the student gets wrong on the new automation templates

Of 47 decisions, 36 are real student misses, 8 are student-correct FPs, and 3 are ambiguous. The cell has 299 FN vs
169 FP, and 20% of its FNs are within 0.10 of the label's threshold. By template:
- **Repeated templates just under threshold (12 decisions).** The student sees the right function but is
  under-confident on templates it never trained on. Probabilities wobble around the threshold across copies of one
  template:
  - A design-generation prompt [template name and text omitted]: implement
    p=0.50 / 0.61 / 0.61 vs threshold 0.62 on three copies.
  - A visual-review prompt [rubric text omitted]: review hit on one copy, but p=0.19 on another.
    scope and criteria are 0.29–0.34 vs thresholds 0.34 / 0.38.
  - A TL1 bucket-repair prompt [opening text omitted]: revise hit on one copy, p=0.27 on another.
- **Instructions buried in long chunks (6).** Repair and revision prompts put the instruction after thousands of
  characters of code, diagnostics JSON or a quoted report. See P3. The student gives P=0 or only constraint.
- **Space-stripped orchestrator prompts (6).** Two examples were given [openings of two space-stripped prompts; text omitted].
  The tokenizer sees few words, and
  revise / scope / constraint / criteria are all missed.
- **Relayed review feedback (3).** TL1 resume and candidate-context blocks; see P5.
- **Reports sent in the user role (2).** A relayed TL1 verify report [heading text omitted] and a handoff analysis. y3 gives C.report or C.feedback. The student predicts nothing in C, because train
  AIUR is almost all P.
- **Student FPs here are mostly right (8 of 11).** Flash and Haiku omitted labels: [a no-tools instruction; text omitted] is a
  constraint, [a request to verify a candidate; text omitted] is a verify request, and [a sentence reporting a parser
  run; text omitted] is a finding.

So the fresh-AIUR gap is not ambiguity. It is template novelty, and it shows up as under-confidence and truncation.
That matches track C's finding that fresh adaptation (~13k Flash labels, about $19) lifts this cell from LOO-FA 0.637
to 0.720.

## 4. Recommendations

**Taxonomy boundaries to tighten** (each one decides about 1–3% of disputes, but all three teachers would apply it
the same way):
1. **intent / plan / progress.** Use tense and aspect. "I'll / I'm going to / let me X", said mid-task, is
   C.deliberation.plan. C.report.intent is only for taking up the received task at its start. A present
   progressive with "now" ("I'm checking X now", or a gerund status line [corpus example omitted]) is C.report.progress. An alternative is
   to score intent and plan as one label.
2. **investigate / information.** A yes/no or "how would we…" question to an agent is
   P.request.input.information, unless it asks for discovery work ("dig into", "find out", "trace").
   Otherwise accept the parent.
3. **Relayed material in user-role automation.** Say explicitly what an orchestrator does when it relays reviewer
   findings [relay template text omitted]: it makes a P.request.work.revise. Say also that
   a relayed verify report or analysis is labeled C by its function, whoever the sender is.
4. **Verification instructions.** "Verify before reporting: X passes" is both P.request.work.verify and
   P.frame.criteria.
5. **Acknowledgments.** A bare "Great" is C.feedback.accept only when it evaluates something the other party
   supplied.

**Label-construction fixes** (no new spend; re-derive from stored results):
1. **Fix evidence-quote validation.** Normalize JSON escapes (`\\n`, `\"`, `>`) and markdown emphasis before
   matching. Rejected quotes caused 6 of 25 "y3 too strict" cases in this sample, and Flash has unsupported labels
   on 21.5k rows.
2. **Use an abstain-aware intersection.** Treat a teacher's masked label (uncertain or unsupported, or the child of
   a returned parent) and an empty result with need_context as an abstention, not a "no". Then require ≥2 positive
   votes and no outright rejection. On gold3 this adds 1,244 positives to y3's 12,727, and the current student
   scores 0.788 vs 0.774 against it.
3. **For C.report, C.deliberation(.plan), C.report.finding, P.frame.scope and P.request.work.investigate,
   intersection-of-three is too strict.** 68–93% of the student's FPs on these labels have 2 votes, and every
   2-vote FP I judged was correct or defensible (25 of 25). Report a credited or majority metric beside y3.
   **Keep strict y3 for C.report.verification and completion.** On those labels 2-vote FPs are rarer (0.33–0.53)
   and completion is easy to over-claim.
4. **Haiku is the lone dissenter in 46% of 2-vote labels.** It hedges at parents and returns empty sets on sub-agent
   results. Down-weight its veto, or prompt it to commit to leaves.

**Model fixes** (aimed at the real errors, the FNs):
1. **Fresh-template adaptation** (track C's recipe) for AIUR: it fixes the near-threshold misses, the biggest
   fresh-AIUR group.
2. **Find the instruction in long chunks.** Give the model the last paragraph(s) after a code / JSON / quote run and
   the first ~500 characters, rather than a fixed 70/30 cut. Or use 1,024 tokens for AIUR and delegation chunks.
3. **More context for short human replies,** so the prior question or option list is visible. The pause, option and
   answer misses all depended on it.
4. **Per-cohort leaf thresholds, tuned on dev only.** The student systematically under-commits leaves and keeps
   parents. If the target changes to the abstain-aware version, retune thresholds for it, since dev thresholds tuned
   to strict y3 bias it toward misses.
5. **Low priority:** text normalization for space-stripped prompts (word segmentation or a char-aware tokenizer).
   It is a single template family.

## Open questions
- A second annotator on the 38 student-only decisions would test my anchoring. These carry the "y3 is too strict"
  claim. The 74 FN verdicts are low-risk.
- Should the business metric be y3 (strict) or credited? The parity story changes: strict says the student matches
  Haiku; credited says it trails the LLMs by 0.03–0.05 (test) and 0.06–0.14 (fresh AIUR).
