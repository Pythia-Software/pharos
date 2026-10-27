# Libraries and application setup

Pharos.app can run from Applications or from an SSD. Its library is a separate
folder. The native app opens the standard Pharos header and Home view. First
launch introduces Pharos and guides new setup one decision at a time. Existing
history and backup recovery are separate branches. If no library is connected,
the startup dialog offers registered locations and recovery.

## Change the current library

Open **Settings → Libraries** to see the current library, its folder, and the
other registered libraries on this Mac. Choose **Switch to library** to change
which collection Pharos browses. Unavailable libraries stay visible; reconnect
their drive before selecting them. Pharos saves pending UI session changes
before switching and keeps you in Settings. The selection is remembered on the
next launch.

**Open a library…** expands a form on this page for choosing a library folder or
pasting its `library.toml` path. **New library…** expands a form for its name,
storage and parent folder, with the resolved destination shown before creation.
The standard local location is proposed by default. When the default name is
already registered, Pharos proposes the next available numbered name. Creating
a library takes you to **Settings → Sources** to choose what to add.

These actions stay in Settings and use inline forms. Each library keeps its own
source choices, saved views and preferences. MCP assignment is a separate,
explicit choice in **Library used by MCP**. Creation, opening and switching
wait until an active transfer or recovery finishes.

## Choose locations

The setup guide asks, in order:

1. **Your Macs:** one Mac or several, using illustrated choices.
2. **Storage:** introduce the library as a folder of files and recommend this
   Mac for one-Mac use, or an attached SSD for several Macs. Either choice is
   available; choosing several Macs does not configure transfers automatically.
3. **Name and folder:** name the library and choose its parent folder. Local
   storage can use the standard Pharos Libraries folder; SSD storage requires
   an explicit folder on the drive.
4. **Review:** show the resolved folder and choices before creating files.

Back preserves answers. Changing the Mac count resets the storage recommendation
and parent folder; changing storage clears the previous parent. Opening existing
history skips these creation questions. Recovery instead asks for the backup,
a new destination, then review. Source approval follows creation, with optional
backup information disclosed separately.

Choosing **On this Mac** uses
`~/Library/Application Support/Pharos/Libraries/<name>/` (or the configured support
folder), even when Pharos.app itself runs from an SSD. The parent folder picker
creates `<name>/` inside the chosen parent. No library files are installed in
Applications by default. Older callers that omit an explicit storage choice
retain the portable app's default of `Libraries/<name>/` beside its bundle.

A library keeps these components together:

```
My Library/
  library.toml
  pharos-library.json
  hosts/<host-id>.toml
  catalog/catalog.sqlite3
  captures/
  preserved/
  staging/
  deliveries/                 small delivery receipts, when used
```

The app registration, primary connection and MCP assignment are stored under
`~/Library/Application Support/Pharos/applications/<installation-id>/`. Runtime
endpoint files there are private, temporary credentials. Each service binds an
available loopback port. Configured legacy ports remain supported for standalone
CLI use; the managed app does not depend on them.

The installation identity uses this Mac plus the app's volume and relative
location. An update replacing the bundle keeps the identity. A different app
location is a separate installation: open its libraries and review its MCP
settings. Re-register agent commands after moving an app to another location.

## Sources and preferences

After creating a library, Pharos checks common application and data-folder
locations on this Mac. Detected tools appear selected in the guide; remaining
tools are available under **Other tools**. This initial presence check uses
metadata only, without parsing conversations, opening databases, or running a
shell. Continue to inspect the selected tools' history locations, including
their relevant environment overrides. Review discovered
folders, exclude individual profiles, or check an additional folder for a
selected tool. The final review lists the approved locations and estimated
source-file size before **Add and capture** saves the choices and copies history.
Indexing is a separate next step. **Set up later** saves an empty host choice
without discovering or capturing sources. Settings → Sources reopens this same
guide. Conductor and TL1 do not implicitly select Claude Code or Codex.

Source
bindings remain inside `hosts/<host-id>.toml`; carrying the library to another
Mac does not grant that Mac access to the original paths or OS permissions.

Saved queries, computed-column definitions, bookmarks, dashboards and preferences live in the catalog and
travel with the library. A history transfer keeps each library's preferences
separate. Full recovery carries saved state. A source bookmark can remain after
the bookmarked history has moved; the transcript is then unavailable in that
source library.

The native managed window uses nonpersistent WebKit storage. Preview caches are
scoped to a service credential. Annotations and drafts, page history, last-used
table queries and navigation-panel state are saved in the library, under a
per-Mac/per-application session namespace. They survive relaunch and updates
without leaving a portable app's history on the Mac. Quit, update and native
Eject await pending session saves before closing its service. History transfers
do not merge these independent session values.
Portable use leaves no Pharos helper or history cache running on the Mac while
its SSD is absent. Small updater, window, app-registration and explicitly
requested integration settings can remain; macOS permission records are separate.

## MCP and the primary connection

The first registered library becomes the MCP assignment for this application on
this Mac. Merely connecting to another library never changes it. Use **Library used by MCP** in
Settings → Libraries to change it explicitly, then use the assigned library's
MCP settings to register or update an agent client.

MCP answers only when that app's primary connection is its assigned library and
that service is ready. Otherwise it returns a helpful unavailable error. It
does not open the assigned catalog directly. Responses completing after a
connection or assignment switch are discarded. Private Ask Pharos requests stay
bound to their originating library.

The application must remain running, though its window may be closed. This is
a change from older bundled `mcp` registrations, which could read the catalog
after the app quit. Existing registered libraries use the same managed gate.
Explicitly standalone CLI installations retain direct-catalog access.

Managed registrations use `mcp-app --application APP`. Older `--config ... mcp`
registrations launched by the same installed app also enter its broker gate once
that config is registered. An explicitly standalone, unbundled CLI retains its
existing direct-library compatibility mode.

## Capture locally and deliver later

1. Keep Pharos.app and a pending library on the Mac if capture must continue
   while an archive SSD is absent.
2. Connect to the pending library and authorize its sources.
3. Open **Capture locally, transfer later** and choose the pending library and
   destination archive. Enable the pair explicitly.

While the app is open, pending capture runs on a bounded five-minute cadence.
A returning destination also triggers a delivery attempt. Captures are indexed
locally, so pending history can be browsed. Delivery moves the entire pending
history and its retained evidence, then empties the pending catalog and removes
delivered evidence. Source content fingerprints suppress unchanged re-capture;
changed conversations can become pending again without repopulating unchanged
conversations from the same source container. Pending capture checks content even
when size and modification time are unchanged; ordinary incremental capture
reuses recorded hashes without rereading transcript history. Neither browsing
selection nor MCP assignment follows the delivery target automatically.

Delivery proof includes each normalized conversation's metadata, instructions
and aliases, including the current workspace association. A metadata change
can return to pending even when its text is unchanged. Workspace metadata has
separate proof: title, branch, outcome and other workspace changes can be delivered without recapturing unchanged
conversations. Proof names the exact destination instance. Explicitly selecting a
different destination invalidates the old suppression checkpoints and recaptures
surviving originals. Recovery clears these checkpoints and their indexing
shortcuts so surviving originals can be read again; it cannot recreate
originals that no surviving copy contains.

Raw database snapshots can be large. Pending generations remain until verified
delivery, including indexed evidence; a long disconnected period can exceed two
generations. The capture and copying paths keep their
free-space margin and pause on insufficient space. A source discovery or index
failure prevents delivery; no incomplete capture is presented as successful.
There is no required backup or retained recent-history cache after a move.

Pending evidence protection is stored with the library and applies to ordinary
UI, scheduled and CLI captures too. Pausing automatic delivery leaves that
protection in place, so undelivered snapshots cannot become eligible for normal
generation pruning.

Use **Eject** in the library drive panel or menu bar to release the libraries
on that volume and eject it. An app installed on the Mac stays open with its
local libraries available. If the app runs from that SSD, Eject also closes its
broker and bundled MCP processes, then quits before ejecting. **Release library**
is also available for a later Finder eject. Releasing the archive keeps local
pending capture configured and suspends delivery until reconnect or drive return.
Finder ejects also release managed data libraries; quit a portable app first if
using Finder to eject its own drive.
Release is temporary: it preserves the selected library, MCP assignment and
delivery pair. Relaunch reconnects the selected library when its drive is
available. A released source pauses capture as well as delivery until reconnect.

## Verified transfer and recovery

**Move or copy history** works between registered libraries. Both services pause
for the operation. A consistent catalog snapshot preserves catalog-only sessions
that no longer exist in retained database captures. History snapshots exclude
private Ask chats, their searchable mirrors and provenance, and independent user
settings. Raw captures and preserved artifacts are copied and verified by size and SHA-256. Imports are retained
under `captures/.imports/` outside ordinary capture generation pruning.

Reconciliation maps native identities to destination IDs, guards older copies,
unions divergent history, and rebuilds derived totals and search. Conflicting
revisions remain in the immutable history snapshot. Repeating an unchanged copy
reuses its inventory. Cleanup starts only after durable destination acceptance
for the exact writable instance. The source is rebuilt with its user state and
small capture checkpoints; delivered text is absent from its FTS, materialized
indexes, unused database pages and raw payloads.

An interrupted move has a durable source cleanup journal. Retry the same move to
finish it. If the source changed before cleanup began, retry takes a fresh
inventory. Once source cleanup has begun, it can resume only against the original
verified destination instance. A replaced or recovered target cannot validate
old proof. Recovery cannot recreate history if no surviving copy contains it.

**Restore a backup** creates a separate candidate in a new folder. It accepts a
complete local backup, or a self-contained copied library. A backup is locked for
reading and remains unchanged. A copied library is held under an exclusive use
lease while SQLite, including its WAL, is snapshotted. External references in a
copied library require a full backup instead. Known capture evidence and catalog
integrity are checked before the candidate is published. Validation includes
previous file versions, every retained database generation, and each nested
import's history snapshot and receipt inventory; missing evidence prevents
publication.

The candidate has a new logical ID and writable instance, with its source lineage
and backup recovery point recorded. It carries saved state but starts with sync
schedules paused and no automatic delivery pair. To weave a backup into an
existing library, recover a candidate first, then copy its history into that
library. The destination's independent preferences remain intact. For a cloud archive, use
[Cloud Backups](cloud-backups.md) or `pharos cloud-restore` to download, decrypt
when needed, and verify a recovery folder, then recover that folder into a
separate candidate here. Recovery also pauses cloud-backup schedules; ordinary
moves retain each library's cloud destinations, credentials and schedules.

Opening a catalog written by a newer incompatible build is refused before
migration or transfer. This implementation advances the catalog writer guard to
21 to preserve raw-evidence authority and delivery checkpoints. Capture manifest
version 5 also protects pending-file observations and retention. App rollback
does not roll a library back.

## CLI operations

These operations use the same verification and library ownership boundary as the
managed app:

```sh
pharos app --application /Applications/Pharos.app
pharos mcp-app --application /Applications/Pharos.app
pharos library-transfer --from /path/A/library.toml --to /path/B/library.toml
pharos library-transfer --from /path/A/library.toml --to /path/B/library.toml --move
pharos library-recover --backup /path/Backup --to /path/NewCandidate
```

A recovery destination must not exist. Failed candidates remain under a
`.pharos-recovery-*` staging name for inspection. Neither restore nor history
import configures capture, backups or agent registrations implicitly.
