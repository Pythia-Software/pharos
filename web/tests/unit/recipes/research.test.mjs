import test from 'node:test';
import assert from 'node:assert/strict';
import {cardCoverQuery,recipeHarness} from '../helpers/recipes.mjs';
import {filterBoxSamples} from '../../../src/compact-box-model';
const now=Date.parse('2026-10-15T12:00:00Z'),{card,table,metric}=recipeHarness(now);
const call=(id,extra={})=>({id,started_at:now-1000,tool_name:'WebSearch',tool_category:'web',status:'ok',search_query:'sqlite query plans',repository_name:'repo',conversation_id:'one',title:'Fixture work',url:null,host:null,...extra});
const archive=(id,extra={})=>({id,called_at:now-1000,tool_name:'search_work',status:'ok',result_count:0,...extra});

test('search-count boxes include single searches and retain sparse conversation cohorts',()=>{
 const rows=[1,2,9].flatMap((n,c)=>Array.from({length:n},(_,i)=>call(`${c}-${i}`,{conversation_id:`c-${c}`})));
 rows.push(call('sparse',{repository_name:'sparse'}),call('missing-conversation',{conversation_id:null}),call('sql',{tool_category:'meta'}));
 const result=filterBoxSamples(metric('research-conversation-searches',rows),3);assert.equal(result.buckets.length,1);const summary=result.buckets[0].distribution.summary;assert.deepEqual([summary.n,summary.min,summary.median,summary.max],[3,1,2,9]);
 assert.equal(table('research-conversation-searches',rows).length,4);
 assert.equal(card('research-conversation-searches').recipe.exploration.coverMetric.groupLimit,10000);
});

test('web source shares use only primary web fields and shared URLs deduplicate conversation IDs',()=>{
 const rows=[call('a',{url:'https://docs.example/a',host:'docs.example'}),call('b',{url:'https://docs.example/a',host:'docs.example'}),call('c',{url:'https://docs.example/a',host:'docs.example',conversation_id:'two',status:'error'}),call('param',{url:'https://docs.example/a?q=1',host:'docs.example',conversation_id:'two'}),call('shell',{tool_category:'command',url:'https://api.example/a',host:'api.example'}),call('results-only',{host:null,url:null}),call('local',{host:'localhost:3000',url:'http://localhost:3000/'})];
 assert.equal(metric('primary-domains',rows).buckets.reduce((sum,b)=>sum+b.value,0),5);
 const {recipe,query}=card('primary-domains'),cover=cardCoverQuery(query,recipe);assert.equal(cover.query.aggregations[0].display.kind,'list');assert.equal(cover.query.aggregations[0].display.maxGroups,12);assert.equal(cover.query.aggregations[0].display.showOther,false);
 const urls=table('primary-urls',rows);assert.equal(urls.length,1);assert.equal(urls[0].card_conversation_id,2);assert.equal(urls[0].card_count,3);assert.equal(urls[0].url,'https://docs.example/a');
});

test('archive empty-search rates exclude unknown counts and failures and retain zero-rate methods',()=>{
 const rows=Array.from({length:10},(_,i)=>archive(String(i),{result_count:i<2?0:5}));
 rows.push(...Array.from({length:10},(_,i)=>archive(`positive-${i}`,{tool_name:'search_text',result_count:1})),...Array.from({length:9},(_,i)=>archive(`sparse-${i}`,{tool_name:'search_messages'})),archive('unknown',{result_count:null}),archive('negative',{result_count:-1}),archive('error',{status:'error'}),archive('disabled',{status:'disabled'}),archive('other',{tool_name:'read_work'}));
 const rates=table('archive-empty-search-rate',rows);assert.equal(rates.length,2);assert.equal(rates.find(r=>r.tool_name==='search_work').rs_empty_rate,.2);assert.equal(rates.find(r=>r.tool_name==='search_work').rs_calls,10);assert.equal(rates.find(r=>r.tool_name==='search_text').rs_empty_rate,0);
});
