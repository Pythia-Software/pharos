# Ask suggestions

Ask Pharos suggests questions above its composer. The aim is to nudge people
toward the kinds of question this library can answer well. A suggestion is a
generic invitation to a conversation, such as "Find the causes of testing
latency in the pharos repository." It is not a report of what is wrong, and it
is not a card. What Pharos noticed decides which questions to offer, and never
appears in them: no failing test names, error text, sites, counts or evidence
lines. Suggestions appear only after Ask Pharos is enabled. Someone who has not
opted in to spending CLI quota is not invited into it.

The system has three layers:

1. Signals are private facts from the library.
2. Areas are the parts of someone's work where a signal found something.
3. Families are the generic questions about an area.

Many signals can raise one area. For example, failing tests, long test runs
and repeated reruns all raise "Tests and builds". One area offers several
questions of different forms.

## Catalog

`web/src/ask-suggestions.ts` defines:
- the areas, each with the site map pages it is about and the signals that
  score it;
- the families, each with an area and a form (`understand`, `compare`,
  `change`, `find`, `improve`, `check`, `learn`), a prior, its text variants
  and, optionally, the subset of its area's signals it needs;
- the pages that no question is about, each with a reason.

`npm --prefix web run generate-site-map` writes the catalog to
`internal/archive/assets/ask-suggestions.json`, which Go embeds.
`npm run check-site-map` fails when that file is stale. Generation fails when:
- a site map page is neither covered by an area nor excluded with a reason;
- an area has no question;
- a family names a signal outside its area;
- a question contains digits, quotes, backticks, or a placeholder other than
  `{scope}` and `{period}`.

A new page therefore needs a question or a decision.

`{scope}` becomes " in the NAME repository" only when two things hold: the
signal found most of its evidence in one named repository, and the library
has at least two named repositories. Otherwise it becomes nothing.
`{period}` becomes "this week", "this month" or "lately", depending on when
the signal's evidence falls.

## Signals

`internal/archive/ask_suggestions.go` implements every signal the catalog
names (`askSignalFuncs`), and a Go test checks the two lists match. Each
signal returns:

| Field | Meaning |
|---|---|
| score | in [0, 1]; 0 means nothing worth asking about |
| support | how much evidence the score rests on |
| repository | the repository holding most of the evidence, if one does |
| period | whether the evidence stands out this week or over the month |
| lead | one private sentence for the agent |

Each signal requires real support before it scores. For example, failing
test or build commands need at least 10 failures in 28 days, and compactions
need at least 3 across 2 conversations. A missing table (agent events, safety
reviews) or unbuilt findings mean no score. An empty library scores nothing,
so it gets no suggestions; nothing is padded with canned questions.

Signals read summaries, not conversation bodies:

| Source | Signals |
|---|---|
| `tool_usage_daily` (the Tools rollup, by `day`) | tests and builds, tool failures, command time, truncated output, web and MCP calls, active days, repositories |
| `agent_session_usage` (by `usage_hour`) | tokens, change in tokens against earlier weeks, cache reuse, reasoning, model mix, delegation, parallel conversations |
| `model_requests` (via `model_requests_compacted_idx`) | compactions |
| `message_authorship` (by `day`) | your writing, back-and-forth, pasted and attached context |
| `agent_events` (confident events, as shown elsewhere) | friction and reactions, by conversation |
| `tool_urls` with `tool_calls` | hosts agents reached for the first time this week, excluding search-result links, this Mac, local networks and bare addresses |
| safety reviews, `FindingsOverview`, `workspaces`, `ask_sessions` | open risky-action reviews, open and watched findings, older work to recall, earlier chats |
| the freshness ledger and backup history | library health, backups |

`pharos.unseen` is the one signal scored while ranking, because it depends on
this Mac's page visits. It needs at least 20 retained visits, and at least
three areas scoring 0.3 or more whose pages have never been visited.

## Background item

The Background Processing item Ask Pharos suggestions (`ask.suggestions`)
exists only where Ask Pharos is enabled. It runs Hourly by default and is
adaptive. Its inputs are:
- the completed generation of each analysis-chain stage;
- the safety and findings build markers;
- the local day, since every window moves daily.

When the inputs change, the item runs every signal, each with a 30 s deadline.
A failed signal is recorded in the row's `errors` and leaves only its own
areas out. The result is written as one `meta` row (`ask_suggestions`), with
each signal's time in `timingsMs`. Enabling Ask Pharos requests a run, and so
does the first suggestions request before any run.

On a 110 GB copy of the production library (7.4M messages, data through
2026-11-01), all signals took about 12 s with a cold page cache and under 2 s
warm. The slowest were first-seen hosts (~5 s cold) and compactions (~4 s
cold). The one-time `model_requests_compacted_idx` build took 7 s. To repeat
the measurement on a copy, see the header of
`internal/archive/ask_suggestions_probe_test.go`:

```sh
PHAROS_ASK_SUGGESTIONS_PROBE=/path/to/copy/catalog.sqlite3 \
  go test -tags probe -run '^TestAskSuggestionsProbe$' -v ./internal/archive
```

## Ranking

`GET /api/ask/suggestions?page=URL&seed=N` ranks the families from the stored
row, this Mac's visits and its Ask history. It never writes. A family's score
is the product of these factors:
- its prior;
- its best signal's score;
- an outcome multiplier, from earlier questions of the family sent as
  offered: the answered share, smoothed toward 75%, within [0.5, 1.2];
- ×1.3 when one of its area's pages is the page the user is on;
- ×1.15 when one of its area's pages was visited this week;
- ×0.2 when the family was asked in the last 14 days, or ×0.4 when a recent
  question shares half its words.

A seeded weighted draw (`hash(seed, day, family)^(1/score)`) orders the
candidates, so stronger questions usually lead and Refresh (`seed + 1`)
reshuffles. The slate holds up to 12 questions:
- one question per area on the first pass, without three of one form in a
  row;
- a second pass allows a second question per area, never two in a row.

The same row and inputs always give the same slate.

## Sending a suggestion

When a draft began as a suggestion, the composer sends `suggestion: {id,
text}` with the question. The turn records the family as `suggestion`, and
records `suggestionEdited` when the prompt shares less than 60% of its words
with what was offered.

A question sent as offered gives the agent `suggestionLeads`: the private
sentences of the family's signals, such as which test commands failed most.
They are labelled as untrusted, possibly partial context to check with the
Pharos tools. Like the page URI, they stay out of the conversation index. An
edited question gets no leads, and its outcome does not count toward the
family's outcomes.

## Not built yet

- An offline evaluation tool that sends every eligible suggestion through Ask
  against a replica and grades the answers, to start priors from measured
  quality.
- Impression-based fatigue. Without writes on the request path, rotation comes
  from the seed and from demoting what was recently asked.
