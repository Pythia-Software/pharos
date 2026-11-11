# Claude export coverage audit

Audited 2026-11-11 against the original collected export, including the conversation named **Fervo Energy financial outlook**. Counts below describe that export, not every version of Claude's export format. Private export contents and identifiers remain outside the repository.

## Tokens and API cost

The conversation export contains **168 conversations, 912 chat messages and 3,796 content blocks**. None of its conversation or message envelopes contains a model name, request usage, input/output token counts, cache usage or billing amounts. All **30 `token_budget` blocks have `remaining: null`**. The only numeric token-related field found in structured conversation data is one tool input, `text_content_token_limit: 60000`: a requested output limit, not measured model consumption. MCP `serverInfo` metadata describes a tool server, not a model.

The Fervo conversation contains two chat messages and **36 tool calls**. Its missing Tokens and API cost values reflect missing evidence in the original export. Character counts, thinking duration, token budgets and tool output limits cannot recover the model's repeated/cached input or actual output usage. A subscription bill cannot be derived from these records either; Pharos's API cost is a list-price equivalent.

The reader now explains missing consumption explicitly. The importer also accepts explicit assistant-message `usage` and `model` fields if an export supplies them, once per message. This compatibility behavior is tested with a **synthetic fixture**; these fields were absent from the inspected export. Pasted JSON, source documents, tools' payloads and token budgets do not establish request usage.

## Conversation content

| Source content | Observed blocks | Representation |
| --- | ---: | --- |
| Text | 1,089 | Conversation prose, with readable source links for exported citations |
| Thinking | 567 | Exported thinking text or summaries; explicit unavailability when neither exists |
| Tool use | 913 | Calls retaining IDs, arbitrary input shapes, integration information and original metadata |
| Tool result | 912 | Paired results, structured/display payloads, rich source links and original metadata |
| Injected prompts | 280 | Context, separate from the person's words |
| Token budget | 30 | Metadata explicitly labeled as a budget, excluded from consumption |
| Image | 1 | Attachment identity and exported source metadata |
| Document | 4 | Attachment title, identity and exported source metadata |

The export also contains **831 citations across 111 text blocks**, five extracted attachments and 47 file references across 17 messages. File references contain names and IDs; they do not themselves contain file bytes. Extracted attachment text is readable without opening raw JSON. Files are labeled by their names and actual types instead of all being called images.

Conversation summaries and all conversation-envelope fields are retained in an export-details event. Each message's envelope, including its update time, sender, parent, flattened text and future fields, is retained once alongside its block records. Each block's original fields—including timestamps, citation offsets, voice metadata, thinking visibility/truncation flags, integration metadata and unknown fields—remain available under Raw event. Empty text blocks are retained too. The parser version is bumped so existing captures can be reindexed.

## Supplementary files

All six collected parts were inspected. Previously, capture preserved the five non-conversation parts but the adapter never indexed them.

| Part | Observed contents | Representation |
| --- | --- | --- |
| Projects | One project, including a document and project metadata | Searchable export document with readable document text and complete source JSON |
| Memories | 26 memory files, including content and update times | Searchable export document with named readable sections and complete source JSON |
| Feedback | Two reflections; empty feedback array | Searchable export document preserving both the content and the empty array |
| Frames | Nine artifacts, 1,003 files, 78,222,355 expanded bytes | One archive entry per artifact, preserving version manifests, comments, file inventories and text files |
| Claude Code cloud | Nine JSON files: project files, channel memories, channels, memberships, messages, agents, routines, runs and session transcripts | Searchable source documents plus the recorded cloud conversation with user/assistant/tool roles |

The cloud transcript contains 31 fragments belonging to one session (seven user fragments and 24 assistant fragments). Fragment metadata and continuation flags remain in source evidence; tool calls and results share their recorded call IDs.

A full adapter read of the original managed collection now produces **190 archive entries**, including the 168 original conversations, 21 supplementary document/artifact entries and one cloud session. Account-wide documents remain separate: their current contents are not asserted to have been injected into an earlier conversation. Matching ZIP and unzipped imports, capture readback, and rejection of malformed supplementary JSON are covered by tests.

Binary artifact files have searchable filenames, types, sizes and content hashes. Their original bytes remain in the captured export; the transcript does not execute exported HTML/JavaScript or embed binary previews. Text artifact files are readable as source. Original source JSON remains available alongside the readable document sections. Account-profile files and download credentials remain outside transcript import.

## Render verification

The real Fervo conversation was imported into a disposable test catalog, then its actual overview, conversation, original-message and delegation responses were rendered with the current UI. Desktop and phone checks cover the missing-usage explanation, 36-call total, expandable results and source links. Synthetic browser fixtures additionally cover citations, extracted attachments, inactive exported scripts, overflow and existing long-transcript behavior.

## Readable MCP calls

The same export contains 41 Claude Docs calls: 27 updates, seven guide reads,
four batches (including creation of a document tab), one comment creation, one document read and one Markdown export.
The reader parses the recorded operations and receipts, including JSON embedded
in MCP text blocks. A replacement is labeled **Added to document** only when a
matching `pending_ended` receipt confirms that it resolved an empty section.
Other replacements remain updates. Missing receipts, errors, partial batches and
unknown outcomes remain distinct. Small receipts survive shortened output; full
original payloads remain available through the existing raw disclosures.

Document actions link by the recorded artifact identity to the imported artifact
conversation in the same account, with the exported `page.md` selected. No
client-side identity reconstruction or external Claude link is required. The
link opens the final exported snapshot; an edit's rendered Markdown describes
that recorded operation, not a reconstructed historical document version.
Unimported artifacts have a readable target identity but no invented local link.

Explicit presentations also cover exported memory reads, Claude Browser/Chrome
batches and actions, remote device commands and Gmail thread searches. A bounded
read-only sample of recent production calls additionally informed presentations
for Conductor diffs/review comments, Codex GitHub reads and Pharos searches,
conversation excerpts, statistics and site links. Standard MCP text and
structured-content wrappers are unwrapped; Markdown, source code and tabular
results retain their different presentations. Unknown tools and operation shapes
keep the generic tool view. Raw source payloads remain available in every case.

The actual Fervo document action and its imported Markdown destination were
verified end to end at desktop and phone widths using disposable catalog
responses. Production data was read only. Synthetic unit/browser fixtures cover
failure/partial outcomes, unknown tools, Unicode exports, nested MCP envelopes,
source safety, account isolation and narrow-screen layout.

A read-only model replay of all 169 exported MCP call/result pairs recognized
163 calls, including all 41 Claude Docs calls. The six remaining calls are
custom skill, weather and device-permission operations and retain their generic
representations. This count describes this particular export, not all MCP tools.
