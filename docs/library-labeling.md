# Library labeling

`pharos labeling` prepares a library's conversation text for labeling by a large "teacher" model, offline, so the labels can later train a small local classifier. It is a deliberate command-line workflow: nothing runs when Pharos starts, captures, indexes or opens a page, and no command here writes the catalog.

This page covers preparation, planning and status. Running a labeling job and exporting training data come later.

## Commands

```sh
pharos [--config PATH] labeling prepare --dir DIR [--catalog PATH] [--fresh-days 14] \
    [--limit-conversations N | --conversation ID ...] [--splits-from OLD_DIR] (--snapshot-copy | --live) [--replace] [--dry-run]
pharos labeling plan --dir DIR --route claude|codex --model M --effort E [--items-per-request 4] \
    [--max-request-chars 12000] [--concurrency 3] [--cohort C ...] [--split S ...] [--limit-items N]
pharos labeling status --dir DIR
```

- **prepare** reads one snapshot of the catalog and writes a dataset to `DIR`. It needs to be told what it reads: `--catalog PATH --snapshot-copy` for a backup copy (recommended), or `--live` for a live catalog, which is the configured library's unless `--catalog` names another (after the same drive checks other commands make). Without either it refuses and says why. `--limit-conversations N` prepares a fixed sample of N conversations (those whose works are not mirrors, in the order of the SHA-256 of their IDs); `--conversation` prepares named ones. Either restriction is recorded, and the manifest then calls the result a restricted sample, not the library. `--dry-run` computes the same manifest and prints it as JSON without creating or writing anything (with a configured library it leases the library's lock file only if it already exists). `--splits-from` keeps the splits an earlier dataset gave its conversations (see below). Progress (phase, counts, elapsed time) goes to standard error.
- **plan** reads only a prepared dataset and estimates a labeling run: how many requests, how many tokens, what it would cost, and how long it might take. It calls no model and writes nothing.
- **status** prints a dataset's manifest summary, or says that preparation is incomplete.

Choose a dataset directory outside the catalog folder, for example `<library>/labeling/<name>`. A dataset holds private conversation text: its files are created readable only by you (0600, directory 0700). `prepare` refuses a directory that is not empty unless `--replace` is given, and `--replace` removes only a dataset's own files; it refuses a dataset that holds any labeling job, request or attempt records, so paid results are never lost. One command at a time can prepare into a directory (`DIR/.lock`).

## Reading the catalog

Prepare from a backup copy when you can. Pharos's own backup produces one: a checkpointed catalog file that nothing writes. Pass it with `--catalog PATH --snapshot-copy`: preparation then opens it immutable (`mode=ro&immutable=1`), so SQLite reads only the main file and creates or touches no `-wal` or `-shm` beside it. A copy with a non-empty `-wal` file is refused, since its main file is then incomplete; a stale `-shm` is ignored. `--snapshot-copy` refuses the configured library's own catalog (compared as the same file, not by name), and preparation fails, publishing nothing, if the copy's size or modification time changed while it was read. The manifest records the copy's path, size and modification time.

A live catalog is opened read-only (`mode=ro`, `query_only`) on one connection, and every read happens inside one read transaction, so everything preparation sees belongs to one snapshot, including the write-ahead log. **That read transaction holds back the service**: while it lasts (minutes on a large library), Pharos cannot checkpoint past it, so its write-ahead log keeps growing and checkpoint and maintenance work waits. Prepare from a live catalog only when nothing else is possible, and say so with `--live`. Preparation never initializes, migrates or writes a catalog, and never uses `immutable=1` on a live one.

Preparation reads catalog schemas 7 (the tool ledger, which launches and question-tool answers come from) through the version this build writes. It refuses newer and older catalogs. An older catalog lacks some tables and columns. Preparation checks which, supplies them as empty for its own queries (temporary views on its connection), and says in the manifest's `coverage` what is missing and what that costs. For example, a schema-14 catalog has no normalized reasoning store, so its thinking is excluded (`reasoning_store_absent`) and the manifest says thinking is unavailable from that snapshot. A missing feature is never reported as a zero.

Human contribution comes from the existing authorship classifier ([Human authorship](human-authorship.md)), run in memory on the same snapshot rather than read from the stored ledger, so it covers exactly the snapshotted messages without waiting for or writing that ledger. The manifest records whether the stored ledger was current, for comparison only. Identity and lineage come from the usage rules: mirrored works are counted once, and each unit's canonical conversation, root conversation, execution run and delegation depth are those the Usage pages resolve, using the headless-run launches derived on the same snapshot rather than the stored launch ledger, which can lag behind new runs. A depth comes from the unit's agent session (its embedded session, or its conversation's main one); without one, the stored depth is used and marked `stored`, and a conversation whose parent is missing, conflicting or circular is marked unresolved.

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

A delegation or result that repeats a message of the child it started is kept, with `duplicate_of` naming that message, so the two are never counted as independent events. Texts are compared with whitespace collapsed: equal to any child message, a child's first request containing the delegation's prompt, or a result containing the child's last reply. The manifest reports, for delegations and for results, how many had a recorded child and how many matched each way.

A forked conversation starts with a copy of its parent's history. Its leading records are walked alongside the parent's, in order: while each text record repeats the parent's next one (same role, same text), it is excluded as `inherited_history`, with `duplicate_of` naming the parent message, together with the records between them (tool calls and results), before any cohort is chosen. The first difference ends the copy, so a short "continue" typed after it is new. A person's message copied into a fork is never labeled again, and never as an agent's request. Authorship treats every conversation with a parent as a sub-agent; for labeling, a continuation (a Codex filename fork, whose lineage is the root's own execution) is classified as the root's, so the person's new messages there are human.

Every other record is excluded and counted by reason in the manifest: messages in mirrored works, unselected branches, empty text, invalid UTF-8, tool calls and results, metadata, other roles (`system`, `developer`), and other kinds; `result` records (possible copies of the final reply); unavailable reasoning (redacted or encrypted, never inferred); thinking in conversations whose reasoning extraction is pending; answers to question tools (`AskUserQuestion`, `request_user_input`) that arrive as tool results, which become actions (below) or, when the result cannot be parsed, are counted as `human_answer_in_tool_result`, a coverage gap; messages the classifier gave no result (`authorship_missing`, for example one without a send time), which never fall back to the raw text; and the human-piece exclusions below. Exclusions of messages that could have been units are also kept row by row in the dataset.

## Human pieces (`human-pieces-v1`)

The classifier splits each user message into spans. Preparation gives each span a role:

- **Target** text may receive labels about the person: typed text (`estimated_typed`: what no other rule claimed, an estimate rather than proof), comments inside recognized page-feedback reports (`feedback_comment`), the person's own earlier messages sent again (`reused_own_message`, linked to the source; reuse inherits the source's attribution: the re-sent text is found in the source message and only the bytes that were target there, resolved the same way, stay target, while the rest, or text whose source is missing or cannot be matched, is context `reused_unverified`, so a re-sent log or old agent output never becomes the person's words), and one-click templates and slash commands (`reused_template`: no new words, but a real request).
- **Context** is material that helps interpret the person but is not theirs: quoted text (`quoted`, linked to the agent output it matches), likely pastes (`likely_pasted`), and attachment references. It is kept with its source but supplies no evidence.
- **Dropped** text is harness (instructions, generated report fields, question forms) and secrets.

The text a teacher sees is the target pieces in order. Other material between, before or after them is replaced by markers, one for each run of the same kind, so pieces are never joined into a sentence the person did not write:

- Context shows what the person is reacting to: `⟦quoted agent text, context only, 312 chars: "…"⟧`, with an excerpt of at most 300 characters (its head and tail when longer) and at most 1,200 excerpt characters per unit; markers after that budget give only the length. Kinds are `quoted agent text` (matches agent output), `quoted text` (lines quoted with `>`), `likely pasted text` and `attachment reference`.
- Dropped material gives only its kind and length: `⟦excluded: question form, 40 chars⟧`, `generated page feedback`, `harness text`, `redacted secret`.

Harness blocks that open a message are dropped without a marker, and so are leading `<in-app-browser-context …>` blocks, which authorship-v8 does not recognize and counts as typed (Human Words still does; a follow-up for that feature). These are counted as `unrecognized_harness_tags`. Each marker's byte range in the visible text is stored (`marker_ranges`), so evidence quoted from inside a marker can be rejected. Every piece keeps its exact byte range in the stored message. A JSON-encoded feedback comment is one piece over its encoded bytes, shown decoded (`json-string-v1`).

Anything the context redaction patterns match (API keys, bearer tokens, `token=` and similar URL parameters), in any cohort's text, becomes a dropped `redacted_secret` piece: it is never shown, excerpted or quotable. The manifest counts them.

There is no word-count threshold. A message that is only `?`, `+1`, a number or an emoji is kept: a deliberate short reply is meaningful. Punctuation left over after excluding copied material (a quote, a paste, unverified reused text) is excluded as `residual_punctuation`; next to an attachment, a secret or harness it is kept. A message with no target text is excluded as `no_target_text`, or as `action_only` when its only content was a question-form answer.

Answers to questions an agent asked are recorded as actions (`actions` table), never as labeled text:

- A prewritten option submitted as a flattened message (the question title followed by the option) is a `submitted_option` with status `recognized_question_form`: the question, the option as offered, the message that submitted it, the byte ranges of the question and answer lines, and the tool call that asked. Each answer line counts once, for the most recent matching question. The actor is the submitting unit's (a person's, or an agent's for an automated message).
- Answers in a question tool's result (`User has answered your questions: "Q"="A"`, Conductor's `User responses:` with one numbered line per question, or a JSON `answers` object) have status `tool_result_answer` and actor `human` only when the conversation is an interactive session (no automated or headless prompt, not delegated) and the answer responds to a question tool call recognized in the same conversation; otherwise actor `unknown` and status `tool_result_answer_unverified`. The result message is the submission and the asking call (its message and call ID) the question; branch selection and inherited history apply first. Each answer is one `submitted_option` per selected option, or a `free_text_answer` with the text when the answer is not an offered option.

An unsubmitted default or a skipped question is not an answer. Results that hold no answer (`{"accepted": true}`, tool errors) stay the counted `human_answer_in_tool_result` gap; the manifest lists their forms (`actions.unparsed_shapes`, structure only, letters masked).

## Context (`context-v1`)

Each unit carries the context the measured prompts used: `Previous user request:` and the last 1,400 characters of the previous request unit's visible text (a person's or an agent's in the user role, so a sub-agent's replies see their assignment), then `Previous assistant message:` and the last 800 characters of the previous agent reply, both within the same agent session (the main session, or an embedded sub-agent's), (a part is left out when there is none), stripped, with API keys and tokens redacted, and cut to its first 2,200 characters. A prompt shows the last 600 characters of that (`context_prompt`). Characters are Unicode code points. Context only ever comes from earlier messages. The dataset stores the rendered strings and the messages they came from.

The measured experiments built the same strings from the stored authorship ledger's typed spans, joined by a fragment marker, and from native capture files. Here the previous user request is the previous request unit's visible text, markers included, and agent requests count: the measured extractor used only human typed text. The catalog keeps no final or commentary channel, so a unit's channel is `analysis` for thinking and `unspecified` otherwise. Target text is not redacted.

## Long text (`chunking-v1`)

A unit's visible text longer than 6,000 characters is split into items of at most 6,000: at the last paragraph break in the second half of the window, else the last sentence end there, else the last space, else between characters. No cut falls inside a character, before a combining mark, joiner, variation selector or emoji modifier, between the two halves of a flag, or inside a marker. Nothing is truncated: the items join to the full text, and each records its range in the visible text and the byte ranges it covers in the stored message. A chunk holding only markers is merged into the one before it when that fits, and otherwise dropped and counted (`dropped_chunks`, `marker_only_chunks`).

Sentence ranges of every target piece are stored against the stored message (`sentences`, `sentences-v1`: after `.`, `!`, `?` and similar marks followed by space, and at line breaks; a transformed piece is one range), so later work can choose sentence units. Long units are counted separately; merging their chunk labels is not yet validated.

## Families and splits (`splits-v1`)

Conversations that share text or execution form one family: mirrors and aliases, root and parent conversations, forks, delegated children, headless runs and the command that launched them, and conversations where the classifier found a re-sent or quoted passage of at least 200 bytes from another. Repeated lines do not link families: a reused instruction would chain unrelated work together. Identical text alone never links families: a stock reply or canned prompt would chain unrelated work together. Instead each item stores a hash of its target text (markers removed, lowercased, whitespace collapsed; `target_hash`). An item whose text occurs in more than one split is marked `cross_split_duplicate`, and a held-out item (dev, test, fresh) whose text also occurs in train is marked `seen_in_train`, which evaluation excludes, as near-duplicate filtering does. The manifest counts both. A family whose first activity is within `--fresh-days` (14) of the snapshot (of when a copy was taken, for a snapshot copy) is the `fresh` holdout; every other family is `train`, `dev` or `test` (80/10/10) by a hash of its ID (its smallest member's).

A family's ID can change when a new conversation links into it, and the fresh window moves with time, so a later preparation can assign a conversation to a different split. The dataset stores every conversation's split (`families`); `--splits-from OLD_DIR` keeps them: a family with a conversation the earlier dataset assigned takes that conversation's split, so a conversation that joins an old family inherits it, and only new families are assigned afresh. A family whose conversations the earlier dataset put in different splits goes to `quarantine`: it belongs to neither training nor evaluation. The manifest reports how many conversations inherited, joined an old family, or were quarantined. It also shows split coverage by cohort, provider, depth and length, and family sizes: the number of families with units, the largest and the top ten as shares of units, and each split's share of units.

## The dataset

`DIR/dataset.sqlite3` holds `units` (with the original message text and its hash, visible text and marker ranges, context, lineage, family and split), `pieces`, `sentences`, `items`, `actions`, `exclusions` (with `duplicate_of` for inherited history), `families` (every conversation) and `meta`. A finished dataset uses SQLite's rollback journal, so reading it (`plan`, `status`) leaves no files behind. `DIR/manifest.json` is written last, atomically, and only when preparation completed: a directory without it is incomplete, and `prepare.incomplete.json` records the attempt and, when it failed or was interrupted, why. The manifest holds the scope and options, snapshot identity, contract versions (units, pieces, marker, context, chunking, splits, taxonomy `candidate-0.3` and its SHA-256), counts by cohort, provider, depth and length, exclusions by reason, reasoning coverage, authorship status, duplicates, long units, invalid UTF-8, actions, split coverage, and a statement of the population it describes.

## Planning

`plan` skips delegation units that repeat a child's message (`duplicate_of`) and reports how many (`--include-duplicates` keeps them). It packs the selected items, in a fixed order, into requests of at most `--items-per-request` items whose text and context total at most `--max-request-chars`; a larger item goes alone. It then estimates tokens from measurements of 18 four-item requests at effort high on 2026-10-27: per request, about 14,300 input tokens of prompt, taxonomy, schema and CLI framing for the `claude` route (Haiku 5.5) and 16,100 for `codex` (Luna), plus the items at 3.6 characters per token; per item, about 1,006 output tokens for Haiku and 212 for Luna, reasoning included. Each constant has a flag (`--overhead-tokens`, `--output-tokens-per-item`, `--seconds-per-request`, `--chars-per-token`). The cost is priced with the price book built into Pharos for the named model on the day of the plan, at standard rates without cache or batch discounts; a model the book has no confirmed price for is shown as unpriced, never guessed. Runtime is the largest of the measured request time (17 s for Haiku, 10 s for Luna) over `--concurrency`, the input over an assumed `--tokens-per-minute` (2 million) and the output over a fifth of that, plus 25%, with a planning high of three times that. The plan prints every assumption; effort levels other than high are flagged as uncalibrated.

## Known limits

- Human pieces inherit the classifier's limits: typed text is an estimate, and prose written elsewhere and pasted without cues still counts as typed.
- Embedded sub-agent sessions are identified by the session ownership the catalog records; a sub-agent prompt stored as a user message outside such a session is treated as the classifier treats it.
- Delegation duplicates are found only through a recorded delegation-to-child link (results through the delegation's call ID).
- Inherited history is recognized by identical text only; a fork that reformats its copy is not recognized.
- Preparing even a restricted sample classifies every user message in the library, since copies and templates are found across all of them.
