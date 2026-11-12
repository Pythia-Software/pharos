// Agent events inside a ribbon (layout: event-link-layout.ts). Used by the
// conversation fingerprint and the session timeline's rows. The overlay only
// draws; the ribbon beneath it hit-tests events with its tool dots and
// message spans (useRibbonPointer). Hover is transient: it emphasises one arc
// (its target and every event pointing at it) across every ribbon on the
// page and dims the rest.
import React,{useLayoutEffect,useMemo,useState,useSyncExternalStore} from 'react';
import {PageLink} from './nested-pages';
import {EVENT_GROUPS,eventColor,eventGroup,glyphPath,layoutEventArcs,linkedTarget,targetDescription,targetEvidence,weakTarget,type AgentEventLink,type EventLayout,type EventTarget,type LayoutEvent} from './event-link-layout';
import {eventCardView,markIds} from './event-merge';
import type {HitTarget} from './ribbon-marks';
import './event-link-overlay.css';

/** events: every id the hovered mark stands for (a merged mark's members), so the same events stay lit in rows that draw them apart. */
export type EventLinkFocus={event:string|null;target:string|null;message?:string|null;events?:string[]}|null;
let focus:EventLinkFocus=null;const listeners=new Set<()=>void>();
export function setEventLinkFocus(next:EventLinkFocus){if(focus?.event===next?.event&&focus?.target===next?.target&&focus?.message===next?.message&&String(focus?.events)===String(next?.events))return;focus=next;listeners.forEach(fn=>fn());}
export const useEventLinkFocus=()=>useSyncExternalStore(fn=>{listeners.add(fn);return()=>{listeners.delete(fn);};},()=>focus,()=>null);

const clip=(text:string,n:number)=>text.length>n?text.slice(0,n-1)+'…':text;
export function EventGlyph({group,hollow=false,size=10}:{group:string;hollow?:boolean;size?:number}){
 return <svg className="event-link-glyph" width={size} height={size} viewBox="-6 -6 12 12" aria-hidden="true"><path d={glyphPath(eventGroup(group).shape,0,0,4.6)} fill={hollow?'none':eventColor(group)} stroke={eventColor(group)} strokeWidth={hollow?1.4:0}/></svg>;
}
/** Legend: the groups present (shape and colour), and what an arc means. */
export function EventLinkLegend({events,renderGroup,heading=<strong>Agent events</strong>,renderHint}:{events:AgentEventLink[];heading?:React.ReactNode;renderHint?:(icon:React.ReactNode,label:string)=>React.ReactNode;renderGroup?:(group:typeof EVENT_GROUPS[number])=>React.ReactNode}){
 if(!events.length)return null;const groups=new Set(events.map(e=>e.group)),linked=events.some(e=>linkedTarget(e)),open=events.some(e=>!linkedTarget(e));
 const hint=(icon:React.ReactNode,label:string)=>renderHint?renderHint(icon,label):<span>{icon}{label}</span>;
 return <div className="lt-legend-line event-link-legend">{heading}{EVENT_GROUPS.filter(g=>groups.has(g.name)).map(g=>renderGroup?<React.Fragment key={g.key}>{renderGroup(g)}</React.Fragment>:<span key={g.key}><EventGlyph group={g.name}/>{g.short}</span>)}
  {linked?hint(<svg width="26" height="12" aria-hidden="true"><path d="M2 10C8 2 16 3 24 3" fill="none" stroke="var(--event-link)" strokeWidth="1.2"/><path d="M2 7V11" stroke="var(--event-link)" strokeWidth="2"/></svg>,'Arc back to the earlier text it is about'):null}
  {open?hint(<EventGlyph group={events.find(e=>!linkedTarget(e))!.group} hollow/>,'Open: Ungrounded'):null}</div>;
}
/** One card per mark: a merged mark (event-merge.ts) lists every kind with its probability, then its text once and each target once. */
export function EventLinkCard({events,href,close}:{events:AgentEventLink[];href?:(e:AgentEventLink)=>string|null;close?:()=>void}){
 const shown=events.slice(0,4),total=events.reduce((n,e)=>n+(e.members?.length??1),0);
 return <div className="event-link-cards">{total>1?<p>{total} agent events</p>:null}{shown.map(e=>{const view=eventCardView(e),linked=view.targets,link=href?.(e),merged=(e.members?.length??1)>1;return <div key={e.id} className="event-link-card">
  {merged?<div className="event-link-card-kinds">{view.labels.map(m=><span key={m.kind} className="event-link-card-head"><EventGlyph group={m.group} hollow={!linkedTarget(m)}/><strong>{m.label}</strong><small>{Math.round(m.probability*100)}%</small></span>)}<small>{e.source==='thinking'?'in thinking':'in a reply'}</small></div>
   :<div className="event-link-card-head"><EventGlyph group={e.group} hollow={!linked.length}/><strong>{e.label}</strong><small>{e.source==='thinking'?'in thinking':'in a reply'}</small></div>}
  {view.sentences.map((text,i)=><blockquote key={i} className={`is-observation event-${eventGroup(e.group).key}`}><b>Observation</b>{clip(text,260)}</blockquote>)}
  {linked.length?linked.map((t,i)=><TargetQuote key={i} target={t}/>):<small>{targetDescription(view.unlinked)}</small>}
  {link?<PageLink href={link} className="event-link-source" onClick={event=>{event.stopPropagation();close?.();}}>{linked.length?'Show both in conversation →':'Show in conversation →'}</PageLink>:null}
 </div>;})}{events.length>shown.length?<small>{events.length-shown.length} more here</small>:null}</div>;
}
function TargetQuote({target}:{target:EventTarget}){
 return <blockquote className={`is-target${weakTarget(target)?' is-weak':''}`}><b>{targetDescription(target)}</b>“{clip(target.text??'',220)}”<small>{targetEvidence(target)}</small></blockquote>;
}

/** An event on a ribbon: x and its target's x in [0, 1] of the ribbon's width. */
/** also: where a merged mark's further targets sit (event-merge.ts). */
export type PlacedEvent={event:AgentEventLink;x:number;targetX:number|null;offRibbon:boolean;also?:{key:string;x:number;weak:boolean}[]};
/** position gives an event's x and targetPosition its target's, in [0, 1] (null when off the ribbon). */
export function placeEvents(events:AgentEventLink[],position:(e:AgentEventLink)=>number|null,targetPosition:(e:AgentEventLink)=>number|null=e=>{const t=linkedTarget(e);return t?position({...e,message_id:t.message_id!}):null;}):PlacedEvent[]{
 return events.flatMap(e=>{const x=position(e);if(x===null)return [];const t=linkedTarget(e),tx=t?targetPosition(e):null;
  const also=(e.also??[]).flatMap(other=>{const ox=targetPosition({...e,target:other});return other.message_id?[{key:other.message_id,x:Math.max(0,ox??0),weak:weakTarget(other)}]:[];});
  return [{event:e,x,targetX:t?Math.max(0,tx??0):null,offRibbon:!!t&&tx===null,...(also.length?{also}:{})}];});
}
/** Arcs and markers in the ribbon's pixels (event-link-layout.ts). */
export function eventLayout(placed:PlacedEvent[],width:number,height:number,marker=3.2):EventLayout|null{
 if(!width||!height||!placed.length)return null;
 return layoutEventArcs(placed.map((p):LayoutEvent=>({id:p.event.id,x:p.x*width,targetKey:p.targetX===null?null:linkedTarget(p.event)!.message_id!,targetX:p.targetX===null?null:p.targetX*width,weak:p.targetX!==null&&weakTarget(linkedTarget(p.event)!),also:p.also?.map(a=>({...a,x:a.x*width}))})),{width,height,marker});
}
/** Hit targets for the ribbon's shared hit test (ribbon-marks.ts): each marker, and each arc's anchor (every event on that arc). */
export type EventHitTarget=HitTarget&{events:string[];arc:string|null;event:string|null};
export function eventHitTargets(layout:EventLayout|null):EventHitTarget[]{
 if(!layout)return [];
 return [...layout.markers.map(m=>({x:m.x,y:m.y,r:m.r,events:[m.id],arc:m.arc,event:m.id})),...layout.arcs.map(arc=>({x:arc.x0,y:layout.axis,r:1.5,events:arc.events,arc:arc.key,event:null}))];
}
/** What hovering a target emphasises: its arc (target and every event on it) in every ribbon on the page. */
export function eventTargetFocus(target:EventHitTarget,events:Map<string,AgentEventLink>):EventLinkFocus{
 const e=target.event?events.get(target.event):undefined;
 return {event:target.event,target:target.arc,message:e?.message_id??null,...(e?.members?{events:markIds(e)}:{})};
}
export type EventLinkOverlayProps={placed:PlacedEvent[];layout:EventLayout|null;width:number;height:number;label:string};
/** Draws only: hover, hit testing and pinning belong to the ribbon under it (useRibbonPointer), so events, tool dots and message spans behave alike. */
export function EventLinkOverlay({placed,layout,width,height,label}:EventLinkOverlayProps){
 const current=useEventLinkFocus(),byID=useMemo(()=>new Map(placed.map(p=>[p.event.id,p])),[placed]);
 if(!placed.length||!layout)return null;
 const focusArc=current?.target??null,lit=(e:AgentEventLink)=>!!current&&markIds(e).some(id=>id===current.event||current.events?.includes(id)),focused=(arc:string|null,e?:AgentEventLink)=>!current||(e!==undefined&&lit(e))||(arc!==null&&arc===focusArc);
 return <svg className={`event-link-overlay${current?' has-focus':''}`} width={width} height={height} role="img" aria-label={label}>
  {layout.arcs.map(arc=>{const on=focused(arc.key),strong=current&&on;const offRibbon=arc.events.some(id=>byID.get(id)?.offRibbon);return <g key={arc.key} className={`event-link-arc${strong?' is-focus':''}${on?'':' is-dim'}`}>
   <path className="event-link-path" d={arc.path} strokeDasharray={arc.weak?'3 2':undefined}/>
   {/* The target's anchor: a tick on the axis, or an arrow at the edge for a target left of the ribbon. */}
   <path className="event-link-anchor" d={offRibbon?`M${arc.x0+1} ${layout.axis-3}L${arc.x0+4} ${layout.axis}L${arc.x0+1} ${layout.axis+3}`:`M${arc.x0} ${layout.axis-arc.side*3.5}V${layout.axis+arc.side*1.5}`}/>
  </g>;})}
  {layout.markers.map(m=>{const e=byID.get(m.id)!.event,on=focused(m.arc,e),color=eventColor(e.group);const merged=(e.members?.length??1)>1;return <path key={m.id} data-agent-event={e.id} data-agent-events={merged?markIds(e).join(' '):undefined} className={`event-link-marker${merged?' is-merged':''}${m.arc?' is-linked':' is-unlinked'}${on?'':' is-dim'}${lit(e)?' is-focus':''}`}
    d={glyphPath(eventGroup(e.group).shape,m.x,m.y,m.r)} fill={m.arc?color:'none'} stroke={m.arc?'none':color} strokeWidth={m.arc?undefined:1.2}/>;})}
 </svg>;
}
/** An element's size in CSS pixels, kept current. */
export function useBoxSize(ref:React.RefObject<Element|null>){
 const [size,setSize]=useState({width:0,height:0});
 useLayoutEffect(()=>{const element=ref.current;if(!element)return;const resize=()=>{const rect=element.getBoundingClientRect();setSize(previous=>previous.width===rect.width&&previous.height===rect.height?previous:{width:rect.width,height:rect.height});};resize();const observer=new ResizeObserver(resize);observer.observe(element);return()=>observer.disconnect();},[]);
 return size;
}
