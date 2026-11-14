// Human events on the conversation reader's timeline: one marker for each
// engagement annotation in a person's message (reader-model.js,
// engagementTimeline). A person's message is an instant on the timeline's
// clock, so its markers would pile onto one point; humanEventLayout gives each
// such message a slot and squeezes the rest of the clock in proportion, and
// humanEventPlacement puts each marker where its annotation starts in the
// message, stacking the ones that would touch as tool-call marks are stacked.
import type {TimeScale} from './timeline-idle-model';
import {laneCandidates,laneMark,placeMarks} from './ribbon-marks';

export type HumanEvent={id:string;message_id:string;slot:number;className:string;dimension:string;name:string;label:string;labelName:string;title:string;score:number|null;start:number;end:number;position:number;text:string};
export type HumanEventGroup={message_id:string;at:number;events:HumanEvent[]};
export type HumanEventSlot={left:number;width:number};
export type HumanEventDimension={id:string;name:string;className:string;slot:number;events:HumanEvent[]};

/** A dimension's mark on the timeline: its display name's first letter (the nine names each start with their own). */
export const humanEventLetter=(name:string)=>name.trim().charAt(0).toUpperCase()||'?';

/** CSS pixels one marker takes on a row: an agent-event glyph and its gap. */
export const HUMAN_EVENT_SYMBOL_PX=10;
/** A marker's radius for stacking, and the gap kept between two. */
export const HUMAN_EVENT_RADIUS=3.5;
/** A message's slot is at most this many markers wide; more of them stack. */
export const HUMAN_EVENT_SLOT_SYMBOLS=8;
/** The most of the row all people's messages may take together. */
export const HUMAN_EVENT_SHARE=.5;

/** The dimensions present, in slot order: the Timeline key's chips and the
 * rows Human events folds out into. */
export function humanEventDimensions(events:HumanEvent[]):HumanEventDimension[]{
  const dimensions=new Map<string,HumanEventDimension>();
  for(const event of events){
    const dimension=dimensions.get(event.dimension)??{id:event.dimension,name:event.name,className:event.className,slot:event.slot,events:[]};
    dimension.events.push(event);
    dimensions.set(event.dimension,dimension);
  }
  return [...dimensions.values()].sort((a,b)=>a.slot-b.slot||a.id.localeCompare(b.id));
}

/** Each message's markers in timeline order. `visible` is the Timeline key's
 * choice by dimension ID; a message off the clock or left without a marker has no
 * group. */
export function humanEventGroups(events:HumanEvent[],at:(messageID:string)=>number|undefined,visible:(dimension:string)=>boolean=()=>true):HumanEventGroup[]{
  const groups=new Map<string,HumanEventGroup>();
  for(const event of events){
    if(!visible(event.dimension))continue;
    const time=at(event.message_id);if(time===undefined||!Number.isFinite(time))continue;
    const group=groups.get(event.message_id)??{message_id:event.message_id,at:time,events:[]};
    group.events.push(event);groups.set(event.message_id,group);
  }
  return [...groups.values()].map(group=>({...group,events:[...group.events].sort((a,b)=>a.position-b.position||a.slot-b.slot)})).sort((a,b)=>a.at-b.at);
}

/** The clock with a slot at each group's time. A slot is as wide as its
 * markers side by side on a row `trackPx` wide, up to HUMAN_EVENT_SLOT_SYMBOLS
 * of them; when all slots together would pass `share` of the row they shrink
 * alike. Everything else keeps its proportions in what is left. */
export function humanEventLayout(scale:TimeScale,groups:HumanEventGroup[],trackPx:number,symbolPx=HUMAN_EVENT_SYMBOL_PX,share=HUMAN_EVENT_SHARE):{scale:TimeScale;slots:Map<string,HumanEventSlot>}{
  const slots=new Map<string,HumanEventSlot>();
  if(!groups.length||!(trackPx>0))return {scale,slots};
  const wanted=groups.map(group=>({group,x:Math.max(0,Math.min(1,scale.fraction(group.at))),width:(Math.min(group.events.length,HUMAN_EVENT_SLOT_SYMBOLS)*symbolPx+2)/trackPx})).sort((a,b)=>a.x-b.x);
  const total=wanted.reduce((sum,item)=>sum+item.width,0),shrink=total>share?share/total:1,rest=1-total*shrink;
  let before=0;
  const placed=wanted.map(item=>{const width=item.width*shrink,left=rest*item.x+before;before+=width;return {...item,width,left};});
  for(const item of placed)slots.set(item.group.message_id,{left:item.left,width:item.width});
  // x is a fraction on the given clock; a slot opens just after its own x.
  const forward=(x:number)=>{let y=rest*x;for(const item of placed){if(item.x>=x)break;y+=item.width;}return y;};
  const backward=(y:number)=>{let skipped=0;for(const item of placed){if(y<=item.left)break;if(y<item.left+item.width)return item.x;skipped+=item.width;}return rest>0?(y-skipped)/rest:0;};
  return {slots,scale:{...scale,cuts:scale.cuts.map(cut=>({...cut,fraction:forward(cut.fraction)})),fraction:at=>forward(scale.fraction(at)),time:fraction=>scale.time(backward(fraction))}};
}

/** Slots on a clock that cannot be widened (the workspace Timeline, where
 * every lane shares one): each message's slot starts at its instant and is
 * as wide as its markers side by side, up to HUMAN_EVENT_SLOT_SYMBOLS, kept
 * inside the row. Slots may overlap what follows; the markers then stack. */
export function humanEventPointSlots(scale:Pick<TimeScale,'fraction'>,groups:HumanEventGroup[],trackPx:number,symbolPx=HUMAN_EVENT_SYMBOL_PX):Map<string,HumanEventSlot>{
  const slots=new Map<string,HumanEventSlot>();
  if(!(trackPx>0))return slots;
  for(const group of groups){
    const width=Math.min(1,(Math.min(group.events.length,HUMAN_EVENT_SLOT_SYMBOLS)*symbolPx+2)/trackPx),x=scale.fraction(group.at);
    if(!Number.isFinite(x)||x<0||x>1)continue;
    slots.set(group.message_id,{left:Math.max(0,Math.min(1-width,x-symbolPx/2/trackPx)),width});
  }
  return slots;
}

/** A message of a workspace Timeline lane as the service sends it: its
 * engagement spans index engagement_text, the stored text (a lane otherwise
 * holds an excerpt), and authorship is the text's parts in order. */
export type LaneHumanMessage={id:string;role:string;kind:string;engagement?:unknown[];engagement_text?:string;authorship?:{category:string;bytes:number}[]};
type EngagementTimeline=(messages:unknown[])=>HumanEvent[];
/** A lane's Human events. The display rules are the reader's and live in one
 * place, reader-model.js (engagementTimeline: the score threshold, whole
 * words, typed text only, labels never shown, display names), a classic
 * script on every page; this hands it each message in the reader's shape. */
export function laneHumanEvents(messages:LaneHumanMessage[],timeline:EngagementTimeline|undefined=(globalThis as {engagementTimeline?:EngagementTimeline}).engagementTimeline):HumanEvent[]{
  if(!timeline)return [];
  const shaped=messages.flatMap(message=>{
    if(!message.engagement?.length||typeof message.engagement_text!=='string')return [];
    let offset=0;const spans=(message.authorship??[]).map(part=>{const span={category:part.category,start:offset,end:offset+part.bytes};offset+=part.bytes;return span;});
    return [{id:message.id,role:message.role,kind:message.kind,text:message.engagement_text,engagement:message.engagement,authorship:{spans}}];
  });
  return shaped.length?[...timeline(shaped)]:[];
}

/** Where a marker's centre sits along the row, in CSS pixels: its
 * annotation's start in the message, mapped across the message's slot. The
 * same on the folded row and on its dimension's row. Null without a slot. */
export function humanEventX(event:HumanEvent,slot:HumanEventSlot|undefined,trackPx:number,symbolPx=HUMAN_EVENT_SYMBOL_PX){
  if(!slot||!(trackPx>0))return null;
  const room=Math.max(0,slot.width*trackPx-symbolPx);
  return slot.left*trackPx+symbolPx/2+Math.max(0,Math.min(1,event.position))*room;
}

/** The markers of one row, each at its x and in a lane: the row's lanes and
 * the walk that fills them are the tool-call marks' (ribbon-marks.ts), so
 * markers that would touch stack above and below the axis, and when a
 * stretch has more than the lanes hold they overlap as little as they can. */
export function humanEventPlacement(events:HumanEvent[],slots:Map<string,HumanEventSlot>,trackPx:number,height:number,r=HUMAN_EVENT_RADIUS){
  const lanes=laneCandidates(height,r);
  const marks=events.flatMap(event=>{const x=humanEventX(event,slots.get(event.message_id),trackPx);return x===null?[]:[laneMark(event.id,x,r,lanes)];});
  return new Map(placeMarks(marks).map(mark=>[mark.id,mark]));
}

/** A marker's accessible name: its label, its score and what it covers. */
export function humanEventTitle(event:HumanEvent){
  return `Human event · ${event.title}${event.score==null?'':` ${Math.round(event.score*100)}%`}: ${event.text}`;
}
