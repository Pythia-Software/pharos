import React,{useMemo} from 'react';
import {useViewState} from './view-state';
import type {ParallelStyle} from './label-compact-fingerprint';
import {LabelConversationCard} from './label-conversation-card';
import type {SourceMatch} from './label-discovery-model';
import type {LabelConversation,Taxonomy} from './label-trajectories-model';
import type {SelectSource} from './label-trajectories-ui';
export function ConversationBrowser({matches,select,heading='Matching conversations',layout='cards',taxonomy}:{taxonomy?:Taxonomy;matches:SourceMatch[];select:SelectSource;heading?:string;layout?:'cards'|'sidebar'}){
 const [parallel]=useViewState('label_parallel','steps');
 const [page,setPage]=useViewState('label_match_page','0'),[,setView]=useViewState('label_lab','fingerprints');
 const groups=useMemo(()=>{const map=new Map<string,{conversation:LabelConversation;hits:SourceMatch[]}>();for(const match of matches){const id=match.stream.conversation.id,row=map.get(id)??{conversation:match.stream.conversation,hits:[]};if(!row.hits.some(hit=>hit.stream.id===match.stream.id&&hit.message.id===match.message.id))row.hits.push(match);map.set(id,row);}return [...map.values()];},[matches]);
 const at=Math.max(0,Math.min(Math.floor(Number(page)||0),Math.max(0,Math.ceil(groups.length/10)-1))),rows=groups.slice(at*10,at*10+10);
 return <section className={`lt-conversation-browser is-${layout}`} aria-label="Matching conversations"><div className="lt-browser-heading"><h3>{heading}</h3><span>{groups.length} conversations · {matches.length} matches</span></div><div className="lt-conversation-cards">{rows.map(({conversation:c,hits})=><LabelConversationCard key={c.id} conversation={c} hits={hits} taxonomy={taxonomy} parallel={parallel as ParallelStyle} onInspect={()=>{select(c,hits[0].message);setView('labels');}}/>)}</div>{!rows.length?<p className="lt-note">No retained conversations match these filters.</p>:null}<div className="lt-browser-pagination"><button type="button" data-view-field="label_match_page" disabled={!at} onClick={()=>setPage(String(at-1))}>Previous</button><span>{groups.length?at*10+1:0}–{Math.min(groups.length,(at+1)*10)}</span><button type="button" data-view-field="label_match_page" disabled={(at+1)*10>=groups.length} onClick={()=>setPage(String(at+1))}>Next</button></div></section>;
}
