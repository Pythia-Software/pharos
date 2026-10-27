# Library labeling

`pharos labeling` prepares a library's conversation text for labeling by a large "teacher" model, offline, so the labels can later train a small local classifier. It is a deliberate command-line workflow: nothing runs when Pharos starts, captures, indexes or opens a page, and no command here writes the catalog.

This page covers preparation, planning and status. Running a labeling job and exporting training data come later.

## Commands

```sh
pharos [--config PATH] labeling prepare --dir DIR [--catalog PATH] [--fresh-days 14] \
    [--limit-conversations N | --conversation ID ...] [--replace] [--dry-run]
pharos labeling plan --dir DIR --route claude|codex --model M --effort E [--items-per-request 4] \
    [--max-request-chars 12000] [--concurrency 3] [--cohort C ...] [--split S ...] [--limit-items N]
pharos labeling status --dir DIR
```

- **prepare** reads one snapshot of the catalog and writes a dataset to `DIR`. `--catalog` names the catalog file; without it, the configured library's catalog is used, after the same drive checks other commands make. `--limit-conversations N` prepares a fixed sample of N conversations (those whose works are not mirrors, in the order of the SHA-256 of their IDs); `--conversation` prepares named ones. Either restriction is recorded, and the manifest then calls the result a restricted sample, not the library. `--dry-run` computes the same manifest and prints it as JSON without creating or writing anything. Progress (phase, counts, elapsed time) goes to standard error.
- **plan** reads only a prepared dataset and estimates a labeling run: how many requests, how many tokens, what it would cost, and how long it might take. It calls no model and writes nothing.
- **status** prints a dataset's manifest summary, or says that preparation is incomplete.

Choose a dataset directory outside the catalog folder, for example `<library>/labeling/<name>`. A dataset holds private conversation text: its files are created readable only by you (0600, directory 0700). `prepare` refuses a directory that is not empty unless `--replace` is given, and `--replace` removes only a dataset's own files. One command at a time can prepare into a directory (`DIR/.lock`).

## Reading the catalog

Preparation opens the catalog read-only (`mode=ro`, `query_only`) on one connection and does every read inside one read transaction, so everything it sees belongs to one snapshot, including the write-ahead log. It never initializes, migrates or locks the catalog for writing, and never uses `immutable=1`, so it can run beside a service that is indexing. While it reads (minutes on a large library), the service's checkpoints cannot shrink the write-ahead log past the snapshot; the log is trimmed again after preparation ends. Preparation refuses a catalog whose schema version is not the one this build writes.

Human contribution comes from the existing authorship classifier ([Human authorship](human-authorship.md)), run in memory on the same snapshot rather than read from the stored ledger, so it covers exactly the snapshotted messages without waiting for or writing that ledger. The manifest records whether the stored ledger was current, for comparison only. Identity and lineage come from the usage rules: mirrored works are counted once, and each unit's canonical conversation, root conversation, execution run and delegation depth are those the Usage pages resolve. A depth comes from the unit's agent session (its embedded session, or its conversation's main one); without one, the stored depth is used and marked `stored`, and a conversation whose parent is missing, conflicting or circular is marked unresolved.

The snapshot identity in the manifest is the start and end time, catalog path, schema version, SQLite `data_version`, the tool-ledger, authorship-input, authorship and reasoning-extraction generations, and the build (commit and executable hash).

## Units (`labeling-units-v1`)

A unit is one message. Labels are per message; sentence boundaries and byte positions are kept so later work can choose a smaller unit. Each unit belongs to one cohort:

| Cohort | Messages | Text labeled |
|---|---|---|
| `human` | user-role messages a person sent | the human pieces (below) |
| `agent_in_user_role` | user-role messages the classifier attributes to a script, headless run, other agent, TL1 or sub-agent, and user-role messages of an embedded sub-agent session | the message after its leading harness blocks |
| `agent_reply` | assistant and agent messages | the whole message |
| `delegation` | delegations and their results | the whole stored record |
| `thinking` | retained reasoning, thinking and analysis | the whole text; summaries keep `reasoning_format` |

A delegation or result whose prompt or content repeats a message of the child it started is kept, with `duplicate_of` naming that message, so the two are never counted as independent events.

Every other record is excluded and counted by reason in the manifest: messages in mirrored works, unselected branches, empty text, invalid UTF-8, tool calls and results, metadata, other roles (`system`, `developer`), and other kinds; `result` records (possible copies of the final reply); unavailable reasoning (redacted or encrypted, never inferred); thinking in conversations whose reasoning extraction is pending; a person's answer that reached the agent only as a tool result (a coverage gap, not labeled); messages the classifier gave no result (`authorship_missing`, for example one without a send time), which never fall back to the raw text; and the human-piece exclusions below. Exclusions of messages that could have been units are also kept row by row in the dataset.

## Human pieces (`human-pieces-v1`)

The classifier splits each user message into spans. Preparation gives each span a role:

- **Target** text may receive labels about the person: typed text (`estimated_typed`: what no other rule claimed, an estimate rather than proof), comments inside recognized page-feedback reports (`feedback_comment`), the person's own earlier messages sent again (`reused_own_message`, linked to the source), and one-click templates and slash commands (`reused_template`: no new words, but a real request).
- **Context** is material that helps interpret the person but is not theirs: quoted text (`quoted`, linked to the agent output it matches), likely pastes (`likely_pasted`), and attachment references. It is kept with its source but supplies no evidence.
- **Dropped** text is harness: instructions, generated report fields and question forms.

The text a teacher sees is the target pieces in order. Each run of context or dropped material between, before or after them is replaced by a marker such as `⟦excluded: quoted text, 312 chars⟧`, naming the material and its length in characters, so pieces are never joined into a sentence the person did not write. Harness blocks that open a message are dropped without a marker. Every piece keeps its exact byte range in the stored message. A JSON-encoded feedback comment is one piece over its encoded bytes, shown decoded (`json-string-v1`).

There is no word-count threshold. A message that is only `?`, `+1`, a number or an emoji is kept: a deliberate short reply is meaningful. Punctuation left over after excluding a quote or paste is excluded as `residual_punctuation`. A message with no target text is excluded as `no_target_text`, or as `action_only` when its only content was a question-form answer.

A prewritten option submitted to a question the agent asked in the same conversation is recorded as an action (`actions` table): the question, the option as offered, the message that submitted it, the byte ranges of the question and answer lines, and the tool call that asked. An unsubmitted default or a skipped question is not an answer.

## Context (`context-v1`)

Each unit carries the context the measured prompts used: `Previous user request:` and the last 1,400 characters of the previous human unit's visible text, then `Previous assistant message:` and the last 800 characters of the previous agent reply in the conversation (a part is left out when there is none), stripped, with API keys and tokens redacted, and cut to its first 2,200 characters. A prompt shows the last 600 characters of that (`context_prompt`). Characters are Unicode code points. Context only ever comes from earlier messages. The dataset stores the rendered strings and the messages they came from.

The measured experiments built the same strings from the stored authorship ledger's typed spans, joined by a fragment marker, and from native capture files. Here the previous user request is the previous human unit's visible text, markers included. The catalog keeps no final or commentary channel, so a unit's channel is `analysis` for thinking and `unspecified` otherwise. Target text is not redacted.

## Long text (`chunking-v1`)

A unit's visible text longer than 6,000 characters is split into items of at most 6,000: at the last paragraph break in the second half of the window, else the last sentence end there, else the last space, else between characters, never inside a character, emoji sequence or combining mark. Nothing is truncated: the items join to the full text, and each records its range in the visible text and the byte ranges it covers in the stored message. Long units are counted separately; merging their chunk labels is not yet validated.

## Families and splits (`splits-v1`)

Conversations that share text or execution form one family: mirrors and aliases, root and parent conversations, delegated children, headless runs and the command that launched them, and conversations the classifier found copying each other. A family whose first activity is within `--fresh-days` (14) of the snapshot is the `fresh` holdout; every other family is `train`, `dev` or `test` (80/10/10) by a hash of its ID, so the split is the same in every preparation. The manifest shows split coverage by cohort, provider, depth and length.

## The dataset

`DIR/dataset.sqlite3` holds `units` (with the original message text and its hash, visible text, context, lineage, family and split), `pieces`, `items`, `actions`, `exclusions`, `families` and `meta`. `DIR/manifest.json` is written last, atomically, and only when preparation completed: a directory without it is incomplete, and `prepare.incomplete.json` records the attempt and, when it failed or was interrupted, why. The manifest holds the scope and options, snapshot identity, contract versions (units, pieces, marker, context, chunking, splits, taxonomy `candidate-0.3` and its SHA-256), counts by cohort, provider, depth and length, exclusions by reason, reasoning coverage, authorship status, duplicates, long units, invalid UTF-8, actions, split coverage, and a statement of the population it describes.

## Planning

`plan` packs the selected items, in a fixed order, into requests of at most `--items-per-request` items whose text and context total at most `--max-request-chars`; a larger item goes alone. It then estimates tokens from measurements of 18 four-item requests at effort high on 2026-10-27: per request, about 14,300 input tokens of prompt, taxonomy, schema and CLI framing for the `claude` route (Haiku 5.5) and 16,100 for `codex` (Luna), plus the items at 3.6 characters per token; per item, about 1,006 output tokens for Haiku and 212 for Luna, reasoning included. Each constant has a flag (`--overhead-tokens`, `--output-tokens-per-item`, `--seconds-per-request`, `--chars-per-token`). The cost is priced with the price book built into Pharos for the named model on the day of the plan, at standard rates without cache or batch discounts; a model the book has no confirmed price for is shown as unpriced, never guessed. Runtime is the largest of the measured request time (17 s for Haiku, 10 s for Luna) over `--concurrency`, the input over an assumed `--tokens-per-minute` (2 million) and the output over a fifth of that, plus 25%, with a planning high of three times that. The plan prints every assumption; effort levels other than high are flagged as uncalibrated.

## Known limits

- Human pieces inherit the classifier's limits: typed text is an estimate, and prose written elsewhere and pasted without cues still counts as typed.
- Embedded sub-agent sessions are identified by the session ownership the catalog records; a sub-agent prompt stored as a user message outside such a session is treated as the classifier treats it.
- Delegation duplicates are found only through a recorded delegation-to-child link and identical text.
- Preparing even a restricted sample classifies every user message in the library, since copies and templates are found across all of them.
