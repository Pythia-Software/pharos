import {useEffect,useState} from 'react';
import {type LabelMessage,type LabelSnapshot,type Taxonomy} from './label-trajectories-model';
export type TimelineLabelSource={messages:Map<string,LabelMessage[]>;taxonomy:Taxonomy};
export type NativeTimelineMessage={preview?:string;preview_truncated?:boolean;authorship?:LabelMessage['authorship'];id:string;conversation_id?:string;role:string;kind:string;created_at:string|null;text:string;model?:string;call_id?:string;labels?:{ids:string[];probs:Record<string,number>;segment:string}};
// The work page reads its own bounded session lanes, not the lab's population.
export function nativeTimelineLabels(activity:{lanes:{session:string;messages?:NativeTimelineMessage[];labels_pending?:boolean}[];taxonomy?:Taxonomy}|null):TimelineLabelSource {
 return {taxonomy:activity?.taxonomy??[],messages:new Map((activity?.lanes??[]).map(lane=>[lane.session,(lane.messages??[]).map((m):LabelMessage=>({id:m.id,preview:m.preview,preview_truncated:m.preview_truncated,authorship:m.authorship,conversation_id:m.conversation_id,role:m.role,kind:m.kind,at:m.created_at,text:m.text,text_source:'Retained message excerpt',truncated:true,session_id:lane.session,generation_model:m.model,call_id:m.call_id,segment:m.labels?.segment??'',chunks:m.labels&&!lane.labels_pending?1:0,labels:lane.labels_pending?[]:(m.labels?.ids??[]).map(id=>[id,m.labels?.probs[id]??0])}))]))};
}
let pendingSnapshot:Promise<LabelSnapshot>|undefined;
export function useTimelineLabelSource(enabled:boolean,override?:TimelineLabelSource){
 const [source,setSource]=useState<TimelineLabelSource>();
 useEffect(()=>{if(!enabled||override)return;let active=true;pendingSnapshot??=fetch('/api/usage/label-trajectories').then(r=>{if(!r.ok)throw new Error('Snapshot unavailable');return r.json();});pendingSnapshot.then(snapshot=>{if(!active)return;const messages=new Map<string,LabelMessage[]>();for(const c of snapshot.conversations)for(const m of c.messages){const sid=m.session_id??c.main_session??c.id,list=messages.get(sid)??[];if(!list.some(other=>other.id===m.id))list.push(m);messages.set(sid,list);}setSource({messages,taxonomy:snapshot.taxonomy});},()=>{});return()=>{active=false;};},[enabled,override]);
 return override??source;
}
