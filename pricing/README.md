# API price history

## Dashboard colors

`model_color_rules.json` encodes the approved palette policy; `model_colors.json`
contains the complete inventory, class assignments, generated swatches and class
order bundled with the frontend. Model keys use the same normalization as
usage/pricing: no `claude-` prefix, snapshot date, or `-1m`/`[1m]` suffix.

After adding sourced model/date records to `cost_changes.json`, use Node 24:

```sh
npm --prefix web run generate-model-colors -- --preview
npm --prefix web run check-model-colors
npm --prefix web run build
npm --prefix web test -- model-colors chart-colors
```

Generation automatically adds recognizable priced models and may recolor any
existing default. Do not pick a new isolated hex value or preserve an old default
at the expense of the palette. `--preview` writes the complete catalog to
`.context/model-colors.html` with light/dark swatches; `--check` verifies freshness
without writing. Normal builds regenerate colors; the web check also rejects stale
catalogs. Rejected price rows do not introduce models or dates.

Provider families remain Anthropic orange, Google blue, OpenAI green, and Other
providers purple. Class rules give preferred `[oldest, newest]` relative luminance,
optional `single_luminance`, and optional hue/saturation overrides. Defaults keep
versions related while using a broad range for distinguishable adjacent releases.
The generator interpolates in OKLab, fits roughly even perceptual steps, and keeps
the latest contemporary core models ordered from lighter/weaker to darker/stronger.
Historical class ranges may overlap. Finite-gamut crowding is reported if version
spacing must be reduced; the generator still preserves chronological direction
and capability order.

`lightness_bounds`, `minimum_version_step` and `contemporary_step` use cube-root
relative luminance, a perceptual depth approximation used to fit the endpoints.
The resulting colors are interpolated in OKLab and rounded to sRGB hex values.

`ranked_classes` defines both the capability hierarchy and presentation order:
Haiku → Sonnet → Opus → Fable / Mythos; Flash → Gemini/Argon; and
Luna → Terra → Sol → Astra. Fable and Mythos share the Fable class. Specialized
classes follow in rule-definition order and are not ranked against core tiers.
A genuinely new class needs an explicit rule and reviewed rank (or a specialized
classification), rather than a guessed place on the intelligence scale. Add a
`match` regex against normalized names to recognize that class's future versions
without editing code; overlapping matches are rejected. Explicit existing catalog
class assignments take precedence. Unrecognizable new models stop generation
before any file is written and identify the missing rule.

Chronology is based on the first non-rejected `effective_from` date, not today's
date or the current usage view. Same-day siblings remain distinct but nearby;
undated aliases occupy a midpoint without consuming a release step. Saved user
choices remain exact even when generated defaults change. See
[Model colors](../docs/model-colors.md) for the full behavior and implementation.

The `sources` entries map the usage dataset's provider/source values (Claude,
Codex, ChatGPT, Antigravity, etc.) into provider groups without changing table
filters. Codex and ChatGPT have different green shades. The model chart groups
stacks and legends by provider, then model class and version; the five largest
models are initially shown, with the rest behind **Other**. Expanding reveals
five more at a time without recoloring existing series. Unlisted models get
stable shades; recognizable classes use their class color. Other chart dimensions
get stable, collision-checked colors rather than cycling through a short palette.
Rebuild the frontend after editing this catalog.

Users can override provider and model colors in Settings → Model & provider
colors. The shared resolver uses the catalog colors by default and shifts them
together when a provider color changes. Unlisted model suggestions are
deterministic. User choices take precedence and survive changes to provider
bases. See [Model colors](../docs/model-colors.md).

Click a chart legend chip to open its filter menu: **Show only this value**,
**Add to selection** (or **Remove from selection**), and **Show all except this
value**. **Show all values** resets that chart's selection without clearing date
or unrelated table filters. Excluding a value includes every other value,
including models folded into **Other**, future models, and missing values.
Menus support arrow-key navigation, Escape, and clicking outside to dismiss.

The **Rolling** control applies a trailing average over 0–10 chart periods:
**0** turns smoothing off, **1** keeps the original values, and **N** averages
the current period with the preceding N−1 days, weeks, or months. Empty periods
inside the displayed range count as zero. At the start of the range, only
available periods are averaged; data outside the current filters is not used.
Token share is calculated from the averaged token counts, not by averaging
each period's percentages. Tooltips and **Other** breakdowns show the same
averaged values as the bars. Table data, totals, and date selection remain
unchanged. The setting is remembered separately for Machine Tokens and Human
Words.

Usage table repository, provider, and model cells show a color badge only when
that facet is selected in the graph. The badge matches the current legend,
including the **Other** color for folded values; expanding **Other** updates
those badges to the newly shown colors. Reported model snapshots and context
variants use their model family's color. These badges are presentation only:
the original cell text, raw values, sorting, copying, and quick filters remain
unchanged.

## Price history

`cost_changes.json` holds the API list prices Pharos uses to turn token
usage into API-equivalent cost. The app embeds this file and imports it into the
`cost_changes` and `model_aliases` tables whenever its contents change. The
`cost_on_date` and `model_alias_on_date` views give each row the half-open date
interval `[effective_from, effective_to)` it applies to.

Cost is **not a bill**. It prices each token category at the standard rate in
effect on the day of use. It ignores subscriptions, batch/flex/priority tiers,
and long-context premiums, so it is a lower bound.

## File contract

```json
{
  "version": 1,
  "currency": "USD",
  "unit": "per_million_tokens",
  "changes": [
    { "provider": "anthropic", "model": "claude-opus-4-8", "effective_from": "2026-05-28",
      "input": 5, "cache_read": 0.5, "cache_write_5m": 6.25, "cache_write_1h": 10, "output": 25,
      "status": "proposed", "source_url": "https://…", "source_title": "…", "notes": "…", "retrieved_at": "2026-09-24" }
  ],
  "aliases": [
    { "alias": "opus", "provider": "anthropic", "model": "claude-opus-4-8", "effective_from": "2026-05-28",
      "status": "proposed", "source_url": "https://…", "source_title": "…", "notes": "…" }
  ]
}
```

- **Rates** are USD per million tokens. Use `null` when the provider has no rate for a category (for example, OpenAI cache writes). Input and output are required.
- **`effective_from`** is the date the price took effect. A price applies until the next row for the same model; an alias applies until the next row for the same alias.
- **Model matching** ignores a `claude-` prefix, snapshot dates (`-20251001`), and `-1m`/`[1m]` context variants, so a single row covers `claude-opus-4-8`, `opus-4-8`, and `opus-4-8-1m`.
- **Aliases** map names such as Claude Code's bare `opus` to the concrete model they meant on a given date.
- **`status`**: `proposed` rows are imported but never used for cost. After source review and user approval, a person or the agent flips approved rows to `confirmed`. `rejected` keeps a checked-and-wrong finding from being proposed again.
- Every row must cite an `https://` source that states the price or mapping.
- **`assumption: true`** marks a price no provider published, such as a Codex-only model priced like its closest API sibling, or pre-launch usage priced at the launch price. `notes` must explain the basis. These rows count toward cost, but that cost is shown as `≈$…` with status `assumed`, so you can filter it out.

Validate the file and see which of your models still lack a confirmed price:

```sh
pharos --config ~/Library/Application\ Support/AI\ Work\ Archive/archive.toml pricing check [FILE]
```

Cost cells show `—` for unpriced usage, `≈` for assumed prices, and a trailing `+` when part of the usage had no rate. Settings → Health shows the share of usage that is priced.

## Refreshing prices

Run a research agent by hand when prices are out of date. On the Usage page,
**Copy refresh prices prompt** copies a prompt built from your catalog: which
models are unpriced and when they were used, the newest price per provider,
which models are priced by assumption, and the rules above. Paste it into a new
agent session in this repository. The button stays available after copying and
is highlighted, with a count, when
usage includes an unpriced model that you haven't copied a prompt for yet.
Copying clears the new-model highlight, but a missing-price notice stays until
the usage has a confirmed price. Placeholders such as `<synthetic>` are ignored.
The same prompt is available from
the command line:

```sh
pharos --config ~/Library/Application\ Support/AI\ Work\ Archive/archive.toml pricing prompt
```

The agent initially adds `proposed` rows, presents their rates and sources, and
explicitly asks whether you approve the pricing changes. An explicit yes, your
request to create a PR for those changes, or a PR you create for them counts as
approval. The agent then marks the approved rows `confirmed` and reruns the
pricing checks. When you request a PR, it confirms the rows before committing
and opening the PR. An agent opening a PR without your request does not count
as approval. Rows remain `proposed` if you decline or have not answered.

If you have already approved through one of those actions, the agent proceeds
without asking again. Rebuild with `./launch.sh` so the app embeds the updated
file.
