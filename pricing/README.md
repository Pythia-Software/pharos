# API price history

## Dashboard colors

`model_colors.json` is the static dashboard color catalog, bundled with the
frontend. Model keys use the same normalization as usage/pricing: no `claude-`
prefix, snapshot date, or `-1m`/`[1m]` context suffix. Add a unique hex color and
model class here when adding a priced model. Anthropic models use orange,
Google models use blue (base `#4285f4`), and OpenAI models use green. Classes
and versions get distinct tints/shades within those families. Within each model
class, older versions use lighter tints and newer versions use deeper, darker
shades. Keep that direction when adding new versioned colors.

The `sources` entries map the usage dataset's provider/source values (Claude,
Codex, ChatGPT, Antigravity, etc.) into provider groups without changing table
filters. Codex and ChatGPT have different green shades. The model chart groups
stacks and legends by provider, then model class and version; the five largest
models are initially shown, with the rest behind **Other**. Expanding reveals
five more at a time without recoloring existing series. Unlisted models and
other chart dimensions get stable, collision-checked colors, rather than cycling
through a short palette. Unknown models with recognizable names stay in their
provider's hue family. Rebuild the frontend after editing this catalog.

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
- **`status`**: `proposed` rows are imported but never used for cost. A person flips a row to `confirmed` after checking its source. `rejected` keeps a checked-and-wrong finding from being proposed again.
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

The agent adds only `proposed` rows. Review each row's source, flip the correct
ones to `confirmed` (or `rejected`), and rebuild with `./launch.sh` so the app
embeds the updated file.
