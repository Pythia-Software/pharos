// Geometry and labels shared by the compact card covers: pies and box plots.
import test from "node:test";
import assert from "node:assert/strict";
import * as core from "@pythia-software/query-table-core";
import { boxBytes, boxDetails, boxDuration, boxOutliers, boxWhiskerExtent, sharedBoxScale } from "../../../src/compact-box-model";
import { compactPieSlices, pieWedgePath } from "../../../src/compact-pie-model";

test("pie geometry divides a shared circle evenly and conserves a folded Other", () => {
  const result={id:"pie",coverage:"exact",groupCount:4,buckets:Array.from({length:4},(_,i)=>({keys:[String(i)],value:1,count:1}))};
  const pie=compactPieSlices(result,4);
  assert.equal(pie.total,4);
  for(const slice of pie.slices) {
    assert.ok(Math.abs(slice.end-slice.start-Math.PI/2)<1e-12);
    assert.match(pieWedgePath(slice.start,slice.end),/A 48 48 0 0 1/);
  }
  assert.ok(Math.abs(pie.slices.at(-1).end-pie.slices[0].start-2*Math.PI)<1e-12);
  const folded=compactPieSlices({...result,other:{keys:["Other"],value:2,count:2}},2);
  assert.equal(folded.total,6);
  assert.deepEqual(folded.slices.map(s=>s.value),[1,1,4]);
  assert.ok(compactPieSlices({...result,coverage:"partial"},4).error);
});

test("shared box axes cover every visible extreme, preserve zeros, and use one mapping across models", () => {
  const buckets = [
    { keys: ["cheap"], value: 1, count: 3, distribution: { kind: "box", summary: { n: 3, min: .001, q1: .2, median: .5, mean: 10, q3: .8, max: 100, low: .001, high: 1, outlierCount: 2, outliers: [10], whiskers: "tukey", method: "exact-linear" } } },
    { keys: ["expensive"], value: 10, count: 3, distribution: { kind: "box", summary: { n: 3, min: .1, q1: 2, median: 5, mean: 20, q3: 8, max: 200, low: .1, high: 10, outlierCount: 1, outliers: [200], whiskers: "tukey", method: "exact-linear" } } },
  ];
  const before = structuredClone(buckets), scale = sharedBoxScale(buckets, 2, "log");
  assert.equal(scale.log, true);
  assert.ok(scale.min <= .001 && scale.max >= 200);
  assert.ok(Math.abs((scale.fraction(10) - scale.fraction(1)) - (scale.fraction(1) - scale.fraction(.1))) < 1e-12);
  assert.ok(boxOutliers(buckets[0].distribution.summary).includes(100), "a sampled outlier array must not hide the actual maximum");
  assert.deepEqual(buckets, before);
  buckets[0].distribution.summary.min = 0;
  buckets[0].distribution.summary.low = 0;
  const zero = sharedBoxScale(buckets, 2, "log");
  assert.equal(zero.log, false);
  assert.equal(zero.min, 0);
  assert.equal(zero.fraction(0), 0);
});

test("sparse skewed box whiskers meet interpolated quartiles without passing the true extremes", () => {
  // The reported GPT 6.1 Sol Ultra cohort: Q3 lies past the last observed inlier.
  for (const values of [[28798,729273,3177124,1728195886],[0,100,101,102],[5,5,5],[1,2,3,4,5]]) {
    const summary=core.boxSummary(values,"tukey"), original=structuredClone(summary), extent=boxWhiskerExtent(summary);
    assert.ok(summary.min<=extent.low && extent.low<=summary.q1);
    assert.ok(summary.q3<=extent.high && extent.high<=summary.max);
    const axis=sharedBoxScale([{keys:[],value:summary.median,distribution:{kind:"box",summary}}],1,"log");
    for (const value of [extent.low,summary.q1,summary.median,summary.q3,extent.high,summary.max]) assert.ok(axis.fraction(value)>=0 && axis.fraction(value)<=1);
    assert.deepEqual(summary,original,"render geometry does not alter statistical values or classify new outliers");
  }
  const summary=core.boxSummary([28798,729273,3177124,1728195886],"tukey");
  assert.equal(summary.high,3177124);assert.equal(summary.q3,434431814.5);assert.deepEqual(summary.outliers,[1728195886]);
  const tooltip=boxDetails("Codex GPT 6.1 Sol Ultra",summary,"tokens");
  assert.match(tooltip,/Observed whisker range: 28,798 – 3,177,124/);assert.match(tooltip,/Max: 1,728,195,886/);assert.match(tooltip,/Interpolated quartile/);
});

test("duration and byte boxes show readable units with exact values in hover, without changing count or currency formats", () => {
  assert.equal(boxDuration(0),"0ms");
  assert.equal(boxDuration(500),"500ms");
  assert.equal(boxDuration(1500),"1.5s");
  assert.equal(boxDuration(60000),"1min");
  assert.equal(boxDuration(3600000),"1h");
  const summary={n:3,min:1000,q1:1500,median:2000,mean:3000,q3:4000,max:6000,low:1000,high:6000,outlierCount:0,outliers:[],whiskers:"tukey",method:"exact-linear"};
  assert.match(boxDetails("command",summary,"ms"),/Median: 2s \(2,000 ms\)/);
  assert.match(boxDetails("command",summary,"ms"),/Max: 6s \(6,000 ms\)/);
  assert.ok(!boxDetails("command",summary,"ms").includes("$"));
  assert.match(boxDetails("count",summary,"calls"),/Median: 2,000 · Mean: 3,000/);
  assert.match(boxDetails("cost",summary,"USD"),/Median: \$2,000\.00/);
  assert.equal(boxBytes(0),"0 B");
  assert.equal(boxBytes(1536),"1.5 KiB");
  assert.equal(boxBytes(1073741824),"1 GiB");
});
