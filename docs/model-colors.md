# Model and provider colors

Settings → Model & provider colors saves a base color for each model provider
(Anthropic, Google, OpenAI, or the Other providers group). The existing brand
palette supplies defaults. Recorded provider labels such as Claude, Codex,
ChatGPT and Antigravity follow that palette; each label can also have its own
choice. Models stay in their provider's color family: oranges for Anthropic,
blues for Google, greens for OpenAI, and purples for Other providers. Classes
use different parts of that range, with versions following a smooth light-to-dark progression. Each model
editor offers five suggested shades, a free color picker, light/dark previews,
Save color, and Use recommended. Provider editors offer Restore brand.

`pricing/model_color_rules.json` owns the palette policy. Provider hues keep
Anthropic orange, Google blue and OpenAI green. `ranked_classes` records the
capability hierarchy: Haiku → Sonnet → Opus → Fable / Mythos, Flash → Gemini/Argon,
and Luna → Terra → Sol → Astra. Fable and Mythos share a class; specialized
classes such as Nano Banana are not ranked against the core tiers.

Each class supplies preferred oldest/newest relative luminance, with optional
hue/saturation and singleton preferences. Generation spaces catalog dates along
a straight OKLab ramp. Older versions are lighter; newer versions are deeper,
with clear perceptual steps and no alternating hue or saturation. Wide historical
class ranges can overlap. A constrained fit adjusts the class endpoints together
so the latest releases available at each catalog date keep stronger core tiers
darker. Bounds avoid white/black extremes. If a very dense history cannot fit the
requested version spacing, the generator reduces that spacing and reports the
crowding explicitly; capability order and chronological direction remain required.

The complete inventory, model class assignments, generated swatches and generated
`class_order` live in `pricing/model_colors.json`, which settings, chart stacks
and legends consume. After updating `pricing/cost_changes.json`, run:

```sh
npm --prefix web run generate-model-colors -- --preview
```

The generator adds newly priced models whose classes are recognizable, rebalances
all default colors, and writes `.context/model-colors.html`, with every catalog
entry shown on light and dark backgrounds. A normal frontend build also runs
generation. `npm --prefix web run check-model-colors` checks freshness without
writing; it is part of `npm --prefix web run check` and bundle verification.
Generation is deterministic for the same inventory, dates and rules, independent
of previous colors, usage, current date, filtering and ranking. New releases may
recolor older defaults: useful comparisons take precedence over historical color
stability. Explicit saved user overrides remain exact.

A genuinely new class requires an explicit class rule and capability position
(or omission from `ranked_classes` for a specialized class). Add an optional
`match` regular expression over normalized model names to recognize that class's
future versions without code changes; ambiguous matches fail generation. Existing
catalog class assignments take precedence. New model/provider identities and
release dates are never invented to choose a color.

Chronology uses the earliest non-rejected `effective_from` in the pricing
catalog, not a claimed launch date. Same-date siblings receive nearby distinct
shades without an invented ordering. Undated aliases get a class midpoint and
an explicit date-unavailable label; they do not consume a release step. Unpriced
models discovered only in usage retain a deterministic provider/class fallback
until they have catalog metadata. Merely discovering such names does not rebalance
the global catalog.

A provider edit shifts automatic model colors together and updates recorded provider colors.
Explicit model and recorded-provider choices remain exact, including deliberate
duplicates. Restoring a model uses its provider's current base color; restoring
a provider keeps all individual choices. The shared model normalization folds
Claude prefixes, snapshots, and context suffixes without changing source text
or query identities. Known catalog models and models observed in conversations,
agent sessions, hourly usage, and model requests appear in the editor. Saved
choices remain editable even if their models disappear from the current data.

`/api/model-colors` reads saved choices (`?inventory=1` also discovers models) and accepts one `{id,color}` edit at a
time. IDs use `provider:`, `model:`, or `source:` and existing stable keys.
Each override is a separate JSON-string preference under `pharos-model-color:`
in the library's `ui_preferences`, preserving independent edits and traveling
with the library. An empty color removes only that override. Reads never write
or allocate preferences. The blocking preferences script includes saved colors
without scanning history; inventory discovery happens after page mount.

`model-colors.ts` owns recommendations, persistence and CSS properties.
`chartSeriesStyle` uses stable CSS identities for models and recorded providers,
so mounted charts, cached SVG covers and table-facet badges update without a
data refetch or URL reset. CSS references use bounded hashes of the normalized
identity, keeping both direct references and nested color mixes within the
published Query Table color-length limit. Model/effort combinations derive
from the same model color, with a distinct ordered shade for each supported
effort level. Query Table's public theme supplies overrides for catalog and actual
result keys, including root scopes, unpriced models and raw snapshot names.
The page uses `ViewDisclosure` for provider sections; pickers are persistent
settings with unapplied drafts. No dependency is patched.

The published Query Table 1.0.5 compound pie/donut limitation also applies to
models and providers: whole-tuple slices cannot yet honor the host's `colorBy`.
This remains an upstream blocker; see the [handoff](handoffs/query-table-pie-color-by.md).
Single-field pies use the palette. Names, legends and tooltips remain necessary
when many models have close shades or the user chooses low-contrast colors.
