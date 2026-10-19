import test from 'node:test';
import assert from 'node:assert/strict';
import * as core from '@pythia-software/query-table-core';
import {schemas} from '../helpers/recipes.mjs';
import {queryRecipes,instantiateRecipe} from '../../../src/curiosity-data';
import {cardCoverQuery} from '../../../src/card-cover';
import {researchTheses,researchReviewKey,researchFeedback} from '../../../src/research-review-model';
import {filterBoxSamples} from '../../../src/compact-box-model';
import {compactPieSlices} from '../../../src/compact-pie-model';
const now=Date.parse('2026-10-15T12:00:00Z'),caps={version:3,profile:'qt-sqlite-v1',groupedRows:true,calendarGrouping:true,computedSourceFilters:true,maxRows:10000,maxGroups:10000,maxPopulation:250000},execution={profile:'qt-sqlite-v1',fields:{},resolvedRevisions:{},planToken:''};
const call=(id,extra={})=>({id,started_at:now-1000,tool_name:'WebSearch',tool_category:'web',status:'ok',search_query:'sqlite query plans',repository_name:'repo',conversation_id:'one',title:'Fixture work',url:null,host:null,...extra});
const archive=(id,extra={})=>({id,called_at:now-1000,tool_name:'search_work',status:'ok',result_count:0,...extra});
function card(id,days=7){const recipe=queryRecipes.find(r=>r.id===id);return{recipe,query:instantiateRecipe(recipe,days,new Date(now)).query};}
function table(id,rows,days=7){const {recipe,query}=card(id,days);return core.applyQuery(rows,query,schemas[recipe.dataset],{now}).rows;}
function metric(id,rows){const {recipe,query}=card(id),cover=cardCoverQuery(query,recipe);return core.evaluateMetrics(rows,cover.query,schemas[recipe.dataset],{now}).metrics.find(m=>m.id===cover.metricId);}

test('research review covers all four active definitions and compiles covers and destinations',()=>{
 const ids=researchTheses.flatMap(t=>t.cards.map(c=>c.id));assert.deepEqual(ids,["research-conversation-searches","primary-domains","primary-urls","archive-empty-search-rate"]);
 assert.deepEqual([...ids].sort(),queryRecipes.filter(r=>r.category==='research'&&!r.retired).map(r=>r.id).sort());
 for(const id of ['research-repositories','research-queries','research-query-reach','empty-mcp-searches']) { const {recipe,query}=card(id);assert.equal(recipe.retired,true,id);for(const state of [query,cardCoverQuery(query,recipe).query])assert.deepEqual(core.toQueryRequestV3(state,schemas[recipe.dataset],caps,execution,{now,rows:false}).diagnostics,[],id); }
 for(const id of ids){const {recipe,query}=card(id,30);for(const state of [query,cardCoverQuery(query,recipe).query]) assert.deepEqual(core.toQueryRequestV3(state,schemas[recipe.dataset],caps,execution,{now,rows:false}).diagnostics,[],id);assert.ok(instantiateRecipe(recipe,30).description.includes('30'),id);assert.ok(recipe.action,id);}
});

test('repository shares conserve exact Other and exclude non-web query metadata',()=>{
 const rows=Array.from({length:20},(_,i)=>call(String(i),{repository_name:i===0?null:`repo-${i}`}));
 rows.push(call('sql',{tool_category:'meta'}),call('mcp',{tool_category:'mcp'}),call('blank',{search_query:''}),call('missing',{search_query:null}),call('old',{started_at:now-8*86400000}),call('error',{status:'error'}),call('no-result',{status:'no_result'}));
 const pie=compactPieSlices(metric('research-repositories',rows),5);assert.equal(pie.total,22);assert.equal(pie.slices.length,6);assert.equal(pie.slices.at(-1).other,true);assert.equal(pie.slices.reduce((sum,s)=>sum+s.value,0),22);
 assert.ok(table('research-repositories',rows).some(r=>r.repository_name===null));
 const {recipe,query}=card('research-repositories');assert.equal(cardCoverQuery(query,recipe).query.aggregations[0].groupLimit,10000);
});

test('search-count boxes include single searches and retain sparse conversation cohorts',()=>{
 const rows=[1,2,9].flatMap((n,c)=>Array.from({length:n},(_,i)=>call(`${c}-${i}`,{conversation_id:`c-${c}`})));
 rows.push(call('sparse',{repository_name:'sparse'}),call('missing-conversation',{conversation_id:null}),call('sql',{tool_category:'meta'}));
 const result=filterBoxSamples(metric('research-conversation-searches',rows),3);assert.equal(result.buckets.length,1);const summary=result.buckets[0].distribution.summary;assert.deepEqual([summary.n,summary.min,summary.median,summary.max],[3,1,2,9]);
 assert.equal(table('research-conversation-searches',rows).length,4);
 assert.equal(card('research-conversation-searches').recipe.exploration.coverMetric.groupLimit,10000);
});

test('query frequency and conversation reach have distinct denominators and exact query keys',()=>{
 const rows=[call('a'),call('b'),call('c',{conversation_id:'two',status:'error'}),call('d',{search_query:'Different case'}),call('e',{search_query:'different case'}),call('missing',{conversation_id:null}),call('sql',{tool_category:'meta',conversation_id:'three'})];
 const counts=table('research-queries',rows);assert.equal(counts.length,1);assert.equal(counts[0].card_count,4);
 const reach=table('research-query-reach',rows);assert.equal(reach.length,1);assert.equal(reach[0].rs_conversations,2);assert.equal(reach[0].rs_calls,3);
 assert.equal(table('research-query-reach',[call('a'),call('b')]).length,0);
});

test('web source shares use only primary web fields and shared URLs deduplicate conversation IDs',()=>{
 const rows=[call('a',{url:'https://docs.example/a',host:'docs.example'}),call('b',{url:'https://docs.example/a',host:'docs.example'}),call('c',{url:'https://docs.example/a',host:'docs.example',conversation_id:'two',status:'error'}),call('param',{url:'https://docs.example/a?q=1',host:'docs.example',conversation_id:'two'}),call('shell',{tool_category:'command',url:'https://api.example/a',host:'api.example'}),call('results-only',{host:null,url:null}),call('local',{host:'localhost:3000',url:'http://localhost:3000/'})];
 assert.equal(metric('primary-domains',rows).buckets.reduce((sum,b)=>sum+b.value,0),5);
 const {recipe,query}=card('primary-domains'),cover=cardCoverQuery(query,recipe);assert.equal(cover.query.aggregations[0].display.kind,'list');assert.equal(cover.query.aggregations[0].display.maxGroups,12);assert.equal(cover.query.aggregations[0].display.showOther,false);
 const urls=table('primary-urls',rows);assert.equal(urls.length,1);assert.equal(urls[0].card_conversation_id,2);assert.equal(urls[0].card_count,3);assert.equal(urls[0].url,'https://docs.example/a');
});

test('archive empty counts and rates exclude unknown counts and failures and retain zero-rate methods',()=>{
 const rows=Array.from({length:10},(_,i)=>archive(String(i),{result_count:i<2?0:5}));
 rows.push(...Array.from({length:10},(_,i)=>archive(`positive-${i}`,{tool_name:'search_text',result_count:1})),...Array.from({length:9},(_,i)=>archive(`sparse-${i}`,{tool_name:'search_messages'})),archive('unknown',{result_count:null}),archive('negative',{result_count:-1}),archive('error',{status:'error'}),archive('disabled',{status:'disabled'}),archive('other',{tool_name:'read_work'}));
 const count=table('empty-mcp-searches',rows);assert.equal(count.reduce((sum,r)=>sum+r.card_count,0),11);
 const rates=table('archive-empty-search-rate',rows);assert.equal(rates.length,2);assert.equal(rates.find(r=>r.tool_name==='search_work').rs_empty_rate,.2);assert.equal(rates.find(r=>r.tool_name==='search_work').rs_calls,10);assert.equal(rates.find(r=>r.tool_name==='search_text').rs_empty_rate,0);
});

test('selected archive periods and existing source metric IDs remain executable',()=>{
 const rows=[archive('old',{called_at:now-8*86400000})];assert.equal(table('empty-mcp-searches',rows).length,0);assert.equal(table('empty-mcp-searches',rows,30)[0].card_count,1);
 for(const [id,metricId] of [['primary-urls','url-conversations'],['empty-mcp-searches','empty-searches']])assert.ok(card(id).recipe.measures.some(m=>m.id===metricId));
});

test('research feedback is isolated and exports exact archive windows with card/group/set feedback',()=>{
 assert.equal(researchReviewKey,'pharos-research-review-v2');
 const draft=researchFeedback.readReviewFeedback(JSON.stringify({overall:'Compare counts with reach.',theses:{archive:'Keep the denominator visible.'},cards:{'archive-empty-search-rate':{verdict:'keep',note:'Inspect the missing counts.'},'skill-body-sizes':{verdict:'drop',note:'Other study'},'research-queries':{verdict:'drop',note:'Earlier review'}}}));assert.equal(draft.cards['skill-body-sizes'],undefined);assert.equal(draft.cards['research-queries'],undefined);
 const report=researchFeedback.exportFeedback(draft,7,{'archive-empty-search-rate':'Pharos empty-search rates'},{'archive-empty-search-rate':30});
 for(const text of ['Research cards — review 2','past 7 days','past 30 days','Compare counts with reach.','Keep the denominator visible.','Inspect the missing counts.','Verdict: keep']) assert.ok(report.includes(text),text);
});
