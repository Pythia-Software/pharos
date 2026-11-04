// Agent events inside a ribbon (layout: event-link-layout.ts). Used by the
// conversation fingerprint and the session timeline's rows. Hover is
// transient: it emphasises one arc (its target and every event pointing
// at it) across every ribbon on the page and dims the rest.
import React,{useLayoutEffect,useMemo,useRef,useState,useSyncExternalStore} from 'react';
import {PageLink} from './nested-pages';
import {EVENT_GROUPS,eventColor,eventGroup,glyphPath,layoutEventArcs,linkedTarget,targetDescription,targetEvidence,weakTarget,type AgentEventLink,type LayoutEvent} from './event-link-layout';
import type {useRibbonInspector} from './label-ribbon-inspector';
import './event-link-overlay.css';

export type EventLinkFocus={event:string|null;target:string|null;message?:string|null}|null;
let focus:EventLinkFocus=null;const listeners=new Set<()=>void>();
export function setEventLinkFocus(next:EventLinkFocus){if(focus?.event===next?.event&&focus?.target===next?.target&&focus?.message===next?.message)return;focus=next;listeners.forEach(fn=>fn());}
export const useEventLinkFocus=()=>useSyncExternalStore(fn=>{listeners.add(fn);return()=>{listeners.delete(fn);};},()=>focus,()=>null);

const clip=(text:string,n:number)=>text.length>n?text.slice(0,n-1)+'…':text;
export function EventGlyph({group,hollow=false,size=10}:{group:string;hollow?:boolean;size?:number}){
 return <svg className="event-link-glyph" width={size} height={size} viewBox="-6 -6 12 12" aria-hidden="true"><path d={glyphPath(eventGroup(group).shape,0,0,4.6)} fill={hollow?'none':eventColor(group)} stroke={eventColor(group)} strokeWidth={hollow?1.4:0}/></svg>;
}
/** Legend: the groups present (shape and colour), and what an arc means. */
export function EventLinkLegend({events}:{events:AgentEventLink[]}){
 if(!events.length)return null;const groups=new Set(events.map(e=>e.group)),linked=events.some(e=>linkedTarget(e)),open=events.some(e=>!linkedTarget(e));
 return <div className="lt-legend-line event-link-legend"><strong>Agent events</strong>{EVENT_GROUPS.filter(g=>groups.has(g.name)).map(g=><span key={g.key}><EventGlyph group={g.name}/>{g.short}</span>)}
  {linked?<span><svg width="26" height="12" aria-hidden="true"><path d="M2 10C8 2 16 3 24 3" fill="none" stroke="var(--event-link)" strokeWidth="1.2"/><path d="M2 7V11" stroke="var(--event-link)" strokeWidth="2"/></svg>Arc back to the earlier text it is about</span>:null}
  {open?<span><EventGlyph group={events.find(e=>!linkedTarget(e))!.group} hollow/>On the axis: no target found</span>:null}</div>;
}
export function EventLinkCard({events,href,close}:{events:AgentEventLink[];href?:(e:AgentEventLink)=>string|null;close?:()=>void}){
 const shown=events.slice(0,4);
 return <div className="event-link-cards">{events.length>1?<p>{events.length} agent events</p>:null}{shown.map(e=>{const t=e.target,linked=linkedTarget(e),link=href?.(e);return <div key={e.id} className="event-link-card">
  <div className="event-link-card-head"><EventGlyph group={e.group} hollow={!linked}/><strong>{e.label}</strong><small>{e.source==='thinking'?'in thinking':'in a reply'}</small></div>
  <blockquote className={`is-observation event-${eventGroup(e.group).key}`}><b>Observation</b>{clip(e.sentence,260)}</blockquote>
  {linked?<blockquote className={`is-target${weakTarget(linked)?' is-weak':''}`}><b>{targetDescription(t)}</b>“{clip(linked.text??'',220)}”<small>{targetEvidence(linked)}</small></blockquote>:<small>{targetDescription(t)}</small>}
  {link?<PageLink href={link} className="event-link-source" onClick={event=>{event.stopPropagation();close?.();}}>{linked?'Show both in conversation →':'Show in conversation →'}</PageLink>:null}
 </div>;})}{events.length>shown.length?<small>{events.length-shown.length} more here</small>:null}</div>;
}

type Inspector=ReturnType<typeof useRibbonInspector>;
export type EventLinkOverlayProps={events:AgentEventLink[];
 /** Message id → x in [0, 1] of the ribbon's width, or null when the message is not on it. */
 position:(messageID:string)=>number|null;
 /** Target message id → x in [0, 1]; defaults to position. A target on another row (timeline) still has an x. */
 targetPosition?:(messageID:string)=>number|null;
 inspector:Inspector;href?:(e:AgentEventLink)=>string|null;marker?:number;label:string};
export function EventLinkOverlay({events,position,targetPosition=position,inspector,href,marker=3.2,label}:EventLinkOverlayProps){
 const box=useRef<SVGSVGElement>(null),[size,setSize]=useState({width:0,height:0}),current=useEventLinkFocus();
 useLayoutEffect(()=>{const element=box.current?.parentElement;if(!element)return;const resize=()=>{const rect=element.getBoundingClientRect();setSize(previous=>previous.width===rect.width&&previous.height===rect.height?previous:{width:rect.width,height:rect.height});};resize();const observer=new ResizeObserver(resize);observer.observe(element);return()=>observer.disconnect();},[]);
 const placed=useMemo(()=>events.flatMap(e=>{const x=position(e.message_id);if(x===null)return [];const t=linkedTarget(e),tx=t?targetPosition(t.message_id!):null;return [{event:e,x,targetX:t?Math.max(0,tx??0):null,offRibbon:!!t&&tx===null}];}),[events,position,targetPosition]);
 const byID=useMemo(()=>new Map(placed.map(p=>[p.event.id,p])),[placed]);
 const layout=useMemo(()=>size.width?layoutEventArcs(placed.map((p):LayoutEvent=>({id:p.event.id,x:p.x*size.width,targetKey:p.targetX===null?null:linkedTarget(p.event)!.message_id!,targetX:p.targetX===null?null:p.targetX*size.width,weak:p.targetX!==null&&weakTarget(linkedTarget(p.event)!)})),{width:size.width,height:size.height,marker}):null,[placed,size.width,size.height,marker]);
 if(!placed.length)return null;
 const focusArc=current?.target??null,focused=(arc:string|null,id?:string)=>!current||(id!==undefined&&current.event===id)||(arc!==null&&arc===focusArc);
 const show=(target:Element,list:AgentEventLink[],x:number,arc:string|null,id:string|null)=>{setEventLinkFocus({event:id,target:arc,message:id?list[0]?.message_id??null:null});inspector.show(target,<EventLinkCard events={list}/>,x);};
 const leave=()=>{setEventLinkFocus(null);inspector.leave();};
 const pin=(event:React.MouseEvent,list:AgentEventLink[])=>{event.preventDefault();event.stopPropagation();inspector.pin(event.currentTarget,<EventLinkCard events={list} href={href} close={inspector.close}/>,event.clientX);};
 return <svg ref={box} className={`event-link-overlay${current?' has-focus':''}`} width={size.width} height={size.height} role="group" aria-label={label} onPointerLeave={leave}>
  {layout?<>
   {layout.arcs.map(arc=>{const on=focused(arc.key),strong=current&&on;const offRibbon=arc.events.some(id=>byID.get(id)?.offRibbon);return <g key={arc.key} className={`event-link-arc${strong?' is-focus':''}${on?'':' is-dim'}`}>
    <path className="event-link-halo" d={arc.path}/>
    <path className="event-link-path" d={arc.path} strokeDasharray={arc.weak?'3 2':undefined}/>
    {/* The target's anchor: a tick on the axis, or an arrow at the edge for a target left of the ribbon. */}
    <path className="event-link-anchor" d={offRibbon?`M${arc.x0+1} ${layout.axis-3}L${arc.x0+4} ${layout.axis}L${arc.x0+1} ${layout.axis+3}`:`M${arc.x0} ${layout.axis-arc.side*3.5}V${layout.axis+arc.side*1.5}`}/>
    <rect className="event-link-hit" x={arc.x0-5} y={layout.axis-6} width={10} height={12} onPointerEnter={event=>show(event.currentTarget,arc.events.map(id=>byID.get(id)!.event),event.clientX,arc.key,null)} onClick={event=>pin(event,arc.events.map(id=>byID.get(id)!.event))}/>
   </g>;})}
   {layout.markers.map(m=>{const p=byID.get(m.id)!,e=p.event,on=focused(m.arc,e.id),r=m.arc?marker:marker*.72;return <g key={m.id} data-agent-event={e.id} className={`event-link-marker${m.arc?' is-linked':' is-unlinked'}${on?'':' is-dim'}${current?.event===e.id?' is-focus':''}`}
     onPointerEnter={event=>show(event.currentTarget,[e],event.clientX,m.arc,e.id)} onClick={event=>pin(event,[e])}>
    <path className="event-link-marker-halo" d={glyphPath(eventGroup(e.group).shape,m.x,m.y,r)}/>
    <path d={glyphPath(eventGroup(e.group).shape,m.x,m.y,r)} fill={m.arc?eventColor(e.group):'var(--event-halo)'} stroke={eventColor(e.group)} strokeWidth={m.arc?0:1.2}/>
    <rect className="event-link-hit" x={m.x-r-2} y={m.y-r-2} width={2*r+4} height={2*r+4}/>
   </g>;})}
  </>:null}
 </svg>;
}
