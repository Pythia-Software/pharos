import test from "node:test";
import assert from "node:assert/strict";
import { core, schemas, queryRecipes, instantiateRecipe, cardCoverQuery } from "../helpers/recipes.mjs";
import { weekdayMetric, weekdays, usesWeekdayCover } from "../../../src/weekday-cover";
import { cardKind } from "../../../src/card-cover";
import { cardLoadingPresentation } from "../../../src/card-loading-model";

function evaluate(id, rows, from = "2026-09-28T00:00:00Z", to = "2026-10-12T00:00:00Z", zone = "UTC", metricWhere = [], now = Date.parse(to)) {
  const recipe = queryRecipes.find(recipe => recipe.id === id);
  const item = instantiateRecipe(recipe, 30, new Date(now));
  const source = cardCoverQuery(item.query, recipe).query;
  const query = { ...source, where: [...(recipe.where ?? []), { field: recipe.field, op: ">=", value: from }, { field: recipe.field, op: "<", value: to }],
    aggregations: source.aggregations.map(metric => ({ ...metric, where: metricWhere, dateGrouping: { ...metric.dateGrouping, timeZone: zone } })) };
  const clause = query.aggregations[0], result = core.evaluateMetrics(rows, query, schemas[recipe.dataset], { now }).metrics[0];
  assert.equal(result.error, undefined);
  return { ...weekdayMetric(clause, result, query, now, recipe.weekday.unit), clause, raw: result, query, now };
}
const summary = (result, weekday) => result.result.buckets.find(bucket => bucket.keys[0] === weekday).distribution.summary;
const message = (id, sent_at, typed_words) => ({ id, sent_at, typed_words });

test("weekday boxes compare daily totals and include quiet weekdays at window edges", () => {
  const rows = [message("a", "2026-09-28T09:00:00Z", 10), message("b", "2026-09-28T12:00:00Z", 30), message("c", "2026-10-05T09:00:00Z", 80), message("pasted", "2026-10-05T10:00:00Z", 0)];
  const words = evaluate("human-words-weekday", rows), messages = evaluate("human-messages-weekday", rows);
  assert.deepEqual(words.result.buckets.map(bucket => bucket.keys[0]), weekdays);
  assert.equal(words.days, 14);
  assert.equal(words.observations, 3);
  assert.equal(summary(words, "Monday").n, 2);
  assert.equal(summary(words, "Monday").median, 60);
  assert.equal(summary(words, "Monday").q1, 50);
  assert.equal(summary(words, "Monday").q3, 70);
  assert.equal(summary(messages, "Monday").median, 1.5);
  for (const weekday of weekdays.slice(1)) {
    assert.equal(summary(words, weekday).n, 2, weekday);
    assert.equal(summary(words, weekday).max, 0, weekday);
  }
});

test("parallelism first weights by elapsed time per day, then compares active days equally", () => {
  const rows = [
    { id: "a", started_at: "2026-09-28T09:00:00Z", unit_ms: 10800000, elapsed_ms: 1800000, elapsed_hours: .5 },
    { id: "b", started_at: "2026-09-28T12:00:00Z", unit_ms: 3600000, elapsed_ms: 5400000, elapsed_hours: 1.5 },
    { id: "c", started_at: "2026-10-05T09:00:00Z", unit_ms: 36000000, elapsed_ms: 7200000, elapsed_hours: 2 },
  ];
  const parallel = evaluate("parallel-weekday", rows), active = evaluate("parallel-active-weekday", rows);
  assert.equal(summary(parallel, "Monday").median, 3.5);
  assert.equal(summary(parallel, "Monday").n, 2);
  assert.equal(summary(parallel, "Tuesday"), null);
  assert.equal(parallel.days, 2);
  assert.equal(summary(active, "Monday").median, 2);
  assert.equal(summary(active, "Tuesday").median, 0);
});

test("weekday observations exclude both partial boundary days", () => {
  const rows = [message("partial-start", "2026-10-11T13:00:00Z", 1000), message("monday", "2026-10-12T09:00:00Z", 30), message("partial-end", "2026-10-14T09:00:00Z", 2000)];
  const result = evaluate("human-words-weekday", rows, "2026-10-11T12:00:00Z", "2026-10-14T12:00:00Z");
  assert.equal(result.days, 2);
  assert.equal(result.observations, 1);
  assert.equal(summary(result, "Monday").median, 30);
  assert.equal(summary(result, "Tuesday").median, 0);
  assert.equal(summary(result, "Sunday"), null);
  assert.equal(summary(result, "Wednesday"), null);

  const filteredRows = [message("outside", "2026-10-01T09:00:00Z", 1000), ...rows];
  for (const [op, value, days] of [[">=", "2026-10-11T12:00:00Z", 2], [">=", "-60h", 2], [">", "2026-10-12T00:00:00Z", 1]]) {
    const filtered = evaluate("human-words-weekday", filteredRows, "2026-10-01T00:00:00Z", "2026-10-14T12:00:00Z", "UTC", [{ field: "sent_at", op, value }]);
    assert.equal(filtered.days, days, `${op} ${value}`);
    assert.equal(summary(filtered, "Sunday"), null);
    assert.equal(summary(filtered, "Wednesday"), null);
    assert.equal(summary(filtered, "Tuesday").median, 0);
  }
  for (const [op, value, days] of [["<", "2026-10-13T12:00:00Z", 1], ["<=", "2026-10-13T00:00:00Z", 1]]) {
    const filtered = evaluate("human-words-weekday", filteredRows, "2026-10-11T12:00:00Z", "2026-10-15T00:00:00Z", "UTC", [{ field: "sent_at", op, value }]);
    assert.equal(filtered.days, days, `${op} ${value}`);
    assert.equal(summary(filtered, "Monday").median, 30);
    assert.equal(summary(filtered, "Tuesday"), null);
  }
  const disjoint = evaluate("human-words-weekday", filteredRows, "2026-10-12T00:00:00Z", "2026-10-14T00:00:00Z", "UTC", [{ field: "sent_at", op: "<", value: "2026-10-11T00:00:00Z" }]);
  assert.equal(disjoint.days, 0);
  const instant = evaluate("human-words-weekday", filteredRows, "2026-10-01T00:00:00Z", "2026-10-14T00:00:00Z", "UTC", [{ field: "sent_at", op: "=", value: "2026-10-12T09:00:00Z" }]);
  assert.equal(instant.days, 0, "an equality selects an instant, not a complete day");
});

for (const [from, to, times, weekday] of [
  ["2026-03-07T00:00:00-07:00", "2026-03-10T00:00:00-06:00", ["2026-03-08T01:30:00-07:00", "2026-03-08T03:30:00-06:00"], "Sunday"],
  ["2026-10-31T00:00:00-06:00", "2026-11-03T00:00:00-07:00", ["2026-11-01T01:30:00-06:00", "2026-11-01T01:30:00-07:00"], "Sunday"],
]) test(`local weekday buckets keep DST days whole: ${from}`, () => {
  const result = evaluate("human-words-weekday", times.map((time, i) => message(String(i), time, 10 + 10 * i)), from, to, "America/Denver");
  assert.equal(result.days, 3);
  assert.equal(summary(result, weekday).n, 1);
  assert.equal(summary(result, weekday).median, 30);
});

test("date-only query and metric bounds use UTC instants before selecting complete local days", () => {
  const rows = [message("monday", "2026-10-12T12:00:00Z", 30), message("partial-tuesday", "2026-10-13T12:00:00Z", 1000)];
  const from = "2026-10-12T00:00:00-06:00", to = "2026-10-15T00:00:00Z";
  for (const result of [
    evaluate("human-words-weekday", rows, from, "2026-10-14", "America/Denver", [], Date.parse(to)),
    evaluate("human-words-weekday", rows, from, to, "America/Denver", [{ field: "sent_at", op: "<", value: "2026-10-14" }]),
  ]) {
    assert.equal(result.days, 1);
    assert.equal(summary(result, "Monday").median, 30);
    assert.equal(summary(result, "Tuesday"), null, "18:00 local is a partial-day end");
  }
  const tokyoRows = [message("partial", "2026-10-12T12:00:00Z", 1000), message("tuesday", "2026-10-13T12:00:00Z", 50)];
  for (const tokyo of [
    evaluate("human-words-weekday", tokyoRows, "2026-10-12", "2026-10-14T00:00:00+09:00", "Asia/Tokyo"),
    evaluate("human-words-weekday", tokyoRows, "2026-10-11T00:00:00+09:00", "2026-10-14T00:00:00+09:00", "Asia/Tokyo", [{ field: "sent_at", op: ">=", value: "2026-10-12" }]),
  ]) {
    assert.equal(tokyo.days, 1);
    assert.equal(summary(tokyo, "Monday"), null, "09:00 local is a partial-day start");
    assert.equal(summary(tokyo, "Tuesday").median, 50);
  }
});

test("weekday distributions reject incomplete or erroneous daily reductions", () => {
  const { clause, raw, query, now } = evaluate("human-words-weekday", [message("a", "2026-09-28T09:00:00Z", 10)]);
  for (const bad of [{ coverage: "partial" }, { hiddenGroups: 1 }, { groupCount: 1000 }, { buckets: [{ keys: ["2026-09-28"], value: null, count: 1, error: "invalid" }] }]) {
    assert.throws(() => weekdayMetric(clause, { ...raw, ...bad }, query, now, "words"), /Complete daily totals/);
  }
});

test("weekday templates retain daily detail queries and switch edited covers to native charts", () => {
  for (const recipe of queryRecipes.filter(recipe => recipe.weekday)) {
    const item = instantiateRecipe(recipe, 90);
    assert.deepEqual(recipe.days, [90]);
    assert.equal(item.query.aggregations[0].dateGrouping.timeZone, Intl.DateTimeFormat().resolvedOptions().timeZone);
    assert.equal(cardKind(recipe), "Distribution");
    assert.equal(cardLoadingPresentation(item.query, { recipe }).kind, "box");
    assert.ok(usesWeekdayCover(recipe, item.query.aggregations[0], recipe.cover));
    assert.ok(!usesWeekdayCover(recipe, item.query.aggregations[0], { ...recipe.cover, kind: "line" }));
    assert.ok(!usesWeekdayCover(recipe, { ...item.query.aggregations[0], groupBy: ["repository_name"] }, recipe.cover));
    assert.ok(!usesWeekdayCover(recipe, { ...item.query.aggregations[0], op: "avg" }, recipe.cover));
    assert.equal(cardLoadingPresentation(item.query, { recipe, cover: { ...recipe.cover, maxGroups: 4 } }).count, 4);
    const date = { field: recipe.field, op: ">=", value: "2026-10-12T12:00:00Z" };
    const calendarFilters = ["day", "week", "month", "date", "hour_of_day"].filter(field => core.resolveFieldName(schemas[recipe.dataset], field)).flatMap(field => {
      const calendar = { field, op: "=", value: "2026-10-05" };
      return [calendar, { ...calendar, negated: true }, { any: [calendar, { field: "repository_name", op: "=", value: "other" }] }];
    });
    for (const predicate of [{ any: [date, { field: "repository_name", op: "=", value: "other" }] }, { ...date, negated: true }, { ...date, op: "!=" }, ...calendarFilters]) {
      for (const query of [
        { ...item.query, where: [...item.query.where, predicate] },
        { ...item.query, aggregations: [{ ...item.query.aggregations[0], where: [predicate] }] },
      ]) {
        assert.equal(usesWeekdayCover(recipe, query.aggregations[0], recipe.cover, query), false, JSON.stringify(predicate));
        assert.notEqual(cardLoadingPresentation(query, { recipe }).kind, "box", JSON.stringify(predicate));
      }
    }
    const repositoryFilter = { ...item.query, where: [...item.query.where, { any: [{ field: "repository_name", op: "=", value: "one" }, { field: "repository_name", op: "=", value: "two" }] }] };
    assert.equal(usesWeekdayCover(recipe, repositoryFilter.aggregations[0], recipe.cover, repositoryFilter), true);
  }
});
