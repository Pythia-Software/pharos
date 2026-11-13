# Project colors

Projects with more than 3 conversations have a saved default color and an optional user override. Smaller projects use fallback colors. Settings
→ Project colors offers a picker, previews on both backgrounds, Save color, and
Restore default. Restore returns to the original saved default; it does not
reinspect the checkout or reshuffle other projects. Deliberate overrides are
preserved exactly, including duplicate colors.

## Assignment

`internal/archive/project_colors.go` assigns defaults after ingestion and during project resolution and startup migration, once a project has more than 3 conversations across its workspaces. Requests that read colors never allocate
them. Each preference is keyed by a SHA-256 of the existing stable project ID
(`repository:<id>` or `folder:<folder key>`), not its display name, ranking,
checkout location, visible neighbors, or theme. Defaults and overrides live in
the catalog's `ui_preferences`, so they travel with the library. Separate keys
prevent an edit to one project from overwriting another project's settings.
Assignments for temporarily absent projects remain reserved. Existing choices for projects below the threshold are retained but hidden until the project qualifies again.

New projects look for a chromatic `theme_color` in conventional web manifests,
then favicon/icon SVG or PNG files (including PNG frames inside ICO files).
Manifest hints take precedence. SVG discovery reads literal fill/stroke hex
colors; raster discovery uses alpha-weighted, quantized colored pixels,
ignoring transparent padding and neutral backgrounds. Discovery reads a small
fixed list of local paths, bounded to 512 KiB per file and 1024×1024 decoded
pixels. It never executes repository code, walks directories, or fetches a
remote resource. Missing checkouts, unsupported ICO bitmap frames, named/CSS
SVG colors, and weak neutral signals use the automatic palette. Only new
assignments inspect files: later branding edits do not change an identity.

Defaults use OKLab distance to maximize separation from existing choices.
Strong repository hints first try the same hue and nearby shades; crowded
brand colors fall back to the wider palette. Both existing defaults and user
overrides are reserved, so restoring a default does not cause a new collision.
Initial batches sort by project ID; later projects never rebalance earlier
ones. Defaults target relative luminance .21, giving chart marks at least 3:1
contrast against both light page/panel and dark page/panel backgrounds. The
same hex value is used in both themes. Labels use the normal text color.

Exact duplicates are avoided while candidate colors remain available.
Perceptual separation inevitably decreases in large libraries; color alone
cannot identify hundreds of projects or guarantee discrimination for every
form of color blindness. Keep names, legends, and tooltips alongside marks.
User choices can have lower contrast; the two previews show the result.

## Rendering contract

The blocking preferences script includes the project registry before React
mounts. `web/src/project-colors.ts` adapts names and raw repository IDs to
stable project IDs and installs theme-independent CSS properties. A successful
edit changes those properties, updating already-mounted marks and cached card
previews without refetching data or resetting view state. Failed saves leave
the displayed palette intact.

`chartSeriesStyle` shares project colors across project/repository fields,
including root and ID variants. Dashboard metrics, trends, compact covers,
parallelism ribbons and their cards use this resolver. The public Query Table
theme supplies project overrides to metric panels and editor previews; no
dependency patches are needed. Model, effort and event-kind palettes retain
their established precedence in mixed groupings. A project color describes
the project dimension; it does not replace a model color when a model is the
chosen dimension.

Query Table 1.0.5 does not apply `colorBy` to compound pie/donut slices. A
single-field project pie is consistent; a pie grouped by both project and
workspace still colors whole tuples. The [upstream handoff](handoffs/query-table-pie-color-by.md)
records the required dependency capability. The integration does not patch or
replace that renderer locally.

Values without an eligible saved color use the chart or Query Table fallback palette. Chart fallback colors have the same contrast target. Empty/no-project groups remain neutral. Two projects
with the same name retain separate persisted identities and editable colors.
Legacy results grouped only by that ambiguous name render neutrally: such a
result has already combined the identities, so it cannot truthfully select
one project's color. ID-valued results resolve each project separately.
