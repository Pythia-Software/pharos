# Query Table: remove tooltips that repeat visible content

Status: blocked on an upstream published release. Pharos currently pins the
four Query Table npm packages and Go module to 1.0.0. The host-owned UI has
been audited and cleaned up separately.

## Requested behavior

Hover help should add information: a definition, calculation assumption,
coverage limitation, unavailable action reason, or otherwise hidden detail.
Do not repeat a visible label, value, or sentence. Icon-only controls still
need action names and accessible labels. Chart marks may expose values that
are not printed on the chart. Full text or precision can help when the visible
presentation actually omits it; identical text adds nothing.

## Confirmed upstream edit points

Inspected in `@pythia-software/query-table-ui` 1.0.0's installed distribution;
the distribution identifies the original source paths below:

- `src/cellFormatting.tsx`, `renderCellDisplay`: successful numeric formatting
  always adds `title: cellValueText(value)`, even when the result is the same
  text. For example, a number display with value `42` prints `42` and gives it
  a `42` tooltip. Chip displays always put the raw key in `title`, including
  when the visible label is that same key. Text displays with `lines` or
  `truncate` attach a full-text title even when the content fits.
- `src/renderers.ts`, `defaultRenderers.truncate`: unconditionally titles the
  text. Only offer full-text help when text is actually clipped.
- `src/JsonCell.tsx`, `JsonCell`: `.qt-json-preview-text` uses the preview
  itself as its title on successful inspection. Keep parse-error explanations;
  the JSON inspector already exposes the full value.
- `src/grouping/GroupingControls.tsx`: the grouped-column label renderer creates
  `.qt-chip-field` with `title: label` and usually the same visible label.
  Remove the duplicate; retain help only for a genuinely different label or
  hidden meaning. Locate it by `.qt-chip-field` and `data-column-role`.

Audit the remaining native metric legends and list rows under the same rule:
legends should not repeat their labels, while chart marks can retain hidden
values, units, and evidence. Pharos's existing compact metric adapter no longer
copies native legend text into its own tooltip.

## Validation and integration

Cover plain and renamed chips, exact and rounded numbers, multiline text that
fits or clips, valid JSON previews and parse errors, and grouped-column labels.
Use unit tests for content selection and browser tests only for real overflow
behavior. Retain accessible names and useful icon-only action help.

Publish the upstream fix, then upgrade all four npm packages and the Go module
to the same release in Pharos, regenerate schemas as needed, and rebuild the
frontend. Do not patch installed packages, generated bundles, or the Go module
cache to suppress these tooltips locally.
