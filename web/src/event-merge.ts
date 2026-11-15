// One mark for events on near-identical text, on the timeline's session rows
// and Agent events row and on the fingerprint, as in the conversation
// reader. The grouping rule is the reader's own (assets/event-spans.js):
// events of one message whose sentences contain one another or share 80% of
// what they cover. The breakdown rows (group, kind) keep every event, since
// they exist to decompose.
//
// A merged mark shows its rarest kind: the kind with the fewest events in
// the view's population (counts: on the timeline every session of the
// workspace, whatever the zoom; on a fingerprint its conversation). Ties go
// to the earlier group in the reader's order, then the kind's key, then the
// likelier event.
import {eventSpanGroups} from '../../internal/archive/assets/event-spans.js';
import {EVENT_GROUPS,linkedTarget,type AgentEventLink,type EventTarget} from './event-link-layout';

export type KindCounts=Map<string,number>;
export function eventKindCounts(events:Iterable<AgentEventLink>):KindCounts{
 const counts:KindCounts=new Map();for(const e of events)for(const m of e.members??[e])counts.set(m.kind,(counts.get(m.kind)??0)+1);return counts;
}
const groupRank=(e:AgentEventLink)=>{const i=EVENT_GROUPS.findIndex(g=>g.name===e.group);return i<0?EVENT_GROUPS.length:i;};
/** Members rarest first (see above); the first names the mark. */
export function byRarity(members:AgentEventLink[],counts:KindCounts){
 return [...members].sort((a,b)=>(counts.get(a.kind)??0)-(counts.get(b.kind)??0)||groupRank(a)-groupRank(b)||a.kind.localeCompare(b.kind)||b.probability-a.probability||a.id.localeCompare(b.id));
}
const targetKey=(t:EventTarget)=>t.message_id??'';
/**
 * The marks to draw: an event alone, or one event per group with members
 * (every event in it). A merged mark takes its rarest member's id, kind and
 * group, starts where the group's text starts, and points at the rarest
 * member's target (or the first member's that has one); targets in other
 * messages are its `also`, so their arcs reach it too.
 */
export function mergeAgentEvents(events:AgentEventLink[],counts:KindCounts=eventKindCounts(events)):AgentEventLink[]{
 const byMessage=new Map<string,AgentEventLink[]>();for(const e of events){const list=byMessage.get(e.message_id)??[];list.push(e);byMessage.set(e.message_id,list);}
 const merged=new Map<AgentEventLink,AgentEventLink>(),skip=new Set<AgentEventLink>();
 for(const list of byMessage.values())for(const group of eventSpanGroups(list.map(e=>e.span??null))){
  if(group.length<2)continue;
  const members=byRarity(group.map(i=>list[i]),counts),first=members[0],linked=members.map(linkedTarget).filter((t):t is EventTarget=>!!t);
  const target=linkedTarget(first)??linked[0]??first.target,positions=members.map(m=>m.position).filter((p):p is number=>typeof p==='number');
  const also=[...new Map(linked.filter(t=>targetKey(t)!==(target?.message_id??'')).map(t=>[targetKey(t),t])).values()];
  const mark:AgentEventLink={...first,target,members,...(positions.length?{position:Math.min(...positions)}:{}),...(also.length?{also}:{})};
  // The mark takes the place of its earliest member in reading order.
  const at=group.map(i=>list[i]);merged.set(at[0],mark);for(const e of at.slice(1))skip.add(e);
 }
 return events.flatMap(e=>skip.has(e)?[]:[merged.get(e)??e]);
}
/** The ids a mark stands for: its own, or every merged member's. */
export const markIds=(e:AgentEventLink)=>(e.members??[e]).map(m=>m.id);
/** What a merged mark's card lists: each kind once (the likelier), each sentence once (one inside another is dropped), each target once. */
export function eventCardView(e:AgentEventLink){
 const members=e.members??[e],kinds=new Map<string,AgentEventLink>();
 for(const m of members){const seen=kinds.get(m.kind);if(!seen||m.probability>seen.probability)kinds.set(m.kind,m);}
 const texts=[...new Set(members.map(m=>m.sentence.trim()).filter(Boolean))],sentences=texts.filter(text=>!texts.some(other=>other!==text&&other.includes(text)));
 const targets=[...new Map(members.map(linkedTarget).filter((t):t is EventTarget=>!!t).map(t=>[`${t.message_id}\u0000${t.text??''}`,t])).values()];
 return {labels:[...kinds.values()],sentences,targets,unlinked:members.every(m=>!linkedTarget(m))?members[0].target:null};
}
