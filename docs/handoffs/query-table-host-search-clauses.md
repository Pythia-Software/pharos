# Query Table: host search clauses in the query lifecycle

**Delivered in Query Table 0.9.7** with host query state; see
[the saved host query state handoff](query-table-saved-host-query-state.md).

For the concrete saved-search implementation contract, API proposal, Pharos
migration points and acceptance matrix, see
[Save the complete query, including host URL parameters](query-table-saved-host-query-state.md).
This document retains the broader clause-composition context.

## Context

Pharos's Search prototype (the `/library` route) puts the published Query
Table 0.9.5 builder in charge of columns, row filters, grouping, HAVING,
ordering, paging and metrics. `web/src/explore-search.tsx` adds a **Search in**
row for Messages, Files or URLs. Its modal uses the existing Pharos search
editor; the applied condition appears in the builder.

`web/src/search-results-view.tsx` adds a **Display as** row in the same
builder, choosing Table or Conversations. This changes presentation, keeping
filters, columns, sorting and paging. Grouped results show Table; choosing
Conversations exits grouping through the public `exitGrouping` helper and
restores the source query. Column choices remain available for returning to
Table. The preferred presentation travels in the existing `view=conversation`
URL parameter.

The integration uses the public `whereRow` slot to compose the display row,
the public `QueryFilterRow` and the content row. This preserves native grouping;
supplying `clauses` instead would replace Query Table's grouping presentation.
The source filter API is a stable proxy so metadata effects do not restart
each time a grouped result produces a new controller facade.

## Prototype boundary

Content search is a host population restriction, applied before the table
query. It is serialized in `text_builder` or `kind`, `search` and `case` URL
parameters. Table state remains in `q_library`. Existing links still restore
both. Text search has message-level scope, Boolean/proximity rules, optional
word variations and bounded search batches; files and URLs have their own
case setting. These are not ordinary predicates on a workspace row.

The native saved-query, undo and reset controls currently own `QueryState`,
not this additional search definition or result presentation. Collapsing the builder also uses its
native summary of table state. A production integration should include the
host condition in those interactions, without recreating the native controls
or representing it as a fake WHERE field.

## Upstream request

Provide a public way for an additional population clause to participate in
the existing builder lifecycle. Prefer extending the current composition
APIs over introducing a second builder:

- Compose an additional row and collapsed summary with the built-in rows,
  including the source WHERE and result HAVING of a grouped query.
- Associate a serializable, versioned host definition with a saved query and
  restore it atomically with table state. Match saved-query identity using
  both definitions.
- Include accepted host edits in undo/redo; expose reset and restoration
  hooks. Canceling a draft must not change the applied population.
- Preserve the host definition through Share, grouping and drilldowns, and
  retain compatibility with saved queries that contain only table state.
- Include host result presentation in saved definitions and folded summaries,
  preserving built-in column and grouping controls. A conversation renderer
  needs source records; it must never be handed synthetic grouped rows.

The host should continue executing its search and reporting batch coverage.
Query Table should not implement Pharos's search engine. Existing
`summary`, `matchesSavedQuery`, `shareUrl` and `savedQueryControls` hooks may
cover parts of this; document their supported composition and add only the
missing lifecycle support upstream. Consume any required changes through a
published release, keeping all four npm packages and the Go module aligned.

## Acceptance examples

Save a query with Repository = pharos, a message phrase and a message-role
restriction; clear it, reload, and reopen the save. Restore both definitions.
Undo a content edit without losing grouping, then redo it. Reset the whole
query and remove the host condition. Collapse the builder and still see that
a content restriction applies. Open an older table-only save with no content
restriction. A canceled change from Messages to Files must retain the applied
message search. Test the lifecycle in core/React units; reserve browser tests
for layout, focus and request stability.

## Analyze record-text clauses

Analyze now composes a Record text row through the same public `whereRow`
slot. Unlike Library's content search, this clause matches a chosen text field
of each source record, with the existing parser and unicode61 verifier. Its
field and visual/syntax draft travel in `record_<dataset>`; requests send a
validated `record_search` source restriction. Source WHERE, grouping, HAVING,
metrics and pagination remain native Query Table operations. A collapsed
summary retains the keyword condition. Message scopes and corpus-based fuzzy
expansion are excluded from the record editor.

Include this host definition in the requested lifecycle extension as well:
Save/restore, undo/redo and reset must retain or clear it atomically with the
table query, including grouped source state. Today those native actions own
QueryState; keyword clauses have explicit edit/remove actions and URL state.
Do not encode a keyword tree as a fake field, patch Query Table locally, or
silently reduce a complex keyword tree to field substring predicates.
