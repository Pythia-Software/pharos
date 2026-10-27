# Query Table: fit chart group labels instead of clipping them

## Request

In `@pythia-software/query-table-ui` 0.9.5, `MetricChart` reserves a fixed
left margin for group labels: `left = Math.min(78, W * 0.24)`. Box rows and
horizontal bars draw each label at `left - 8`, right-aligned, inside an SVG
that clips its content. A label wider than about 70 px therefore loses its
start. For example, "root_model_family-0" is drawn as "odel_family-0".

Please make one of these changes and publish it in a release:

- size the margin to the widest label up to a cap (for example 40% of the
  width), and ellipsize labels beyond it, keeping the full label in the
  existing tooltip and `aria-label`;
- or wrap labels onto two lines, as list displays already do with
  `list.maxLabelLines`.

## Why Pharos needs it

The Tokens and Cost Distribution view (`web/src/dashboard-analysis.tsx`)
draws one box per model, repository or provider + model group, and real
labels are long (repository names; "claude / claude-opus-4"). Pharos must not
patch the renderer. Its browser structure suite now treats `.qt-metric` as a
container, so it reports SVG text cut off by its chart. It lists this case as
known in `web/tests/browser/structure.test.mjs` (`KNOWN`).

## Acceptance

- At 1440 px and 390 px, a box or horizontal-bar chart with 30-character
  group labels shows each label's start, either whole or ellipsized at its
  end.
- No label text extends outside the chart's SVG.
- Short labels keep today's layout.
