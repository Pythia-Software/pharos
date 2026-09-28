# Human authorship

**Your writing** in Usage estimates how much text you typed (or dictated) into agent chats. Transcripts store a lot under the "user" role that you did not write: harness instructions, one-click prompts, attachment references, pasted logs, agent output copied from another chat, and prompts sent by scripts. Each user message is split into spans, and each span gets a category and the reason it was assigned. The totals are the sum of those spans, and the transcript reader shows the spans for each message ("11 words typed · 219 from elsewhere"). A copied or re-sent span links to the conversation and message its text came from. The classification changes nothing else: messages are stored and displayed as the source recorded them.

The classification is derived from retained messages into `message_authorship` (one row per user message, with characters and words per category). It is rebuilt in the background when Usage or Settings is opened after messages changed, at most every five minutes. A full rebuild over about 20,000 user messages takes around ten seconds.

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

## Categories

Rules run in this order, and the first rule to claim a character keeps it.

| Category | Rule |
|---|---|
| **Automated** | The whole message, when a script or another agent sent it: the senders above, TL1 workspaces, and sub-agent conversations (which include forked copies of the parent's history). |
| **Harness** | `<system_instruction>`, `<system-reminder>`, `<environment_context>`, `<user_instructions>`, or `<recommended_plugins>` blocks at the start of the message. The stored message keeps this text, since it is part of the prompt and counts toward context growth everywhere else; only this classification sets it apart. |
| **Template** | The whole message, when its normalized text (attachment references collapsed, case and whitespace ignored, at least 10 characters) was sent in 5 or more sessions on 3 or more days. This catches Conductor's buttons (Create a PR, Review, Rebase, Resolve conflicts) without a hard-coded list. Sending one prompt to many sessions on a single day is not a template: the first copy counts as typed and the rest as re-sent. A leading slash command such as `/compact` is also a template; its arguments stay typed. |
| **Attachment** | Conductor attachment markers (`@⟦name⟧(path)`), `.context/attachments/…` paths, and image placeholders. |
| **Quoted** | Runs of 12 or more words that match agent output from the 48 hours before the message, in any conversation. Lines starting with `>` also count. |
| **Re-sent** | Runs of 12 or more words that match any of your earlier messages, however old, so a saved prompt counts as typed the first time only. A line of 8 or more words already sent in 2 other conversations is re-sent too. |
| **Pasted** | Heuristics: fenced code blocks; blocks of program output or code (at least three machine lines, at most two prose lines between any two of them, and no more prose lines than machine lines); markdown tables; regions formatted like agent replies (two or more headings, or three or more bolded bullets); generated page-feedback reports; identifiers (see below); and messages whose remaining text arrived faster than 20 characters a second since your previous message in the same conversation (checked only when at least 600 characters remain). |
| **Typed** | Everything else. |

Machine lines are log, stack-trace, diff, JSON, and file:line lines; lines tagged like `[deploy] …`; shell prompts (`user@host dir %`, `PS C:\>`); error headers; `key=value` output; rules, underlines, and box drawing; source-code statements; text indented as a block (with spaces or non-breaking spaces) rather than as a list item; and any line of four or more words that appears twice in the message or was sent in 2 other conversations before. People rarely retype a line word for word, so a repeated line is program output or reused text; numbers and IDs are ignored when comparing lines, so the same log line from another run matches.

Identifiers are judged by token shape: a whitespace-separated token is an identifier when it mixes digits into letters, or contains an underscore, camelCase, an inner dot, `://`, or symbols such as `=`, `@`, `#`, `$`, `{`, or `[`, or is 40 or more characters long. Plain words, including hyphenated and slashed compounds (`frontend/backend`), are words; short labels (`M1`, `PR-3`), quantities (`2m`), abbreviations (`i.e.`), numbers, and punctuation count toward neither. A line of 4 or more counted tokens at least half identifiers (or 8 or more at least 35% identifiers) is pasted as a whole: log lines, selectors, JSON, and code. Elsewhere, a run of consecutive identifiers totalling 20 or more characters is pasted (a path, URL, or ID inside a typed sentence), and the words around it stay typed. This runs after the typing-speed check. An English dictionary (with typo tolerance) was tried in place of plain words and changed under 0.3% of typed characters, most of it wrongly, so none is used.

Word runs are compared as overlapping 8-word shingles over lowercase letters and digits, so punctuation and formatting changes still match. Harness blocks are left out of the comparison. Pasted is the one category inferred from how text looks rather than where it came from. Usage therefore reports typed words as a range, from typed to typed plus pasted. Its chart shows words by day, week, or month over the last 30 days to all time, either typed only or every category stacked.

## The conversation table

Your writing lists one row per Library work that has classified user messages, with words per category (and the combined groups the chart uses), typed and total user turns, the typed share of user-turn words, words per typed turn, the longest typed message, and the work's tokens and API-equivalent cost. Typed words per 1M tokens compares how much you wrote with how much the agents ran. A work that holds only sub-agent conversations spawned from another work (Codex stores each spawned thread as its own session) is folded into that work: its prompts count as automated there and its tokens are added to the parent's. The chart, cards, and breakdown above the table sum the daily text of the rows that match the table's filters.

## Known limits

- Text pasted from outside the archive (a web chat, a document, a terminal) is found only when it looks like code, logs, tables, or agent formatting. Prose written by an agent elsewhere, such as a review or a campaign prompt pasted as a session's first message, still counts as typed; the typing-speed check needs an earlier message in the same conversation.
- Agent output is compared only within the 48-hour window.
- Text you pasted and then edited counts as quoted only where runs of 12 or more words still match. Your edits count as typed.
- An identifier you typed yourself (a function name, a short path) counts as pasted when it is 20 or more characters or dominates its line.
- Dictation cannot be told apart from typing; both count as yours.
- Senders are recorded as sources are re-ingested. Until a session is re-indexed, headless runs from before this change count as typed unless another rule claims them.
