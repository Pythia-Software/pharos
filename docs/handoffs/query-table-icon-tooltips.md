# Query Table: icon-button tooltips

Pharos audited the published `@pythia-software/query-table-ui` 1.0.1. Some
icon-only controls have accessible names but no visible hover explanation.
The host cannot fix these in the package or its generated bundle under its
Query Table dependency policy.

Please audit upstream icon-only buttons and supply brief, native hover tooltips
(or equivalent delayed tooltips) for actions whose purpose is not obvious.
Keep the existing accessible names, state, and keyboard behavior. Known examples
in the 1.0.1 distribution `dist/index.js`:

- JSON inspector `qt-json-toggle`: dynamically say “Expand” or “Collapse”.
- Property editor and column editor up/down arrows (`qt-property-icon-button`
  and `qt-editor-move`): “Move up” and “Move down”.
- Field picker and representative editor back arrows: “Back to fields” and
  “Back to columns”.
- Reorder handle (`qt-reorder-handle`): “Reorder”.

The above list is a starting point; audit the remaining controls too. X buttons
are visually self-explanatory. Verify visible tooltip text and changing state in
browser tests, then publish an aligned Query Table release for Pharos to consume.
