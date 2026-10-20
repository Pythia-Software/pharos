import test from "node:test";
import assert from "node:assert/strict";
import { cardCoverQuery, recipeHarness } from "../helpers/recipes.mjs";
import { sharedBoxScale, boxDetails, filterBoxSamples } from "../../../src/compact-box-model";
import { compactMetricPresentation } from "../../../src/card-presentation";
import { stackedRoleRows } from "../../../src/compact-stacked-bars";
const now = Date.parse("2026-10-15T12:00:00Z"), harness = recipeHarness(now), { card } = harness;
const fact = (id, extra = {}) => ({ id, provider: "one", model_family: "alpha", first_usage_at: now - 1000, total_tokens: 10, cost_usd: .001, cache_read_input_tokens: 9, uncached_input_tokens: 1, agent_session_id: "session", conversation_id: "conversation", root_conversation_id: "conversation", root_provider: "one", root_model_family: "alpha", root_effort: "high", price_status: "priced", ...extra });
const facts = [fact("a"), fact("b", { total_tokens: 90, cost_usd: .018, cache_read_input_tokens: 0, uncached_input_tokens: 90 }), fact("unpriced", { price_status: "unpriced", total_tokens: 900, cost_usd: 900 }), fact("assumed", { price_status: "assumed", cost_usd: 50 }), fact("partial", { price_status: "partial", cost_usd: 80 }), fact("missing-session", { agent_session_id: null, total_tokens: 50, price_status: "unpriced" }), fact("other-provider", { provider: "two", total_tokens: 100, cost_usd: .05 }), fact("other-model", { model_family: "beta", total_tokens: 100, cache_read_input_tokens: 0, uncached_input_tokens: 0 })];
const destination = (id, population = facts) => harness.table(id, population);
const metrics = (id, population = facts) => harness.metric(id, population);

test("model cost shares count only fully priced costs", () => {
  const cost = metrics("model-cost-share");
  assert.ok(Math.abs(cost.buckets.reduce((sum, bucket) => sum + bucket.value, 0) - .07) < 1e-10);
});

test("conversation reach counts membership rather than treating models as exclusive buckets", () => {
  const rows = destination("model-conversation-reach");
  assert.ok(rows.every(row => row.mc_conversations === 1));
  assert.equal(rows.reduce((sum, row) => sum + row.mc_conversations, 0), 3);
  assert.equal(new Set(facts.map(row => row.conversation_id)).size, 1);
});

test("token pie preserves its full population beyond named and returned model groups", () => {
  const rows = Array.from({ length: 15 }, (_, index) => fact(String(index), { model_family: `model-${index}`, total_tokens: (index + 1) * 10 }));
  const pie = metrics("model-token-share", rows);
  assert.equal(pie.coverage, "exact");
  assert.ok(pie.other);
  const named = pie.buckets.slice(0, 6).reduce((sum, bucket) => sum + bucket.value, 0);
  const remainder = pie.buckets.slice(6).reduce((sum, bucket) => sum + bucket.value, 0) + pie.other.value;
  assert.equal(named + remainder, 1200);
  assert.ok(remainder > pie.other.value);
});

test("conversation pie conserves model/conversation pairings across overlapping conversations", () => {
  const rows = [fact("a"), fact("b", { model_family: "beta" }), fact("c", { model_family: "beta" }), fact("d", { conversation_id: null })];
  const pie = metrics("model-conversation-reach", rows);
  assert.equal(pie.coverage, "exact");
  assert.equal(pie.buckets.reduce((sum, bucket) => sum + bucket.value, 0), 2);
  assert.equal(new Set(rows.filter(row => row.conversation_id).map(row => row.conversation_id)).size, 1);
  assert.equal(card("model-conversation-reach").recipe.cover.kind, "pie");
});

test("whole conversation effort means include children in one top-level configuration", () => {
  const rows = [fact("root", { cost_usd: 2 }), fact("child", { provider: "two", model_family: "beta", effort: "low", agent_session_id: "child", conversation_id: "child", cost_usd: 4 }), fact("other-root", { root_conversation_id: "second", agent_session_id: "second", cost_usd: 6 }), fact("low-root", { root_conversation_id: "third", root_effort: "low", cost_usd: 1 }), fact("unpriced", { cost_usd: 100, price_status: "assumed" }), fact("orphan", { cost_usd: 100, root_conversation_id: null })];
  const cohorts = destination("effort-cost", rows), high = cohorts.find(row => row.root_effort === "high");
  assert.equal(high.mc_session_cost, 6);
  assert.equal(high.mc_sessions, 2);
  assert.equal(high.mc_cost, 12);
  assert.equal(cohorts.length, 2, "child settings must not create another cohort for the same conversation");
  const share = metrics("effort-cost-share", rows);
  assert.equal(share.buckets.reduce((sum, bucket) => sum + bucket.value, 0), 113, "actual effort shares retain priced facts with unresolved ancestry");
});

test("conversation distributions sum delegated models and repeated facts into one observation", () => {
  const rows = [fact("root", { cost_usd: 2 }), fact("repeat", { cost_usd: 4 }), fact("child", { provider: "two", model_family: "beta", agent_session_id: "child", conversation_id: "child", cost_usd: 10 }), fact("other", { root_conversation_id: "second", agent_session_id: "second", cost_usd: 24 }), fact("unpriced", { price_status: "unpriced", cost_usd: 100 }), fact("orphan", { root_conversation_id: null, cost_usd: 100 })];
  const result = metrics("model-session-costs", rows);
  assert.equal(result.buckets.length, 1);
  const distribution = result.buckets[0].distribution;
  assert.equal(distribution.kind, "box");
  assert.equal(distribution.summary.n, 2);
  assert.equal(distribution.summary.min, 16);
  assert.equal(distribution.summary.median, 20);
  assert.equal(distribution.summary.mean, 20);
  assert.equal(distribution.summary.max, 24);
  const table = destination("model-session-costs", rows);
  assert.equal(table.length, 2);
  assert.equal(table.reduce((sum, row) => sum + row.mc_cost, 0), 40, "delegated cost is counted once across model cohorts");
});

test("session scatters show whole conversations and match grouped destinations", () => {
  const rows = [fact("root", { total_tokens: 100, input_tokens: 80, output_tokens: 20, cost_usd: 2 }), fact("child", { provider: "two", model_family: "beta", conversation_id: "child", agent_session_id: "child", total_tokens: 50, input_tokens: 40, output_tokens: 10, cost_usd: 1 }), fact("other", { root_conversation_id: "second", agent_session_id: "second", total_tokens: 25, input_tokens: 10, output_tokens: 15, cost_usd: .5 }), fact("unpriced", { total_tokens: 1000, input_tokens: 900, output_tokens: 100, price_status: "unpriced", cost_usd: 100 }), fact("orphan", { root_conversation_id: null, total_tokens: 9999, cost_usd: 100 })];
  for (const [id, x, y] of [["session-token-cost", "mc_tokens", "mc_cost"], ["session-input-output", "mc_input", "mc_output"]]) {
    const scatter = metrics(id, rows), table = destination(id, rows), { recipe } = card(id);
    for (const bucket of scatter.buckets) {
      const row = table.find(row => JSON.stringify(recipe.exploration.keys.map(key => row[key])) === JSON.stringify(bucket.keys));
      assert.equal(bucket.value, row[x], id);
      assert.equal(bucket.y, row[y], id);
    }
    assert.equal(scatter.buckets.length, 2);
    assert.equal(scatter.buckets[0].value, id === "session-token-cost" ? 150 : 1020);
    assert.equal(scatter.buckets[0].y, id === "session-token-cost" ? 3 : 130);
    assert.deepEqual(scatter.buckets[0].keys, ["one", "alpha", "conversation"]);
  }
});

test("delegation boxes exclude zeros, count nested executions once, and preserve model/effort cohorts", () => {
  const rows = [0, 0, 1, 2, 5].flatMap((count, i) => [
    fact(`root-${i}`, { root_conversation_id: `root-${i}` }),
    ...Array.from({ length: count }, (_, j) => fact(`delegate-${i}-${j}`, { root_conversation_id: `root-${i}`, agent_session_id: `agent-${i}-${j}`, delegated_session_id: `delegate-${i}-${j}`, model_family: "child-model", effort: "low", price_status: j % 2 ? "unpriced" : "priced" })),
  ]);
  // Repeated facts and launch handles name the same execution as its transcript.
  rows.push({ ...rows.find(row => row.delegated_session_id === "delegate-4-0"), id: "repeat-handle", agent_session_id: "handle" });
  rows.push(fact("old-delegate", { root_conversation_id: "root-0", delegated_session_id: "old", first_usage_at: now - 8 * 86400000 }));
  rows.push(fact("orphan", { root_conversation_id: null, delegated_session_id: "orphan" }));
  rows.push(fact("unknown-effort", { root_conversation_id: "unknown", root_effort: null }));
  const table = destination("session-delegation-count", rows), result = metrics("session-delegation-count", rows);
  assert.equal(table.length, 3);
  assert.equal(table.filter(row => row.root_effort === "high").reduce((sum, row) => sum + row.mc_delegates, 0), 8);
  assert.deepEqual(table.filter(row => row.root_effort === "high").map(row => row.mc_delegates).sort((a, b) => a - b), [1, 2, 5]);
  const summary = result.buckets.find(bucket => bucket.keys[2] === "high").distribution.summary;
  assert.deepEqual([summary.n, summary.min, summary.q1, summary.median, summary.q3, summary.max], [3, 1, 1.5, 2, 3.5, 5]);
  assert.ok(Math.abs(summary.mean - 8 / 3) < 1e-12);
  assert.equal(result.buckets.find(bucket => bucket.keys[2] === null), undefined);
  assert.deepEqual(card("session-delegation-count").recipe.exploration.coverMetric.groupBy, ["root_provider", "root_model_family", "root_effort"]);
  const scale = sharedBoxScale(result.buckets, 6, "linear", true);
  assert.equal(scale.min, 0);
  assert.ok(scale.max >= 5);
  assert.ok(scale.ticks.every(Number.isInteger));
  const tooltip = boxDetails("alpha · high", summary, "Delegated runs / conversation");
  assert.match(tooltip, /Median: 2 · Mean: 2\.67/);
  assert.match(tooltip, /Min: 1 · Max: 5/);
  assert.ok(!tooltip.includes("$"));
});

test("relationship scatters declare log–log axes without transforming their metrics", () => {
  for (const id of ["session-token-cost", "session-input-output"]) {
    const { recipe, query } = card(id), cover = cardCoverQuery(query, recipe);
    const metric = cover.query.aggregations.find(metric => metric.id === id);
    assert.equal(metric.display.xScale.mode, "log", id);
    assert.equal(metric.display.yScale.mode, "log", id);
    assert.match(metric.display.xLabel, /\(log\)$/);
    assert.match(metric.display.yLabel, /\(log\)$/);
    assert.ok(!metric.expression.includes("LOG"));
    assert.ok(!metric.expressionY.includes("LOG"));
    assert.deepEqual(recipe.where, query.grouping.source.where.filter(term => term.field !== "first_usage_at"));
  }
});

test("effort composition orders known effort levels before unknown effort", () => {
  const result = metrics("effort-cost-share", [fact("h", { effort: "high" }), fact("l", { effort: "low" }), fact("n", { effort: null }), fact("u", { effort: "ultra" })]);
  const { recipe, query } = card("effort-cost-share"), projected = cardCoverQuery(query, recipe), clause = projected.query.aggregations[0];
  const presentation = compactMetricPresentation({ state: "ready", total: 4, summary: "Effort", coverMetricId: clause.id, metrics: [{ clause, result }] });
  assert.deepEqual(presentation.metric.result.buckets.map(bucket => bucket.keys[0]), ["low", "high", "ultra", null]);
});

test("cohort thresholds apply to whole-conversation samples before visible limits", () => {
  const rows = [5,4].flatMap((n, model) => Array.from({length:n},(_,i)=>[fact(`${model}-${i}`, {root_conversation_id:`${model}-${i}`,root_model_family:`model-${model}`,cost_usd:1}),fact(`repeat-${model}-${i}`, {root_conversation_id:`${model}-${i}`,root_model_family:`model-${model}`,cost_usd:2})]).flat());
  const raw = metrics("model-session-costs", rows), before = structuredClone(raw);
  const filtered = filterBoxSamples(raw, card("model-session-costs").recipe.cover.minSamples);
  assert.equal(filtered.buckets.length,1);
  assert.equal(filtered.buckets[0].distribution.summary.n,5);
  assert.equal(filtered.processedRows,5);
  assert.deepEqual(raw,before);
  const cohorts = [3,2].flatMap((n, cohort) => Array.from({length:n},(_,i)=>fact(`${cohort}-${i}`, {root_conversation_id:`${cohort}-${i}`,root_effort:`effort-${cohort}`,delegated_session_id:`child-${cohort}-${i}`})));
  cohorts.push(...Array.from({length:10},(_,i)=>fact(`zero-${i}`,{root_conversation_id:`zero-${i}`,root_effort:'effort-1',delegated_session_id:null})));
  const qualifying = filterBoxSamples(metrics("session-delegation-count",cohorts),card("session-delegation-count").recipe.cover.minSamples);
  assert.equal(qualifying.buckets.length,1);
  assert.equal(qualifying.buckets[0].distribution.summary.n,3);
  assert.equal(qualifying.buckets[0].distribution.summary.min,1);
  assert.equal(destination("session-delegation-count",cohorts).length,5);
  for (const id of ["model-session-costs","session-delegation-count"]) {
    const {recipe,query}=card(id);
    assert.equal(cardCoverQuery(query,recipe).query.aggregations[0].groupLimit,10000);
  }
});

test("tree metric headlines surface rare outliers and open ranked conversation destinations", () => {
  const rows=[fact("root",{root_conversation_id:"big",delegated_run_depth:0}),...Array.from({length:9},(_,i)=>fact(`child-${i}`,{root_conversation_id:"big",delegated_session_id:`child-${i}`,delegated_run_depth:i+1})),fact("small",{root_conversation_id:"small",delegated_run_depth:0})];
  const id="largest-subagent-weight", metric=metrics(id,rows), table=destination(id,rows), {recipe}=card(id);
  assert.equal(metric.buckets[0].value,9);
  assert.deepEqual(metric.buckets[0].keys,[]);
  assert.equal(table[0].root_conversation_id,"big");
  assert.equal(table[0][recipe.exploration.valueField],9);
  assert.equal(recipe.cover.minSamples,undefined);
});

test("human-input scatter sums separate human and output events without context duplication", () => {
  const common={root_provider:"one",root_model_family:"alpha",root_conversation_id:"root",event_at:now-1000};
  const rows=[{...common,id:"human-a",human_input_tokens_estimate:5,human_messages:1,output_tokens:0},{...common,id:"human-b",human_input_tokens_estimate:3,human_messages:1,output_tokens:0},{...common,id:"output-a",human_input_tokens_estimate:0,output_tokens:30},{...common,id:"output-child",human_input_tokens_estimate:0,output_tokens:20},{...common,id:"old-human",event_at:now-8*86400000,human_input_tokens_estimate:999,output_tokens:0}];
  const metric=metrics("session-human-output",rows),table=destination("session-human-output",rows);
  assert.equal(metric.buckets[0].value,8);
  assert.equal(metric.buckets[0].y,50);
  assert.equal(table[0].mc_human_input,8);
  assert.equal(table[0].mc_output,50);
  assert.equal(table[0].mc_human_messages,2);
  assert.match(card("session-human-output").recipe.note,/estimate|Estimat/i);
  const clause=card("session-human-output").recipe.measures[0];
  assert.equal(clause.display.xScale.mode,"log");
  assert.equal(clause.display.yScale.mode,"log");
});

test("three role shares count distinct sessions per model, include parent roots, and retain pure roles", () => {
  const rows=[fact('single-a',{agent_session_id:'single',session_role:'singleton'}),fact('parent',{agent_session_id:'parent',session_role:'parent'}),fact('parent-repeat',{agent_session_id:'parent',session_role:'parent'}),fact('sub-a',{agent_session_id:'sub-a',session_role:'subagent'}),fact('sub-b',{agent_session_id:'sub-b',session_role:'subagent'}),fact('parent-other-model',{agent_session_id:'parent',model_family:'beta',session_role:'parent'}),fact('unknown-role',{agent_session_id:'unknown',session_role:null}),fact('missing-id',{agent_session_id:null,session_role:'subagent'}),fact('inactive',{agent_session_id:'inactive',session_role:'singleton',total_tokens:0})];
  const {recipe,query}=card('model-agent-roles'),clause=cardCoverQuery(query,recipe).query.aggregations[0];
  const result=metrics('model-agent-roles',rows),chart=stackedRoleRows(clause,result,recipe.cover.stackBy);
  assert.deepEqual(chart.roles,['singleton','parent','subagent']);
  assert.deepEqual(chart.rows.map(r=>[r.keys,r.singleton,r.parent,r.subagent,r.total]),[[['one','alpha'],1,1,2,4],[['one','beta'],0,1,0,1]]);
  assert.equal(destination('model-agent-roles',rows).reduce((n,r)=>n+r.mc_sessions,0),5);
  assert.equal(stackedRoleRows(clause,{...result,coverage:'partial'},recipe.cover.stackBy),undefined);
  const one=metrics('model-agent-roles',[rows[0]]);
  const presentation=compactMetricPresentation({state:'ready',total:1,summary:'',stackBy:recipe.cover.stackBy,coverMetricId:clause.id,metrics:[{clause,result:one}]});
  assert.equal(presentation.scalar,false);
  assert.equal(presentation.kind,'list');
  const legacy=stackedRoleRows({...clause,groupBy:['provider','model_family','session_kind']},{...result,groupCount:2,buckets:[{keys:['one','alpha','root'],value:1,count:1},{keys:['one','alpha','subagent'],value:2,count:2}]},'session_kind');
  assert.deepEqual(legacy.roles,['root','subagent']);
  assert.deepEqual([legacy.rows[0].root,legacy.rows[0].subagent,legacy.rows[0].total],[1,2,3]);
});
