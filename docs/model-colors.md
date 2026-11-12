# Model and provider colors

Settings → Model & provider colors saves a base color for each model provider
(Anthropic, Google, OpenAI, Reflection, DeepSeek, OpenCode, Microsoft, xAI,
and Cursor). Models with an unknown developer use an Unidentified fallback;
empty categories are hidden. The existing brand palette supplies defaults. Recorded provider labels such as Claude, Codex,
ChatGPT and Antigravity follow that palette. Mainline models stay in their
provider color families: oranges for Anthropic, blues for Google, and greens for
OpenAI. Specialized OpenAI classes use separate hues: yellow for Image, cyan for
Cyber, turquoise for Rosalind, chartreuse for Codex, and lime for general GPT.
Other developers have independently customizable palettes. Classes
use different parts of that range, with versions following a smooth light-to-dark progression. Each model
editor offers five suggested shades, a free color picker, light/dark previews,
Save color, and Use recommended. Provider editors offer Restore brand.

Each provider shows one saved-color row. A proposed-color row appears beneath
it only when a draft changes an actual model color; both rows share the same
model labels. Select a model swatch or label to open its single inline editor.
Provider and model drafts stay local until Save color; Discard changes clears
pending edits. The former model-editor list and recorded-provider-label editors
are removed. Existing stored source-label choices remain supported by the shared
resolver for compatibility.

Every swatch has a visible model label. Banners group by class and then catalog
date, with undated entries last; Fable and Mythos have separate display groups
while retaining their shared color policy. Dense labels rotate vertically.
Light/dark samples use square borders in both model and project settings.
Advisory warnings flag extremely light/dark bases and insufficient perceptual
separation between adjacent dated versions or contemporary core classes. Aliases
and same-date siblings do not create false variety warnings. Warnings do not
prevent saving a deliberate choice.

`pricing/model_color_rules.json` owns the palette policy. Provider hues keep
Anthropic orange, Google blue and mainline OpenAI green. `ranked_classes` records the
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
shades without an invented ordering. `observed_dates` in the rules can position unpriced labels using reviewed first-use
evidence. Those labels participate in chronology and display First observed
instead of Catalog date. Other undated names get a midpoint without consuming a
release step. Observations never change price history or resolve a floating alias
to a permanent version. Unpriced
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
Provider cards offer transient inline model editors; pickers are persistent
settings with unapplied drafts. No dependency is patched.

The published Query Table 1.0.5 compound pie/donut limitation also applies to
models and providers: whole-tuple slices cannot yet honor the host's `colorBy`.
This remains an upstream blocker; see the [handoff](handoffs/query-table-pie-color-by.md).
Single-field pies use the palette. Names, legends and tooltips remain necessary
when many models have close shades or the user chooses low-contrast colors.

## Reviewed legacy label chronology

A read-only audit of production metadata on 2026-11-12 found:

| Label | Message records | First message | Last message | Palette placement |
| --- | ---: | --- | --- | --- |
| `sonnet` | 973 | 2025-08-09 | 2026-07-14 | Between Sonnet 4 and 4.5 |
| `opus` | 10,999 | 2025-08-09 | 2026-09-15 | Between Opus 4.1 and 4.5 |
| `haiku` | 712 | 2025-11-27 | 2026-07-29 | Between Haiku 4.5 and 5.5 |

These are earliest recorded uses, not release dates. Conversation summary labels
start later (Sonnet 2025-11-09, Opus 2025-11-27, Haiku 2026-03-02), so the
message timestamps provide the earlier evidence. The labels span multiple
releases; the color placement does not assert a fixed underlying version.

Color rules may set `pricing_provider` when the price ledger uses a broader
identity. Reflection, DeepSeek, OpenCode and Microsoft retain their existing
`other` price rows while receiving independent developer palettes. A normalized
model matcher selects the color provider; no source-recorded provider, pricing
row or query identity is rewritten. The API validates provider preference keys
against the same embedded color catalog as the frontend.
