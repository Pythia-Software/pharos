import {EMPTY_QUERY,type QueryState} from '@pythia-software/query-table-core';
import type {HostQueryOptions} from '@pythia-software/query-table-react';
import {recordKeywordHostQuery} from './host-query-state';
import type {FieldDef} from '@pythia-software/query-table-core';
export const messageLabelResultParameters=['label_results'];
export function messageLabelConversationQuery(source:QueryState):QueryState {
 return {...EMPTY_QUERY,limit:40,fitToScreen:true,grouping:{source:{...(source.grouping?.source??source),offset:0,aggregations:[]},keys:['fingerprint'],summaries:[
 {id:'conversation_title',op:'row',field:'title',label:'Conversation'},{id:'conversation_workspace',op:'row',field:'workspace_id',label:'Workspace'},
 {id:'conversation_repository',op:'row',field:'repository_name',label:'Repository'},
 {id:'matching_messages',op:'count_distinct',field:'message_id',label:'Matching messages'},
 {id:'last_match',op:'max',field:'occurred_at',label:'Last match'},
 {id:'source_message',op:'row',field:'message_id',label:'Source message'},
 {id:'open_conversation',op:'link',label:'Conversation',link:{label:{column:'conversation_title'},to:{kind:'template',template:'/work/{field:conversation_workspace}/conversation/{field:fingerprint}?message={field:source_message}'}}},
 ],representative:{orderBy:[{field:'occurred_at',dir:'desc',nulls:'last'}]}},select:[{field:'fingerprint',width:160},{field:'open_conversation',width:230},{field:'conversation_repository',width:130},{field:'matching_messages',width:90},{field:'last_match',width:140}],orderBy:[{field:'last_match',dir:'desc',nulls:'last'}]};
}
export const labelResultsMode=(query:QueryState)=>query.hostState?.params.label_results?.[0]==='table'?'table':'cards';
export function messageLabelHostQuery(fields:FieldDef[]):HostQueryOptions {
 const keyword=recordKeywordHostQuery('message_labels',fields);
 return {...keyword,ownedParameters:[...keyword.ownedParameters,...messageLabelResultParameters],defaults:{version:1,params:{label_results:['cards']}},normalize(state){
  const mode=state.params.label_results?.[0]??'cards';if(!['cards','table'].includes(mode)||state.params.label_results?.length>1)throw new Error('Invalid label result display');
  const params={...state.params};delete params.label_results;
  const value=keyword.normalize!({version:state.version,params});return {...value,params:{...value.params,label_results:[mode]}};
 }};
}
