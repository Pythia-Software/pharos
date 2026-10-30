# Human authorship

**Your writing** in Usage estimates how much text you typed (or dictated) into agent chats. Transcripts store a lot under the "user" role that you did not write: harness instructions, one-click prompts, attachment references, pasted logs, agent output copied from another chat, and prompts sent by scripts. Each user message is split into spans, and each span gets a category and the reason it was assigned. The totals are the sum of those spans, and the transcript reader shows the spans for each message ("11 words typed · 219 from elsewhere"). A copied or re-sent span links to the conversation and message its text came from. The classification changes nothing else: messages are stored and displayed as the source recorded them.

The classification is derived from retained messages into `message_authorship` (one row per user message, with characters and words per category). Manual **Index changes** and **Capture & index** refresh it when its inputs changed and wait for it to finish. Scheduled indexing leaves it pending; the background schedule's **Your writing** item then rebuilds it on its cadence (Hourly while a rebuild takes minutes; see [background updates](background-work.md#11-background-updates)), so Usage keeps the last classified totals until then. Separate source/captured-file index actions also request a background rebuild. Opening Usage or Settings builds it only when it is missing or was classified by an older version of the rules. Record writes invalidate authorship independently of tool usage, including messages containing only user text. Rebuild cost depends on both user messages and retained agent/tool text; sources stream through existing time indexes, and the agent comparison corpus is evicted outside the seven-day window.

## Which messages count

Only `role='user'` messages are classified. Prompts a parent agent sent a sub-agent already use the `agent` role. Linked mirrors of the same work (a Conductor workspace and the native session it wraps) are counted once, as in Usage. If two copies of one message are not linked, the second is counted as re-sent.

The source records who sent a message where it can (`messages.sender`):

| Sender | Source |
|---|---|
| `account:<id>` | Conductor's `sender_id`, for messages you sent |
| `agent:<session>` | Conductor's `sender_session_id`, for messages another agent sent |
| `automation:<key>` | Conductor's `sender_api_key_name` |
| `automation:claude-sdk-cli` | Claude events with the `sdk-cli` entrypoint (headless `claude -p`) |
| `automation:codex-exec` | Codex sessions with the `codex_exec` originator or `exec` source |
| `automation:agy-print` | Antigravity CLI conversations run with `-p` (print mode), named in the CLI's per-run logs |

Two headless runs record nothing in their transcripts that says so:

- **Claude** records the entrypoint it was started through, and Conductor runs Claude through the Agent SDK (`sdk-ts`) too. A `claude -p` started from a shell inside another Claude session inherits that session's `CLAUDE_CODE_ENTRYPOINT=sdk-ts`, so its events look like a Conductor session's. Conductor, though, records every session it starts, and Pharos links them. On a Mac whose Conductor Pharos reads, a Claude SDK session (`sdk-ts`, `sdk-py`) that no Conductor session started was started by a script.
- **Antigravity**'s transcripts and conversation databases look the same with and without `-p`. The CLI writes a log per run (`log/cli-<time>.log`), and a print-mode run logs `Print mode: conversation=<id>`. The CLI keeps only about its latest 1,300 run logs, so a capture keeps the rest.

## Headless runs and the command that launched them

A headless run (`claude -p`, `agy -p`, `codex exec`) is stored as a conversation of its own, and records nothing about what started it. Pharos links it to the conversation whose shell command launched it, when exactly one conversation's command fits, on the same Mac:

- **A command that names the agent.** A command running `claude -p`/`--print`, `agy -p`/`--print`/`--prompt`, or `codex exec` (seen through wrappers such as `timeout`, pipes, and loops) was still running when the run started.
- **The command that started a script.** A script calls the agent itself, so its command does not name it, and a command that puts the script in the background (`nohup … &`) returns before the runs start. But a script's runs arrive as a stream: the same working directory, each within 10 minutes of the last. The stream's first run starts moments after the command that started the script. So the stream is linked when one conversation's command started within the 30 seconds before its first run and was still running then, or put work in the background (`&`). When commands in several conversations fit, the one conversation whose command names the agent as a word (`python3 probe.py --cli agy`) is taken. A longer window lets in commands that other sessions happened to be running: on a library where one session's experiment scripts launched about 2,400 runs, these rules linked 99% of them with none linked to the wrong session, while a 2-minute window linked fewer, some wrongly.

No command that began after a run started launched it. Antigravity records a run's start to the second, so for it a command may begin up to a second after the recorded start.

A run inside another work's folder counts only for a command in that work, or one naming that folder: apps start headless runs of their own there (Conductor runs `codex exec` in a workspace's folder to write titles and summaries). A sub-agent's command counts toward its root conversation, a Conductor workspace and the native sessions it ran count as one, and a headless run's own commands never count. Runs an app started (Conductor sessions) and interactive terminals are never linked. The links are derived with the classification, in `conversation_launches`.

A run whose stream's first run started while several conversations had commands starting, none or more than one naming the agent, is left unlinked, as is one started long after its script's command (a script that waits before calling the agent, or a stream that pauses for more than 10 minutes); it still counts as automated when its transcript or logs show it was headless.

## Categories

Rules run in this order, and the first rule to claim a character keeps it.

| Category | Rule |
|---|---|
| **Automated** | The whole message, when a script or another agent sent it: headless runs linked to the command that launched them (the span links to that command), the senders above, complete scheduled `<heartbeat>` envelopes containing an automation ID, valid timestamp, and instructions, TL1 workspaces, sub-agent conversations (which include forked copies of the parent's history), and Claude SDK sessions Conductor did not start. |
| **Harness** | `<system_instruction>`, `<system-reminder>`, `<environment_context>`, `<user_instructions>`, or `<recommended_plugins>` blocks at the start of the message; generated fields and instructions in recognized page-feedback reports; question titles, selected prewritten options, and skipped markers in a recognized response to a question from the same conversation. In `<send_user_message_question_reply>` JSON blocks, the wrapper, questions, IDs, and JSON syntax are harness; only each reply item's `answer` string value remains eligible as human writing. Recorded selected options and skipped markers use the same provenance rules as flattened replies; selected options are recorded as actions. Free-text answers remain eligible as human writing. The stored message keeps this text, since it is part of the prompt and counts toward context growth everywhere else; only this classification sets it apart. |
| **Template** | The whole message, when its normalized text (attachment references collapsed, case and whitespace ignored, at least 10 characters) was sent in 5 or more sessions on 3 or more days. This catches Conductor's buttons (Create a PR, Review, Rebase, Resolve conflicts) without a hard-coded list. Sending one prompt to many sessions on a single day is not a template: the first copy counts as typed and the rest as re-sent. A leading slash command such as `/compact` is also a template; its arguments stay typed. |
| **Attachment** | Conductor attachment markers (`@⟦name⟧(path)`), `.context/attachments/…` paths, and image placeholders. |
| **Quoted** | Contiguous runs of 12 or more words matching one agent source from the seven days before the message, in any conversation. Newly authored file contents, literal file-writing heredocs, and added patch lines from agent tool calls are compared too; removed/context lines and read-only commands are excluded. Short review items of at least four words can match only a source already established by a longer match, after removing formatting and file locations. A copied report header requires two verified items from that source. Lines starting with `>` and explicit pasted conversation blocks also count. |
| **Re-sent** | Runs of 12 or more words that match any of your earlier messages, however old, so a saved prompt counts as typed the first time only. A line of 8 or more words already sent in 2 other conversations is re-sent too. |
| **Pasted** | Heuristics: fenced code blocks; blocks of program output or code (at least three machine lines, at most two prose lines between any two of them, and no more prose lines than machine lines); bounded, explicitly introduced error output and recognized configuration reports; Markdown or tab-separated tables; regions formatted like agent replies (two or more headings, or three or more bolded bullets); identifiers (see below); and messages whose remaining text arrived faster than 20 characters a second since your previous message in the same conversation (checked only when at least 600 characters remain). |
| **Typed** | Everything else. |

### Page-feedback comments

[Agentation's compact, standard, detailed, and forensic Markdown exports](https://github.com/benjitaylor/agentation/blob/main/package/src/utils/generate-output.ts), design-preview exports titled `# <project> design feedback`, and embedded reports titled `# <project> UI feedback` (including Pharos, Alexandria, and AI Work Archive), are split by field. Human `**Feedback:**` values, compact annotation comments, and JSON-encoded `- Feedback:` values count as typed, with the reason **Human feedback comment**. Generated titles, instructions, selectors, source paths, element/selected/nearby page text, viewport, bounds, styles, and other metadata count as harness, so they do not inflate either end of the typed-to-typed-plus-pasted range. JSON quotation marks and encoded whitespace are excluded; escaped newlines and tabs separate words while span offsets still refer to the original stored text.

Copy and re-send matching still applies to the comments; generated report fields are excluded from that matching. Known comment fields are protected from the general formatting, identifier, and typing-speed heuristics: a comment written in a feedback UI can arrive in chat all at once. Text before and after the report follows the ordinary rules. A report header alone is not enough to recognize a report; it must have annotation structure and identifying context. Fenced examples remain pasted code. Automated senders still take precedence over comment fields.

Agentation interpolates comments into Markdown without an explicit end marker. Only the next sequential annotation in the same report format delimits an interior comment, including its paragraphs. Standard, detailed, and forensic headings must also carry element context; unrelated numbered lists and plan headings stay outside the report. For the final comment, a blank line, metadata field, or heading ends the recognized field; ordinary prose following a blank line keeps its ordinary classification. Comments with Markdown that resembles report structure can therefore be ambiguous. Embedded JSON comments preserve paragraph boundaries without this ambiguity. Unknown generators need an observed format adapter rather than a generic assumption that anything labelled feedback is human-authored.

The authorship ledger currently classifies user-role messages. Feedback received only as an MCP tool result is not counted here, even if the original annotation came from a human. To support that route reliably, ingestion needs explicit comment provenance (author kind, annotation ID, and original creation time), separating human comment events from generated context and agent-written replies, with deduplication against copied exports. The same provenance would remove the Markdown boundary ambiguity in new embedded generators.

Machine lines are log, stack-trace, diff, JSON, and file:line lines; lines tagged like `[deploy] …`; shell prompts (`user@host dir %`, `PS C:\>`); error headers; `key=value` output; rules, underlines, and box drawing; source-code statements; text indented as a block (with spaces or non-breaking spaces) rather than as a list item; and any line of four or more words that appears twice in the message or was sent in 2 other conversations before. People rarely retype a line word for word, so a repeated line is program output or reused text; numbers and IDs are ignored when comparing lines, so the same log line from another run matches.

Identifiers are judged by token shape: a whitespace-separated token is an identifier when it mixes digits into letters, or contains an underscore, camelCase, an inner dot, `://`, or symbols such as `=`, `@`, `#`, `$`, `{`, or `[`, or is 40 or more characters long. Plain words, including hyphenated and slashed compounds (`frontend/backend`), are words; short labels (`M1`, `PR-3`), quantities (`2m`), abbreviations (`i.e.`), numbers, and punctuation count toward neither. A line of 4 or more counted tokens at least half identifiers (or 8 or more at least 35% identifiers) is pasted as a whole: log lines, selectors, JSON, and code. Elsewhere, a run of consecutive identifiers totalling 20 or more characters is pasted (a path, URL, or ID inside a typed sentence), and the words around it stay typed. This runs after the typing-speed check. An English dictionary (with typo tolerance) was tried in place of plain words and changed under 0.3% of typed characters, most of it wrongly, so none is used.

Word runs are compared as overlapping 8-word shingles over lowercase letters and digits, so punctuation and formatting changes still match. The index keys complete 12-word candidates from individual sources: a later excerpt shorter than 12 words cannot hide a valid full source. Matching verifies fixed-size windows and coalesces their coverage, so repeated text takes linear work rather than repeatedly extending the same prefix. Harness blocks are left out of the comparison. Pasted is the one category inferred from how text looks rather than where it came from. Usage therefore reports typed words as a range, from typed to typed plus pasted. Its chart shows words by day, week, or month, typed only or every category stacked, or split by repository, app, or provider. A time filter, dragged across the chart or typed into the table, counts only the words sent inside it; the table still lists each conversation's totals.

## The conversation table

Your writing lists one row per Library work that has classified user messages, with words per category (and the combined groups the chart uses), typed and total user turns, the typed share of user-turn words, words per typed turn, the longest typed message, and the work's tokens and API-equivalent cost. Typed words per 1M tokens compares how much you wrote with how much the agents ran. A work that holds only sub-agent conversations spawned from another work (Codex stores each spawned thread as its own session), or only headless runs launched by a command in another work, is folded into that work: its prompts count as automated there and its tokens are added to the parent's. The table counts each row's folded sub-agent works and launched headless runs. The chart, cards, and breakdown above the table sum the daily text of the rows that match the table's filters.

## Single messages

**List → Messages** switches the table to one row per classified user message (the `writing_messages` query table), so filters, sorts, and metrics work on messages rather than conversations: the messages that were mostly pasted, every message a rule labelled, your longest typed messages in a repository. A message's title, repository, and source are those of the work it counts toward, so a sub-agent's or headless run's prompts appear under the work they fold into; **Work ID** filters to them, and each conversation row's **Messages** button sets that filter. **Mostly** is the category holding most of a message's words; **Categories** and **Rules** list every category and rule holding any of its text, most words first, and filter with *includes*. A rule is the reason without its particulars ("Matches recent agent output" for "Matches agent output from 3h earlier"). The key above the table totals words per category for the messages the filters match; clicking a category keeps only messages holding it.

Messages show as a table or as **Highlighted text**, like the Library's Table and Conversations views. The table previews each message on one line, and each span's tooltip names its category and reason. Highlighted text shows each message with every span highlighted in its category's color (the second category of a chart group is striped). Hovering over or focusing a span explains it: its category and words, the full reason, what the rule looks for, and for copied or re-sent text a link to the message it matches. Clicking a span keeps the explanation open. Long harness blocks and other long spans that are not typed are folded to their start, so the words around them stay in view, and a message longer than about 1,800 characters starts collapsed. A page row carries at most 16,000 bytes of its message; **Open in conversation** shows the rest.

Words are whitespace-separated units containing a Unicode letter or digit. Contractions, compounds, quantities, and numeric answers stay together; punctuation and line-leading numbered list markers with following text do not count. Counts use the complete message, so splitting a word or its trailing punctuation across classification spans cannot create another word. Escaped whitespace in feedback comments still separates logical words. A word crossing classifications counts once in the span containing most of its letters and digits (first on ties). A turn containing only typed punctuation has zero typed words and is excluded from typed-turn totals.

The rebuild stores each span in `message_authorship_spans` (one row per span, with its byte range in `messages.text`, category, rule, reason, words, and the conversation and message it matches), and each message's work, word total, main category, categories, and rules in `message_authorship`.

## Known limits

- Text pasted from outside the archive (a web chat, a document, a terminal) is found through explicit quotation/report cues or when it looks like code, logs, tables, or agent formatting. Unmarked prose written by an agent elsewhere can still count as typed; the typing-speed check needs an earlier message in the same conversation.
- Agent output and authored file/tool content are compared only within the seven-day window. Authorship is still an estimate; formatting alone yields likely-pasted text rather than proof of an agent author.
- Tool-source extraction recognizes literal write/edit payloads, added patch lines, and file-writing heredocs, including supported literal tool calls inside `exec`. Static parsing ignores calls mentioned in comments, strings or regex literals and does not execute code or reconstruct computed arguments and interpolated templates. A heredoc requires a real stdout file redirect or a `tee` file operand on its own command; quoted operators, stderr-only redirects, and read-only pipelines do not establish file provenance.
- Text you pasted and then edited counts as quoted where runs of 12 or more words still match, or a short review item matches the same already-established source. Other edits remain eligible as typed.
- An identifier you typed yourself (a function name, a short path) counts as pasted when it is 20 or more characters or dominates its line.
- Dictation cannot be told apart from typing; both count as yours.
- Senders are recorded as sources are re-ingested. Until a session is re-indexed, headless runs from before this change count as typed unless another rule claims them.
- An Antigravity print-mode run whose log the CLI rotated away before Pharos captured it cannot be told from an interactive run; its prompt usually counts as re-sent.
- A headless run an app started in a work's folder (Conductor's `codex exec` title and summary runs) is linked to that work's conversation when it had started a command in the 30 seconds before, as if that command had launched it.
- On a Mac whose Conductor Pharos does not read, Claude SDK sessions count as yours; on one whose Conductor it reads, a session another app ran through the Claude SDK counts as automated.

Identity reconciliation inherits native harness versions only when a value
changes. Empty aliases are stored as `[]`; legacy `null` aliases are skipped.
An unchanged manual analysis chain leaves conversation rows and the Search,
MCP metric and finding-message dirty sets alone.

Rebuilding compares messages, individual spans, launches and cached authored
workspace names with their stored values. Only changed rows are written, in
writer-gated chunks of at most 250 row mutations. Until the final generation
marker is saved, readers may see a mixture of generations; an interruption is
retried by diffing against the chunks already committed. Unchanged messages
keep their trigram index entries. The classifier reads agent replies in day
windows and pages of 250 rows, closing each statement before classification.
The operation's progress records input, classification and storage timings and
counts direct writes for messages, spans, launches and authored name caches.
`TestAuthorshipBackupProbe` (`-tags probe`) measures those phases and the
would-write counts against an explicitly supplied immutable backup.

The classifier also keeps a versioned, derived checkpoint: the latest source
and offset for each copied-text candidate, the source shingles used to verify
it, reused-line origins and their first distinct conversations, template
session/day counts, last user-turn times, and question-form provenance.
Triggers record the earliest changed input time, including user-only messages.
A normal append reads and classifies only that suffix, looking up historical
candidates and lines as needed. It reuses launches when all changed inputs
postdate every existing headless start. Reply replay includes any idle gap
since the previous user turn, so question forms remain recognizable beyond
the seven-day copied-output window. Every conversation's replayed questions
are saved with its earlier history, even when only another conversation has
new user messages. Reused launch links retain a message anchor only when the
command belongs to the attributed parent conversation; a suppressed mirror's
message cannot anchor a link into its representative.

The full classifier remains the fallback for missing/old/interrupted state,
identity or mirror changes, launch overlap, historical edits/deletions or
backfills, equal-time additions, and suffixes exceeding 2,000 user messages.
A template crossing its threshold, or changing an existing template's session
or day counts, also takes the full path: older messages' category or displayed
reason can change. These conservative cases deliberately remain full passes;
stream-local launch replay and targeted historical template replay are not
implemented. This checkpoint work did not change classification rules or
scheduling. Classification `authorship-v11` recognizes structured question
replies, preserves their selected-option provenance, and recognizes scheduled
automation heartbeats; upgrading rebuilds existing classifications. The first changed-data
rebuild populates the extra derived tables;
its production storage cost has not yet been measured.
Checkpoint version 2 forces one full rebuild of version 1 state, which may
have lost question history or restored an incompatible mirror message anchor.

Checkpoint publication is chunked too. Its version is invalidated before
output writes and restored with the generation only after all state writes
succeed and the input revision still matches. A concurrent input change keeps
the dirty evidence and prevents reusing the obsolete checkpoint. The opt-in
`TestAuthorshipIncrementalWorkProbe` measures suffix work on local fixtures;
`TestAuthorshipBackupProbe` can compare the paged and original single-statement
reply streams with `PHAROS_AUTHORSHIP_PROBE_REFERENCE=1`. Backups are opened
immutable/read-only, and the probe reports when their stored results are stale.

Checkpoint builders also hold a separate authorship file lease. It prevents
two classifiers from reading/writing partial auxiliary state concurrently,
while allowing ingest and ordinary readers between SQLite write chunks. The
OS releases the lease if the builder exits.

Activity advances in a mirrored work retain the suffix watermark when the
same representative is still selected. A changed representative is detected
by the graph fingerprint and takes the full fallback. A real ingest test
covers appending to a linked native/Conductor pair.
