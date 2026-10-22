# Query Table handoff: save the complete query, including host URL parameters

Status: upstream implementation requested; no local Query Table patch.
Baseline: published Query Table 0.9.5 / Go v0.9.5.
Consumer: Pharos. Prepared 2026-10-22.

## Problem and required result

Query Table's native Saved controls save `QueryState`. A host can add a search
clause to the builder through `whereRow`, yet its applied definition lives in
additional URL parameters. Saving or loading just the table query can therefore
change which records are searched while the UI claims to have restored a save.

Add a public, optional way to manage a **complete query definition**: table
`QueryState` plus explicitly registered host state. The existing Save, Update,
Saved, Default, Share, Reset and Undo/Redo controls must operate on that complete
definition. The host continues rendering and executing its additional clauses.
Do not require a second saved-search UI or a replacement QueryBuilder.

First acceptance case: save Tool calls grouped by Tool, with source Status =
error, result HAVING Calls > 2, and the Command keyword condition
`(cache OR buffer) NOT obsolete`. Remove the keyword condition and change the
grouping. Loading the saved query must restore all of these together, including
the keyword chip and its editable modal, before any new execution starts.

## Current API gap, verified in 0.9.5

- Core `SavedQuery` contains `id`, `name`, `savedAt`, `query: QueryState`, and an
  optional `snapshotCode`; there is no host payload.
- `StorageAdapter.loadLast/saveLast` accept only `QueryState`.
  `saveNamed/updateNamed` also accept only `QueryState` and an optional snapshot
  code. `listSaved/loadDefaultSaved` return the same table-only SavedQuery.
- React `useSavedQueries` receives `currentQuery: QueryState` and restores through
  `applyQueryState(q)`. `api.saved.load` cannot atomically restore host state.
- UI `matchesSavedQuery`, `summary`, `shareUrl` and `savedQueryControls` are public
  composition hooks. They help presentation or replace controls, but do not add
  missing persistence or controller transactions. Matching both definitions
  while saving only one is insufficient.

Relevant upstream surfaces are the core SavedQuery/StorageAdapter and snapshot
contracts, React `useSavedQueries` and `useQueryTable`, and UI QueryBuilder and
SavedQueriesModal. These names are existing APIs; the API below is a proposal,
not an API that Pharos can currently call.

## Concrete Pharos payloads

| Surface | Table query parameter | Additional definition parameters |
| --- | --- | --- |
| Analyze → Query → Tool calls | `q_tool_calls` | `record_tool_calls` |
| Other Analyze query datasets | `q_<dataset>` | `record_<dataset>` for that dataset only |
| Search / Library | `q_library` | `text_builder`, or `kind` + `search` + `case`; also `view` |
| Older Human messages search | `q_writing_messages` | `message_query` |

`record_tool_calls` currently encodes `{field, draft}`. `field` is an allowlisted
source text field such as `command`; `draft` is Pharos's TextSearchDraft. It
includes visual Boolean groups/exclusions or syntax, phrase/prefix/proximity
rules, literal/regex mode, and case/spacing options. This searches each source
record's selected field, not related conversations or workspaces.

Library's `text_builder` is a different host definition: retained-message scope,
text conditions and matching options select workspaces. File/URL conditions and
`view=conversation` are further host choices. Preserve these distinctions; Query
Table must not translate them into ordinary WHERE predicates.

`record_search` is the compiled **HTTP request** parameter derived from an
Analyze draft. Save the editable definition, not this transport artifact. Never
persist search cursors, result pages, match coverage, loading flags, dialog
visibility, focused controls, API credentials, or arbitrary location.search.

## Proposed data and API contract

Use an additive, versioned envelope, keeping QueryState and its wire protocols
unchanged. For example:

```ts
type HostQueryState = {
  version: 1;
  // Explicitly owned URL parameters only. Arrays preserve repeated values.
  params: Record<string, string[]>;
};

type QueryDefinition = {
  query: QueryState;
  hostState: HostQueryState;
};

// Additive extension of the existing SavedQuery shape.
// Omitted hostState denotes a legacy table-only save, never "keep current".
interface SavedQuery {
  id: string;
  name: string;
  savedAt: number;
  query: QueryState;
  hostState?: HostQueryState;
  snapshotCode?: string;
}
```

A host registers an exact parameter allowlist and a synchronous codec with
`useQueryTable`. Proposed option and controller names:

```ts
useQueryTable({
  // Existing schema, transport, storage and queryParameter options stay.
  hostQuery: {
    ownedParameters: ["record_tool_calls"],
    defaults: { version: 1, params: {} },
    // Validate/migrate the host's JSON, field names and dataset; normalize it
    // for stable identity. May reject; no live state has changed at this point.
    normalize: normalizeRecordKeywordState,
  },
});

api.definition; // One committed QueryDefinition snapshot.
api.setDefinition(previous => ({
  query: { ...previous.query, offset: 0 },
  hostState: nextAcceptedHostState,
}));
// api.saved.save/update/load, api.undo/redo/resetAll, and native UI remain.
```

The library supplies URL read/write for the registered keys. Host codecs can
be extended if a non-parameter representation is preferable, but the following
semantics are required regardless of naming:

1. URL writes **replace** the registered parameter set, deleting missing keys;
   they preserve unrelated host navigation parameters and other tables' state.
   A query parameter owned by core cannot also be owned by the host codec.
2. Save/Update snapshot the committed definition before awaiting I/O. Draft
   keystrokes and an open modal's canceled edits are never captured.
3. Normalize the entire definition for identity. Two identical QueryStates
   with different keyword fields, words, scopes, case, or presentation choices
   are different saved searches. Ignore incidental editor rule/group IDs and
   JSON object key ordering. Preserve meaningful repeated-value ordering unless
   the host codec explicitly declares that parameter unordered.
4. Missing `hostState` on an older save means reset the registered keys to the
   codec's defaults. It must clear an active keyword condition, not inherit it.
   Explicit `{version:1, params:{}}` has the same clear/default semantics.
5. A present but malformed, oversized or unsupported-version payload rejects
   the **whole** restore. Leave the live definition and URL intact, report the
   error, and do not execute a broadened table-only query. Unknown owned field
   names or an incompatible dataset are errors for the host codec.
6. An instance without the corresponding host integration must reject a save
   containing nonempty unsupported host state. It must not silently discard it.
7. Grouping, HAVING, column changes and source-record drilldowns retain host
   source restrictions. Host restrictions are not a result HAVING predicate.
   Leaving grouping restores source QueryState without clearing host state.

## Storage and immutable links

Keep existing adapters source-compatible for hosts that do not opt in. One
possible additive storage design is:

```ts
interface StorageAdapter {
  // Existing methods remain for table-only users.
  loadLastDefinition?(key: string): Promise<QueryDefinition | null>;
  saveLastDefinition?(key: string, value: QueryDefinition): Promise<void>;
  saveNamedDefinition?(
    key: string, name: string, value: QueryDefinition,
    savedAt: number, snapshotCode?: string,
  ): Promise<SavedQuery>;
  updateNamedDefinition?(
    key: string, id: string, value: QueryDefinition,
    savedAt: number, snapshotCode?: string,
  ): Promise<SavedQuery>;
}
```

`listSaved` and `loadDefaultSaved` return the enriched SavedQuery. Update bundled
memory/local-storage adapters and publish custom-adapter migration examples.
With host integration enabled, missing complete-definition persistence must
produce an actionable unsupported-adapter error; do not fall back to table-only
save/update and claim success. Existing table-only adapters must remain usable
when no host state is configured.

An update preserves the saved ID, name and default selection. Failed writes
leave the old complete definition intact. Only a successful complete write can
mark the current definition saved. Editing the live query during an in-flight
save must not relabel the newer live definition as the saved one.

Durable snapshots and `snapshotCode` must identify the **complete** definition.
A short `q_<dataset>=@...` reference must resolve without relying on another
browser's URL, local storage or a separately mutable host record. Use a new
snapshot version/envelope if needed and keep old QueryState-only snapshots
readable with the legacy-clear semantics above. Budget combined query and host
UTF-8 bytes; either support the whole definition through durable storage or fail
explicitly. Never truncate or drop the extra parameters to fit a URL.

The pure Go query executor does not need to interpret host JSON. If the upstream
Go snapshot types/validation cover persistence, extend them for the versioned
opaque payload as needed. Do not change query-v3 WHERE/group/HAVING semantics.

## Controller transaction, history and restoration

The committed controller snapshot must include both parts. Calling a host
callback after `setQuery(saved.query)` is insufficient: React effects could
issue rows/metrics using new table filters with old keywords, or the reverse.

Validate both parts first, then commit one controller transaction that updates
query, host state, URL, saved identity, and the execution generation. Make the
new committed host state available to transport selection in the same render.
Abort obsolete row/metric/metadata requests and reject stale results. There
must be no request for an intermediate mixed definition during saved/default
load, back/forward restoration, reset or undo/redo. Failed restoration must not
partially update either side or overwrite the prior last-used state.

- One accepted modal apply is one history entry, including any offset reset.
  Native table edits and host edits participate in the same undo/redo stream.
- Canceling or dismissing a modal changes no applied state, URL or history.
- Native Reset restores schema/default query state and registered host defaults
  in one operation. Restoring an explicit named default restores its whole
  definition; document reset-to-default versus reset-to-schema behavior.
- Existing restore precedence between explicit URL, named default, last-used
  state and schema defaults remains consistent, with a complete definition at
  each level. An explicit URL without owned host keys means the host defaults,
  not stale last-used keywords. Invalid explicit URLs remain errors.
- A user-selected saved/default query is recognized only when the complete
  definition matches. Updating/removing a default keeps native controls working.
- Collapsed summaries retain the active host condition alongside native source
  WHERE, grouping and result HAVING. Preserve existing composition slots.

## Pharos integration after the release

1. Register dataset-scoped `record_<dataset>` on Analyze, and the explicit
   Library parameters above on Search. Keep message cursors transient. Register
   any legacy rolling-window URL state only with its existing host codec;
   native relative predicates already in QueryState remain there.
2. Update `web/src/storage.ts` and `web/src/bookmarks.ts` to persist and return
   complete definitions through library-backed preferences and durable links.
   `libraryStorageAdapter.saveNamed` currently calls `collectDurableQuery`
   without the extra parameters; `updateNamed` builds a table-only `queryURL`.
   `savedQueries` and `replaceSavedQuery` currently return table-only objects.
   Fix all of these paths, not just the visible bookmark URL.
3. Move accepted host edits in `AnalyzeQueryTable` and `LibraryPage` in
   `web/src/index.tsx` into the public definition transaction. Replace ad hoc
   parameter writes; do not recreate Save/Load controls or monkey-patch the API.
4. Keep `record-keyword-model.ts` validation and dataset/field allowlists.
   Canonicalize its saved draft without ephemeral rule IDs, while reconstructing
   valid editable IDs on load. Keep `record-keyword-clause.tsx`,
   `explore-search.tsx`, and `search-results-view.tsx` as host presentation.
5. Preserve the host's existing search execution, complete-count safeguards,
   and source restriction through query-v3 rows, metrics and metadata.
6. Publish upstream first; pin core, React, UI, codegen and the Go module to
   the same published release, regenerate schemas, and rebuild ignored bundles.
   No Pharos dependency patches, vendoring, package-cache edits, or generated
   bundle workarounds.

## Required acceptance tests

Put state, persistence, validation and transaction tests in core/React units.
Use browser tests only for focus/layout and observable request stability.

| Case | Required outcome |
| --- | --- |
| Save, clear, reload, load | Table query and record keyword chip/modal restore together. |
| Same table query, different keyword text/field/options | Different saved identity; Update changes the existing complete save. |
| Library phrase + user-message scope + Conversation display | All restore; grouped synthetic rows are never sent to the conversation renderer. |
| Save has no host state while a keyword is active | Loading the old save clears the active keyword. |
| Default save and last-used restore | Both persist/restore complete state under the existing precedence rules. |
| Save while modal has an unaccepted draft | Only the applied keyword is stored; cancel creates no history entry. |
| Undo/redo host and table edits | Restore one complete definition per accepted action, including offset. |
| Reset with keyword/grouping/HAVING active | Clear/reset all registered state atomically; a named default uses its complete definition. |
| Group, edit HAVING, inspect source records, exit grouping | Keep the keyword restriction at the source stage. |
| Native Share and immutable short link in another browser | Resolve complete state without ambient local storage. |
| Unknown version, bad JSON/field, oversized state | Reject the whole restore; no partial URL update or broadened request. |
| Save/Update fails or a legacy adapter lacks support | No success state or partial overwrite; preserve previous save and default. |
| Live edit during async Save/Update | Store the captured definition; leave newer live state marked modified. |
| Rapid saved loads, back/forward, delayed old responses | No mixed-definition requests or stale response overwrite. |
| Dataset switch or unrelated URL parameters | Do not leak another table's host state; preserve unrelated navigation. |
| Arbitrary/sensitive/transient query parameters present | Persist only the registered definition keys; exclude credentials and cursors. |
| Registry codec recreates editor IDs | Saved matching remains stable across reloads. |

Ship the public contract, migration examples and upstream tests with the
release. Completion means Pharos can remove the documented Saved/Undo/Reset
limitation using those public APIs, while retaining the native Query Table UI.
