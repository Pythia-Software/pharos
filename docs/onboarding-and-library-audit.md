# Pharos onboarding and library audit

Status: repository audit for the agreed [onboarding and library plan](onboarding-and-library-plan.md).
Evidence records the baseline before this branch's implementation. The design
and operating guide describe the resulting changes; they have not been released.
This audit reads repository code and documentation; it does not inspect
personal libraries, mount disks, or exercise production integrations.

## Scope assessment

The existing capture and indexing model is worth extending. Per-host capture,
source bindings, volume pins, online SQLite snapshots, atomic capture manifests,
copy authority, schema refusal, and graceful release already solve important
parts of portability. The missing boundary is an independently addressable
library, with durable delivery and runtime ownership separate from app location.

Three findings determine the initial design:

1. **Captures alone cannot migrate an arbitrary existing library.** Database
   generations are bounded, while the catalog is the long-term record of indexed
   sessions. Durable user state is also in the catalog. A transport based on raw
   captures must additionally preserve catalog-only history before source cleanup.
2. **Pending storage is not necessarily small.** A database snapshot contains a
   whole source database. The documented 8.7 GB Conductor example uses about
   17.5 GB for latest plus previous, before the catalog, other sources, and
   transfer workspace. These are documented example sizes, not measurements of
   this user's machine or a representative population.
3. **MCP currently bypasses the requested application connection policy.** It
   resolves a config, forwards to a matching service when possible, and can open
   that catalog directly otherwise. A new gate must be authoritative before both
   forwarding and direct access; checking which service currently owns a port
   is insufficient.

## Findings inventory

Severity indicates the consequence if existing behavior is reused unchanged.
Critical means potential information loss; high means a broken product promise,
wrong-library access, or unsafe ownership. Medium means misleading behavior or
incomplete integration.

| ID | Severity | Existing assumption and evidence | Failure scenario | Required follow-up |
| --- | --- | --- | --- | --- |
| L01 | High | [LaunchPlan.resolve](../macos/Launch.swift) chooses a library by `library.toml` beside the app; [loadConfig](../internal/archive/config.go) also uses beside-app discovery and a CWD fallback. | Applications cannot select an SSD library as an ordinary saved choice; CLI and GUI can select different data. | Explicit library registry and resolution shared by GUI, CLI, and integrations; retain legacy discovery as an adoption path. |
| L02 | High | [PharosService](../macos/PharosApp.swift) stores one config, endpoint, process, library volume, and monitor. | Adding another window or library accidentally shares process or shutdown state. | Name application connection state separately from each library service and background transfer operation. |
| L03 | High | [defaultConfig](../internal/archive/config.go) defaults to port 8765; [portable.go](../internal/archive/portable.go) reserves 8766 for portable use. [Server.Serve](../internal/archive/api.go) binds the configured port. | Two external libraries compete for 8766; a port becomes a proxy for library identity. | Bind an available loopback port once, publish the resulting endpoint under library service identity, and verify identity on requests. |
| L04 | High | [cookieName](../internal/archive/service.go) uses the configured port; [preview scope](../internal/archive/preferences.go) hashes catalog path and API token; [storage.ts](../web/src/storage.ts) keeps last-used query state in browser storage. | Reusing an endpoint or moving a library confuses session state and saved previews. | Scope browser state and authentication by installation, library instance, and service lifetime; avoid relying on ports. |
| L05 | High | [mcpServer.dispatch](../internal/archive/mcp_library.go) forwards then calls `direct`; `direct` can open a catalog even without the GUI. | MCP answers for the assigned library while its application is connected to another library. | App-scoped MCP broker with an authoritative assignment and connection gate; managed MCP has no direct-catalog fallback. |
| L06 | High | [mcpServer.forward](../internal/archive/mcp_library.go) treats several HTTP rejection statuses as a reason to fall back; `X-Pharos-Catalog` checks catalog paths. | A wrong-service response falls through to direct access instead of enforcing application policy. | Distinguish explicit unavailable/policy errors from transport errors. Validate library instance and connection generation at the service too. |
| L07 | Medium | [MCPEnabled](../internal/archive/mcp_history.go) and the [MCP settings API](../internal/archive/api.go) store enablement in the library; installation-specific assignment does not exist. | Carrying a library or switching windows changes integration behavior without an explicit local choice. | Store assignment per app installation per Mac; default once to the first library. Opening, reconnecting, or restoring another library cannot reassign it. Preserve opt-in separately. |
| L08 | High | [LibraryVolume.swift](../macos/LibraryVolume.swift), [watchVolume](../macos/PharosApp.swift), and [DriveProcesses](../macos/Launch.swift) couple library release and process cleanup to executables running from the app's directory. | An app in Applications keeps SSD-backed library resources open; closing one library terminates unrelated processes sharing the app binary. | Separate the app's volume from every data volume; target operations by library ownership and client leases. |
| L09 | High | [UpdateLifecycle.swift](../macos/UpdateLifecycle.swift) and [app_update.go](../internal/archive/app_update.go) use app-path leases and handoff markers. | New library services survive an app update, or updating one installed copy interferes with another. | Keep per-install update coordination, enumerate all services and clients of that install, and preserve graceful write completion. |
| L10 | High | [WKWebView setup](../macos/PharosApp.swift) uses normal website storage; [card-preview-cache.ts](../web/src/card-preview-cache.ts) uses IndexedDB. | Portable use leaves conversation-derived titles, previews, or queries on a Mac. | Nonpersistent portable website data store; keep durable choices in the library. Document allowed non-history updater/window/registration residue. |
| L11 | Medium | [hostconfig.go](../internal/archive/hostconfig.go) already stores `hosts/<host-id>.toml` in libraries; non-library configs use another path. | A new unified layout loses bindings when moved between internal storage and SSD. | Reuse host files for every new library. Adopt legacy per-user config without losing source scope or grants. |
| L12 | Critical | [snapshot retention](configuration.md#capture) and [sourceCapture.retain](../internal/archive/capture_sqlite.go) prune old generations; the catalog retains indexed sessions. | A file-only migration omits sessions deleted from the source and no longer in retained snapshots. | Snapshot the catalog consistently and export preserved history alongside captures; prove coverage before cleanup. |
| L13 | Critical | [retain](../internal/archive/capture_sqlite.go) can eventually prune an unindexed generation with a warning to bound space. | Long offline capture drops undelivered history despite the local-library promise. | Pending data requires explicit preservation coverage; safe pause when full. Do not inherit bounded pruning without this condition. |
| L14 | High | [captureManifest](../internal/archive/capture.go) has SHA-256 for ordinary files, but latest SQLite snapshot entries do not carry the same digest. Mutable latest paths and generations have different lifecycles. | Size or mtime is mistaken for verified transfer, or an older manifest overwrites newer destination history. | Verify every transferred payload, including database snapshots, and commit a merged manifest rather than replace it wholesale. |
| L15 | Critical | [copies.go](../internal/archive/copies.go) and [capture_index.go](../internal/archive/capture_index.go) preserve native identity, copy authority, and captured-source versions. | Reusing local database row IDs, summing existing metrics, or treating later wall time as newer content corrupts a merge. | Preserve source identities and provenance; reuse authority semantics. Map catalog-only graph records and verify derived totals through re-derivation. |
| L16 | Critical | [preferences.go](../internal/archive/preferences.go), [query snapshots](../internal/archive/query_table_exploration.go), and [findings_store.go](../internal/archive/findings_store.go) retain non-rebuildable user choices. | Resetting the local catalog after delivery discards bookmarks, durable query links, or finding decisions. | Explicit history/user-state classification. Initial history transfers do not merge preferences; source cleanup retains user state and unavailable bookmark targets. |
| L17 | High | [backup.go](../internal/archive/backup.go) establishes a library ID through backup history and rejects other-volume copies through existing volume guards; a delivery recovery epoch is not defined. | A receipt for last week's writable destination is applied after restoring an older backup. | Identity independent of backup setup, distinct writable instance/recovery epoch, and restore-aware receipt validation. |
| L18 | Critical | Verified source cleanup intentionally removes delivered payload; [backup.go](../internal/archive/backup.go) provides point-in-time recovery material. | A restored archive predates a move and no source or backup still holds the delivered revision. | Report the recovery boundary honestly. Epoch changes invalidate proof but cannot manufacture missing data. Backups remain optional. |
| L19 | High | [checkLibrary](../internal/archive/portable.go) refuses a missing catalog or wrong volume, while legacy defaults and `EnsureDirs` can create directories. | A disconnected SSD path becomes a new empty local library. | Run availability and identity checks before every creation path; creation is an explicit new-library operation. |
| L20 | High | [checkSchemaVersion](../internal/archive/catalog.go) protects the database; [OpenCatalogForQuery](../internal/archive/catalog_query.go) avoids migrations during queries. | A new capture or transfer format bypasses database protection, or a newer writer damages semantics understood differently by an older build. | Retain existing guards; add a compatibility envelope for manifest, transfer, and writer semantics before work starts. |
| L21 | Medium | [Ask Pharos](../internal/archive/ask_agent.go) uses a private lookup protocol; [ask tables](../internal/archive/assets/schema.sql) include host-scoped settings and native-session exclusions. | General history import leaks private Ask context or loses exclusions and re-ingests it as agent history. | Keep chats/settings private to their library; explicitly preserve required exclusion identities without exposing private lookup content. |

## Data classification for migration

The design must classify ownership by behavior, not by whether a table happens
to be SQLite or a file happens to sit in `captures`.

| Class | Examples | History transfer | Full library recovery | Local cleanup |
| --- | --- | --- | --- | --- |
| Archived history | Conversations, messages, source sightings and versions, retained tool evidence, history no longer in source snapshots | Preserve and reconcile by original identity | Preserve | Remove only the revisions covered by verified destination proof |
| Retained evidence | Transcript files, database snapshots, manifests, preserved artifacts referenced by history | Preserve bytes and original locators | Preserve | Remove only verified, unshared payload |
| User-created state | Preferences, bookmarks, query snapshots, computed-column definitions, finding actions/carts/interventions | Keep libraries separate initially | Preserve with the recovered library | Keep; do not rebuild away |
| Privacy metadata | Native Ask-session exclusions and other provenance needed to prevent private re-ingestion | Preserve required identities, not private chat contents | Preserve | Retain protection where needed |
| Derived state | FTS/search projections, recomputable analytics and previews | Recompute rather than blindly add | Rebuild as needed | Rebuild remaining pending history without erasing other classes |
| Machine and runtime state | Library discovery, MCP assignment, endpoints, process/update leases | Do not import or activate | Reconcile explicitly for this Mac | Never interpret move cleanup as an integration change |

## Storage budget and low-space behavior

Estimate separately: retained pending evidence, its searchable catalog, the
consistent transfer snapshot, target staging, and any temporarily coexisting old
and new database generations. Whole-file transcript updates and full database
snapshots mean a small visible conversation increment can require a large copy.

The prototype labels its sizes as examples and lets a reviewer include or omit
the database source. It must never imply that declining a source has already
deleted anything or that raw conversation files owned by another app will be
removed after delivery. Production estimates should inspect only selected source
metadata after permission, rather than scan personal data for this audit.

Before capture or transfer, reserve room for temporary files and durability work.
At the threshold, explain the affected library and pause the operation without
pruning undelivered history. The product can offer a different destination,
source selection, or freeing space. It must not silently retain a lossy subset.

## Boundaries of this audit

This inventory covers the launch/configuration, capture/index, identity,
preferences, backup, integration, release, update, and website-storage paths
named above. At the audited baseline, production moves, restore generations and
multi-library services were missing. The resulting implementation inventories
history tables and references explicitly and includes fixtures for catalog-only
history, verified cleanup and interruption recovery; see the implementation design
for its review boundaries.

The recently discussed backup improvements must be reconciled against their
merged code before building recovery. This audit does not assume an unmerged
backup contract. OS filesystem durability and actual browser/cache behavior need
the repository's native macOS harnesses when the corresponding code changes.
