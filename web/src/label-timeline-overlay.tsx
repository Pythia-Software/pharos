import {type LabelMessage,type Taxonomy} from './conversation-labels';
import type {AgentEventLink} from './event-link-layout';
export type TimelineLabelSource={messages:Map<string,LabelMessage[]>;taxonomy:Taxonomy;events:Map<string,AgentEventLink[]>};
export type NativeTimelineMessage={preview?:string;preview_truncated?:boolean;authorship?:LabelMessage['authorship'];id:string;conversation_id?:string;role:string;kind:string;created_at:string|null;text:string;model?:string;call_id?:string;labels?:{ids:string[];probs:Record<string,number>;segment:string}};
// Bounded message labels from the workspace's recorded session lanes.
export function nativeTimelineLabels(activity:{lanes:{session:string;messages?:NativeTimelineMessage[];labels_pending?:boolean;agent_events?:AgentEventLink[]}[];taxonomy?:Taxonomy}|null):TimelineLabelSource {
 return {taxonomy:activity?.taxonomy??[],events:new Map((activity?.lanes??[]).map(lane=>[lane.session,lane.agent_events??[]])),messages:new Map((activity?.lanes??[]).map(lane=>[lane.session,(lane.messages??[]).map((m):LabelMessage=>({id:m.id,preview:m.preview,preview_truncated:m.preview_truncated,authorship:m.authorship,conversation_id:m.conversation_id,role:m.role,kind:m.kind,at:m.created_at,text:m.text,text_source:'Retained message excerpt',truncated:true,session_id:lane.session,generation_model:m.model,call_id:m.call_id,segment:m.labels?.segment??'',chunks:m.labels&&!lane.labels_pending?1:0,labels:lane.labels_pending?[]:(m.labels?.ids??[]).map(id=>[id,m.labels?.probs[id]??0])}))]))};
}
