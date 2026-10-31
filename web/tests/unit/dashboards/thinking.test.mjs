import test from 'node:test';
import assert from 'node:assert/strict';
import {EMPTY_QUERY,encodeQuery} from '@pythia-software/query-table-core';
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
  const state={...defaultThinking,view,groupBy,model:'model with / characters',range:'custom',from:'2026-10-15T06:00:00.000Z',to:'2026-10-29T06:00:00.000Z',timezone:'America/Denver',hiddenPercentiles:['p75','p95'],calendarRows:'week',calendarMetric:'ratio',hours:7,days:4};
  const params=new URLSearchParams({thinking:encodeViewValue(viewFields.thinking,state)});
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
