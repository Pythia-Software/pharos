import React from 'react';
import type {QueryTableApi} from '@pythia-software/query-table-react';
import {FingerprintImage as MessageLabelFingerprint} from './fingerprint-image';
import {shortLabel,stateColor} from './conversation-labels';
import {conversationRowURL} from './usage-row-model';
import {labelResultsMode} from './message-label-results-model';
import './message-label-results.css';
type Row=Record<string,any>;
export {FingerprintImage as MessageLabelFingerprint} from './fingerprint-image';
export const messageLabelRenderers={label_fingerprint:(ctx:any)=><MessageLabelFingerprint id={String(ctx.value??'')}/>,workspace_fingerprint:(ctx:any)=><MessageLabelFingerprint id={String(ctx.value??'')} workspace/>,message_label:(ctx:any)=><span className="message-label-chip"><i style={{background:stateColor(String(ctx.value))}}/>{ctx.value==='?'?'Uncategorized':shortLabel(String(ctx.value))}</span>};
export function MessageLabelDisplay({api}:{api:QueryTableApi<Row>}){
 const value=labelResultsMode(api.query),set=(mode:string)=>api.setQuery(previous=>({...previous,hostState:{version:1,params:{...previous.hostState?.params,label_results:[mode]}}}));
 return <div className="qt-qb-row search-results-display"><div className="qt-qb-section-header"><span className="qt-qb-kw">display as</span></div><div role="group" aria-label="Display results as" className="search-results-display-options">{['cards','table'].map(mode=><button type="button" data-view-field="label_results" key={mode} className={'qt-btn'+(value===mode?' qt-btn--active':'')} aria-pressed={value===mode} onClick={()=>set(mode)}>{mode==='cards'?'Cards':'Table'}</button>)}</div></div>;
}
export function MessageLabelCards({api}:{api:QueryTableApi<Row>}){
 if(api.loading&&!api.rows.length)return <p role="status">Loading matching conversations…</p>;
 if(!api.rows.length)return <p>No conversations match this query.</p>;
 return <><div className="message-label-result-cards" aria-busy={api.loading}>{api.rows.map((row,index)=>{const record={...row,title:row.conversation_title??row.title,repository_name:row.conversation_repository??row.repository_name,workspace_id:row.conversation_workspace??row.workspace_id,occurred_at:row.last_match??row.occurred_at,message_id:row.source_message??row.message_id,conversation_id:row.conversation_id??row.fingerprint};const url=conversationRowURL(record,record.message_id,record.occurred_at);return <article key={String(api.rowId(row)??index)} className="message-label-result-card"><MessageLabelFingerprint id={String(row.conversation_id??row.fingerprint??'')} profile="card"/><div><h3>{url?<a href={url}>{record.title||'Conversation'}</a>:record.title||'Conversation'}</h3><p>{record.repository_name||'No repository'} · {row.matching_messages??1} matching {row.matching_messages===1?'message':'messages'}</p>{record.occurred_at?<small>{new Date(record.occurred_at).toLocaleString()}</small>:null}{url?<a className="message-label-source" href={url}>Open conversation →</a>:null}</div></article>;})}</div><div className="message-label-result-paging"><button type="button" disabled={!api.query.offset||api.loading} onClick={()=>api.setQuery(q=>({...q,offset:Math.max(0,q.offset-q.limit)}))}>Previous</button><span>{api.query.offset+1}–{api.query.offset+api.rows.length}{api.total!==null?' of '+api.total:''}</span><button type="button" disabled={api.loading||api.rows.length<api.query.limit||api.total!==null&&api.query.offset+api.rows.length>=api.total} onClick={()=>api.setQuery(q=>({...q,offset:q.offset+q.limit}))}>Next</button></div></>;
}
