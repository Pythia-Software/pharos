# ChatGPT export: data outside the normalized model

This is the storage-policy companion to [remaining gaps](chatgpt-export-gaps.md).
“Retained as evidence” means bytes survive in the selected source/capture or in a
raw message/metadata event. It does not mean the field has an indexed entity,
queryable column, relationship, or specialized UI. The inventory is based on the
November 12, 2026 [audit](chatgpt-export-audit.md), not an exhaustive specification
of all future ChatGPT export formats.

## Retained with a normalized home

| Export data | Existing Pharos representation |
| --- | --- |
| Conversation ID/title and time bounds | One workspace and conversation; existing native identity and timestamps. |
| Message text, transcription and message tree | Messages, previous-message links and selected/unselected branch evidence. |
| Explicit reasoning and reasoning summaries | Existing reasoning records, with the source's distinction between text, summary and hidden content. |
| Reasoning recap | Metadata event with its source label and an explicit distinction from measured request latency. |
| Explicit legacy tool input/output | Existing tool-call/result records only when the source actually includes them. |
| Custom instructions, memory injected into a message and hidden context, when included | Existing context records; not additional user prose. |
| Images, audio and conversation-linked Library files | Attachment records containing original descriptors, file identity, name/MIME, availability, size and a preserved-source locator. Local previews resolve by stored attachment ID. |

## Retained as evidence; no dedicated normalized entity

| Data | What is retained/shown | Model gap or deliberate limit |
| --- | --- | --- |
| Conversation settings: archive/star, memory scope, voice, study/read-only state, project/template and other top-level fields | A `chatgpt_export` metadata record keeps the full settings object. Common settings appear in Source details. | Not Pharos workspace lifecycle, bookmarks, projects or voice preferences. No cross-provider filter columns for these fields. |
| Sharing records | Matching conversation ID links the source sharing record into its export metadata; the reader notes its presence. Unmatched rows remain in the sidecar. | No share-grant entity, collaboration history, participant roster or permission synchronization. Import never changes ChatGPT or Pharos sharing. |
| `library_files.json` records | Complete inventory retained. Known-conversation files become attachments, including a missing-byte descriptor when appropriate. File metadata stays on the descriptor. | No standalone Library-file catalog, directory/context-scope graph, deletion/version lifecycle, uploader identity or generated-image lineage. An absent origination message does not justify a synthetic historical message. |
| Original filename mapping | Used for attachment display names and retained in the sidecar. | No global file-identity registry or deduplication across accounts based on filenames. |
| Search groups/results, citations, content references, attribution and snippets | Full message envelope retained; safe HTTP(S) links/snippets appear in a message disclosure. | No search-call entity inferred from results, citation-edge table, source ranking history or offline webpage capture. These sources may include candidates that were not cited in the final answer. |
| Footnotes, entities, hidden placeholders, image groups, followups, widgets and declarative UI | Full raw message metadata; reference types identified in the reader. | No dynamic-widget model. Declarations, actions, scripts and compressed payloads remain inert; Pharos does not replay the export viewer. |
| Account-level memory summaries with reason/confidence | `conversation_summaries.json` remains raw snapshot evidence. The audit's four summaries are strings containing structured memory/reason/confidence. | No durable account-memory entity or reliable conversation association. Do not label these as per-conversation summaries or inject them as if every request received them. |
| Moderation flags, request/message diagnostics, finish/status details, UI and voice timing metadata | Full raw source message retained. | No dedicated safety-event, request-telemetry, audio-segment or sentiment model. Missing measurements remain unknown. |
| Deleted/expired/unavailable file metadata and orphan file records | Raw inventory retained; scoped missing assets remain visible. | No fabricated bytes or conversations. No deleted-content recovery beyond what the producer supplied. |
| `sectioned_conversations.json` | Retained sidecar; empty in this sample. | No inferred project/section hierarchy from an empty category. |
| Sites metadata, other unfamiliar categories, viewer and third-party notices | Managed ZIP keeps these as raw evidence unless explicitly excluded below. | This is not a Pages, Codex, scheduled-task or deployable-Site importer. Bundled viewer documentation is software documentation, not the user's content. |
| Export file inventory, shard mapping, sizes and format metadata | Producer inventory is validated and retained. Managed receipt records retained snapshot hashes and exclusions. | No account-completeness claim. Producer inventory is not instructions to fetch URLs or read arbitrary filesystem paths. |

Unknown JSON fields on messages and conversations are preserved in raw evidence.
Unknown archive categories also survive managed ZIP collection. They do not
silently acquire semantics from similarly named coding-transcript fields.

## Deliberately excluded from managed snapshots

- `user.json`: account identity/contact/profile/subscription fields.
- `user_settings.json`: account-wide preferences, announcements, onboarding and
  feature-rollout state. The conversation's own saved settings remain retained.
- `ads.json`: advertising history/profile/hides/reports. The audited lists were
  empty; future values still have no conversation-analysis purpose here.

Exclusion is by exact basename, including within archive directories. The managed
collector validates the original ZIP first and writes a filtered copy; its receipt
records which files were omitted. The original download is untouched. Producer
inventory entries for these omitted files remain, so the receipt explains why
those bytes are absent. Privacy policy is scoped to this collector: an existing
manually configured whole-ZIP source still preserves that selected ZIP as-is.
Folder capture excludes the same account basenames from its selected members.

## Possible future models

An account-memory store needs account provenance, temporal validity and explicit
conversation/application links. A standalone asset store needs stable provider
IDs, multiple byte revisions, origin links, directory/scopes, generated-media
lineage and a clear distinction between described and available bytes. A citation
model needs typed edges from exact message spans to retained references, while
separating cited sources from search candidates. Audio/video needs segment timing
and transcript-to-media alignment. Conversation settings need namespaced,
provider-specific fields rather than coercion into Pharos bookmarks or lifecycle.

These would expand the product's model. Until designed, raw retention supports
future re-extraction without pretending the normalized catalog already contains
these relationships.
