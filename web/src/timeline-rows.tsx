// The rows every timeline is drawn from. The Timeline's session lanes
// (timeline-session-lane.tsx) and the conversation reader's sticky timeline
// (reader-navigator.tsx) are the same tree of rows: a session, folding out
// into its Message modes, Agent events and Tool actions, then its Human
// events. A lane (TimelineLane) says what to draw on which clock; a surface
// (TimelineSurface) says how its page frames a row and what a click does.
// What differs between the two pages is only what those two parameters say.
import React,{useEffect,useMemo,useState} from 'react';
import {Icon} from './icons';
import {ViewDisclosure} from './view-disclosure';
import {useViewEntry} from './view-state';
import {useRibbonInspector} from './label-ribbon-inspector';
import {RibbonNodeDetails,SessionRibbon,type EventLayer,type RibbonBrushHandlers,type RibbonReveal,type RibbonSourceHref,type TimelineInteraction} from './label-session-ribbon';
import {ribbonEventCount,type RibbonNode} from './label-session-ribbon-model';
import type {TimeRange} from './timeline-label-model';
import type {TimeScale} from './timeline-idle-model';
import {eventKindCounts,mergeAgentEvents,type KindCounts} from './event-merge';
import type {AgentEventLink} from './event-link-layout';
import {timelineEventVisible,type TimelineVisibility} from './timeline-visibility';
import {humanEventDimensions,humanEventLetter,humanEventPointSlots,timelineRowExpands,type HumanEvent,type HumanEventGroup,type HumanEventSlot} from './human-events-model';
import {HumanEventMarkers} from './human-events-rows';

type Inspector=ReturnType<typeof useRibbonInspector>;
/** One row as its surface frames it. `level` is 0 for the session's own row. */
export type TimelineRowState={root:boolean;level:number;expands:boolean;count:number};

/** How a page frames timeline rows. */
export type TimelineSurface={
 /** Hover and click, alike on every row of the surface (TimelineInteraction, label-session-ribbon.tsx). */
 interaction:TimelineInteraction;
 /** The disclosure identity of a row ('session', 'events', 'activity:run', 'human', …). It is saved in URLs, so it does not change. */
 viewKey:(row:string)=>string;
 classes:{node:(root:boolean)=>string;leaf:(root:boolean)=>string;human:string;dimension:string;humanTrack:string};
 /** Whether rows carry their label column. */
 labels:boolean;
 label:(row:TimelineRowState)=>React.HTMLAttributes<HTMLSpanElement>;
 /** What the session's own row is called; the rows under it name themselves (RibbonNodeDetails). */
 rootLabel:(node:RibbonNode,row:TimelineRowState)=>React.ReactNode;
 caret:(expands:boolean)=>React.ReactNode;
 /** A column before the label, such as the session's identity and its branch lines. */
 lead?:(root:boolean)=>React.ReactNode;
 /** What holds a row's ribbon, when the ribbon is not itself the cell. */
 track?:(ribbon:React.ReactNode,root:boolean)=>React.ReactNode;
 /** A row that does not fold out, when it is more than its content in a `leaf` box. */
 leaf?:(row:{node:RibbonNode;level:number;empty:boolean;content:React.ReactNode;inspector:Inspector})=>React.ReactNode;
};

/** A session's Human events. Markers sit in slots laid over the lane's clock unless `layout` gives slots the clock was widened for. A marker opens `href`, or else calls `jump`; `revealAt` answers a click between markers. */
export type TimelineHumanEvents={groups:HumanEventGroup[];layout?:{slots:Map<string,HumanEventSlot>;trackPx:number};href?:(event:HumanEvent)=>string|null;jump?:(event:HumanEvent)=>void;revealAt?:(fraction:number)=>void};

/** What one session's rows draw, and on which clock. */
export type TimelineLane={
 surface:TimelineSurface;visibility?:TimelineVisibility;range:TimeRange;scale:TimeScale;
 events?:EventLayer;
 /** Events per kind across the workspace, the population a merged mark's rarest kind comes from (event-merge.ts); the lane's own events when absent. */
 eventCounts?:KindCounts;
 human?:TimelineHumanEvents;caller?:boolean;sourceHref?:RibbonSourceHref;brushHandlers?:RibbonBrushHandlers;preview?:TimeRange|null;showUnknownTail?:boolean;onReveal?:(hit:RibbonReveal)=>void;
};

/** Agent events on a lane's clock: each at its sentence's share of its message
 * (`times` is the lane's agentEventClock), left out when hidden in the Key or
 * outside the window. An arc still runs back to a target before the window's
 * end, and stops at the right edge for one after it. */
export function timelineEventLayer({events,visibility={},times,scale,range,href}:{events:AgentEventLink[];visibility?:TimelineVisibility;times:{at:(event:AgentEventLink)=>number|null;targetAt:(event:AgentEventLink)=>number|null};scale:TimeScale;range:TimeRange;href?:EventLayer['href']}):EventLayer{
 const fraction=(at:number|null)=>at===null?null:at<range[0]?-1:at>range[1]?2:scale.fraction(at);
 return {events:events.filter(event=>timelineEventVisible(visibility,event)),position:event=>{const f=fraction(times.at(event));return f===null||f<0||f>1?null:f;},targetPosition:event=>{const f=fraction(times.targetAt(event));return f===null||f<0?null:Math.min(1,f);},href};
}

/** The events one row draws. The session's row draws them all and an Agent
 * events row its own, each with their arcs; both merge events on
 * near-identical text into one mark (event-merge.ts). The group and kind rows
 * below keep every event, since they exist to decompose. Other rows draw none. */
export function timelineRowEvents(node:RibbonNode,layer:EventLayer|undefined,root:boolean,counts?:KindCounts):EventLayer|undefined{
 if(!layer)return undefined;
 const own=node.id==='events'||node.id.startsWith('events:');
 if(!root&&!own)return undefined;
 const ids=new Set(node.items.flatMap(item=>item.event?[item.event.id]:[]));
 const events=root?layer.events:layer.events.filter(event=>ids.has(event.id));
 return {...layer,events:root||node.id==='events'?mergeAgentEvents(events,counts??eventKindCounts(layer.events)):events};
}

/** One row and, folded out, the rows under it. */
export function TimelineRow({node,lane,level=0}:{node:RibbonNode;lane:TimelineLane;level?:number}){
 const {surface}=lane,root=level===0;
 const viewKey=surface.viewKey(node.id),[open]=useViewEntry('disclosures',viewKey,false);
 const inspector=useRibbonInspector();
 const eventKey=node.items.flatMap(item=>item.event?[item.event.id]:[]).join(' ');
 const eventLayer=useMemo(()=>timelineRowEvents(node,lane.events,root,lane.eventCounts),[lane.events,lane.eventCounts,root,node.id,eventKey]);
 // A session's Human events hang off its own row, which therefore folds out while it has any to show, even with every other section hidden in the Key.
 const human=root?lane.human:undefined,humanGroups=human?.groups.length??0;
 const expands=timelineRowExpands(node.children.length,humanGroups);
 const row:TimelineRowState={root,level,expands,count:ribbonEventCount(node.items)};
 const ribbon=<SessionRibbon visibility={lane.visibility} node={node} range={lane.range} clock={lane.scale} brushHandlers={lane.brushHandlers} preview={lane.preview} caller={lane.caller} inspector={inspector} sourceHref={lane.sourceHref} interaction={surface.interaction} onReveal={lane.onReveal} showUnknownTail={lane.showUnknownTail} eventLayer={eventLayer}/>;
 const label=surface.labels?<span {...surface.label(row)}>{root?surface.rootLabel(node,row):<RibbonNodeDetails node={node}/>} {surface.caret(expands)}</span>:null;
 const content=<>{surface.lead?.(root)}{label}{surface.track?surface.track(ribbon,root):ribbon}</>;
 if(!expands||surface.interaction==='none')return surface.leaf?<>{surface.leaf({node,level,empty:!node.items.length&&!humanGroups,content,inspector})}</>:<div className={surface.classes.leaf(root)}>{content}</div>;
 return <ViewDisclosure viewKey={viewKey} className={surface.classes.node(root)} summary={content}>{open?node.children.map(child=><TimelineRow key={child.id} node={child} lane={lane} level={level+1}/>):null}{open&&human?<TimelineHumanRows lane={lane}/>:null}</ViewDisclosure>;
}

const HUMAN_ROW_HEIGHT=28;
/** Human events, after a session's Agent events and Tool actions rows: a
 * marker per engagement annotation in each of the person's messages, placed in
 * that message's slot by where the annotation starts. Like Agent events, the
 * row folds out into one row per kind (here, dimension); a marker keeps its x
 * on both. */
function TimelineHumanRows({lane}:{lane:TimelineLane}){
 const {surface,human}=lane,given=human?.layout;
 const viewKey=surface.viewKey('human'),[open]=useViewEntry('disclosures',viewKey,false);
 const inspector=useRibbonInspector();
 // A clock shared by every lane cannot widen for one lane's markers, so their slots are laid over what follows each message's instant (humanEventPointSlots), at the width the track turns out to have.
 const [track,setTrack]=useState<HTMLSpanElement|null>(null),[measured,setMeasured]=useState(0);
 useEffect(()=>{if(given||!track||typeof ResizeObserver==='undefined')return;const measure=()=>setMeasured(Math.round(track.clientWidth));const observer=new ResizeObserver(measure);observer.observe(track);measure();return()=>observer.disconnect();},[track,!given]);
 const events=human?.groups.flatMap(group=>group.events)??[];
 if(!human||!events.length)return null;
 const trackPx=given?.trackPx??measured,slots=given?.slots??humanEventPointSlots(lane.scale,human.groups,trackPx),dimensions=humanEventDimensions(events);
 const markers=(shown:HumanEvent[])=>trackPx?<HumanEventMarkers events={shown} slots={slots} trackPx={trackPx} height={HUMAN_ROW_HEIGHT} inspector={inspector} jump={human.jump} href={human.href}/>:null;
 // Between markers a track answers as the rows above it do, and leaves the row's fold alone.
 const reveal=human.revealAt?(event:React.MouseEvent<HTMLElement>)=>{event.preventDefault();event.stopPropagation();const rect=event.currentTarget.getBoundingClientRect();if(rect.width)human.revealAt!((event.clientX-rect.left)/rect.width);}:undefined;
 const label=(level:number,content:React.ReactNode)=>surface.labels?<span {...surface.label({root:false,level,expands:level===1,count:events.length})}>{content} {surface.caret(level===1)}</span>:null;
 const lead=surface.lead?.(false);
 return <><ViewDisclosure viewKey={viewKey} className={`${surface.classes.node(false)} ${surface.classes.human}`} summary={<>{lead}{label(1,<><Icon name="human"/><strong>Human events</strong><small>{events.length}</small></>)}
   <span ref={setTrack} className={surface.classes.humanTrack} role="group" aria-label={`Human events. ${human.href?'Open a marker to read its text in the conversation.':'Click a marker to reveal its text.'}`} onClick={reveal}>{markers(events)}</span></>}>
  {open?dimensions.map(dimension=><div key={dimension.id} className={surface.classes.dimension}>{lead}{label(2,<><i className={`eng-glyph ${dimension.className}`}>{humanEventLetter(dimension.name)}</i><strong>{dimension.name}</strong><small>{dimension.events.length}</small></>)}
   <span className={surface.classes.humanTrack} role="group" aria-label={`Human events · ${dimension.name}`} onClick={reveal}>{markers(dimension.events)}</span></div>):null}
 </ViewDisclosure>{inspector.portal}</>;
}
