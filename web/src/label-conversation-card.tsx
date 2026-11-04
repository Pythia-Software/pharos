import React from 'react';
import {CompactFingerprint,type ParallelStyle} from './label-compact-fingerprint';
import {capturedSpan,turnPosition,type SourceMatch} from './label-discovery-model';
import {messageURL,workflowSessions,type LabelConversation,type Taxonomy} from './label-trajectories-model';
import {elapsed} from './label-workflow-time';
/** Shared conversation presentation for discovery and future conversation cards. */
export function LabelConversationCard({conversation:c,hits,onInspect,parallel,taxonomy}:{taxonomy?:Taxonomy;parallel?:ParallelStyle;conversation:LabelConversation;hits:SourceMatch[];onInspect:()=>void}){
 const first=hits[0],pos=turnPosition(first.stream,first.message),span=capturedSpan(c),branches=workflowSessions(c).filter(s=>c.messages.some(m=>m.session_id===s.id)).length;
 return <article className="lt-conversation-result lt-conversation-card"><div className="lt-conversation-card-visual"><CompactFingerprint conversation={c} parallel={parallel} taxonomy={taxonomy} stretch/><div className="lt-conversation-card-stats"><span>{c.messages.length} messages · {branches||1} {branches<=1?'branch':'branches'}</span><span>{hits.length} matches</span></div></div><div className="lt-conversation-card-body"><div className="lt-result-title"><a href={messageURL(c,first.message)}>{c.title}</a><small>{c.repository} · {span===null?'Duration unavailable':elapsed(span)}</small><span>Turn {pos.at} / {pos.total} · {first.stream.session.name}</span></div><div className="lt-result-actions"><button type="button" data-view-field="label_message" onClick={onInspect}>Label detail</button><a href={messageURL(c,first.message)}>Open conversation ↗</a></div></div></article>;
}
