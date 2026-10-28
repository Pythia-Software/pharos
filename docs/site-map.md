# Site map

The site map lists every page Pharos serves, the options each page takes, and,
for the Analyze dashboards, which combinations of options the page draws as
given. Go reads it to browse the site and to build and check links, with no
TypeScript. It is `internal/archive/assets/site-map.json`, embedded with the
other assets.

## Goal

An agent connected to the Pharos MCP server, Ask Pharos included, can browse
the views the site offers, much like reading a CLI's nested `--help`, and get
a link to exactly the view it wants. This example must work end to end:

> "How many tokens have I used per day through Anthropic Opus-class models?
> Show me a 7-day trailing average, grouped by model."

It resolves to the `/analyze` tokens dashboard with:
- the trend view, metric `tokens`, split `model_family`, `rollingPeriods: 7`;
- filters `provider = claude` and `model_family contains opus`;
- a 30-day window.

Pharos records Anthropic usage under the provider `claude`; the usage schema
has no provider `anthropic`. The case is in `testdata/site-map-cases.json`
under `example`.

## Pieces

1. **The site map (this document).** `web/generate-site-map.mjs` bundles
   `web/src/site-map.ts` with esbuild and writes the file, as
   `generate-ask-destinations.mjs` does for Ask's destinations.
   `npm --prefix web run check-site-map` fails when the file is stale, and
   `npm run check` runs it. The map is data, not an algorithm. It lists what
   exists and which combinations are valid, so Go never re-derives
   TypeScript rules.
2. **The link codec.** Query Table 0.9.6 adds `EncodeQueryLink` and
   `DecodeQueryLink` to the Go module, for `2j.` and `2d.` tokens and the
   legacy form. Pharos uses the published release and keeps no copy of the
   format.
   See [the handoff](handoffs/query-table-go-link-codec.md).
3. **MCP tools.** The general Pharos MCP server gains:
   - `browse_site({path?, query?})` lists one level of the map.
   - `site_link({page, options?, filters?, days?, card?})` validates choices
     against the map. It returns `{url, title, summary}`, and its errors list
     the valid choices.
   - Go parses a site URL back into the same state and rebuilds it
     canonically. Parsing and building must round-trip.
4. **Ask integration.** A destination may be a URL. Pharos accepts it only
   when it parses, and replaces it with the canonical URL. Private page
   visits and Ask chats stay on the private lookup protocol, and `ask_chat`
   never appears in the map.

## Coverage

The map covers every page `isUIPage` serves, except token, login and private
routes. Legacy aliases are not offered; only canonical routes are.

| Page | What the map gives |
|---|---|
| Home | The page, with no options |
| Cards | Filters and sort, and every card (see Cards) |
| Library | Text search parameters, and `q_library` where terms |
| Analyze dashboards | Every topic, with chart, filters and window |
| Analyze query | Each dataset, with a where-only `q_<dataset>` |
| Findings | List, finding, tab, repo, and the table view with where-only `q_findings` |
| TL1 | Each page; Runs takes where-only `q_tl1_attempts` |
| Work and reader | IDs, conversation, message, and the delegation explorer's options |
| Settings | Each settings page |

Arbitrary select, aggregation and grouping query states are out of scope.

## Where each fact comes from

`site-map.ts` reads each fact from the module that defines it:

| Facts | Source |
|---|---|
| Routes | `appSections`, `tl1Pages` and `findingsPages` (`app-navigation-model.ts`); `usageTopics` and `usageDatasets` (`usage-workspace-model.ts`); the shell's `VIEW_TITLES` and `data-settings-route` panels (`ui.py`, `managed-library.html`) |
| Page descriptions, alias routes, ID sources, reader parameters | `site-routes.ts`, which owns them |
| Dashboards | `analysisCatalogs`, `dashboardDefaults` (`dashboard-defaults.ts`, which the page uses too), `validAnalysisState` and `normalizeAnalysis` |
| Filterable fields | `schemas/*.schema.json` through Query Table's `loadSchema`, `isFilterable` and `opsForField` |
| Windows | `rollingWindowDays`, `chartRanges`, `withTimeRange`, `resolveRollingQuery`, `readUsageWorkspace` |
| Cards | `queryRecipes`, `instantiateRecipe` (`cardDestination`), `describeRecipeText` |
| Page readers | `findings-route.ts`, `delegation-route.ts`, `card-library-model.ts`, `text-search-model.ts`, `search-composer-state.ts`, `conversationRowURL` |

Some lists live only in Go or in the shell: `isUIPage`, `ROUTES`,
`TL1_PAGES`, the reader's parameters in `updateConversationURL`, and the MCP
tools behind each ID source. `web/tests/unit/site-map/site-map.test.mjs`
compares each of them with the map.

## Regenerating

Run `npm --prefix web run generate-site-map` after changing any of these:
- pages or routes;
- an analysis catalog or dashboard defaults;
- a schema;
- card recipes or their destinations;
- a page's parameters.

It rewrites `internal/archive/assets/site-map.json` and
`testdata/site-map-cases.json`. The generator runs in UTC, so cards that group
by the local calendar carry the UTC time zone.

The generator checks itself, and fails rather than writing a map that
disagrees with the page:
- **Constraint tables.** It runs the page's verdict over every combination
  of each view's enumerated options, then checks that the tables reproduce
  every verdict.
- **Card templates.** It rebuilds each card's URL from its template at every
  window, and compares it with `cardDestination`'s.
- **Window templates.** It rebuilds the window terms and the Parallelism
  ribbon from their templates for every window, and compares them with what
  the page builds.

## Format

All keys are snake_case except option names and analysis state keys, which
are the page's own (`rollingPeriods`, `compareAs`).

### `nodes`

Nodes are keyed by path: `analyze`, `analyze/tokens`, `analyze/query/usage`,
`findings/table`, `settings/sync`. Each node has:

| Key | Meaning |
|---|---|
| `title`, `description` | What the page is |
| `route` | Its path; `{work_id}` marks an ID |
| `fixed` | Parameters the page always carries, such as `analyze_topic` |
| `options` | What a link can choose |
| `children` | Paths of the pages below it |
| `dataset` | The dataset its filters apply to |
| `dashboard` | The topic under `analyze.dashboards` |
| `builder` | The page's URL builder, and its parameter order |
| `settings` | What a link cannot carry, where the page depends on the viewer's settings |

Options and filter fields are addressed below their node:
`analyze/tokens/filters/model_family` is the `model_family` field of the
node's dataset, and `cards/<id>` is a card.

An option has these keys:

| Key | Meaning |
|---|---|
| `name` | The option's name |
| `param` | The query parameter it writes; `key` names a member inside a JSON parameter |
| `kind` | The kind of value (below) |
| `values` | Allowed values, with `label` and `description` |
| `default` | The value the page assumes |
| `id` | The `id_sources` entry an ID comes from |
| `description` | What the option does |

The kinds are:
- `enum`, `text`, `boolean`, `id`, `datetime`;
- `where`: where terms on the node's dataset, encoded with `encodeQuery` as a
  where-only query;
- `window`: rolling days;
- `dashboard_chart`: an Analyze chart (see `analyze`);
- `text_search`: the Library's `text_builder` (see Library);
- `range`: a pair of epoch-millisecond parameters.

Some pages depend on the viewer's settings, and their nodes say so:
- Carbon estimates use the viewer's saved assumptions. A link cannot carry
  them; `analyze_carbon` is not offered.
- Parallelism records follow the saved counting and turn gap. Its Provider
  and Model breakdowns need agent counting.

### `datasets` and `op_sets`

`datasets.<name>` holds the dataset's `label` and `description`, its
`window_field` (where a rolling window applies), and its filterable `fields`.

Each field has:
- `name`, `label`, `type` and `description`;
- `ops`, a key into `op_sets`, which lists the operators;
- `values`, the static options of an enum.

`empty_query` is Query Table's `EMPTY_QUERY`. Query templates leave out the
members equal to it, so a template is merged over `empty_query`. A grouping's
source is merged the same way.

### `id_sources`

Each ID a link needs (`work_id`, `conversation_id`, `message_id`,
`finding_id`, `repository`) maps to the MCP tools that return it, and the
field holding it.

### `windows`

| Key | Meaning |
|---|---|
| `days` | The rolling windows: `rollingWindowDays` |
| `dashboard` | The dashboard range presets, in days |
| `analyze_days`, `analyze_days_default` | The values `readUsageWorkspace` reads back from `analyze_days`, and its fallback |
| `dashboard_terms` | The where terms a dashboard window appends |
| `query_terms` | The where terms a query table window appends |
| `tables` | Lookups keyed by days, or `all` for all time |

In the term templates, `-{window_days}d` takes the window's days.
`{first}` and `{last}` are a dashboard's time fields, and `{field}` is the
dataset's `window_field`. All time appends no terms.

### `analyze`

`analyze.version` is the `v` every canonical chart carries. `labels` and
`options` list the shared values, such as periods and scales.

`analyze.dashboards.<topic>` describes one dashboard:

| Key | Meaning |
|---|---|
| `dataset`, `time` | The dataset, and the fields that hold its range |
| `measures` | Label, unit, kind (`additive`, `ratio`, `count` or `level`), whether it has a per-record value, and a note |
| `breakdowns` | Label and fields |
| `components` | Splits of a measure into parts: which measures each splits, and its fallback |
| `units` | `records` and each unit, with the breakdowns a unit can take |
| `cycles` | Each cycle, or why it is unavailable |
| `unavailable_views` | Views the catalog cannot show, and why |
| `defaults` | The chart the dashboard opens with |
| `views` | Each available view |

`views.<view>` holds:

| Key | Meaning |
|---|---|
| `keys` | The keys of a canonical chart for the view, in order |
| `defaults` | The view's canonical default chart |
| `domains` | For each enumerated option, the values the page accepts |
| `numeric` | Integer ranges (`min`, optional `max`, `integer`) and the legend selection's `max_values` |
| `constraints` | Which combinations the page draws as given |

#### Valid combinations

The page opens a link's `analyze_chart` over the topic's defaults. It rejects
the chart unless `validAnalysisState` accepts it. It then draws
`normalizeAnalysis` of the chart, which may move a setting to one the view can
draw. The map records three verdicts:
- **valid**: the page draws every setting the chart names as given;
- **coercible**: the page opens the chart but draws other settings;
- **invalid**: the page rejects the chart.

The map holds the verdict as **per-option domains plus constraint tables**:

1. The chart is invalid if any of these holds:
   - `v` is not `analyze.version`;
   - `view` is not in `views`;
   - an enumerated key's value is outside `domains`;
   - a numeric key is outside `numeric`;
   - `selection` is not `{values: [string…], excluded: bool}` within
     `max_values`.
2. Otherwise, fill the chart's missing keys from the view's `defaults`. Then
   check each constraint `{option, given, allowed}`. Walk `allowed` by the
   string values of the `given` options, in order, to reach a list. The
   option's value must be in that list.
   - A missing branch means no value is allowed.
   - `multiple_periods` is derived: true when `period` is `hours` with
     `hours` > 1, or `days` with `days` > 1.
3. The chart is valid if every constraint allows it, and coercible otherwise.

The generator derives the tables from the page's own functions. They are not
hand-written.

Why this representation:
- **It factorizes.** The page checks each option's domain on its own, and
  coerces each setting based on only a few others. A full matrix of every
  combination would run to tens of thousands of rows per topic. A
  constraint conditioned on one or two options stays small.
- **Go can explain an error.** The tables list each option's allowed values
  in context, so Go can name the valid choices.
- **It is checked, not assumed.** The generator verifies that the
  conjunction of constraints is exact over every enumerated combination. A
  future rule that does not factorize this way fails the generator, rather
  than producing a wrong map. The constraint shapes are in
  `constraintShapes` in `site-map.ts`.

#### Canonical dashboard links

A canonical chart holds exactly the view's `keys`, in that order, followed by
`selection` when it has values. Build it with `JSON.stringify`'s output:
no spaces and no HTML escaping.

A dashboard link, as `dashboardURL` in `site-map.ts` builds it, is `/analyze`
with these parameters in order:

1. `analyze_mode=dashboards`.
2. `analyze_data`: the topic's dataset.
3. `analyze_topic`.
4. `analyze_days`: the window when it is in `analyze_days`, otherwise
   `analyze_days_default`.
5. `analyze_chart`: the canonical chart.
6. `analyze_filters`: `encodeQuery` of the where terms, followed by
   `dashboard_terms` for the window.
7. For Parallelism only, `analyze_parallelism`: the node's `ribbon` JSON,
   with `range` looked up in `tables.parallelism_range`.

Parameters are form-encoded as `URLSearchParams` does: space as `+`, `*`
kept, and `~` encoded. Go's `url.Values.Encode` sorts keys and differs on
`*` and `~`. Compare links by their parsed parameters, and compare query
tokens by their decoded state.

Query table links follow `queryURL`:
1. `q_<dataset>`: the where terms with `query_terms` appended.
2. `analyze_mode=query`, `analyze_topic` and `analyze_data`, on Analyze.
3. With a window: `window_mode=relative`, `window_days`, `window_field` and
   `window_dataset`.

Findings' table also carries `view=table`. TL1 runs use `/tl1/runs`.

### Library

The `library` node follows `searchURL` and adds the pieces a text search
needs:
- `base_query`: the table query a new search starts from.
- `text_builder_template`: an empty `TextSearchDraft`. Add one
  `rule_template` per condition to `root.children`, with its `kind` and
  `text`.
- `scope_surfaces`: the `surface` and `roles` each scope sets, as
  `chooseScope` does.

The link carries `q_library` first, then the window parameters, then
`text_builder` as JSON. A file or URL search carries `kind`, `search` and
`case` instead of `text_builder`.

### Cards

`cards.items` lists every active card. Retired cards are left out, as the
card library leaves them out. Each card has:

| Key | Meaning |
|---|---|
| `id`, `title`, `description` | The card; text holds `{days}`, `{period}` and `{lastDays}`, which `cards.text_windows` fills for each window |
| `category`, `dataset`, `kind` | Its topic, source dataset and kind |
| `page` | The node it opens |
| `chart` | The `analyze_chart` its link carries, verbatim, which may use older vocabulary |
| `state` | The canonical chart its dashboard draws. Start here to override part of a card |
| `filters` | The dashboard's where terms, other than the window |
| `window` | The window `field`, the windows it supports (`days`), the card library's `default_days`, its window `terms`, and `chart_days`: chart keys that take the window's days |
| `url` | The template for the card's exact link |

`url.params` is an ordered list of `[parameter, spec]`. Each spec is one of:
- a string: a literal value;
- `{days: true}`: the window's days;
- `{query}`: a query template, merged over `empty_query` and encoded;
- `{card: "id" | "title" | "description" | "dataset"}`: that field of the
  card;
- `{card_chart: true}`: the card's `chart`, with `chart_days` set to the
  window;
- `{card_filters: true}`: `filters` followed by the window `terms`;
- `{json, lookup}`: an object whose `lookup` keys come from `windows.tables`.

A card with a `period` (current or all time) has no window and one link.

A card's link keeps `card_*` parameters and its source query. A link that
changes any part of a card is a plain dashboard link built from the card's
`state`, `filters` and the new options.

## Cases

`testdata/site-map-cases.json` holds the cross-language cases. The same
generator writes it.

| Key | Contents |
|---|---|
| `example` | The motivating example, with its canonical URL |
| `analyze` | A spread of each topic's and view's charts. Each has the page's verdict; each valid one has filters using several operators, a window, and the link |
| `cards` | Every card at every window, with its link |
| `nodes` | A link per page, with the state the page's reader reads from it |

Wherever a link carries query tokens, `decoded` holds their state. Compare
decoded state, not deflate bytes.

The node unit tests read each valid link back through the page's own readers:
- dashboards: `readUsageWorkspace` and `readDashboardLink`, then
  `readAnalysisLink`, `validAnalysisState` and `normalizeAnalysis`;
- query tables: `queryFromURL`;
- Library: `restoreSearch`;
- Findings: `readFindingsRoute`;
- the delegation explorer: `readDelegationRoute`.

Each link must yield exactly the state that built it.
