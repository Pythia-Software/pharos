# ChatGPT export audit, November 12, 2026

The fresh consumer export downloaded on November 12 contains enough information
to import the chat history and substantially improve its attachments, references,
and provenance. It does **not** contain the full execution transcript available
from coding agents. At the audit baseline, the largest missing Pharos feature was a linked asset inventory;
the largest source limitation is the omission of tool, system, and developer
messages from this export format.

This is an audit of one real export, not a claim that every account exports the
same categories. No private conversation text, filenames, account details, or
original media have been added to repository fixtures. The original ZIP was left
unchanged. Inspection used ZIP members directly, including the export viewer's
bundled field documentation; its JavaScript was not executed. Private inventories,
field/type counts, and the temporary parser probe output are in
`.context/chatgpt-audit/` in the Munich workspace.

## Implementation follow-up

The inventory and parser counts below describe the **pre-implementation baseline**.
Managed ZIP ingestion now preserves and links conversation assets, retains full
message/settings evidence, displays references and voice context, and offers
Downloads monitoring and resumable ingestion. See the authoritative current
[remaining gaps](chatgpt-export-gaps.md) and
[data outside the normalized model](chatgpt-export-unmodeled-data.md).

## Inventory and integrity

The ZIP is 35,798,280 bytes, containing 80 files and 50,375,017 uncompressed bytes.
Every member passed ZIP CRC validation. All 79 members named by
`export_manifest.json` are present and match their listed sizes. The manifest is
the remaining member and identifies itself separately. This proves the downloaded
archive matches its inventory; it does not prove that the producer exported every
record from the account.

| Input | Observed contents | Current Pharos handling |
| --- | --- | --- |
| `conversations.json` | 34 conversations; 863 nodes, including 34 empty roots and 829 messages; May 26–November 10, 2026 | Indexed |
| 67 `.dat` payloads | 49 WAV, 12 PNG, 2 JPEG, 1 GIF, 3 UTF-8 text files; 36,215,221 bytes | Kept inside an imported ZIP; payloads are not linked to playable/viewable attachments |
| `conversation_asset_file_names.json` | 53 original filename mappings: 49 WAV, 2 JPEG, 1 PNG, 1 Markdown | Ignored by extractor |
| `library_files.json` | 19 file/artifact records, including generated images and text | Ignored by extractor |
| `conversation_summaries.json` | Four account-level memory summaries | Ignored by extractor |
| `shared_conversations.json` | One shared-conversation record | Ignored by extractor |
| `user_settings.json` | One settings record with 44 settings, feature state and announcement history | Ignored by extractor |
| `user.json` | Account identity, email, birth year and subscription flag | Ignored by extractor, but kept in a whole-ZIP capture |
| `ads.json` | Four empty lists | No observed ad records to import |
| `sectioned_conversations.json` | Empty list | No observed sections/projects to import |
| `sites/export_manifest.json` | Request timestamp; no artifacts, warnings, omissions, redactions or incomplete corpora listed | Ignored by extractor |
| `index.html`, `THIRD_PARTY_NOTICES.txt` | Self-contained export viewer, including format documentation, and notices | Raw evidence only |
| `export_manifest.json` | File inventory, sizes, logical-file/shard mapping, format version 1 | Ignored by extractor |

The viewer includes documentation for other export families, including Codex,
Pages, Sites and scheduled tasks. Those bundled documentation strings are **not**
records from this account. There are no corresponding data files in this ZIP.

The Sites manifest records a request at November 11, 14:00:37 UTC. The downloaded
file's local modification time is November 12, 14:12:31 UTC: about 24 hours and
12 minutes later. This is consistent with the user's observed 24-hour delivery,
but does not establish the exact email arrival time or a universal guarantee.
The import instructions now say to allow about 24 hours, with variable delivery.

## What the existing parser actually produces

A temporary, read-only Go probe ran the production ChatGPT adapter against this
ZIP. It produced 34 workspaces and 34 conversations, with 812 normalized records:

| Record kind | Count |
| --- | ---: |
| Message | 559 |
| Reasoning | 139 |
| Metadata | 62 |
| Attachment descriptor | 52 |

The 829 source messages contain 240 user and 589 assistant messages. Normalized
record counts differ because the adapter expands attachments and collapses some
reasoning recap nodes. Sixty-seven recap nodes have no separate normalized row;
two image-only nodes survive as attachment parts rather than a row under the
original message ID. Standalone recap records retain a hidden-reasoning marker,
but not their displayed duration text. These counts must not be presented as
missing human prompts without accounting for that transformation.

All conversations received the parser's current `complete` coverage status and
a model. That status says the parser accepted their structure; it does not imply
complete execution evidence or preservation of every metadata field. Workspace
metadata is empty. Only the 52 attachment descriptors retain `RawText`; ordinary
message metadata without usage evidence does not survive in normalized records.
The original JSON remains recoverable from the ZIP capture.

The parent links and `current_node` are sufficient to rebuild order even though
this export omits `children`. Every message is on its conversation's current path;
this particular sample has no alternate branches. Existing synthetic tests cover
branching, but this export cannot independently validate that behavior.

## Data that fits existing formats with better extraction

| Export evidence | Existing home | Required work or limitation |
| --- | --- | --- |
| IDs, titles, timestamps, roles and model slugs | Workspace, conversation and message records | Already read; retain export provenance as well |
| 443 audio-transcription parts: 195 user, 248 assistant | Existing message text; recorded authorship/modality evidence | Text is already extracted. Preserve `direction` and voice flags so dictated writing can be distinguished from typed writing using source evidence |
| 49 user WAV recordings, totaling 405.7 seconds | Attachment records plus retained evidence | Resolve payloads, original filenames, audio bounds and MIME; enable local playback. No assistant waveform is present |
| Three in-message image pointers | Attachment records plus retained evidence | Resolve the actual bytes and dimensions; all three payloads exist |
| Two `metadata.attachments` records | Existing attachment kind | Parse metadata attachments as well as `content.parts`; deduplicate their overlap with other file references |
| 169 thoughts messages, with 183 thought blocks | Existing reasoning text/summary/hidden markers | Preserve supplied content, summaries and source distinctions; 29 messages have empty thought lists. Do not manufacture unavailable reasoning |
| 99 reasoning recap nodes, 95 with nonempty text | Recorded message evidence, or an explicit source-reported duration field | Preserve the label even when avoiding duplicate reasoning rows. A displayed “thought for” duration is not measured request latency |
| 1,847 search entries in 610 domain groups on 29 messages | Message evidence; source attachments/references | Keep URL, title, snippet and attribution. These are result entries, not 1,847 tool calls or necessarily distinct URLs |
| 190 inline content references, 30 messages with nonempty lists | Message evidence and reader source links | Resolve inline markers to citation metadata rather than leaving opaque markers in text |
| Two past-conversation citations | Evidence-backed related-conversation links | Both targets exist in the export. Preserve their recorded citation and quoted context; do not infer delegation |
| 19 Library records, with source conversation/message IDs where supplied | Attachments and workspace evidence for proven links | Join conservatively, retain standalone assets separately, and preserve unknown associations |
| Four memory summaries | Retained account/source evidence | They have opaque summary IDs and embedded `memory`, `reason`, `confidence` fields; they are not keyed by conversation ID. Do not inject all four into every conversation |
| Archived, starred/pinned, memory-scope, study-mode and voice fields | Namespaced workspace metadata/native context | One pinned/starred conversation and five saved voice settings are present. These are source settings, not Pharos preferences |
| One sharing record | Conversation metadata/raw evidence | Link by conversation ID; retain share identity and anonymity without treating it as another conversation |
| Export file inventory and Sites request timestamp | Collection receipt/provenance evidence | Validate members, preserve source date and receipt, distinguish archive completeness from product fidelity |

`content_references` includes 141 grouped-webpage references, 24 source footnotes,
10 entities, five declarative interactive displays, four follow-up actions, three
hidden placeholders, two client-defined widgets and one image group. Source links
can be represented now as evidence, but Pharos has no shared typed citation or
interactive-card format that preserves all these relationships and renderings.

## Source gaps: data Pharos can use from coding transcripts but this export lacks

| Desired evidence | Finding in this export | Consequence |
| --- | --- | --- |
| Explicit tool calls, arguments, results, status and exit codes | No tool-role messages or recipient fields. Bundled format documentation explicitly says consumer exports omit tool messages and assistant messages addressed to tools, along with system/developer messages | Preserve search/citation evidence without fabricating execution records. Tools and results are now declared varying rather than always supported in the source capability UI |
| System/developer instructions, loaded skills, tool schemas and the full prompt context | No system/developer messages or custom-instruction/memory content messages here | Cannot reconstruct the actual model input. Account memory summaries do not establish which memory was used on a turn |
| Input/output/cache/reasoning token counts and cost | No usage records or token counters | Usage and cost remain unavailable; never substitute zero or estimate from visible text |
| Precise underlying model identity | Model labels are present, but 579 message records use the voice label `bidi` | Preserve the recorded label; it does not identify the underlying model or justify substituting a priced model |
| Effort, service tier, context budget and compaction | No recorded settings/events for these fields | Keep unknown; model name and reasoning text do not establish them |
| Request boundaries, start/finish latency, token streaming and stop/error details | Message creation times and recap prose only; no request IDs, processing status or finish details | Can chart message activity. Cannot claim measured request latency, completion success or precise generation time |
| Repository, working directory, branch, commits, file changes, PR outcomes | No structured execution/Git evidence | Remain unassociated. A code snippet, uploaded Markdown file or repository mention is not a recorded workspace change |
| Parent/child agent executions and delegation | No recorded agent-session relationships | Conversation tree ancestry remains message ancestry; it does not create child agents |
| Initiating device/client and execution machine | No initiation evidence | Do not assign the importing Mac or assume every chat began on the web |
| Full voice recording | 443 transcript segments, but only 49 user recording payloads; no assistant recordings and no video frames | Preserve available recordings and text, with explicit modality coverage |
| Complete generated-file lineage | 14 Library records refer to absent message IDs; two refer to conversations absent from the chat file | Keep unresolved references as unresolved; do not create missing assistant messages or conversations |
| All Library file bytes | 17 exact-ID payload matches, one hash-verified inline text payload under an encoded filename, one GIF record without a payload | Eighteen of nineteen Library records have payloads. The missing GIF is in state `created`; do not present it as downloadable |

Library metadata for the GIF also refers to a spritesheet and speed variants that
are not present as separate ZIP members. The existing GIF is useful, but this is
not a complete set of its generation assets. Metadata availability, byte
availability and source-message availability need separate coverage states.

Together, the conversation references and Library inventory account for all 67
payload files. The 53 original-filename mappings also resolve to existing ZIP
members. This is enough to build deterministic local asset resolution without
contacting the original file service, while keeping the unresolved metadata-only
GIF and its missing derivative assets explicit.

## Additional data without a complete Pharos model

- **Standalone asset Library and version lineage.** Generated images, Markdown,
  an inline text artifact marked `site`, MIME/original filenames, file revisions,
  timestamps, backing stores, visibility and generation metadata outlive individual
  messages. Attachments can cover proven message links, but do not fully model an
  independent asset catalog or its versions. One record is a text artifact, not
  an exported deployable Site with source, deployment and access configuration.
- **Account memory.** Four summaries have their own IDs, reasons and confidence.
  They need a source/account scope rather than pretending to be human messages,
  conversation summaries or instruction-load events.
- **Typed citations and rich cards.** The export includes web provenance, entity
  details, maps/place data, weather state and declarative UI definitions. Five
  compressed widget values decode as gzip/base64url JSON weather state; one map
  state is already JSON. Raw preservation and safe text/link fallbacks are
  possible; exact interactive rendering needs an explicit format and renderer.
  Exported actions and UI definitions must remain inert evidence.
- **Source product state.** Pins, sharing, selected voice, memory permissions,
  account settings and feature/announcement state have no corresponding Pharos
  controls. Preserve useful provenance as source metadata; never apply exported
  settings to Pharos. Account identity/contact details require the same explicit
  retention decision already made for Claude account records.
- **Other product families.** No Pages, Codex, scheduled-task, ad-history or Sites
  artifact corpus is present to design against. Empty sections do not demonstrate
  support for those formats or prove that the account never used the features.

## Original implementation recommendations (historical)

1. **Managed intake matching Claude's applicable controls.** Add a ChatGPT source
   with opt-in Downloads checks, check-now, stop/retry, verification, resumable
   receipt, progress, source totals and browse links. This export is one complete
   ZIP containing its inventory, so it does not require Claude's multipart link
   downloader or account browser-profile controls after download. Use a narrow
   observed filename candidate rule plus manifest/content validation; no broad
   scan of arbitrary Downloads files. Keep snapshots and historical IDs stable.
2. **Preserve the supported evidence consistently.** Currently ZIP capture keeps
   everything, including account records, while folder capture keeps only
   conversation JSON. Managed ZIP/folder import must use the same explicit
   retention policy and retain all selected payloads and inventories for offline
   reindexing. Record intentional exclusions instead of silently dropping data.
3. **Close extraction gaps using existing records.** Add namespaced source
   metadata, complete raw message envelopes, metadata attachments, resolved media,
   voice provenance, recap evidence and citation/source attachments. Keep source
   counts separate from normalized message/attachment counts. Bump the extractor
   version and extend synthetic capture/reindex/conformance fixtures.
4. **Add models only where required.** Scope account memory and standalone assets
   explicitly, then define a shared citation/asset-lineage format if the reader
   needs more than evidence and attachment fallbacks. Represent unresolved assets
   and missing execution evidence without filling the gaps with inferred events.

The audit itself left the real download and the user's library unchanged.
The subsequent implementation is described in [remaining gaps](chatgpt-export-gaps.md)
and [data outside the normalized model](chatgpt-export-unmodeled-data.md). These
supersede the baseline handling and roadmap above.

## Validation after implementation

A production-collector/parser probe against the same ZIP, using a disposable test
library, now produces 34 workspaces/conversations and 930 normalized records:
559 messages, 139 reasoning records, 165 metadata records (including 34 export
settings records), and 67 attachments. Of the attachments, 66 resolve to supplied
bytes and one explicitly reports missing bytes. All 443 transcription parts
survive in raw message evidence, and 29 messages retain search-result groups.
The remaining unscoped payload stays in the raw snapshot; there is no standalone
asset catalog to assign it to. Three account files are intentionally excluded
from the managed copy. The original ZIP remains unchanged.
