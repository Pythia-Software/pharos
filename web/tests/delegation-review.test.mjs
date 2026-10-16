import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {build} from 'esbuild';
import * as core from '@pythia-software/query-table-core';
const built=await build({entryPoints:['src/curiosity-data.ts','src/card-cover.ts','src/delegation-review-model.ts','src/compact-box-model.ts','src/card-presentation.ts'],bundle:true,write:false,platform:'node',format:'esm',outdir:'out'});
const mod=name=>import(`data:text/javascript;base64,${Buffer.from(built.outputFiles.find(f=>f.path.endsWith(`/${name}.js`)).text).toString('base64')}`);
const {queryRecipes,instantiateRecipe}=await mod('curiosity-data'),{cardCoverQuery}=await mod('card-cover'),{delegationTheses,delegationReviewKey,delegationFeedback}=await mod('delegation-review-model'),{filterBoxSamples}=await mod('compact-box-model'),{compactMetricPresentation}=await mod('card-presentation');
const schemas=Object.fromEntries(['usage','tool_calls','tl1_attempts'].map(name=>[name,core.loadSchema(JSON.parse(readFileSync(`../schemas/${name}.schema.json`,'utf8')))]));
const now=Date.parse('2026-10-16T12:00:00Z'),day=86400000;
const caps={version:3,profile:'qt-sqlite-v1',groupedRows:true,calendarGrouping:true,computedSourceFilters:true,maxRows:10000,maxGroups:10000,maxPopulation:250000},execution={profile:'qt-sqlite-v1',fields:{},resolvedRevisions:{},planToken:''};
const fact=(id,extra={})=>({id,first_usage_at:now-1000,last_usage_at:new Date(now-1000).toISOString(),day:'2026-10-16',workspace_id:'work',repository_name:'child repo',root_repository_name:'root repo',root_conversation_id:'root',root_title:'Root task',agent_session_id:id,session_kind:'root',delegated_session_id:null,delegated_run_depth:0,total_tokens:100,price_status:'unpriced',...extra});
function card(id,days=7){const recipe=queryRecipes.find(r=>r.id===id);return {recipe,query:instantiateRecipe(recipe,days,new Date(now)).query};}
function table(id,rows){const {recipe,query}=card(id);return core.applyQuery(rows,query,schemas[recipe.dataset],{now}).rows;}
function metric(id,rows){const {recipe,query}=card(id),cover=cardCoverQuery(query,recipe);return core.evaluateMetrics(rows,cover.query,schemas[recipe.dataset],{now}).metrics.find(m=>m.id===cover.metricId);}
const population=()=>[fact('parent',{total_tokens:1000}),fact('handle',{delegated_session_id:'child',delegated_run_depth:1,total_tokens:100,session_kind:'subagent',price_status:'assumed'}),fact('transcript',{delegated_session_id:'child',delegated_run_depth:1,total_tokens:200,workspace_id:'elsewhere',price_status:'partial'}),fact('nested',{delegated_session_id:'nested',delegated_run_depth:2,total_tokens:400}),fact('single',{root_conversation_id:'single',total_tokens:500}),fact('child-only',{root_conversation_id:'inactive-root',delegated_session_id:'active-child',delegated_run_depth:3,total_tokens:600}),fact('zero-repo',{root_conversation_id:'zero',root_repository_name:'zero repo',total_tokens:300}),fact('unknown',{root_conversation_id:null,delegated_run_depth:null,total_tokens:99999}),fact('cycle',{delegated_session_id:'cycle',delegated_run_depth:null,total_tokens:99999}),fact('old',{first_usage_at:now-8*day,delegated_session_id:'old',delegated_run_depth:1,total_tokens:99999}),fact('future',{first_usage_at:now}),fact('zero',{total_tokens:0})];

test('delegation review compiles four approved exact-window covers and destinations and preserves legacy source metrics',()=>{
 const ids=delegationTheses.flatMap(t=>t.cards.map(c=>c.id));assert.equal(new Set(ids).size,4);assert.deepEqual([...ids].sort(),queryRecipes.filter(r=>r.category==='delegation'&&!r.retired&&r.id!=='tl1-revisits').map(r=>r.id).sort());
 for(const id of ids)for(const days of [7,30]){const {recipe,query}=card(id,days);for(const state of [query,cardCoverQuery(query,recipe).query])assert.deepEqual(core.toQueryRequestV3(state,schemas[recipe.dataset],caps,execution,{now,rows:false}).diagnostics,[],id);assert.ok(instantiateRecipe(recipe,days).description.includes(String(days)));assert.ok(recipe.action);assert.ok(!recipe.title.includes('?'));}
 for(const id of ['subagent-usage','task-delegate-count-ranges','delegation-tree-shape','batched-tools']){const {recipe,query}=card(id);assert.equal(recipe.retired,true,id);for(const state of [query,cardCoverQuery(query,recipe).query])assert.deepEqual(core.toQueryRequestV3(state,schemas[recipe.dataset],caps,execution,{now,rows:false}).diagnostics,[],id);}
 assert.equal(card('tl1-revisits').recipe.retired,undefined,'the separate TL1 definition was not rejected');
 for(const [id,metric] of [['delegation-depth','depth-tokens'],['batched-tools','batched-calls'],['tl1-revisits','revisit-attempts']])assert.equal(card(id).recipe.measures[0].id,metric);
});

test('token shares include linked root transcripts and all pricing statuses once with zero repositories retained',()=>{
 const rows=population();assert.equal(metric('subagent-usage',rows).buckets.reduce((n,b)=>n+b.value,0),1300);
 const r=table('repository-delegated-share',rows).find(r=>r.root_repository_name==='root repo');assert.deepEqual([r.dg_child_tokens,r.dg_tokens,r.dg_runs,r.dg_tasks],[1300,2800,3,3]);assert.equal(r.dg_share,1300/2800);assert.equal(table('repository-delegated-share',rows).find(r=>r.root_repository_name==='zero repo').dg_share,0);
 const {recipe,query}=card('repository-delegated-share');assert.equal(cardCoverQuery(query,recipe).query.aggregations[0].display.format.kind,'percent');
});

test('task participation weights root tasks equally, retaining zero-delegation and child-only roots',()=>{
 const rows=population(),m=metric('delegating-task-share',rows),r=m.buckets.find(b=>b.keys[0]==='root repo');assert.equal(r.value,2/3);assert.equal(r.count,3);assert.equal(m.buckets.find(b=>b.keys[0]==='zero repo').value,0);
 const tasks=table('delegating-task-share',rows);assert.equal(tasks.length,4);assert.equal(tasks.find(r=>r.root_conversation_id==='root').dg_runs,2);assert.equal(tasks.find(r=>r.root_conversation_id==='single').dg_runs,0);
});

test('depth counts canonical runs, merging handles and transcripts and retaining inactive ancestor depth',()=>{
 assert.deepEqual(metric('delegation-depth',population()).buckets.map(b=>[b.keys[0],b.value]),[[1,1],[2,1],[3,1]]);assert.equal(table('delegation-depth',population()).length,3);
});

test('boxes sum canonical own usage before sampling and exclude nondelegating tasks from count distributions',()=>{
 const rows=population(),r=filterBoxSamples(metric('delegated-run-token-ranges',rows),3);assert.equal(r.buckets.length,1);const s=r.buckets[0].distribution.summary;assert.deepEqual([s.n,s.min,s.median,s.max],[3,300,400,600]);
 assert.equal(filterBoxSamples(metric('task-delegate-count-ranges',rows),3).buckets.length,0);assert.equal(table('task-delegate-count-ranges',rows).length,2);
 rows.push(fact('third-task',{root_conversation_id:'third',delegated_session_id:'one',delegated_run_depth:1}));const t=filterBoxSamples(metric('task-delegate-count-ranges',rows),3).buckets[0].distribution.summary;assert.deepEqual([t.n,t.min,t.max],[3,1,2]);
});

test('tree shape uses active descendant counts and resolved depth on linear X and log Y',()=>{
 const result=metric('delegation-tree-shape',population());assert.equal(result.buckets.length,2);const root=result.buckets.find(b=>b.keys[1]==='root'),childOnly=result.buckets.find(b=>b.keys[1]==='inactive-root');assert.deepEqual([root.value,root.y],[2,2]);assert.deepEqual([childOnly.value,childOnly.y],[3,1]);
 const {recipe,query}=card('delegation-tree-shape'),p=compactMetricPresentation({metrics:[{clause:cardCoverQuery(query,recipe).query.aggregations[0],result}],coverMetricId:'dg_depth',fields:schemas.usage.fields});assert.equal(p.axisScales.xScale.mode,'linear');assert.equal(p.axisScales.yScale.mode,'log');assert.equal(p.omittedLogPoints,0);
});

test('batching counts calls while explicit TL1 revisits sum attempts as a separate population',()=>{
 const row=table('batched-tools',[{id:'a',tool_name:'Bash',started_at:now-1000,parallel_count:4,error_count:1},{id:'b',tool_name:'Bash',started_at:now-1000,parallel_count:2,error_count:0},{id:'single',tool_name:'Bash',started_at:now-1000,parallel_count:1}])[0];assert.equal(row.card_count,2);assert.equal(row.card_batch_size,3);
 const attempts=[{id:'a',project:'p',task_id:'task',revisit:true,attempt_count:3,started_at:now-1000},{id:'b',project:'p',task_id:'task',revisit:false,attempt_count:9,started_at:now-1000}];assert.equal(table('tl1-revisits',attempts)[0].card_attempt_count,3);
});

test('delegation feedback has independent durable card/group/set drafts and export',()=>{
 assert.equal(delegationReviewKey,'pharos-delegation-review-v2');const draft=delegationFeedback.readReviewFeedback(JSON.stringify({overall:'Explore work splitting.',theses:{shape:'Keep shared axes.'},cards:{'delegation-depth':{verdict:'keep',note:'Useful depths.'},'task-token-ranges':{note:'Other study'},'batched-tools':{verdict:'drop',note:'Old draft'}}}));assert.equal(draft.cards['task-token-ranges'],undefined);assert.equal(draft.cards['batched-tools'],undefined);const report=delegationFeedback.exportFeedback(draft,30,{'delegation-depth':'Delegated runs by depth'});for(const text of ['Delegation cards — review 2','past 30 days','Explore work splitting.','Keep shared axes.','Useful depths.','Verdict: keep'])assert.ok(report.includes(text));
});
