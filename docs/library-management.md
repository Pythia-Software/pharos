# Libraries and application setup

Pharos.app can run from Applications or from an SSD. Its library is a separate
folder. The native app opens **Libraries** on startup; the same screen is linked
from the library toolbar. It offers creation, opening, recovery, MCP assignment,
manual transfers, and an optional pending-library delivery pair.

## Choose locations

For an app on the Mac, a new library defaults to
`~/Library/Application Support/Pharos/Libraries/<name>/`. An app on an SSD defaults
to `Libraries/<name>/` beside its bundle. A chosen folder can be elsewhere. The
folder picker chooses a parent for a new library and the library folder itself
when opening one. No library files are installed in Applications by default.

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

Choose sources using **Choose sources on this Mac**, after registering a
library. Existing discovery, estimates and opt-in controls are used. Source
bindings remain inside `hosts/<host-id>.toml`; carrying the library to another
Mac does not grant that Mac access to the original paths or OS permissions.

Saved queries, computed-column definitions, bookmarks, dashboards and preferences live in the catalog and
travel with the library. A history transfer keeps each library's preferences
separate. Full recovery carries saved state. A source bookmark can remain after
the bookmarked history has moved; the transcript is then unavailable in that
source library.

The native managed window uses nonpersistent WebKit storage. Preview caches are
scoped to a service credential and local session keys are scoped to a library.
Portable use leaves no Pharos helper or history cache running on the Mac while
its SSD is absent. Small updater, window, app-registration and explicitly
requested integration settings can remain; macOS permission records are separate.

## MCP and the primary connection

The first registered library becomes the MCP assignment for this application on
this Mac. Merely connecting to another library never changes it. Use **MCP
settings** in Libraries to change it explicitly, then use the assigned library's
MCP settings to register or update an agent client.

MCP answers only when that app's primary connection is its assigned library and
that service is ready. Otherwise it returns a helpful unavailable error. It
does not open the assigned catalog directly. Responses completing after a
connection or assignment switch are discarded. Private Ask Pharos requests stay
bound to their originating library.

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
conversations from the same source container. Neither browsing selection nor MCP
assignment follows the delivery target automatically.

Delivery proof includes each normalized conversation's metadata, instructions
and aliases. A metadata change can return to pending even when its text is
unchanged. Workspace metadata has separate proof: title, branch, outcome and
other workspace changes can be delivered without recapturing unchanged
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

Use **Release library** before ejecting. Releasing the archive keeps local pending
capture configured and suspends delivery until reconnect or drive return. Finder
ejects also release managed libraries on that volume. If the app itself runs
from the drive, quit it before ejecting; its managed MCP processes stop too.

## Verified transfer and recovery

**Move or copy history** works between registered libraries. Both services pause
for the operation. A consistent catalog snapshot preserves catalog-only sessions
that no longer exist in retained database captures. History snapshots exclude
private Ask chats and independent user settings. Raw captures and preserved
artifacts are copied and verified by size and SHA-256. Imports are retained
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
integrity are checked before the candidate is published.

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
18 because older writers do not understand delivery checkpoints. App rollback
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
