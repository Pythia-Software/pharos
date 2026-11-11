# Model and provider colors

Settings → Model & provider colors saves a base color for each model provider
(Anthropic, Google, OpenAI, or the Other providers group). The existing brand
palette supplies defaults. Recorded provider labels such as Claude, Codex,
ChatGPT and Antigravity follow that palette; each label can also have its own
choice. Models receive shades of the model provider's base color. Each model
editor offers five suggested shades, a free color picker, light/dark previews,
Save color, and Use recommended. Provider editors offer Restore brand.

Model suggestions darken by the earliest `effective_from` date in the pricing
catalog. This is a catalog date, not a claimed release date. The scale uses a
fixed epoch and bounded luminance, never the current time, usage rank, visible
models, or the number of entries in a provider. Small deterministic hue/chroma
variations separate models sharing a date. Undated models get a stable shade
and an explicit date-unavailable label. Recommendations remain the same as
new models are discovered. Updating a catalog date can update its suggestion.

A provider edit updates automatic model shades and recorded provider colors.
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
