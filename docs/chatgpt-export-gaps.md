# ChatGPT export: remaining gaps

This document tracks the limits of Pharos's ChatGPT export ingestion. It separates
facts absent from the export from improvements Pharos could implement. The
November 12, 2026 [audit](chatgpt-export-audit.md) inspected one consumer export;
other accounts and export versions may differ. The companion
[unmodeled-data document](chatgpt-export-unmodeled-data.md) records data kept only
as evidence or intentionally excluded.

## Implemented parity

Settings → Sources offers an opt-in Downloads watch, Check Downloads now, recent
imports, collection versus ingestion progress, Stop, and Try again. Manual ZIP
import uses the same managed collector. Detection accepts the observed ChatGPT
export filename shape (hex account identifier, timestamp, hex export identifier,
optional browser duplicate suffix); renamed ZIPs require manual selection.
Instructions say to allow about 24 hours for delivery, with variable timing. This
is guidance based on observed delivery, not an exact service guarantee.

The collector validates paths, size limits, duplicate members, ZIP CRCs, the
producer's file inventory when present, and every conversation shard before
publishing. Originals remain untouched. Sanitized snapshots retain selected raw
export evidence, use the original ZIP's SHA-256 as their filename, and record the
retained ZIP's SHA-256 and excluded filenames in `chatgpt-export.json`. One stable
`chatgpt-imports` source updates conversations by their native identities. A busy
library queues collection; interrupted indexing can retry from the snapshot.
Retries revalidate the saved receipt/checksum and recollect a missing or damaged
snapshot from the original ZIP when it is available.
Turning the watch off stops new discovery; queued/manual imports have separate
Stop controls. Already collected snapshots remain when watching is disabled or
a job is stopped.

The existing parser retains message trees and selected branches, prose,
transcriptions, exported reasoning/recaps, explicit tool calls/results when
actually supplied, injected context and attachment descriptors. The reader adds
export limitations, saved conversation settings, sharing presence, reference and
search-result links/snippets, voice transcription direction, and local image/audio
previews. Full message envelopes and settings remain accessible as raw events.
Library files naming a known conversation appear as attachment evidence even
when their origination message is absent. They are not fabricated user messages
or tool executions. Captured bytes can be served after Downloads is removed,
including from completed library transfers and encoded capture files.

## Source gaps: Pharos cannot reconstruct these

| Desired evidence from coding transcripts | Export limitation | Current treatment |
| --- | --- | --- |
| Tool execution graph, exact inputs/outputs, success/failure and request boundaries | Bundled consumer-export documentation explicitly omits system/developer/tool messages and assistant messages addressed to tools. Search results and tool icons can survive independently. | Retain references as references. Parse explicit legacy tool records when present; never infer calls, failed calls, subagents or complete tool coverage from citations. |
| Full prompt context and custom instructions | Omitted system/developer messages can hide instructions that affected the answer. Account memory is not a per-request context trace. | Preserve injected context only when supplied. Reader states the export boundary. |
| Token/cache/reasoning usage and API cost | No corresponding usage evidence in the audited export. | Unknown; do not estimate token counts from text or price an opaque model alias. |
| Exact request latency, throughput and execution duration | Message/conversation timestamps and “Thought for…” recaps are not request start/end telemetry. | Retain timestamps and recap labels; no conversion to measured latency. |
| Precise model, effort and service tier | Most audited messages use `bidi`, an opaque voice model label. Actual effort/tier fields are absent. | Preserve source labels and any explicit recorded settings; do not substitute an underlying priced model. |
| Repository, checkout, branch, commits, patches and machine identity | The export supplies no reliable Git/worktree or execution-machine association. | Leave missing. A mentioned filename or code block is not an observed edit. |
| Assistant speech waveform | The audit has assistant transcriptions but no assistant audio payloads. | Display transcription; a saved voice choice does not create audio. |
| Missing attachment bytes and origin records | One Library file has metadata but no payload; some origin message/conversation IDs do not exist in the export. | Show missing-byte descriptors in known conversations. Retain unrelated inventory as raw evidence; do not manufacture conversations. |
| Proof of a complete account export | CRCs and manifest sizes prove file integrity, not that every account record was exported. | Describe a snapshot, never a live or guaranteed-complete mirror. |

## Implementation gaps: actionable future work

- **Rich references:** URLs, attribution and snippets are readable; inline citation
  marker placement and exact footnote/entity/widget rendering are not reproduced.
  Dynamic widget definitions and compressed weather payloads remain inert source
  evidence. No remote page is fetched to fill an export gap.
- **Media:** previews support local raster images and audio up to 64 MiB per
  file. Larger payloads stay preserved but are not served by the preview endpoint.
  Video/frame sequences, synchronization, word-level audio timing, generated-media
  lineage and document extraction/OCR need dedicated presentation. A compound
  attachment resolves its primary audio/video pointer; nested frames are retained
  in its raw descriptor rather than individually playable events. Playback resolves
  the current capture, completed transfer or configured source; it does not search
  historical capture generations for a file no longer mapped there.
- **Library and memory browsing:** there is no account-wide asset or memory
  browser. Unscoped files, memory summaries and category sidecars remain in the
  preserved snapshot. There is no evidence-based way to attach them to every chat.
- **Source formats:** managed collection currently accepts completed ZIPs. Legacy
  folder/JSON sources still use the generic source path. Folders with a producer
  inventory retain declared members except excluded account files; older folders
  retain known sidecars and exporter-shaped payloads. A JSON-only source cannot
  promise media preservation. ZIPs wrapped in a directory can expose conversation
  shards but sidecar linking currently expects the observed root-level layout.
- **Acquisition:** Pharos does not request exports, read export email, authenticate
  to ChatGPT, or click email download links. This ZIP flow has no Claude-style
  browser-profile or multipart-download controls because there is no manifest of
  signed download links to follow in the observed ChatGPT format.
- **Identity:** native conversation IDs keep repeated snapshots stable within an
  account/source. The managed source currently uses the existing default account
  namespace. A multi-account picker and explicit cross-account migration require
  separate work; do not assume separate exports are separate identities.
- **Snapshot chronology:** the observed producer inventory has no account-wide
  export timestamp. Snapshots use the download's modification time for precedence.
  Re-downloading a known snapshot preserves its original time, but a previously
  unseen older export downloaded later may be treated as newer source evidence.
  There is no snapshot-priority/rollback control yet.
- **Snapshot management:** collection copies a whole ZIP; interrupted copying
  restarts that copy. There is no per-member collection progress or snapshot
  deletion/retention UI. Recent-job controls are host-local; preserved snapshots
  travel with normal captures. Removing a source does not delete library history.

## Verification

Synthetic Go coverage exercises collection, exclusions, checksums, malformed later
shards, unsafe ZIP paths, duplicate members, CRC failures, cancellation, consent,
queue/retry/stop, idempotence and media serving from offline captures with ranges.
Reader and setup unit tests cover evidence presentation and progress. Focused
browser coverage checks Sources/reader layout, polling focus and reference
URL-state restoration. The real export is checked with a disposable, read-only
probe; private text and payloads are not repository fixtures.
