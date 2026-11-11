import { capabilities } from "../helpers/recipes.mjs";
import { thinkingQueryContext, ribbonQueryContext } from "../../../src/dashboard-query-adapters";
import { dashboardExplorations } from "../../../src/dashboard-query-explorations";
import { dashboardQueryLinks } from "../../../src/dashboard-query-links";
import { dashboardQuerySettings } from "../../../src/dashboard-query-settings";
import { schemaDocuments } from "../../../src/schema-documents";
import { coerceValue, loadSchema, toQueryRequestV3 } from "@pythia-software/query-table-core";
import test from 'node:test';
import assert from 'node:assert/strict';
import {EMPTY_QUERY,decodeQueryStrict,encodeQuery} from '@pythia-software/query-table-core';
import {thinkingViews,thinkingBreakdowns,defaultThinking,initialThinking,validateThinking,thinkingRequest,thinkingBrush,thinkingWhere,orderThinkingGroups} from '../../../src/thinking-model';
import {viewFields,parseViewValue,encodeViewValue,retainPageView} from '../../../src/view-contract';
import {usageWorkspaceURL,readUsageWorkspace,usageDashboardReturn} from '../../../src/usage-workspace-model';
import {thinkingCardRecipes} from '../../../src/thinking-card-recipes';
import {instantiateRecipe} from '../../../src/curiosity-data';
import {cardNavigationURL} from '../../../src/card-exploration';
import {queryFromURL} from '../../../src/query-links';
import {parseRangeInput,rangeInputValue} from '../../../src/chart-range';
import {todayBounds} from '../../../src/day-window';

globalThis.location??=new URL("http://localhost/");
const urlOrigin='http://localhost';
test('every Thinking view and breakdown restores through the shared URL contract',()=>{
 for(const [view]of thinkingViews)for(const [groupBy]of thinkingBreakdowns){
  const state={...defaultThinking,view,groupBy,model:'model with / characters',range:'custom',from:'2026-10-15T06:00:00.000Z',to:'2026-10-29T06:00:00.000Z',timezone:'America/Denver',hiddenPercentiles:['p75','p95'],calendarRows:'week',calendarMetric:'ratio',hours:8,days:4};
  const params=new URLSearchParams({thinking:encodeViewValue(viewFields.thinking,state)});
  for(const overview of [false,true])assert.equal(new URL(thinkingRequest(state,overview),urlOrigin).searchParams.get('groupBy'),overview&&groupBy==='none'?'model':groupBy);
  for(const topic of ['thinking-overview','thinking-detail']){const url=new URL(usageWorkspaceURL({mode:'dashboards',topic,dataset:'model_requests'},params),urlOrigin);assert.deepEqual(parseViewValue(viewFields.thinking,url.searchParams.get('thinking')),state);assert.deepEqual(initialThinking(url.searchParams),{...state,timezone:Intl.DateTimeFormat().resolvedOptions().timeZone});assert.equal(readUsageWorkspace(url.searchParams).topic,topic)}
 }
 const values=new URLSearchParams({thinking:JSON.stringify(defaultThinking)});retainPageView(values,'analyze/tokens');assert.equal(values.has('thinking'),false);
});
test('Thinking rejects invalid applied state instead of broadening a query',()=>{
 for(const change of [{view:'mystery'},{groupBy:'missing'},{hours:0},{days:1.5},{hourA:2},{minimum:-1},{timezone:'wrong/zone'},{hiddenPercentiles:['p99']},{from:'2026-10-29T00:00:00Z',to:'2026-10-15T00:00:00Z'},{unknown:true}]){const value={...defaultThinking,...change};assert.equal(validateThinking(value),false,JSON.stringify(change));assert.throws(()=>parseViewValue(viewFields.thinking,JSON.stringify(value)))}
});
test('chart brushing commits exact source bucket boundaries across all views',()=>{
 const data={periods:['a','b','c'],periodBounds:{a:{From:'2026-11-01T07:00:00Z',To:'2026-11-01T08:00:00Z'},b:{From:'2026-11-01T08:00:00Z',To:'2026-11-01T09:00:00Z'},c:{From:'2026-11-01T09:00:00Z',To:'2026-11-01T10:00:00Z'}},calendarPeriods:['day'],calendarBounds:{day:{From:'2026-11-01T06:00:00Z',To:'2026-11-02T07:00:00Z'}}};
 const selected=thinkingBrush(data,defaultThinking,1.2,2.8);assert.deepEqual(selected,{range:'custom',from:data.periodBounds.b.From,to:data.periodBounds.c.To});
 assert.deepEqual(thinkingBrush(data,defaultThinking,2.8,1.2),selected);
 assert.deepEqual(thinkingBrush(data,defaultThinking,0,.9,true),{range:'custom',from:'2026-11-01T06:00:00Z',to:'2026-11-02T07:00:00Z'});
 for(const [view]of thinkingViews){const request=new URL(thinkingRequest({...defaultThinking,...selected,view}),urlOrigin);assert.equal(request.searchParams.get('from'),selected.from);assert.equal(request.searchParams.get('to'),selected.to)}
 assert.equal(thinkingBrush(data,defaultThinking,1,1),null);
});
test('moving card windows stay relative and custom timestamps stay fixed',()=>{
 for(const recipe of thinkingCardRecipes)for(const days of [7,30,90]){
  const item=instantiateRecipe(recipe,days),url=new URL(cardNavigationURL(item.url),urlOrigin),view=initialThinking(url.searchParams);
  assert.equal(view.range,days+'d',recipe.id);assert.equal(view.view,recipe.thinking.state.view??'time');assert.equal(view.groupBy,recipe.thinking.state.groupBy??'none');
  assert.equal(readUsageWorkspace(url.searchParams).topic,recipe.thinking.overview?'thinking-overview':'thinking-detail');
  assert.ok(queryFromURL(item.url,item.query),recipe.id+' retains editable source query');
  // A cover-only population (compactions) keeps the dashboard's whole population,
  // which its before/after pairs need; the card's own query keeps the filter.
  const filters=JSON.stringify(decodeQueryStrict(url.searchParams.get('analyze_filters')).where);
  for(const term of recipe.where??[])assert.equal(filters.includes(JSON.stringify(term)),!recipe.thinking.coverWhere,recipe.id);
  if(recipe.thinking.coverWhere)assert.ok(JSON.stringify(item.query.where).includes(JSON.stringify(recipe.where[0])),recipe.id);
 }
 const now=Date.parse('2026-11-01T19:00:00Z'),where=thinkingWhere({...defaultThinking,range:'today',timezone:'Asia/Tokyo'},now),bounds=todayBounds(new Date(now),'Asia/Tokyo');assert.equal(where[0].value,bounds[0]);assert.equal(where[1].value,bounds[1]);
});
test('legacy links retain absolute instants while following the viewer zone and showing all samples',()=>{
 const timezone='Asia/Tokyo',day='2026-10-20';
 const from=parseRangeInput(day,timezone).toISOString(),to=parseRangeInput(day,timezone,true).toISOString();
 const state={...defaultThinking,timezone,range:'custom',from,to};
 const restored=initialThinking(new URLSearchParams({thinking:encodeViewValue(viewFields.thinking,state)}));
 const request=new URL(thinkingRequest(restored),urlOrigin);
 assert.equal(request.searchParams.get('from'),'2026-10-19T15:00:00.000Z');
 assert.equal(request.searchParams.get('to'),'2026-10-20T15:00:00.000Z');
 assert.equal(restored.timezone,Intl.DateTimeFormat().resolvedOptions().timeZone);
 assert.equal(rangeInputValue(new Date(restored.from),false,timezone),day);
 assert.equal(rangeInputValue(new Date(Date.parse(restored.to)-1),false,timezone),day);
 const legacy=initialThinking(new URLSearchParams({thinking:JSON.stringify({...state,minimum:1000})}));
 assert.equal(legacy.minimum,0);
 assert.equal(legacy.timezone,Intl.DateTimeFormat().resolvedOptions().timeZone);
 assert.equal(legacy.from,state.from);assert.equal(legacy.to,state.to);
});

test('model and effort grouping keeps canonical identity order rather than numeric ranking',()=>{
 const group=(model,effort)=>({key:JSON.stringify([model,effort]),model,effort,label:model+' · '+effort,stats:{median:effort==="low"?100:1}});
 const values=[group('gpt-test','Unreported'),group('gpt-test','high'),group('gpt-test','low'),group('claude-opus-test','high'),group('claude-opus-test','low')];
 const ordered=orderThinkingGroups(values,{...defaultThinking,groupBy:'model_effort',sortBy:'median',sortDirection:'desc'});
 for(const model of ['gpt-test','claude-opus-test'])assert.deepEqual(ordered.filter(v=>v.model===model).map(v=>v.effort),model==='gpt-test'?['low','high','Unreported']:['low','high']);
 assert.equal(new Set(ordered.map(v=>v.key)).size,values.length);
});

test('returning from request evidence respects edited population and preserves presentation',()=>{
 const state={...defaultThinking,view:'calendar',groupBy:'model_effort',model:'old-model',range:'30d'};
 const params=new URLSearchParams({analyze_mode:'dashboards',analyze_topic:'thinking-overview',analyze_data:'model_requests',thinking:JSON.stringify(state),analyze_filters:encodeQuery(EMPTY_QUERY)});
 const where=[{field:'model',op:'=',value:'new-model'},{field:'requested_at',op:'>=',value:'-7d'},{field:'requested_at',op:'<',value:'+0s'}];
 const source=new URL(usageWorkspaceURL({mode:'query',query:{...EMPTY_QUERY,where:[{field:'model',op:'=',value:'old-model'}]},dataset:'model_requests'},params),urlOrigin);
 const unchanged=new URL(usageDashboardReturn(source.searchParams).href,urlOrigin);assert.deepEqual(initialThinking(unchanged.searchParams),initialThinking(params));
 source.searchParams.set('q_model_requests',encodeQuery({...EMPTY_QUERY,where}));
 assert.equal(JSON.parse(source.searchParams.get('thinking')).model,'old-model');
 const returned=new URL(usageDashboardReturn(source.searchParams).href,urlOrigin),restored=initialThinking(returned.searchParams);
 assert.equal(restored.view,'calendar');assert.equal(restored.groupBy,'model_effort');assert.equal(restored.model,'all');assert.equal(restored.range,'7d');
 assert.deepEqual(queryFromURL(source.href).query.where,where);
});


test("every Waiting and Thinking view has four scoped, executable query cards", () => {
 const now=Date.parse('2026-11-03T19:00:00Z'),schema=loadSchema(schemaDocuments.model_requests),where=[{field:'provider',op:'=',value:'codex'}];
 for(const [view]of thinkingViews)for(const [groupBy]of thinkingBreakdowns)for(const overview of [true,false]){
  const state={...defaultThinking,view,groupBy,model:'gpt-test',effort:'high',range:'7d',timezone:'America/Denver'};
  const context=thinkingQueryContext(state,where,overview,now),suggestions=dashboardExplorations(context,schema);
  assert.equal(suggestions.length,4);
  for(const item of suggestions){
   assert.equal(item.blocked,undefined,`${view}/${groupBy}/${overview}: ${item.purpose}`);
   assert.doesNotThrow(()=>toQueryRequestV3(item.query,schema,{...capabilities,regexSourceFilters:true}),`${view}/${groupBy}/${overview}: ${item.purpose} ${JSON.stringify(item.query.grouping?.keys)}`);
   const population=(item.query.grouping?.source??item.query).where;
   for(const term of [...where,...thinkingWhere(state,now)]) {
    if(term.field!=='requested_at')assert.ok(population.some(candidate=>JSON.stringify(candidate)===JSON.stringify(term)));
    else {const actual=population.find(candidate=>candidate.field===term.field&&candidate.op===term.op);assert.ok(actual);const a=Number(coerceValue('datetime',actual.value,now)),b=Number(coerceValue('datetime',term.value,now));assert.ok(term.op==='>='?a>=b:a<=b,'whole-period bounds stay inside the selected range');}
   }
  }
 }
});

test("ribbon query cards retain selected identity, zoom and counting assumptions",()=>{
 const selection={settings:{counting:'workspace',gap:30,repository:'pharos'},bounds:[Date.parse('2026-11-01T01:00Z'),Date.parse('2026-11-02T01:00Z')],selected:{workspace_id:'workspace-one',conversation_id:'conversation-one'}};
 const context=ribbonQueryContext(selection),schema=loadSchema(schemaDocuments.parallelism);
 const links=dashboardQueryLinks('parallelism',EMPTY_QUERY,schema,new URLSearchParams({analyze_topic:'parallelism'}),context);
 assert.equal(links.length,4);
 for(const item of links){
  assert.ok((item.query.grouping?.source??item.query).where.some(term=>term.field==='unit_id'&&term.value==='workspace-one'));
  const settings=dashboardQuerySettings('parallelism',new URL(item.href,urlOrigin).searchParams);
  assert.deepEqual(JSON.parse(settings.parallelism_settings),{counting:'workspace',gap:30});
 }
});

// query-v3 reads (thinking-query.ts), evaluated here by Query Table's own
// engine over rows shaped like model_requests'.
import {evaluateMetrics} from '@pythia-software/query-table-core';
import modelRequestsDocument from '../../../../schemas/model_requests.schema.json';
import groupKeys from '../../../../testdata/thinking-group-keys.json';
import {thinkingBandLabels,thinkingClauses,thinkingFromMetrics,thinkingGroupIdentity,thinkingBreakdownFields,thinkingPeriodKey,thinkingStamp,withThinkingFit,sortThinkingGroups,supportedThinkingPeriod,thinkingQueryState} from '../../../src/thinking-query';
const requestsSchema=loadSchema(modelRequestsDocument);
const evaluate=(rows,state,overview=false,now=Date.parse('2026-11-10T00:00:00Z'))=>{
 const q=thinkingQueryState(state,overview),where=thinkingWhere(q,now);
 const metrics=evaluateMetrics(rows,{...EMPTY_QUERY,where,aggregations:thinkingClauses(q,overview)},requestsSchema,{now}).metrics;
 for(const m of metrics)assert.equal(m.error,undefined,m.id+': '+m.error);
 const options=evaluateMetrics(rows,{...EMPTY_QUERY,aggregations:[{id:'models',op:'count',groupBy:['model']},{id:'efforts',op:'count',groupBy:['effort']}]},requestsSchema,{now}).metrics;
 return thinkingFromMetrics(q,overview,metrics,options,now);
};
// A request as model_requests has it; gap_bin and the calendar fields are
// given, as the service derives them, in UTC.
const request=(id,at,gap,{model='m-a',effort='high',conversation='c1',reasoning=0,bin=null,...rest}={})=>({id,conversation_id:conversation,model,effort,speed:'standard',
 provider:'anthropic',harness:'claude-code',project_name:null,machine:'mac',preceding_tool:'shell',agent_depth:0,requested_at:at,gap_seconds:gap,gap_bin:bin===null?null:thinkingBandLabels[bin],
 reasoning_output_tokens:reasoning,day:at.slice(0,10),month:at.slice(0,7),hour_of_day:Number(at.slice(11,13)),...rest});

test('Thinking group keys match the fitted estimates the service keys',()=>{
 assert.deepEqual(thinkingBandLabels,groupKeys.bands);
 for(const c of groupKeys.cases){
  const keys=thinkingBreakdownFields[c.groupBy].map(field=>c.row[field]??null);
  const id=thinkingGroupIdentity(c.groupBy,keys);
  assert.deepEqual([id.key,id.label],[c.key,c.label],c.groupBy+' '+JSON.stringify(c.row));
 }
});

test('a view joined to the fit reads query-v3 live, at the fit\'s instant',async t=>{
 const {serverQuery,queryCapabilities,execution}=await import('../../support/query-server.mjs');
 const {resultScope}=await import('../../../src/result-cache');
 const {loadThinkingData}=await import('../../../src/thinking-query');
 const {schemas}=await import('../helpers/recipes.mjs');
 const seen=[],now=Date.parse('2026-11-05T12:00:00Z'),prior=global.fetch,priorLocation=global.location;
 global.location={origin:'http://thinking.test',href:'http://thinking.test/'};
 global.fetch=async(url,init={})=>{
  const path=String(url),body=init.body?JSON.parse(init.body):undefined,policy=new Headers(init.headers).get('X-Pharos-Cache');
  if(path.includes('computed-columns'))return Response.json([]);
  if(path.endsWith('capabilities'))return Response.json({computedExecution:execution,queryCapabilities});
  if(path.includes('query-v3')){seen.push({kind:body.metrics.length>2?'view':'options',policy,now:body.now});return Response.json(serverQuery(body,[],schemas.model_requests));}
  seen.push({kind:'fit',now:Number(new URL(path,'http://x').searchParams.get('now'))});
  return Response.json({Adjusted:0,timezone:'UTC',groups:[]});
 };
 t.after(()=>{global.fetch=prior;global.location=priorLocation;});
 const load=view=>{seen.length=0;return loadThinkingData({state:{...defaultThinking,view,range:'7d',timezone:'UTC'},overview:false,where:[],filters:'',now},resultScope(undefined,'cached').signal);};
 await load('calendar');
 assert.deepEqual(seen.filter(r=>r.kind!=='options').sort((a,b)=>a.kind<b.kind?-1:1),[{kind:'fit',now},{kind:'view',policy:'live',now}]);
 assert.equal(seen.find(r=>r.kind==='options').policy,'cached','the Model and Effort choices are not joined to the fit');
 await load('histogram');
 assert.deepEqual(seen.filter(r=>r.kind==='view').map(r=>r.policy),['cached'],'a view without the fit keeps the cache');
 assert.ok(!seen.some(r=>r.kind==='fit'));
});

test('a project named like the missing one is a group of its own',()=>{
 const rows=[request('none','2026-11-02T10:00:00Z',2,{bin:4}),request('named','2026-11-02T11:00:00Z',4,{bin:6,project_name:'No project'})];
 const data=evaluate(rows,{...defaultThinking,timezone:'UTC',view:'histogram',groupBy:'project',range:'all'});
 assert.deepEqual(data.groups.map(g=>[g.label,g.Retained,g.stats.n,g.counts.reduce((a,b)=>a+b,0)]).sort(),[['No project',1,1,1],['“No project”',1,1,1]]);
});

test('query-v3 Thinking keeps missing waits in the denominator and summarises observed ones exactly',()=>{
 const rows=[
  ...[1,2,3,4,5].map((gap,i)=>request('a'+i,`2026-11-0${i+1}T10:00:00Z`,gap+0.5,{conversation:'c'+(i%2),bin:gap<2?4:gap<3?5:gap<5?6:7,reasoning:i*10})),
  request('a-null',`2026-11-02T11:00:00Z`,null,{conversation:'c9',reasoning:5}),
  request('b0','2026-11-03T12:00:00Z',.15,{model:'m-b',effort:'low',bin:0}),
 ];
 const state={...defaultThinking,timezone:'UTC',view:'histogram',groupBy:'model'};
 const data=evaluate(rows,state);
 assert.equal(data.Retained,7);assert.equal(data.Timed,6);assert.equal(data.Conversations,3);
 assert.deepEqual(data.Models,['m-a','m-b']);assert.deepEqual(data.Efforts,['low','high']);
 const a=data.groups.find(g=>g.key==='m-a');
 assert.equal(a.Retained,6);assert.equal(a.Timed,5);assert.equal(a.RetainedConversations,3);assert.equal(a.Conversations,2);
 // Exact-linear percentiles of 1.5…5.5, as Go's thinkingQuantile.
 assert.equal(a.stats.n,5);assert.equal(a.stats.median,3.5);assert.ok(Math.abs(a.stats.p95-5.3)<1e-12);assert.ok(Math.abs(a.stats.low-1.9)<1e-12);
 assert.deepEqual(a.counts.slice(4,8),[1,1,2,1]);assert.equal(a.counts.reduce((x,y)=>x+y),5);
 // Sorted by median, slowest first.
 assert.deepEqual(data.groups.map(g=>g.key),['m-a','m-b']);
 // Reported reasoning counts positive reports, timed or not.
 const tokens=evaluate(rows,{...state,view:'reasoning'});
 const r=tokens.groups.find(g=>g.key==='m-a');
 assert.equal(r.stats.n,5);assert.equal(r.stats.median,20);assert.equal(r.Conversations,3);
 assert.equal(tokens.groups.find(g=>g.key==='m-b').stats.n,0);
 // The Waiting overview adds curves and coverage, untimed models included.
 const overview=evaluate(rows,{...state,groupBy:'none'},true);
 assert.equal(overview.groups[0].cdf.length,101);assert.deepEqual(overview.groups[0].cdf[50],[3.5,.5]);
 assert.deepEqual(overview.coverage.map(g=>[g.key,g.Retained,g.stats.n]),[['m-a',6,5],['m-b',1,1]]);
 assert.deepEqual(overview.agentCoverage.map(g=>g.key),['claude-code']);
});

test('query-v3 Thinking periods hold every observation across clock changes and name buckets as before',()=>{
 for(const [zone,date] of [['America/Denver','2026-03-08'],['America/Denver','2026-11-01'],['Europe/Berlin','2026-10-25'],['Australia/Lord_Howe','2026-10-04']])
  for(const hours of [1,6,24]){
   const start=parseRangeInput(date,zone).getTime()-86_400_000,end=start+3*86_400_000,rows=[];
   for(let t=start;t<end;t+=15*60_000)rows.push(request('r'+t,new Date(t).toISOString(),2,{bin:5,conversation:'c'+(t%7)}));
   const state={...defaultThinking,timezone:zone,view:'time',period:hours===24?'day':'hours',hours,range:'custom',from:new Date(start).toISOString(),to:new Date(end).toISOString()};
   const data=evaluate(rows,state),g=data.groups[0];
   assert.equal(data.periods.reduce((n,key)=>n+(g.periods[key]?.stats.n??0),0),rows.length,`${zone} ${date} ${hours}h`);
   for(const key of Object.keys(g.periods))assert.ok(data.periods.includes(key),key);
   for(const row of rows){
    const key=data.periods.find(k=>Date.parse(data.periodBounds[k].From)<=Date.parse(row.requested_at)&&Date.parse(row.requested_at)<Date.parse(data.periodBounds[k].To));
    assert.ok(key&&g.periods[key],`${row.requested_at} outside its bucket`);
   }
  }
 // A request without a timestamp counts in its group but in no period.
 const undated={...request('undated','2026-11-02T10:00:00Z',3,{bin:6}),requested_at:null,day:null,month:null,hour_of_day:null};
 const all=evaluate([request('dated','2026-11-02T10:00:00Z',2,{bin:5}),undated],{...defaultThinking,timezone:'UTC',view:'time',period:'day',range:'all'});
 assert.equal(all.groups[0].Retained,2);
 assert.deepEqual(Object.keys(all.groups[0].periods),['2026-11-02']);assert.equal(all.groups[0].periods['2026-11-02'].stats.n,1);
 // Both occurrences of the repeated 1 a.m. keep their own offset.
 assert.equal(thinkingPeriodKey(Date.parse('2026-11-01T07:00:00Z'),'hour','America/Denver'),'2026-11-01T01:00:00-06:00');
 assert.equal(thinkingPeriodKey(Date.parse('2026-11-01T08:00:00Z'),'hour','America/Denver'),'2026-11-01T01:00:00-07:00');
 assert.equal(thinkingPeriodKey(Date.parse('2026-11-01T12:00:00Z'),'hours','UTC'),'2026-11-01T12:00:00Z');
 assert.equal(thinkingPeriodKey(Date.parse('2026-11-01T12:00:00Z'),'month','UTC'),'2026-11');
 assert.equal(thinkingStamp(Date.parse('2026-11-01T12:00:00.500Z')),'2026-11-01T12:00:00.5Z');assert.equal(thinkingStamp(Date.parse('2026-11-01T12:00:00Z')),'2026-11-01T12:00:00Z');
});

test('Thinking reads older hour and day buckets as the nearest query-v3 supports',()=>{
 assert.deepEqual(supportedThinkingPeriod({hours:5,days:5000}),{hours:4,days:1000});
 assert.deepEqual(supportedThinkingPeriod({hours:10000,days:3}),{hours:24,days:3});
 const kept={hours:6,days:3};assert.equal(supportedThinkingPeriod(kept),kept);
 const legacy=initialThinking(new URLSearchParams({thinking:JSON.stringify({period:'hours',hours:7,days:2000})}));
 assert.equal(legacy.hours,6);assert.equal(legacy.days,1000);
});

test('time-of-day cells come whole from the fit, which reads the zoned day and hour',()=>{
 const rows=[];
 for(let i=0;i<24;i++)rows.push(request('x'+i,`2026-11-02T09:${String(i).padStart(2,'0')}:00Z`,2,{bin:5,conversation:'c'+(i%3)}));
 const state={...defaultThinking,timezone:'UTC',view:'calendar',groupBy:'model',calendarMetric:'ratio'};
 assert.ok(thinkingClauses(state,false).every(c=>!c.groupBy.includes('hour_of_day')&&!c.groupBy.includes(state.calendarRows)));
 const data=evaluate(rows,state),g=data.groups[0];
 assert.deepEqual(data.calendarPeriods,['2026-11-02']);assert.deepEqual(g.calendar,[]);
 const cells=[{day:'2026-11-02',hour:9,Raw:2,Ratio:.8,N:24,Adjusted:24,Conversations:3,AdjustedConversations:3},{day:'2026-11-02',hour:18,Raw:null,Ratio:null,N:5,Adjusted:0,Conversations:3,AdjustedConversations:0}];
 const fit={Adjusted:24,timezone:'UTC',groups:[{key:'m-a',Adjusted:24,stats:{...g.stats,median:.8},Conversations:3,bands:[{hour:9,days:1,n:24,Ratio:.8,Low:null,High:null}],comparison:{days:0,Ratio:null,Low:null,High:null},
  calendar:cells},{key:'absent',Adjusted:1,bands:[],comparison:{days:0},calendar:[]}],factors:[]};
 const merged=withThinkingFit(data,fit,state),m=merged.groups[0];
 assert.equal(merged.Adjusted,24);assert.equal(m.stats.median,.8);assert.deepEqual(m.calendar,cells);
 assert.equal(merged.groups.length,1);assert.deepEqual(merged.factors,[]);
});

test('Thinking groups sort by the chosen statistic with missing values last',()=>{
 const g=(label,median,n=1)=>({key:label,label,model:'',effort:'',stats:{n,median,p95:median}});
 const groups=[g('b',2),g('a',null,0),g('c',5),g('d',2)];
 assert.deepEqual(sortThinkingGroups(groups,{groupBy:'model',sortBy:'median',sortDirection:'desc'}).map(x=>x.label),['c','b','d','a']);
 assert.deepEqual(sortThinkingGroups(groups,{groupBy:'model',sortBy:'median',sortDirection:'asc'}).map(x=>x.label),['b','d','c','a']);
});
