# Query Table: host content in the run bar

**Resolved:** Query Table 1.0.2 added `runActionsBeforeActivity` and documented
`qt-btn`; Pharos integrates them with Query Table 1.0.3. The request below is
kept as context for the design and its acceptance criteria.

## Request

Please provide a supported React content slot in `QueryBuilder`'s row-count and
update bar immediately before the Request activity (stopwatch) button. Pharos
wants its data-readiness button there, followed by Request activity, Auto-Update
and Run Now. The button opens a host-owned dialog describing whether the
current query's results include pending changes. It must remain available
when the query editor is collapsed.

At the time of the request, Pharos used `@pythia-software/query-table-ui` 1.0.1,
the latest published UI release checked on 2026-11-08. `QueryBuilderProps` offers `whereRow`, `clauses`,
`summary`, `savedQueryControls` and `requestActivityDetails`, but none inserts
content into this action bar. `requestActivityDetails` appears *inside* the
activity panel, not alongside its button. In the published component the bar
is outside the collapsible editor; its action group renders the optional
polling status, Request activity, Auto-Update and Run Now. Pharos currently
renders the readiness button as a sibling of the entire builder in
`web/src/data-taxonomy.tsx`, leaving it beside the top of the editor.

## Proposed API

A named `ReactNode` slot such as `runActionsBeforeActivity`, or an equivalent
named slot in a `slots` object, would meet this request. Render it in the action
group after any auto-update status and before the activity button. Keep the
host node in the normal React tree; do not require a DOM selector, portal,
mutation observer or replacement of Query Table controls. Omission should
preserve the current layout and behavior. The host owns the node, its state,
accessible name and dialog; Query Table should not wrap it in another button or
change its event handling.

This could be the first of a small, documented set of **semantic** insertion
points, rather than a one-off readiness API. If other slots are added, name each
for its location and ordering (for example, before or after run actions), and
define its behavior in expanded and collapsed layouts. Do not require unused
slots to introduce empty spacing. A host must be able to use more than one
builder on a page with independent content and lifecycles.

The readiness control should use Query Table's normal button appearance and
interaction states while retaining Pharos's status-colored icon and accessible
label. Please document the supported button styling or expose a suitable
button primitive if `.qt-btn` is not intended as public styling API. An icon-only
control needs a usable touch target, visible keyboard focus, a meaningful host
accessible name and ordinary tab ordering.

## Acceptance

- A host-supplied button appears immediately before Request activity in the run
  action group, both with the query editor expanded and collapsed. The status
  text, activity panel, Auto-Update and Run Now continue to work as before.
- On narrow and touch layouts, all actions remain visible and operable without
  horizontal overflow or overlap, including when auto-update status is present.
  The current narrow layout has a two-column action grid and makes status span
  both columns; adding a child must not accidentally obscure or reorder actions.
- Keyboard order follows the visual order; the host button opens its dialog,
  and closing it returns focus to the invoking button. Loading, error,
  auto-update and rerender changes do not remount or duplicate the host node.
- With the slot omitted, existing consumers and layouts remain unchanged.
  Include upstream component and browser coverage for populated and empty slots,
  collapsed and expanded states, active auto-update, and a narrow viewport.

## Pharos integration plan

Publish the change upstream. Pharos can then pin the four npm packages and Go
module to the same release, pass its existing `DataReadinessIndicator` from
`SourceQueryBuilder` to the new slot, remove the sibling and its layout rule,
update the data-readiness coverage note, and rebuild the frontend. Preserve the
existing `query:${dataset}` identity, dataset, current query, empty-result
semantics and focus restoration. Verify Risky actions and another shared query
table at desktop and phone widths, including a collapsed editor and the dialog.
No dependency patch, DOM reparenting, generated-bundle edit or local Query Table
fork is needed or permitted.
