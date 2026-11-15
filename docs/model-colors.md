# Model and provider colors

Settings → Colors → Model & provider colors saves a base color for each model provider
(Anthropic, Google, OpenAI, Reflection, DeepSeek, OpenCode, Microsoft, xAI,
and Cursor). Models with an unknown developer use an Unidentified fallback;
empty categories are hidden. The existing brand palette supplies defaults. Recorded provider labels such as Claude, Codex,
ChatGPT and Antigravity follow that palette. Mainline models stay in their
provider color families: oranges for Anthropic, blues for Google, and greens for
OpenAI. Specialized classes use a separate 75° hue fan, preserving the relative
relationships of the authored class hues around the provider root.
Other developers have independently customizable palettes. Core classes use neighboring hues, with newer and stronger models gaining
chroma at a useful middle lightness rather than becoming nearly black. Each model
editor offers a prominent color-picker button with its hex value, light/dark previews,
and Use recommended. Provider headers offer Save, Discard changes and Restore
brand; a model editor is a compact single row without an individual Save button.
Each action appears only when it would change something: Save and Discard
changes while the provider has pending edits, Restore brand while the proposed
provider color is not the brand color, and Use recommended while the proposed
model color is not its recommended shade. When an action removes itself, focus
returns to that editor's color picker.

Each provider shows one saved-color row. A proposed-color row appears beneath
it only when a draft changes an actual model color; both rows share the same
model labels. Select a swatch in either row or a model label to toggle its single
inline editor. Both rows show an edit icon on the active model, in a contrasting
color for that row’s background. Instructions appear once in
the page introduction.
Provider and model drafts, including Use recommended and Restore brand, stay
local until the provider header’s Save action. That action commits all pending
changes for the provider in one atomic request. Discard changes beside Save clears
all its pending edits. A reset preview follows the proposed provider color even
when the model currently has a saved override. The former model-editor list and recorded-provider-label editors
are removed. Existing stored source-label choices remain supported by the shared
resolver for compatibility.

Every swatch has a visible model label. Banners group by class and then catalog
date, with undated entries last; Fable and Mythos have separate display groups
while retaining their shared color policy. Class headings stay on one compact
horizontal line and truncate when needed, with full names available on hover.
Dense model labels rotate vertically. Picker buttons, save/reset buttons and
light/dark samples share the same row height; picker buttons have no border.
Light/dark samples use square outer borders in both model and project settings;
the color chips have no inner border.
Advisory warnings flag extremely light/dark bases and insufficient perceptual
separation between adjacent dated versions or contemporary core classes. Aliases
and same-date siblings do not create false variety warnings. Warnings do not
prevent saving a deliberate choice.

`pricing/model_color_rules.json` owns the palette policy. Provider hues keep
Anthropic orange, Google blue and mainline OpenAI green. `ranked_classes` records the
capability hierarchy: Haiku → Sonnet → Opus → Fable / Mythos, Flash → Gemini/Argon,
and Luna → Terra → Sol → Astra. Fable and Mythos share a class; specialized
classes such as Nano Banana are not ranked against the core tiers.

The production **Hue fan** policy uses the preset selected in the November 12
color playground: vividness 100%, tier emphasis 50%, frontier lightness 67%, core
hue spread 30°, and specialist spread 75°. These inputs live in the rules'
`palette` object. The generator, custom provider roots and playground share the
same OKLCH formula. The former endpoint-luminance fit is removed.

A model's score combines its reviewed tier position and ordinal release position
within its class. Higher scores increase chroma and approach the frontier
lightness. Core tiers fan over adjacent hues; specialist categories have their
own wider fan and no capability rank. Standalone developers retain their own
provider root. A neutral root stays neutral. Each output is mapped into sRGB by
reducing chroma at fixed lightness and hue, rather than clipping RGB channels.
New releases progress consistently within a class. Historical cross-class
lightness ordering is no longer a constraint: it could drive frontier shades
back toward black. Dense histories may still have close neighbors; generation
and settings report these instead of promising a minimum perceptual separation.

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
instead of Catalog date. Other undated catalog names get a midpoint without consuming a
release step. Observations never change price history or resolve a floating alias
to a permanent version. Unpriced
models discovered only in usage retain a deterministic provider/class fallback
within their class until they have catalog metadata; the identity selects a
deterministic shade without asserting a release date. Merely discovering such names does not rebalance
the global catalog.

A provider edit shifts automatic model colors together and updates recorded provider colors.
Explicit model and recorded-provider choices remain exact, including deliberate
duplicates. Restoring a model uses its provider's current base color; restoring
a provider keeps all individual choices. The shared model normalization folds
Claude prefixes, snapshots, and context suffixes without changing source text
or query identities. Known catalog models and models observed in conversations,
agent sessions, hourly usage, and model requests appear in the editor. Saved
choices remain editable even if their models disappear from the current data.

`/api/model-colors` reads saved choices (`?inventory=1` also discovers models) and accepts a batch `{changes:{id:color,...}}` or a legacy single `{id,color}` edit.
The entire batch is validated before a single transaction writes its changes. IDs use `provider:`, `model:`, or `source:` and existing stable keys.
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

## Local algorithm playground

Run `nvm use`, then `npm --prefix web run demo:model-colors` and open
`http://127.0.0.1:18790`. Pass `-- --port 0` to choose a free port, or an explicit
port. The loopback server builds the standalone experiment into the gitignored
`.context/model-color-lab/` directory. It watches its sources; refresh after edits.
It needs the installed web dependencies but no running Pharos service or library.

The playground compares the exact production resolver against three experimental
OKLCH policies: **Chroma ladder** weights capability tier and release position,
**Hue fan** also spreads tiers over adjacent hues, and **Freshness first** gives
the latest release in each class its strongest color. Root pickers, vividness,
tier weight, frontier lightness and hue spread update the provider's full catalog
palette and representative charts. Light/dark appearances use identical colors.
Neutral roots retain their neutral intent; specialist hue offsets are optional.

The default preset is Hue fan with vividness 100%, tier emphasis 50%, frontier
lightness 67% and core tier hue spread 30°. Specialists have a separate spread
control (0–300°, starting at 75°). Wider hue fan scales their existing authored
hue offsets around the provider root, keeping the relative class relationships;
a lone specialist with no authored offset gets its own hue at half the span.
Original specialist hues restores the earlier fixed offsets, and Use provider
root removes those offsets. These controls leave core tiers and independent
developers unchanged. Preview specialist mix selects a synthetic chart with the
latest catalog model from each specialist class, so the effect is visible in
charts as well as the settings palette. In this mix, the chart reference uses
the selected algorithm with its original specialist hues, so the comparison
isolates the specialist treatment. Specialist positions do not imply rank.

The model identities, reviewed tier order and catalog/observed dates come from
the repository. Release position is an ordinal over distinct dates within each
class, with a midpoint for undated labels. Same-date siblings share that position
and get a small symmetric hue offset. Usage is explicitly synthetic, with
frontier-heavy, balanced, historical and specialist mixes; both chart panels use identical
data and root colors. The inspector shows chroma, lightness and fill contrast,
and the workload summary reports weighted chroma and closest pair separation.

All experiment controls and root colors are preserved in its URL. Export palette
downloads JSON with the inputs, metadata, current colors and experimental colors.
There are no library writes and no saved user overrides are loaded. Current
palette reads production recommendations; Hue fan at the default settings now
matches production, including custom roots. The playground still lets you compare
other settings, Chroma ladder and Freshness first. Close neighbors remain visible
in the inspector and the production advisory warnings. Sources live in `web/experiments/model-color-lab/` and the
launcher is `tools/model-color-lab.mjs`. The standard web typecheck includes the
experiment; to check just the experiment:

```sh
npx --prefix web tsc --project web/experiments/model-color-lab/tsconfig.json --noEmit
```
