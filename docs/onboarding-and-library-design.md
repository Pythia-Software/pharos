# Pharos library implementation design

Status: implementation design following the agreed
[plan](onboarding-and-library-plan.md) and
[repository audit](onboarding-and-library-audit.md).
The [clickable prototype](mocks/onboarding.html) explores the user experience
with simulated data. The production implementation is in this branch. The build stages below capture
its review boundaries; [library management](library-management.md) documents use.

## Product contract

An application installation discovers and connects to named libraries. A library
is independent of app location and includes its saved user choices. History can
move or copy between libraries; importing history does not merge their settings.
An app installed locally may capture pending history locally while an archive
is disconnected. A portable app installs no local helper, retains no local
history or history-derived cache, and does nothing while absent. Small documented
update/window/registration settings may remain on each Mac.

Moves remove delivered source history after verified durable acceptance. There
is no recent-history cache and no requirement to configure a backup. Full
restoration carries saved user state; importing backup history into an existing
library preserves the source backup and the target's independent settings.

## Identity and storage ownership

Use opaque identities with separate purposes:

| Identity | Lifetime and responsibility |
| --- | --- |
| Library ID | Logical library lineage, created at initialization or explicit adoption, independent of backup configuration |
| Writable instance ID and recovery epoch | Distinguish writable copies and recovery boundaries; used in receipts and active service ownership |
| Installation ID | A particular installed application on this Mac, preserved across an app update; identifies connection and MCP settings |
| Host ID | Existing capture/source provenance; source definitions remain scoped to this identity inside each library |
| Service lifetime ID | A single running service with its bound endpoint and runtime credential; prevents stale endpoint reuse |
| Transfer ID | A resumable operation whose source snapshot, target identity, verification, and cleanup progress are recorded |

A library descriptor stores name, identity, format/writer compatibility, and
relative component locations. Existing catalogs and per-host files remain in
the library. User-visible names and mount paths are not identities. Supported
restore and copy operations explicitly create a writable-instance transition;
unmanaged Finder copies require detection and an explicit choice before writes.

New local libraries live under `~/Library/Application Support/Pharos/Libraries/`
by default. External libraries live in a chosen dedicated folder. No mutable
library data belongs beside an app in Applications. App location is selected
separately, including an SSD app beside or elsewhere from its library.

The Mac's registry stores library bookmarks/volume identity and installation
records. Host files remain in libraries in every mode. Secrets and OS grants
are not copied as source authorization. Portable previews use a nonpersistent
website data store; durable user choices still go through the library API.

Application installation resolution must survive Sparkle replacing the bundle
and an SSD mounting under another name. Resolve the registry entry using its
saved app location plus volume identity; do not write an identity into a signed
bundle or derive identity from the release version. Moving or duplicating an
app must have an explicit reconciliation path rather than share an installation
record merely because its name is Pharos.

## Application connection and MCP

Each installation record on each Mac stores `mcp_library_id` and a primary
connected library. The first registered usable library becomes the assignment
once. Merely discovering a temporary restore candidate is not registration.
Opening a different library changes the connection, not the MCP assignment.
Only MCP settings can explicitly change the assignment later. Assignment does
not itself grant source access or enable a previously declined MCP integration.

The primary connection is an explicit application state, not whichever window
most recently received focus. A background capture or destination service does
not count as the app being connected to its MCP library. Other library services
may continue performing explicitly authorized jobs while MCP is unavailable.
An app can expose several windows, but switching its primary connection is one
visible operation; future pinned secondary windows must not switch it by focus.

| App state | MCP behavior |
| --- | --- |
| Primary connection equals assigned library and service is ready | Answer from that library, subject to existing access controls |
| Primary connection is another library | Return an unavailable tool error naming both libraries and the explicit settings action |
| Assigned library is disconnected, upgrading, locked, or being released | Explain that state; serve no library data |
| Application is stopped | Report the app as offline; do not start another catalog implicitly |
| User explicitly changes assignment | Increment connection policy generation; subsequent requests use the new assignment only if it is the primary connection |

Use an app-scoped MCP entry point whose registration identifies the installation,
not a config file beside the app or a hard-coded port. It contacts the app's
live broker. Gate every lookup by assignment, primary connection, service
lifetime, and connection policy generation. The broker and target service both
validate this scope. Recheck before returning a completed result after a switch;
cancel or discard a result whose policy generation became stale. Library data
must never be exposed while forming an error about another library.

Managed MCP has no direct-catalog fallback. Existing explicitly configured CLI
MCP mode is a separate compatibility path that must not bypass a registration
managed by an installation. Migration distinguishes those modes and rewrites
managed registrations deliberately. Ask Pharos keeps its existing private lookup
protocol, with each chat bound to its starting library and privacy exclusions.

## Runtime resources and ownership

Bind each library service to an available loopback port using the actual
listener; do not probe a free port, close it, and bind again. Publish its bound
endpoint, lifetime ID, and private runtime credential atomically after ownership
and readiness checks. A registry record alone is not proof of a live service.

One service owns writes to a writable library instance. GUI, CLI, capture,
indexing, backup, and transfers coordinate through that owner. Existing release,
capture, index, and update leases remain useful, but library ownership must not
be inferred from executable directory. Canonical paths and filesystem identity
checks prevent aliases from opening competing owners of the same catalog.

Cookies and website/session storage require library-instance and service scope.
Dynamic ports are not persistent identity. Per-install update coordination must
quiesce every process using that installation's binary, while release or eject of
a data volume targets only the affected libraries and clients. An app in
Applications remains open after releasing an external library. An app running
on the ejected SSD must itself exit without leaving a helper behind.

## Transfer boundary

Take a consistent source catalog snapshot with SQLite's backup API, then build
a versioned transfer inventory tying archived history and its retained evidence
to that snapshot. Coordinate capture manifests and pinned payload versions so
their references cannot be pruned during inventory or copying. Reject unsupported
formats before source changes.

The inventory includes catalog-only archived sessions, provenance and authority,
messages and retained tool/evidence envelopes, referenced artifacts, and privacy
exclusion identities needed to prevent private re-ingestion. Original provider,
account, native conversation/message identity, and source versions survive.
Destination-local surrogate IDs and paths are mapped explicitly. Reuse the
existing copy authority and anti-regression rules rather than comparing capture
wall times across hosts or adding derived totals.

Raw captures are the efficient transport when sufficient. A consistent catalog
snapshot remains necessary for history absent from retained captures. Do not use
the existing canonical export as a lossless interchange until an entity and
field coverage audit proves that it carries all retained information. Retain the
source snapshot and postpone cleanup when any required entity is unsupported.

User preferences, bookmarks, dashboards, private Ask chats, source grants, and
schedules do not merge during initial history import. Native privacy exclusions
are separately classified and must remain effective. Full-library recovery is
the operation that carries the entire library's saved state.

### Durable operation states

1. **Plan:** identify source snapshot, target writable instance/epoch, payload
   revisions, required space, and intended move or copy. Show counts and storage.
2. **Copy:** stage immutable payload at the target; retry individual items using
   their recorded content identity. Preserve existing destination generations.
3. **Reconcile:** apply history and a merged manifest through the target owner.
   Retain overlap rules; incomplete or older input cannot replace richer history.
4. **Verify:** check all payload digests, referential coverage, supported entity
   coverage, target identity/epoch, and durable acceptance of catalog-only data.
5. **Receipt:** record the target's durable accepted inventory and then make the
   source receipt durable. Repeated acceptance of the same inventory is safe.
6. **Cleanup:** for moves, remove only source revisions covered by the receipt.
   New or changed source revisions remain pending. Preserve user state, capture
   checkpoints, and privacy protections. Complete interrupted cleanup on retry.

An operation is verified only when its retained representation remains safe
under normal destination retention. Copying a soon-to-be-pruned database
snapshot is not sufficient proof. Indexing may be deferred only when retained
evidence and its retention guarantee are themselves sufficient.

Cleanup is per verified inventory, not a blanket deletion of a library folder
or catalog. Unshared evidence payload may be reclaimed after reference checks.
Preserve the small delivery ledger needed to avoid recapturing unchanged source
history. New revisions can return to pending without recreating all delivered
payload. For a bookmark retained in the source, keep a destination reference or
clearly unavailable target; do not retain the delivered transcript for the link.

## Restore and compatibility

Restore into a separate candidate, validate the backup's completeness, catalog,
referenced payload and compatibility, then register it as a new writable
instance. Capture, delivery and schedules start inactive. Full recovery carries
user state; history import into an existing library uses the transfer boundary
without source cleanup. Cloud backup is shown as future capability until a
provider and key-recovery contract actually exist.

A new recovery epoch invalidates previous delivery proof for that writable
target. Recapture or redelivery requires surviving evidence. If the only copy
was lost after a move and before a backup, a restore cannot recreate it. Show the
recovery point and known delivery gaps. Do not silently retarget pending delivery
to a restored candidate with the same display name.

Keep existing schema guards and add explicit manifest/transfer/writer
compatibility. Inspect compatibility before background writes, initialization,
or target cleanup. App rollback cannot roll library data back. Conversion needs
resumable progress and a recoverable checkpoint without requiring an external
backup destination. Insufficient checkpoint space prevents that conversion,
not ordinary compatible library use.

## Storage and disconnected states

Calculate estimates from the selected sources and actual library inventory.
Show source evidence, browsing catalog, retained generations, transfer staging,
and required free margin separately. The mock's example uses 17.5 GB for two
generations of an 8.7 GB database; it does not claim every user needs this space.
Pause before space is exhausted; never prune the only representation of pending
history to satisfy a retention cap. Keep the last successful checkpoint.

When an SSD returns, resolve volume plus library identity even if its mount path
changed. Refuse same-name substitutions. Unexpected removal invalidates service
handles; reconnect creates a new service lifetime after recovery checks. Explicit
whole-library delivery can resume without changing UI or MCP assignment.

## Build stages and acceptance gates

| Stage | Change | Required evidence before proceeding |
| --- | --- | --- |
| 1 Library registry and adoption | Descriptor/instance identity, host files for all libraries, independent app/library selection, legacy adoption | Fixtures for moved paths, missing volumes, duplicate copies, rollback of failed adoption, and unchanged original installs |
| 2 Runtime isolation | Writer ownership, dynamic listener publication, scoped credentials/storage, data-volume release | Two independent libraries coexist; aliases cannot compete; unrelated service on an old port is rejected; eject closes the correct catalog |
| 3 App connection and MCP | Per-install/per-Mac assignment defaulting once, explicit settings change, broker policy gate | Switch, reconnect, restart, update, and two installed copies; wrong-library and late responses never leak data; no direct fallback |
| 4 Portable storage and onboarding | Source selection, estimates, nonpersistent portable web storage, optional backup, review and library picker | Clickable-flow feedback; native/browser checks of permissions, focus, no residue of history, and disconnect behavior |
| 5 Lossless history copy | Entity coverage inventory, versioned transfer snapshots, manifest merge, catalog-only import, verification | Old/new/divergent/duplicate fixtures; privacy exclusions; catalog-only sessions survive; preferences remain separate; interruption/retry is idempotent |
| 6 Verified moves and pending capture | Delivery ledger, immutable revision cleanup, safe low-space pause, reconnect delivery | Crash before/after receipt, source changes during copy, shared artifacts, unchanged re-capture, failed verification; no early cleanup and no retained delivered cache |
| 7 Recovery and rollout | Candidate restores, epoch transition, restore rehearsal, legacy backup compatibility, guides | Same-name/older restore cannot validate stale proof; original backup and production library remain untouched during rehearsal |

Use the repository's narrow Go/JS tests while iterating and its fast Go tier on
every change. Before a PR, run the full Go suite, web checks, and full JS matrix.
Use native release/eject/update harnesses only when their code changes. Add
data-safety fixtures to existing test files, including slow coverage where
needed. No Query Table package patches or generated bundle commits are required
by this design.

## Prototype review

The HTML mock runs entirely in memory and performs no capture, filesystem writes,
provider calls, or real MCP registration. It covers new/open/recover paths,
local/portable app placement, source storage estimates, optional backup, setup
review, library switching/disconnection, explicit MCP assignment, verified moves,
and restored recovery candidates. A review panel exposes failure scenarios so
the intermediate states can be inspected without timers or production data.

Record feedback against the product promises and build-stage acceptance gates.
Prototype behavior is not evidence that production transfer or durability has
been implemented.

## Implementation record

- `library_registry.go` defines library descriptors, recovery lineage, per-Mac
  app registration and stable volume-relative installation discovery.
- `application.go` and `application_jobs.go` implement the primary connection,
  allocated service endpoints, authenticated proxy, MCP gate, ownership and
  asynchronous recovery/delivery jobs. Source and target services pause during
  offline transfer; native Finder release targets the affected data volume.
- `library_transfer.go` retains sanitized catalog snapshots and verified evidence,
  maps native identities, applies copy-version guards and preserves conflicting
  snapshots. Imports can carry earlier immutable imports without flattening them;
  evidence lookup follows verified inventories through those retained roots.
- Source cleanup atomically publishes a fresh catalog containing user state and
  small checkpoints, with an applied marker in the same SQLite commit. A durable
  pending receipt coordinates payload cleanup and resume. Content fingerprints
  and conversation fingerprints prevent unchanged history from repopulating
  pending when a shared source container changes. Pending raw generations remain
  until verified delivery; insufficient space pauses capture.
  Fingerprints reuse the complete normalized ingest input, including metadata,
  instructions and aliases. Cleanup copies these small hashes through SQL without
  loading transcripts. Workspace metadata has independent fingerprints and merge
  eligibility, so metadata-only changes do not rehydrate delivered transcripts.
  TL1 installation eligibility is frozen before any table advances its version.
  Cleanup preserves saved computed-column definitions with other user state.
  Pending retention protection lives in the library
  descriptor, so scheduled and CLI captures obey it even when delivery is paused.
  A different target instance invalidates obsolete delivery suppression; recovery
  clears it and stale indexing shortcuts before publishing a candidate.
- `library_recover.go` restores backups or self-contained copies into separate
  inactive candidates. Each candidate receives a new logical identity and
  writable instance while recording its original lineage and recovery point.
- The native managed window uses nonpersistent WebKit storage. Update coordination
  recognizes broker child services and the managed MCP entry point; portable quit
  stops those MCP processes. The download guide now separates app placement from
  library placement. Legacy explicit launch plans and standalone CLI use remain.

Validation includes lifecycle fixtures for identity, copies, duplicate and newer
revisions, compound keys, catalog-only history, independent preferences, private
Ask exclusions, paused recovery, stale receipts, interrupted cleanup, source
changes, hash-based recapture, MCP switches and late responses. Browser coverage
checks the production library screen at desktop and narrow widths. An isolated
live-broker smoke test exercises two services, credentials, MCP assignment, the
real proxy scripts and complete shutdown. Native eject and update harnesses use
their own disposable disk image. No personal library is used for these checks.
