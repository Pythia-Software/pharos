# Portability

Pharos currently ships as a macOS application. The portability groundwork keeps
shared behavior separate from native mechanisms so future Windows, Linux and
Web work can reuse it. Windows/Linux desktop hosts, distributions and a Web
product require a separate task; they are not the next automatic refactoring
step. Compilation alone does not establish support.

## Architecture direction

Keep library policy in Go and screens in the shared HTML/TypeScript UI. Native
hosts own windows, OS integration and embedded webviews. The existing host is
Swift/WKWebView. The direction for future desktop hosts is direct system-webview
integration, without adopting Electron, Tauri or Wails. WebView2 and
GTK/WebKitGTK are candidates; implementation language, bindings, runtime floors
and packaging still need target-specific validation.

The core owns library selection, operation admission, release verdicts, capture,
indexing, backup and recovery. A host presents these decisions and performs the
authorized native action. Preserve the distinct quit, library release, physical
eject and update lifecycles described in [library management](library-management.md)
and [releases and updates](releases-and-updates.md).

Prefer small functions and platform-tagged implementations. Keep helpers beside
their archive callers unless the primitive is shared across packages. Extend an
existing contract before inventing another abstraction. There is no universal
`Platform` object or separate scheduler for each OS.

## Existing boundaries

| Area | Owner and contract |
| --- | --- |
| Advisory locks | [`internal/platform/filelock`](../internal/platform/filelock/filelock.go) borrows caller-owned descriptors. Unsupported, contention and other errors remain distinct. Callers own metadata, lock lifetime and close/unlink ordering. |
| Buffered-file flush | [`internal/platform/filesync`](../internal/platform/filesync/filesync.go) is a plain flush. It does not replace the stronger publication/durability guarantees in [`durability.go`](../internal/archive/durability.go). |
| Filesystem observations | [`internal/platform/fsinfo`](../internal/platform/fsinfo/fsinfo.go) reports caller-available capacity and read-only mount status independently. Unknown capacity cannot admit a write; unknown mount status cannot authorize an immutable SQLite read. |
| Local object identity | [`internal/platform/localidentity`](../internal/platform/localidentity/localidentity.go) observes one stat result for cache invalidation. Missing identity prevents cache reuse; it never becomes a pathname hash or a cross-host identity. |
| Library, backup and history identity | [`file_observation.go`](../internal/archive/file_observation.go), [`file_identity.go`](../internal/archive/file_identity.go) and [`service_volume.go`](../internal/archive/service_volume.go) have different persisted and transient meanings. Preserve those distinctions. |
| Process actions and observations | [`process_isolation.go`](../internal/archive/process_isolation.go), [`process_liveness.go`](../internal/archive/process_liveness.go), `labeling_process*.go`, `process_replace*.go`, `input_open*.go` and `finder_info*.go` isolate native behavior. Liveness, ownership and process-start identity are separate facts. |
| Work and resource accounting | [`work-portability.md`](work-portability.md) defines portable scheduling policy, cooperative CPU grants, counter availability and native-reader limits. |
| Recorded paths | [`internal/sourcepath`](../internal/sourcepath) and [`capture_index.go`](../internal/archive/capture_index.go) separate source spelling, archive-relative names and physical host paths. |
| UI host services | [`host-services.js`](../internal/archive/assets/host-services.js), with [`TypeScript declarations`](../web/src/host-services.d.ts), owns feature-script transport and clipboard behavior. |

An unavailable observation remains unknown. The caller decides whether it can
omit diagnostics, copy without an optimization, or must refuse an operation.
An unsupported required action must fail before side effects. Do not weaken a
lock, identity, containment or durability guarantee to make a target compile.
Current fallback files deliberately leave some operations unsupported.

## Compatibility that must survive refactoring

- **Persisted identities have different encodings.** `localidentity.DeviceInode`
  preserves legacy signed Darwin device text, while `rootIdentity` preserves
  unsigned device/inode history keys. The v1 library descriptor's
  `directory_inode` is another contract. The service watchdog's device value is
  transient. Unifying their shapes requires a deliberate format migration.
- **Recorded paths belong to their source.** Existing capture manifests use
  legacy POSIX lexical semantics; they do not establish Windows path flavor.
  Translate through the longest matching prefix and retain blocked mappings so
  rejection cannot select a shorter, different source. Validate/localize archive
  names before host I/O. Lexical containment does not prove symlink-safe access.
  Never reinterpret old paths using the indexing machine's home, separators,
  case folding or Unicode normalization.
- **Ownership outlives a request.** Borrowed descriptors stay caller-owned;
  cancellation joins workers before releasing grants. A PID or a present process
  does not establish ownership across PID reuse. Unknown liveness cannot justify
  stale-state cleanup or destructive process actions.
- **Capture export is a bounded transport.** The [capture-export contract](capture-export.md)
  covers catalog-free export and offline verification of captured Claude Code
  and Codex evidence. There is no capture-only importer. Hash verification does
  not authenticate the sender or prove complete provider history. Reuse capture
  and SQLite snapshot machinery rather than building another collector engine.
- **Time data and timezone policy are separate.** [`cmd/pharos`](../cmd/pharos/main.go)
  embeds `time/tzdata`. Keep that fallback; Windows zone-name mapping, effective
  analytics zones and cache invalidation still require explicit decisions.

## Host services and trust

Feature code calls `window.pharosHost` or the existing shell clipboard helper.
The compatibility adapter preserves current native handler payloads and replies;
its `protocolVersion` describes the JavaScript API, not a verified native
handshake. Capability hints are resolved again at action time. Missing handlers
and native rejections remain visible failures, with no automatic replay of
update installation or eject.

Keep folder-picker ownership and stale-completion handling with the document
lifecycle. Browser file handles are not service-machine filesystem paths. A
release acknowledgement is not confirmation of physical ejection. One-way menu
dispatch is not a native acknowledgement.

The JavaScript adapter is not an authorization boundary. Native handlers validate
the actual sender's main frame and exact application origin, including port,
before dispatching effects. The Mac owner is `NativeBridgeSender` in
[`PharosApp.swift`](../macos/PharosApp.swift). Preserve authenticated loopback
access and native navigation/download policy. A future host must supply real
sender provenance; renderer-provided origin fields are not evidence of trust.
Browser/remote service exposure and a private host-to-broker control channel
remain separate designs, not capabilities supplied by this adapter.

## Checks and maintenance

Fast checks are part of ordinary Go and web validation:

```sh
go test ./internal/architecture
npm --prefix web run check-host-boundaries
```

The [Go source check](../internal/architecture/native_test.go) scans production
`internal/` and `cmd/` files for all OSes without importing the service or invoking
native tools. Native leaves have exact reviewed paths; legacy exceptions are
scoped to file, function and operation, with reasons. Platform filenames alone
are not approval. Native leaves need OS/cgo constraints, and stale exceptions
fail. New exceptions need a specific reason and behavioral coverage.

The [frontend source check](../web/host-boundaries.mjs) discovers authored JS/TS
and executable HTML/Python shell fragments, including aliases and computed
access. Exact generated bundles are excluded because their authored sources
are scanned. Native transport and clipboard implementations belong in the
approved host adapter. Both checks detect known structural drift; they do not
prove every dynamic command or interprocedural flow safe.

Extend owner tests for unavailable/unsupported operations, permission errors,
unknown/replaced identity, valid zeroes, reset counters and handle/drain
lifetimes. Preserve persisted-format fixtures. Keep pure policy tests native-free;
run native lifecycle tests when changing the mechanisms they exercise.

Cross-compilation is **opt-in only**, outside setup, merge checks, npm checks and
normal Go tests. Run it only when explicitly requested, after setup has built
the embedded frontend:

```sh
go test -tags portability_compile -run '^TestCrossPlatformCompilation$' -timeout 15m ./internal/architecture
```

The tagged test builds production packages for Darwin/arm64, Linux/amd64 and
Windows/amd64 with CGO disabled. It does not execute binaries, validate cgo
inference backends, test foreign native fixtures or package a desktop app.

## Before resuming a port

These are unresolved requirements, not a scheduled implementation wave:

- Establish where intended users' histories live: Windows-native, WSL, Linux
  desktop, remote host and container are distinct source environments. Collection
  near approved sources plus immutable delivery is a useful direction; export
  alone does not supply ingestion, receipts or safe cross-host identity.
- Implement and test required native guarantees on the target: lock/metadata
  interoperability (including Swift/Go update coordination), durable publication,
  library identity, process containment, cancellation, crash cleanup and restart.
  Source checks and successful builds cannot replace those tests.
- Resolve source path/shell context and destination filename collisions before
  enabling new writers. Windows `.cmd`/`.bat` wrappers need a reviewed launch
  path; do not add a generic shell fallback around provider arguments.
- Choose supported local library storage and discovery defaults. Cloud-synced
  live catalogs, WSL UNC access, network filesystems and cross-OS writable-disk
  use each need their own safety and performance proof.
- Validate the actual embedded engines for focus, selection, accessibility,
  downloads, private storage, navigation trust, lifecycle and resource cost.
  Decide host bindings/language, runtime floors, packaging, signing and update
  ownership from that evidence. A Web product needs its own access/storage model.
