import {EMPTY_QUERY,type QueryState} from '@pythia-software/query-table-core';
import type {HostQueryOptions} from '@pythia-software/query-table-react';
import {recordKeywordHostQuery} from './host-query-state';
import type {FieldDef} from '@pythia-software/query-table-core';
export const messageLabelResultParameters=['label_results'];
// Retain only a few result pages. A refresh clears even off-screen ribbons;
// an older failed request must not evict its newer replacement.
export function messageLabelFingerprintCache<Value>(load:(id:string)=>Promise<Value>,limit=80){
 const entries=new Map<string,Promise<Value>>();
 return {
  read(id:string):Promise<Value>{
   const found=entries.get(id);
   if(found){entries.delete(id);entries.set(id,found);return found;}
   const pending=Promise.resolve().then(()=>load(id));entries.set(id,pending);
   pending.catch(()=>{if(entries.get(id)===pending)entries.delete(id);});
   while(entries.size>limit)entries.delete(entries.keys().next().value!);
   return pending;
  },
  clear(){entries.clear();},
 };
}
// What a ribbon says about itself beside its marks: a bounded sample, labels
// still being produced, and events left out until they are rebuilt for
// changed evidence (events_pending, apart from label freshness: the
// conversation reader still shows those events, marked pending).
export function messageLabelRibbonNotes(value:{truncated?:boolean;pending?:boolean;events_pending?:number}|undefined):{badge:string;note:string}[] {
 if(!value)return [];
 const events=Number(value.events_pending)||0;
 return [
  value.truncated?{badge:'Sample',note:'Fingerprint uses a bounded retained-message sample'}:null,
  value.pending?{badge:'Labeling',note:'Some labels are still being produced'}:null,
  events?{badge:'Events pending',note:`Events for ${events.toLocaleString()} conversation${events===1?' are':'s are'} left out until they are rebuilt for changed session evidence`}:null,
 ].filter((n):n is {badge:string;note:string}=>Boolean(n));
}
export function messageLabelConversationQuery(source:QueryState):QueryState {
 return {...EMPTY_QUERY,limit:40,fit:"screen" as const,hostState:{version:1,params:{...source.hostState?.params,label_results:['cards']}},grouping:{source:{...(source.grouping?.source??source),offset:0,aggregations:[]},keys:['fingerprint'],summaries:[
 {id:'conversation_title',op:'row',field:'title',label:'Conversation'},{id:'conversation_workspace',op:'row',field:'workspace_id',label:'Workspace'},
 {id:'conversation_repository',op:'row',field:'repository_name',label:'Repository'},
 {id:'matching_messages',op:'count_distinct',field:'message_id',label:'Matching messages'},
 {id:'last_match',op:'max',field:'occurred_at',label:'Last match'},
 {id:'source_message',op:'row',field:'message_id',label:'Source message'},
 {id:'open_conversation',op:'link',label:'Conversation',link:{label:{column:'conversation_title'},to:{kind:'template',template:'/work/{field:conversation_workspace}/conversation/{field:fingerprint}?message={field:source_message}'}}},
 ],representative:{orderBy:[{field:'occurred_at',dir:'desc',nulls:'last'}]}},select:[{field:'fingerprint',width:160},{field:'open_conversation',width:230},{field:'conversation_repository',width:130},{field:'matching_messages',width:90},{field:'last_match',width:140}],orderBy:[{field:'last_match',dir:'desc',nulls:'last'}]};
}
export const labelResultsMode=(query:QueryState)=>query.hostState?.params.label_results?.[0]==='cards'?'cards':'table';
export function messageLabelHostQuery(fields:FieldDef[]):HostQueryOptions {
 const keyword=recordKeywordHostQuery('message_labels',fields);
 return {...keyword,ownedParameters:[...keyword.ownedParameters,...messageLabelResultParameters],defaults:{version:1,params:{label_results:['table']}},normalize(state){
  const mode=state.params.label_results?.[0]??'table';if(!['cards','table'].includes(mode)||state.params.label_results?.length>1)throw new Error('Invalid label result display');
  const params={...state.params};delete params.label_results;
  const value=keyword.normalize!({version:state.version,params});return {...value,params:{...value.params,label_results:[mode]}};
 }};
}
