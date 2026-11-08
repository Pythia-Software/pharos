import React,{useEffect,useId,useLayoutEffect,useMemo,useRef,useState} from 'react';
import {Icon,type IconName} from './icons';
import {SessionIdentity,SessionBranches,sessionBranchX as branchX} from './session-identity';
import {ViewDisclosure} from './view-disclosure';
import {useViewEntry} from './view-state';
import {useRibbonInspector,useRibbonPointer,type RibbonHit} from './label-ribbon-inspector';
import {PageLink} from './nested-pages';
import {timelinePreviewText,trimTimelineSnippet} from './timeline-preview';
import {agentEventClock,ribbonLayers,ribbonCounts,ribbonEventCount,ribbonPointerHits,ribbonToolPlacement,ribbonOriginParts,sessionRibbonTree,type ActivityMark,type RibbonNode,type RibbonObservation} from './label-session-ribbon-model';
import {shortLabel,specificLabels,stateColor,type LabelMessage,type Taxonomy} from './conversation-labels';
import type {TimeRange,TimelineSessionBranch} from './timeline-label-model';
import {authorshipSegments,callerSource} from './timeline-caller-model';
import {eventKindCounts,markIds,mergeAgentEvents,type KindCounts} from './event-merge';
import {EventGlyph,EventLinkCard,EventLinkOverlay,eventHitTargets,eventLayout,eventTargetFocus,placeEvents,setEventLinkFocus,useEventLinkFocus} from './event-link-overlay';
import {linkedTarget,type AgentEventLink} from './event-link-layout';
import {timeScale,type TimeScale} from './timeline-idle-model';
import {timelineEventVisible,timelineSectionVisible,visibleTimelineTree,type TimelineVisibility} from './timeline-visibility';

// Agent events draw in each session's summary row, the way they draw in the
// fingerprint (event-link-layout.ts): an event sits in the row of the session
// that said it, its arc runs back to its target's time on that row's axis
// (an arrow at the left edge when the target is before the window), and
// hovering an event bands its message and its target in every row, so a
// subagent's event lines up with the caller's prompt it is about.
type EventLayer={events:AgentEventLink[];position:(e:AgentEventLink)=>number|null;targetPosition:(e:AgentEventLink)=>number|null;href?:(e:AgentEventLink)=>string|null};

/** eventCounts: events per kind across the workspace, the population a merged mark's rarest kind comes from (event-merge.ts). */
type Props={visibility?:TimelineVisibility;scale?:TimeScale;events?:AgentEventLink[];eventCounts?:KindCounts;eventHref?:(e:AgentEventLink)=>string|null;eventLayer?:EventLayer;branch?:TimelineSessionBranch;openSession?:()=>void;current?:boolean;sourceHref?:(item:RibbonObservation)=>string|null;collapsed?:boolean;hideEvents?:boolean;caller?:boolean;boundaryMessages?:LabelMessage[];boundaryMarks?:ActivityMark[];sessionID:string;depth:number;name:string;metadata:string;messages:LabelMessage[];marks:ActivityMark[];inferred:{start:number;end:number}[];taxonomy:Taxonomy;range:TimeRange;detail:boolean;brushHandlers:Pick<React.HTMLAttributes<HTMLDivElement>,"onPointerDown"|"onPointerMove"|"onPointerUp"|"onPointerCancel">;preview:TimeRange|null;foldControl:React.ReactNode;select:(item:RibbonObservation,anchor:HTMLButtonElement)=>void;restore:(item:RibbonObservation,anchor:HTMLButtonElement)=>void};
function SourceContent({item,sourceHref,close,children}:{item:RibbonObservation;sourceHref?:Props['sourceHref'];close?:()=>void;children:React.ReactNode}){
 const href=sourceHref?.(item);
 return href?<PageLink href={href} className="lt-observation-source" onClick={event=>{event.stopPropagation();close?.();}}>{children}<span>Show in conversation →</span></PageLink>:<>{children}</>;
}
function MessagePreview({message}:{message:LabelMessage}){
 const body=trimTimelineSnippet(timelinePreviewText(message.preview??message.text,{input:message.kind==='delegation',unwrap:message.role!=='user'||message.kind!=='message'})),characters=Array.from(body);
 const clipped=characters.length>640||message.preview_truncated===true||message.preview===undefined&&message.truncated&&Array.from(message.text).length>=320;
 const label=message.chunks?specificLabels(message)[0]?.[0]:undefined;
 return <blockquote className="lt-message-quote" style={label?{'--message-label-color':stateColor(label)} as React.CSSProperties:undefined}>{characters.slice(0,640).join('')}{clipped&&!body.endsWith('…')?'…':''}</blockquote>;
}
function ObservationCard({items,sourceHref,eventHref,close}:{items:RibbonObservation[];sourceHref?:Props['sourceHref'];eventHref?:Props['eventHref'];close?:()=>void}){
 const events=items.flatMap(item=>item.event?[item.event]:[]);
 if(events.length&&events.length===items.length)return <EventLinkCard events={events} href={eventHref} close={close}/>;
 const counts=ribbonCounts(items),labels=new Map<string,Set<string>>(),origins=new Map<string,Set<string>>();
 for(const item of items){
  if(item.label){const ids=labels.get(item.label)??new Set<string>();ids.add(item.message!.id);labels.set(item.label,ids);}
  if(item.authorship){const ids=origins.get(item.authorship)??new Set<string>();ids.add(item.message!.id);origins.set(item.authorship,ids);}
 }
 const messages=[...new Map(items.flatMap(item=>item.message?[[item.message.id,item.message] as const]:[])).values()];
 const composition=[...new Map(items.filter(item=>item.composition).map(item=>[JSON.stringify([item.message!.id,item.composition,item.authorship]),item])).values()];
 const tools=[...new Map(items.filter(item=>item.mark).map(item=>[item.id,item])).values()];
 const totals=[counts.messages>1?`${counts.messages} messages`:null,counts.activity>1?`${counts.activity} tool calls`:null,counts.inferred>1?`${counts.inferred} inferred spans`:null].filter(Boolean);
 return <>
  {totals.length?<p>{totals.join(' · ')}</p>:null}
  {messages.length<=1?<div className="lt-observation-tags">
   {[...labels.keys()].slice(0,8).map(id=><span key={id}><i style={{background:stateColor(id)}}/>{id==='?'?'Uncategorized':shortLabel(id)}</span>)}
   {counts.thinking===1?<span>Thinking</span>:null}
   {[...origins.keys()].map(category=><span key={category}><i className={`lt-source-swatch is-${callerSource(category).pattern}`}/>{callerSource(category).name}</span>)}
  </div>:<dl>
   {[...labels].slice(0,8).map(([id,ids])=><React.Fragment key={id}><dt><i style={{background:stateColor(id)}}/>{id==='?'?'Uncategorized':shortLabel(id)}</dt><dd>{ids.size}</dd></React.Fragment>)}
   {[...origins].map(([category,ids])=><React.Fragment key={category}><dt>{callerSource(category).name}</dt><dd>{ids.size}</dd></React.Fragment>)}
  </dl>}
  {composition.length?composition.slice(0,4).map(part=><SourceContent key={part.id} item={part} sourceHref={sourceHref} close={close}>{part.excerpt?<blockquote className="lt-message-quote" style={part.label&&part.label!=='?'?{'--message-label-color':stateColor(part.label)} as React.CSSProperties:undefined}>{composition.length>1?<strong>{callerSource(part.authorship!).name}</strong>:null}{trimTimelineSnippet(part.excerpt.text)}{part.excerpt.truncated?'…':''}</blockquote>:<small>This part is outside the retained preview.</small>}</SourceContent>):(sourceHref?messages.slice(0,4):messages.length===1?messages:[]).map(message=><SourceContent key={message.id} item={items.find(item=>item.message?.id===message.id)!} sourceHref={sourceHref} close={close}><MessagePreview message={message}/></SourceContent>)}
  {tools.slice(0,4).map(item=>{
   const mark=item.mark!,elapsed=mark.end_known?mark.end-mark.start:null;
   return <SourceContent key={item.id} item={item} sourceHref={sourceHref} close={close}><div className="lt-tool-preview">
    <div className="lt-observation-tags"><span><i className="lt-tool-swatch" style={{color:item.color}}/>{mark.name}</span><span className={mark.error?'is-error':'lt-tool-meta'}>{elapsed===null?'End not recorded':elapsed<1000?`${Math.round(elapsed)}ms`:`${(elapsed/1000).toLocaleString(undefined,{maximumFractionDigits:2})}s`}{mark.error?' · Error':''}</span></div>
    {mark.preview?<pre>{timelinePreviewText(mark.preview,{input:true})}{mark.preview_truncated?'…':''}</pre>:<small>Arguments not retained</small>}
   </div></SourceContent>;
  })}
  {items.some(item=>item.inferred)?<span>Inferred orchestration</span>:null}
 </>;
}
/** While an event is hovered anywhere on the page: its message and its target's message, outlined in every row that has them. */
function EventFocusBand({node,x}:{node:RibbonNode;x:(at:number)=>number}){
 const focus=useEventLinkFocus();
 const spans=useMemo(()=>{const spans=new Map<string,[number,number]>();for(const item of node.items)if(item.message){const s=spans.get(item.message.id);spans.set(item.message.id,s?[Math.min(s[0],item.start),Math.max(s[1],item.end)]:[item.start,item.end]);}return spans;},[node.items]);
 if(!focus)return null;
 return <g pointerEvents="none">{[[focus.target,'is-target'],[focus.message,'is-observation']].flatMap(([id,kind])=>{const span=id?spans.get(id):undefined;if(!span)return [];const left=x(span[0]),right=Math.max(left+4,x(span[1]));return [<rect key={kind} className={`event-link-band ${kind}`} x={left-2} y={1} width={right-left+4} height={30} vectorEffect="non-scaling-stroke"/>];})}</g>;
}
export function SessionRibbon({node,range,clock,brushHandlers,preview,caller,inspector,sourceHref,disabled=false,eventLayer,visibility}:{visibility?:TimelineVisibility;clock:TimeScale;eventLayer?:EventLayer;node:RibbonNode;range:TimeRange;brushHandlers:Props["brushHandlers"];preview:TimeRange|null;caller?:boolean;inspector:ReturnType<typeof useRibbonInspector>;sourceHref?:Props["sourceHref"];disabled?:boolean}){
 const pattern=useId().replace(/:/g,''),container=useRef<HTMLDivElement>(null);
 const [scale,setScale]=useState({x:1,y:1});
 useEffect(()=>{inspector.close();},[range[0],range[1],node.id,visibility]);
 // Counter the SVG's nonuniform stretch so pattern cells stay in CSS pixels,
 // just as vector-effect keeps tool markers independent of the row's height.
 useLayoutEffect(()=>{
  const element=container.current;if(!element)return;
  const resize=()=>{const rect=element.getBoundingClientRect();if(!rect.width||!rect.height)return;const x=1000/rect.width,y=32/rect.height;setScale(previous=>previous.x===x&&previous.y===y?previous:{x,y});};
  resize();const observer=new ResizeObserver(resize);observer.observe(element);return()=>observer.disconnect();
 },[]);
 const width=1000/scale.x,height=32/scale.y;
 const drawn=useMemo(()=>node.items.filter(item=>!item.event),[node.items]);
 const items=useMemo(()=>{const labeled=new Set(drawn.filter(item=>item.label&&!item.authorship).map(item=>item.message?.id));return ribbonLayers(drawn.filter(item=>!item.authorship||!labeled.has(item.message?.id)));},[drawn]);
 const inspectItems=useMemo(()=>{if(!caller)return drawn;const composed=new Set(drawn.filter(item=>item.composition).map(item=>item.message?.id));return drawn.flatMap(item=>item.message&&!item.composition&&!composed.has(item.message.id)?[item,...ribbonOriginParts(item)]:[item]);},[caller,drawn]);
 // Marks in the row's pixels: agent events (with their arcs) first, then
 // tool dots in the lanes they leave free (ribbon-marks.ts). Both take the
 // same hit test, hover and pin below, alongside the message spans.
 const placedEvents=useMemo(()=>eventLayer?placeEvents(eventLayer.events,eventLayer.position,eventLayer.targetPosition):[],[eventLayer]);
 const layout=useMemo(()=>eventLayout(placedEvents,width,height),[placedEvents,width,height]);
 const eventItems=useMemo(()=>{const own=new Map(node.items.flatMap(item=>item.event?[[item.event.id,item] as const]:[]));return new Map(placedEvents.map(p=>[p.event.id,{...own.get(p.event.id)??{id:JSON.stringify(['event',p.event.id]),start:0,end:0,title:p.event.label,color:'',thinking:p.event.source==='thinking'},event:p.event}]));},[node.items,placedEvents]);
 const px=(at:number)=>Math.max(0,Math.min(width,clock.fraction(at)*width));
 const tools=useMemo(()=>ribbonToolPlacement(inspectItems,px,height,layout?.markers??[]),[inspectItems,clock,width,height,layout]);
 const targets=useMemo(()=>[...[...tools.values()].map(m=>({...m,ids:[m.id],event:null as string|null,arc:null as string|null,events:[] as string[]})),...eventHitTargets(layout).map(t=>({...t,ids:t.events.map(id=>eventItems.get(id)!.id)}))],[tools,layout,eventItems]);
 const hitItems=useMemo(()=>[...inspectItems,...eventItems.values()],[inspectItems,eventItems]);
 const events=useMemo(()=>new Map(placedEvents.map(p=>[p.event.id,p.event])),[placedEvents]);
 const x=(at:number)=>Math.max(0,Math.min(1000,clock.fraction(at)*1000)),patternTransform=`scale(${scale.x} ${scale.y})`;
 const typed=new Map<string,{left:number;width:number;y:number;height:number}>();
 const glyphs=items.map(({item,height,y})=>{
  const instantPart=item.end===item.start?item.composition:undefined;
  const width=instantPart?2*(instantPart.end-instantPart.start):item.authorship?x(item.end)-x(item.start):Math.max(2,x(item.end)-x(item.start)),left=instantPart?Math.min(998,x(item.start))+2*instantPart.start:Math.min(998,x(item.start));
  // A round zero-length stroke remains circular in screen space.
  if(item.mark){
   const m=tools.get(item.id);if(!m)return null;const cx=m.x*scale.x,cy=m.y*scale.y;
   return <g key={item.id} data-tool-id={item.mark.tool_id}>{item.mark.end_known&&item.end>item.start?<line x1={cx} x2={m.x1*scale.x} y1={cy} y2={cy} stroke={item.color} strokeWidth={1} vectorEffect="non-scaling-stroke"/>:null}{item.start>=range[0]&&item.start<=range[1]?<path d={`M${cx} ${cy}h0`} fill="none" stroke={item.color} strokeWidth={2*m.r} strokeLinecap="round" vectorEffect="non-scaling-stroke"/>:null}</g>;
  }
  const parts=caller&&item.message?item.authorship?[{category:item.authorship,start:0,end:1}]:authorshipSegments(item.message):[];
  return <g key={item.id}><rect x={left} y={y} width={width} height={height} fill={item.color} opacity={item.inferred ? .3 : 1}/>{parts.map((part,index)=>{
   const source=callerSource(part.category),start=item.start+(item.end-item.start)*part.start,end=item.start+(item.end-item.start)*part.end;
   if(start>range[1]||end<range[0])return null;
   const partLeft=item.end===item.start?left+width*part.start:x(start),partWidth=item.end===item.start?width*(part.end-part.start):x(end)-x(start);
   if(part.category==='typed'&&partWidth>0){
    const key=JSON.stringify([item.message!.id,item.composition??{start:part.start,end:part.end}]);
    if(!typed.has(key)||typed.get(key)!.height<height)typed.set(key,{left:partLeft,width:partWidth,y,height});
   }
   return source.pattern==='solid'?null:<rect key={index} x={partLeft} y={y} width={partWidth} height={height} fill={`url(#${pattern}-${source.pattern})`}/>;
  })}</g>;
 });
 const card=(hits:RibbonObservation[])=>(pinned:boolean)=>pinned?<ObservationCard items={hits} sourceHref={sourceHref} eventHref={eventLayer?.href} close={inspector.close}/>:<ObservationCard items={hits}/>;
 const eventHover=(e:AgentEventLink)=>()=>setEventLinkFocus({event:e.id,target:linkedTarget(e)?.message_id??null,message:e.message_id,...(e.members?{events:markIds(e)}:{})});
 const unhover=()=>setEventLinkFocus(null);
 const candidates=useMemo(()=>[...new Map(hitItems.map(item=>[item.mark||item.event?item.id:JSON.stringify([item.message?.id,item.composition]),item])).values()].sort((a,b)=>a.start-b.start||a.id.localeCompare(b.id)).map((item):RibbonHit=>({card:card([item]),hover:item.event?eventHover(item.event):unhover})),[hitItems,sourceHref,eventLayer?.href]);
 const hitAt=(event:React.MouseEvent<Element>):RibbonHit|null=>{
  const rect=event.currentTarget.getBoundingClientRect(),f=(event.clientX-rect.left)/rect.width,step=5/rect.width,at=clock.time(f),tolerance=(clock.time(Math.min(1,f+step))-clock.time(Math.max(0,f-step)))/2;
  const {items:hits,target}=ribbonPointerHits(hitItems,at,tolerance,{x:(event.clientX-rect.left)/rect.width*width,y:(event.clientY-rect.top)/rect.height*height},targets);
  if(!hits.length)return null;
  return {card:card(hits),hover:target&&(target.event||target.arc)?()=>setEventLinkFocus(eventTargetFocus(target,events)):unhover};
 };
 const interactive=!disabled&&!!sourceHref&&node.children.length>0;
 const pointer=useRibbonPointer({inspector,hitAt,candidates,pinnable:!!sourceHref,disabled,suspended:!!preview,unhover});
 return <div ref={container} className={`lt-session-ribbon ${node.id==='session'?'is-session':'is-detail'}`} {...(disabled?{}:brushHandlers)} onPointerLeave={inspector.leave}><svg viewBox="0 0 1000 32" preserveAspectRatio="none" role={interactive?'button':'img'} tabIndex={interactive?0:undefined} aria-label={interactive?`Inspect ${node.id==='session'?'Events':node.title} ribbon. Arrow keys browse segments; Enter pins details.`:`${node.title} ribbon`} aria-describedby={inspector.id} {...pointer}>

  <defs>
   <pattern id={pattern+'-diagonal'} width="7" height="7" patternUnits="userSpaceOnUse" patternTransform={patternTransform}><path d="M-2 2 2 -2M0 7 7 0M5 9 9 5" stroke="var(--ink)" strokeWidth="1.3" opacity=".65"/></pattern>
   <pattern id={pattern+'-dots'} width="5" height="5" patternUnits="userSpaceOnUse" patternTransform={patternTransform}><circle cx="2" cy="2" r=".8" fill="var(--ink)" opacity=".6"/></pattern>
   <pattern id={pattern+'-rings'} width="8" height="8" patternUnits="userSpaceOnUse" patternTransform={patternTransform}><circle cx="4" cy="4" r="1.8" fill="none" stroke="var(--ink)" strokeWidth="1" opacity=".65"/></pattern>
   <pattern id={pattern+'-grid'} width="8" height="8" patternUnits="userSpaceOnUse" patternTransform={patternTransform}><path d="M0 8V0H8" fill="none" stroke="var(--ink)" strokeWidth="1" opacity=".55"/></pattern>
   <pattern id={pattern+'-crosshatch'} width="8" height="8" patternUnits="userSpaceOnUse" patternTransform={patternTransform}><path d="M0 0 8 8M0 8 8 0" fill="none" stroke="var(--ink)" strokeWidth="1" opacity=".55"/></pattern>
   <pattern id={pattern+'-dashes'} width="8" height="6" patternUnits="userSpaceOnUse" patternTransform={patternTransform}><path d="M2 3H6" stroke="var(--ink)" strokeWidth="1" opacity=".6"/></pattern>
  </defs>
  <rect x="0" y="15" width="1000" height={node.id==='session'?2:.6} fill="var(--muted)" opacity={node.id==='session'?.3:.12}/>
  {glyphs}
  {[...typed].map(([key,rect])=>{const dx=Math.min(scale.x,rect.width/2),dy=Math.min(scale.y,rect.height/2);return <path key={key} className="lt-typed-outline" d={`M${rect.left+dx} ${rect.y+dy}h${Math.max(0,rect.width-2*dx)}v${Math.max(0,rect.height-2*dy)}h${-Math.max(0,rect.width-2*dx)}z`} fill="none" strokeWidth="1.6" vectorEffect="non-scaling-stroke"/>;})}
  {clock.cuts.map(cut=><line key={cut.end} className="lt-idle-cut-line" x1={cut.fraction*1000} x2={cut.fraction*1000} y1="0" y2="32" vectorEffect="non-scaling-stroke"/>)}
  {preview?<rect x={x(preview[0])} y="0" width={x(preview[1])-x(preview[0])} height="32" fill="var(--gold)" opacity=".25"/>:null}
  <EventFocusBand node={node} x={x}/>
 </svg>{eventLayer?.events.length?<EventLinkOverlay placed={placedEvents} layout={layout} width={width} height={height} label={`${placedEvents.reduce((n,p)=>n+(p.event.members?.length??1),0)} agent events`}/>:null}{inspector.portal}</div>;
}
function RecursiveRibbonRow({node,props,level=0}:{node:RibbonNode;props:Props;level?:number}){
 const viewKey=`delegation-labels:${props.sessionID}:${node.id}`,[open]=useViewEntry('disclosures',viewKey,false),counts=ribbonCounts(node.items),root=level===0;
 const inspector=useRibbonInspector();
 const identity=root?<SessionIdentity name={props.name} metadata={props.metadata} depth={props.depth} branch={props.branch} folded={!!props.foldControl} current={props.current} openSession={props.openSession}/>:<span className="lt-session-gutter" aria-hidden="true"><SessionBranches depth={props.depth} branch={props.branch} root={false}/></span>;
 const icons:Record<string,IconName>={activity:'settings',read:'read',write:'edit',run:'command',git:'tree',network:'web',delegate:'tree',wait:'more',poll:'refresh',other:'settings'};
 // An Agent events row draws only its own events (with their arcs).
 const ownEvents=node.id==='events'||node.id.startsWith('events:')?new Set(node.items.flatMap(item=>item.event?[item.event.id]:[])):null;
 // The session row and the Agent events row merge events on near-identical
 // text into one mark (event-merge.ts); the group and kind rows below keep
 // every event, since they exist to decompose.
 const merge=root||node.id==='events',ownKey=ownEvents?[...ownEvents].join(' '):'';
 const eventLayer=useMemo(()=>{
  const layer=root?props.eventLayer:ownEvents&&props.eventLayer?{...props.eventLayer,events:props.eventLayer.events.filter(e=>ownEvents.has(e.id))}:undefined;
  return layer&&merge?{...layer,events:mergeAgentEvents(layer.events,props.eventCounts??eventKindCounts(props.eventLayer!.events))}:layer;
 },[props.eventLayer,props.eventCounts,root,merge,ownKey]);
 const eventGlyph=ownEvents&&node.id!=='events'?node.items[0]?.event:undefined;
 const icon=(node.id==='events'||node.id==='labels:thinking')?'thought':node.id==='activity'?'settings':node.id.startsWith('activity:')?icons[node.items[0]?.mark?.category??'other']:node.id==='labels'?'read':node.id==='authorship'?'human':undefined;
 const count=ribbonEventCount(node.items,ownEvents?'events':'session');
 const label=<span className={`lt-session-row-label${root?' is-root':''}`} style={{paddingLeft:root?8:8+Math.min(level-1,8)*9}} aria-label={root?`${count} events`:undefined}>{root?<strong className="lt-event-count">{count}</strong>:<>{eventGlyph?<EventGlyph group={eventGlyph.group}/>:icon?<Icon name={icon}/>:node.items[0]?.label?<i style={{background:node.items[0].color}}/>:null}<strong>{node.title}</strong><small>{ownEvents?counts.events:counts.messages||counts.activity||counts.inferred}</small></>}{node.children.length?<Icon name="chevron-right" className="lt-session-caret"/>:<span className="lt-session-caret" aria-hidden="true">·</span>}</span>;
 const content=<>{identity}{!props.hideEvents && label}<SessionRibbon visibility={props.visibility} node={node} range={props.range} clock={props.scale??timeScale(props.range)} brushHandlers={props.brushHandlers} preview={props.preview} caller={props.caller&&timelineSectionVisible(props.visibility??{},'origins')} inspector={inspector} sourceHref={props.sourceHref} disabled={props.collapsed} eventLayer={eventLayer}/></>;
 if(props.collapsed)return <div className="lt-session-row lt-session-preview">{content}</div>;
 if(!node.items.length)return <div className={`lt-session-row lt-session-empty ${root?'lt-session-empty-root':''}`}>{identity}{!props.hideEvents && label}<SessionRibbon visibility={props.visibility} node={node} range={props.range} clock={props.scale??timeScale(props.range)} brushHandlers={props.brushHandlers} preview={props.preview} caller={props.caller&&timelineSectionVisible(props.visibility??{},'origins')} inspector={inspector} sourceHref={props.sourceHref} disabled={props.collapsed} eventLayer={eventLayer}/></div>;
 if(!node.children.length)return <div className="lt-session-leaf"><button type="button" data-ribbon-event={node.items[0]?.id} data-view-field="delegation_event" className="lt-session-row" ref={anchor=>{if(anchor)props.restore(node.items[0],anchor);}} style={{'--row-depth':level} as React.CSSProperties} onClick={event=>{if(props.sourceHref)inspector.pin(event.currentTarget,<ObservationCard items={node.items} sourceHref={props.sourceHref} eventHref={props.eventHref} close={inspector.close}/>,undefined,event.detail===0);else props.select(node.items[0],event.currentTarget);}} onFocus={event=>inspector.show(event.currentTarget,<ObservationCard items={node.items}/>)} onBlur={inspector.leave}>{content}</button></div>;
 return <ViewDisclosure viewKey={viewKey} className={`lt-session-node ${root?'lt-session-root':''}`} summary={content}>{open?node.children.map(child=><RecursiveRibbonRow key={child.id} node={child} props={props} level={level+1}/>):null}</ViewDisclosure>;
}
export function RecursiveSessionRibbon(props:Props){
 const fullTree=useMemo(()=>sessionRibbonTree(props.messages,props.marks,props.inferred,props.taxonomy,props.range,props.detail,props.caller,props.boundaryMessages,props.boundaryMarks,props.events),[props.messages,props.marks,props.inferred,props.taxonomy,props.range,props.detail,props.caller,props.boundaryMessages,props.boundaryMarks,props.events]);
 const tree=useMemo(()=>visibleTimelineTree(fullTree,props.visibility??{},props.caller),[fullTree,props.visibility,props.caller]);
 const untimed=props.messages.filter(m=>!m.at||!Number.isFinite(Date.parse(m.at))).length;
 const eventLayer=useMemo(()=>{
  if(!props.events?.length)return undefined;
  // The same clock the tree's event rows use: each event at its sentence's share of its message.
  const times=agentEventClock(props.messages,props.boundaryMarks??props.marks,props.boundaryMessages??props.messages);
  const clock=props.scale??timeScale(props.range),fraction=(at:number|null)=>at===null?null:at<props.range[0]?-1:at>props.range[1]?2:clock.fraction(at);
  return {events:props.events.filter(event=>timelineEventVisible(props.visibility??{},event)),position:(e:AgentEventLink)=>{const f=fraction(times.at(e));return f===null||f<0||f>1?null:f;},targetPosition:(e:AgentEventLink)=>{const f=fraction(times.targetAt(e));return f===null||f<0?null:Math.min(1,f);},href:props.eventHref};
 },[props.visibility,props.events,props.messages,props.boundaryMessages,props.boundaryMarks,props.marks,props.range[0],props.range[1],props.scale,props.eventHref]);
 props={...props,eventLayer};
 return <div className="lt-session-tree">{props.foldControl?<div className="lt-session-fold-anchor"><div className="lt-session-fold" style={{left:branchX(props.depth)-8}}>{props.foldControl}</div></div>:null}<RecursiveRibbonRow node={tree} props={props}/>{untimed?<small className="lt-session-untimed">{untimed} messages without timestamps cannot be placed on this clock.</small>:null}</div>;
}
