import {test} from 'node:test';
import assert from 'node:assert/strict';
import {estimateFlag, estimatedField, withEstimateMetrics, estimateMetricFlags} from '../../../src/usage-estimate-model';

test('estimate markers apply to consumption or duration, never unrelated counts',()=>{
  assert.equal(estimateFlag(false),false);
  assert.equal(estimatedField({is_estimate:true},'output_tokens'),true);
  assert.equal(estimatedField({is_estimate:1},'cost_usd'),true);
  assert.equal(estimatedField({is_estimate:1},'call_count'),false);
  assert.equal(estimatedField({duration_is_estimate:1},'duration_ms'),true);
  assert.equal(estimatedField({is_estimate:0},'total_tokens'),false);
});

test('metric provenance follows filters and scope without changing the requested measures',()=>{
  const request={version:3,rows:false,metrics:[{id:'output',op:'sum',field:'output_tokens',groupBy:['model'],where:[{field:'model',op:'eq',value:'claude'}],scope:'shownRows'},{id:'sessions',op:'count',groupBy:[]}]};
  const out=withEstimateMetrics('usage',request);
  assert.equal(request.metrics.length,2);
  assert.deepEqual(out.metrics.slice(0,2),request.metrics);
  assert.equal(out.metrics.length,3);
  assert.deepEqual(out.metrics[2].where,request.metrics[0].where);
  assert.equal(out.metrics[2].scope,'shownRows');
  assert.equal(out.metrics[2].expression,'MAX(IF([is_estimate],1,0))');
  const flags=estimateMetricFlags({metrics:[{id:out.metrics[2].id,buckets:[{value:1}]}]});
  assert.deepEqual([...flags],['output']);
  assert.equal(estimateMetricFlags({metrics:[{id:out.metrics[2].id,buckets:[{value:0}]}]}).size,0);
  assert.equal(withEstimateMetrics('messages',request),request);
  assert.equal(withEstimateMetrics('usage',{...request,grouping:{summaries:[]}}).metrics[2].expression,'MAX([__pharos_estimate])');
});
