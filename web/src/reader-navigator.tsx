import React,{useEffect,useId,useMemo,useRef,useState} from 'react';
import {createPortal} from 'react-dom';
import {createRoot} from 'react-dom/client';
import {RibbonNodeDetails,SessionRibbon} from './label-session-ribbon';
import {sessionRibbonTree,agentEventClock,ribbonEventCount,type RibbonNode} from './label-session-ribbon-model';
import {useRibbonInspector} from './label-ribbon-inspector';
import {timeScale,type IdleGap} from './timeline-idle-model';
import {mergeAgentEvents} from './event-merge';
import {TimelineLegend,timelineLegendCounts,timelineLegendDomains,timelineToolCategories} from './timeline-legend';
import {timelineEventVisible,timelineSectionVisible,timelineValueVisible,updateTimelineVisibility,visibleTimelineTree,type TimelineVisibility} from './timeline-visibility';
import {useViewEntry,useViewState} from './view-state';
import {ViewDisclosure} from './view-disclosure';
import {availableReaderNavigators,readerNavigatorClock,readerNavigatorScale,nearestReaderMessage,type ReaderNavigatorData} from './reader-navigator-model';
import {trackReaderViewport} from './reader-navigator-layout';
import {humanEventDimensions,humanEventGroups,humanEventLayout,humanEventLetter,timelineRowExpands,type HumanEvent,type HumanEventGroup,type HumanEventSlot} from './human-events-model';
import {HumanEventMarkers} from './human-events-rows';
import {Icon} from './icons';
import './reader-navigator.css';

type ReaderHit={track:Element;x:number;y:number};
type ReaderRibbonProps={node:RibbonNode;sessionID:string;range:ReturnType<typeof readerNavigatorClock>['range'];scale:ReturnType<typeof timeScale>;eventLayer:NonNullable<React.ComponentProps<typeof SessionRibbon>['eventLayer']>;visibility:TimelineVisibility;jump:(fraction:number,hit?:ReaderHit)=>void;timed:boolean;depth?:number;extra?:React.ReactNode};
const chevron=<svg className="reader-section-chevron" viewBox="0 0 12 12" aria-hidden="true"><path d="m4 2 4 4-4 4"/></svg>;
function ReaderRibbonSection({node,sessionID,range,scale,eventLayer,visibility,jump,timed,depth=0,extra}:ReaderRibbonProps){
  const viewKey=`reader-timeline:${sessionID}:${node.id}`,[open]=useViewEntry('disclosures',viewKey,false);
  const inspector=useRibbonInspector({interactive:false});
  const ownEvents=new Set(node.items.flatMap(item=>item.event?[item.event.id]:[]));
  const layer=node.id==='session'?{...eventLayer,events:mergeAgentEvents(eventLayer.events)}:node.id==='events'||node.id.startsWith('events:')?{...eventLayer,events:eventLayer.events.filter(event=>ownEvents.has(event.id))}:undefined;
  const ribbon=<span className="reader-section-track" onClick={event=>{event.preventDefault();event.stopPropagation();const track=event.currentTarget,rect=track.getBoundingClientRect();jump((event.clientX-rect.left)/rect.width,{track,x:event.clientX,y:event.clientY});}}><SessionRibbon node={node} visibility={visibility} range={range} clock={scale} brushHandlers={{}} preview={null} inspector={inspector} disabled hover showUnknownTail={timed} eventLayer={layer}/></span>;
  const label=<span className="reader-section-label" style={{paddingLeft:Math.min(depth,6)*10}}>{node.id==='session'?<><strong>{node.title}</strong><small>{ribbonEventCount(node.items)}</small></>:<RibbonNodeDetails node={node}/>} {node.children.length?chevron:null}</span>;
  if(!node.children.length&&!extra)return <div className="reader-section-leaf">{label}{ribbon}</div>;
  return <ViewDisclosure viewKey={viewKey} className="reader-section-node" summary={<>{label}{ribbon}</>}>{open?node.children.map(child=><ReaderRibbonSection key={child.id} node={child} sessionID={sessionID} range={range} scale={scale} eventLayer={eventLayer} visibility={visibility} jump={jump} timed={timed} depth={depth+1}/>):null}{open?extra:null}</ViewDisclosure>;
}

const HUMAN_ROW_HEIGHT=28;
/** Human events: a marker per engagement annotation in each of the person's
 * messages, placed in that message's slot by where the annotation starts.
 * Like Agent events, the row folds out into one row per kind (here,
 * dimension); a marker keeps its x on both. */
function HumanEventsRows({sessionID,groups,slots,trackPx,jump,jumpAt,depth=0}:{sessionID:string;groups:HumanEventGroup[];slots:Map<string,HumanEventSlot>;trackPx:number;jump:(event:HumanEvent)=>void;jumpAt:(fraction:number)=>void;depth?:number}){
  const viewKey=`reader-timeline:${sessionID}:human`,[open]=useViewEntry('disclosures',viewKey,false);
  const inspector=useRibbonInspector({interactive:false});
  const events=groups.flatMap(group=>group.events),dimensions=humanEventDimensions(events);
  const markers=(row:HumanEvent[])=><HumanEventMarkers events={row} slots={slots} trackPx={trackPx} height={HUMAN_ROW_HEIGHT} inspector={inspector} jump={jump}/>;
  // Between markers a track answers as the rows above it do: reveal the message nearest that time, and leave the row's fold alone.
  const reveal=(event:React.MouseEvent<HTMLElement>)=>{event.preventDefault();event.stopPropagation();const rect=event.currentTarget.getBoundingClientRect();if(rect.width)jumpAt((event.clientX-rect.left)/rect.width);};
  const label=(content:React.ReactNode,level:number)=><span className="reader-section-label" style={{paddingLeft:Math.min(level,6)*10}}>{content}</span>;
  const summary=<>{label(<><Icon name="human"/><strong>Human events</strong><small>{events.length}</small>{chevron}</>,depth)}
    <span className="reader-section-track reader-human-track" role="group" aria-label="Human events. Click a marker to reveal its text." onClick={reveal}>{markers(events)}</span></>;
  return <><ViewDisclosure viewKey={viewKey} className="reader-section-node reader-human-events" summary={summary}>
    {open?dimensions.map(dimension=><div key={dimension.id} className="reader-section-leaf reader-human-dimension">
      {label(<><i className={`eng-glyph ${dimension.className}`}>{humanEventLetter(dimension.name)}</i><strong>{dimension.name}</strong><small>{dimension.events.length}</small></>,depth+1)}
      <span className="reader-section-track reader-human-track" role="group" aria-label={`Human events · ${dimension.name}`} onClick={reveal}>{markers(dimension.events)}</span>
    </div>):null}
  </ViewDisclosure>{inspector.portal}</>;
}
/** The hover card of a turn on the Turns row: what the turn's header says. */
function TurnCard({card}:{card:NonNullable<ReaderNavigatorData['turns'][number]['card']>}){
  return <div className="event-link-cards"><div className="event-link-card reader-turn-card">
    <div className="event-link-card-head"><strong>{card.title}</strong>{card.when?<small>{card.when}</small>:null}</div>
    <blockquote>{card.prompt}</blockquote>
    <small>{[...card.stats,card.tokens].filter(Boolean).join(' · ')}</small>
  </div></div>;
}

function ReaderNavigator({panel,data,keyHost}:{panel:HTMLElement;data:ReaderNavigatorData;keyHost:HTMLElement}){
  const [visibility,setVisibility]=useViewState<TimelineVisibility>('timeline_visibility',{});
  const [skipIdle]=useViewState<boolean>('timeline_idle',true);
  const [singleSession,setSingleSession]=useState(()=>panel.parentElement?.dataset.readerSingleSession==='true');
  useEffect(()=>{const reader=panel.parentElement;if(!reader)return;const update=()=>setSingleSession(reader.dataset.readerSingleSession==='true');reader.addEventListener('pharos:reader-sessions',update);update();return()=>reader.removeEventListener('pharos:reader-sessions',update);},[panel]);
  const [workspaceGaps,setWorkspaceGaps]=useState<IdleGap[]|undefined>(()=>(panel as HTMLElement&{pharosReaderIdleGaps?:IdleGap[]}).pharosReaderIdleGaps);
  useEffect(()=>{const update=()=>setWorkspaceGaps((panel as HTMLElement&{pharosReaderIdleGaps?:IdleGap[]}).pharosReaderIdleGaps);panel.addEventListener('pharos:reader-idle',update);update();return()=>panel.removeEventListener('pharos:reader-idle',update);},[panel]);
  const host=useRef<HTMLElement>(null),inspector=useRibbonInspector({interactive:false}),turnInspector=useRibbonInspector({interactive:false});
  const clock=useMemo(()=>readerNavigatorClock(data.messages,data.marks),[data]);
  const baseScale=useMemo(()=>readerNavigatorScale(clock,data.callerIDs,skipIdle,workspaceGaps),[clock,data.callerIDs,skipIdle,workspaceGaps]);
  const [trackPx,setTrackPx]=useState(640);
  // The Timeline key's Human events chips choose the dimensions drawn here; the Highlights key's Engagement chips choose the underlines.
  const humanKeys=useMemo(()=>humanEventDimensions((data.humanEvents??[]).filter(event=>clock.byID.has(event.message_id))).map(({id,name,className})=>({id,name,className})),[data,clock]);
  const humanGroups=useMemo(()=>humanEventGroups(data.humanEvents??[],id=>clock.byID.get(id)?.[0],dimension=>timelineSectionVisible(visibility,'human')&&timelineValueVisible(visibility,'human',dimension)),[data,clock,visibility]);
  const humanLayout=useMemo(()=>humanEventLayout(baseScale,humanGroups,trackPx),[baseScale,humanGroups,trackPx]),scale=humanLayout.scale;
  const ribbonMessages=useMemo(()=>[...new Map(clock.messages.filter(message=>['message','reasoning','thinking'].includes(message.kind)).map(message=>[message.id,message])).values()],[clock]);
  const tree=useMemo(()=>sessionRibbonTree(ribbonMessages,clock.marks,[],data.taxonomy,clock.range,true,false,clock.messages,clock.marks,data.events),[data,clock,ribbonMessages]);
  const visibleTree=useMemo(()=>tree?visibleTimelineTree(tree,visibility):null,[tree,visibility]);
  const contexts=useMemo(()=>{const nextByStream=new Map<string,number>();return data.contexts.map(entry=>({...entry,end:0})).reverse().map(entry=>{const start=clock.byID.get(entry.message.id)?.[0]??clock.range[0],end=nextByStream.get(entry.sample.stream)??clock.range[1];nextByStream.set(entry.sample.stream,start);return {...entry,end:Math.max(start,end)};}).reverse();},[data,clock]);
  const eventClock=useMemo(()=>agentEventClock(clock.messages,clock.marks),[clock]);
  const timelineEvents=useMemo(()=>data.events.filter(event=>eventClock.at(event)!==null),[data,eventClock]);
  const eventLayer=useMemo(()=>({events:timelineEvents.filter(event=>timelineEventVisible(visibility,event)),position:(event:ReaderNavigatorData['events'][number])=>{const at=eventClock.at(event);return at===null?null:scale.fraction(at);},targetPosition:(event:ReaderNavigatorData['events'][number])=>{const at=eventClock.targetAt(event);return at===null?null:scale.fraction(at);}}),[timelineEvents,eventClock,scale,visibility]);
  const tools=Object.fromEntries([...new Set(clock.marks.filter(mark=>!mark.error).map(mark=>mark.category))].map(id=>[id,timelineToolCategories[id]??id]));
  const timelineVisible=timelineLegendCounts(visibility,timelineLegendDomains(ribbonMessages,[],data.taxonomy,tools,clock.marks.some(mark=>mark.error),timelineEvents,humanKeys)).visible>0;
  const unknownTools=clock.timed?[...new Set(clock.marks.filter(mark=>!mark.end_known).map(mark=>mark.error?'error':mark.category))]:[];
  const at=(id:string)=>clock.byID.get(id);
  const position=(start:number,end:number)=>({left:`min(${scale.fraction(start)*100}%,calc(100% - 3px))`,width:`${Math.max(0,scale.fraction(end)-scale.fraction(start))*100}%`});
  const jump=(fraction:number,hit?:ReaderHit)=>{
    // A dot can straddle its timestamp by several screen pixels. Prefer the
    // actual mark under the pointer before falling back to the timeline time.
    let selected:string|undefined,distance=5;
    if(hit)for(const mark of hit.track.querySelectorAll('[data-tool-id] > path,[data-agent-event]')){
      const rect=mark.getBoundingClientRect(),dx=Math.max(rect.left-hit.x,0,hit.x-rect.right),dy=Math.max(rect.top-hit.y,0,hit.y-rect.bottom),next=Math.hypot(dx,dy);
      if(next>=distance)continue;
      const tool=mark.parentElement?.getAttribute('data-tool-id'),event=mark.getAttribute('data-agent-event');
      const id=tool?clock.marks.find(item=>item.tool_id===tool)?.message_id:data.events.find(item=>item.id===event)?.message_id;
      if(id){selected=id;distance=next;}
    }
    const id=selected??nearestReaderMessage(clock.byID,scale.time(Math.max(0,Math.min(1,fraction))));if(id)data.jumpMessage(id);
  };
  const available=availableReaderNavigators(data,clock.byID),hasContext=available.includes('context'),hasTurns=available.includes('turns'),hasBars=timelineVisible||hasContext||hasTurns,contextNote=useId();
  const sessionKey=`reader-timeline:${data.sessionID}:session`,[sessionOpen]=useViewEntry('disclosures',sessionKey,false);
  const expandable=singleSession&&timelineRowExpands(visibleTree?.children.length??0,humanGroups.length);
  const humanRow=humanGroups.length?<HumanEventsRows sessionID={data.sessionID} groups={humanGroups} slots={humanLayout.slots} trackPx={trackPx} jump={event=>data.jumpHumanEvent?data.jumpHumanEvent(event):data.jumpMessage(event.message_id)} jumpAt={fraction=>jump(fraction)} depth={singleSession?0:1}/>:null;
  const timelineLabel=expandable?<span className="reader-section-label"><strong>Timeline</strong><small>{ribbonEventCount(visibleTree!.items)}</small>{chevron}</span>:<span className="reader-navigator-label">Timeline</span>;
  const timelineTrack=<div className="reader-navigator-track reader-navigator-timeline" role="group" aria-label="Session timeline. Click to reveal a message." onClick={event=>{if(expandable){event.preventDefault();event.stopPropagation();}const track=event.currentTarget,rect=track.getBoundingClientRect();jump((event.clientX-rect.left)/rect.width,{track,x:event.clientX,y:event.clientY});}}>
    <SessionRibbon node={visibleTree!} visibility={visibility} range={clock.range} clock={scale} brushHandlers={{}} preview={null} inspector={inspector} disabled showUnknownTail={clock.timed} eventLayer={{...eventLayer,events:mergeAgentEvents(eventLayer.events)}}/>
    <button type="button" className="reader-navigator-keyboard" aria-label="Browse session timeline" title="Left / right: move through the session · Home / End: first / last message" onFocus={event=>{const band=host.current?.querySelector<HTMLElement>('.reader-navigator-window');event.currentTarget.dataset.position=band?.dataset.position||'0';}} onClick={event=>{event.stopPropagation();jump(Number(event.currentTarget.dataset.position??0));}} onKeyDown={event=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(event.key))return;event.preventDefault();const button=event.currentTarget,previous=Number(button.dataset.position??0),next=event.key==='Home'?0:event.key==='End'?1:Math.max(0,Math.min(1,previous+(event.key==='ArrowLeft'?-.02:.02)));button.dataset.position=String(next);jump(next);}}/>
  </div>;
  useEffect(()=>{if(host.current&&hasBars)return trackReaderViewport(panel,host.current,data,clock.byID,scale);},[panel,data,clock,scale,hasBars]);
  useEffect(()=>{const track=host.current?.querySelector<HTMLElement>('.reader-navigator-window-track');if(!track||typeof ResizeObserver==='undefined')return;const measure=()=>{const width=Math.round(track.clientWidth/8)*8;if(width>0)setTrackPx(width);};const observer=new ResizeObserver(measure);observer.observe(track);measure();return()=>observer.disconnect();},[hasBars]);
  return <><aside ref={host} className="reader-navigator" aria-label="Session navigator" data-range={clock.range.join(',')}>
    {hasBars?<>
      <div className="reader-navigator-bars">
        {timelineVisible?(expandable?<ViewDisclosure className="reader-navigator-timeline-disclosure" viewKey={sessionKey} summary={<>{timelineLabel}{timelineTrack}</>}>
          {sessionOpen?visibleTree!.children.map(child=><ReaderRibbonSection key={child.id} node={child} sessionID={data.sessionID} range={clock.range} scale={scale} eventLayer={eventLayer} visibility={visibility} jump={jump} timed={clock.timed}/>):null}{sessionOpen?humanRow:null}
        </ViewDisclosure>:<div className="reader-navigator-row">{timelineLabel}{timelineTrack}</div>):null}
        {timelineVisible&&!singleSession&&visibleTree&&(visibleTree.children.length||humanRow)?<ReaderRibbonSection node={visibleTree} sessionID={data.sessionID} range={clock.range} scale={scale} eventLayer={eventLayer} visibility={visibility} jump={jump} timed={clock.timed} extra={humanRow}/>:null}
        {hasContext||hasTurns?<div className={`reader-navigator-row${hasContext?'':' is-turns'}`}>
          <span className="reader-navigator-label">{hasContext?'Turns · context':'Turns'}</span>
          <div className={`reader-navigator-track reader-navigator-turns${hasContext?' reader-navigator-context':''}`} role="group" aria-label={hasContext?'Conversation turns and context growth':'Conversation turns'} aria-describedby={hasContext?contextNote:undefined}>
            {hasContext?contexts.map(entry=>{const time=at(entry.message.id);if(!time)return null;return <button key={JSON.stringify([entry.message.id,entry.sample.stream])} type="button" className={`token-marker${entry.sample.compact?' compaction-marker':''}`} style={{...position(time[0],entry.end),'--context-profile':entry.sample.compact?'none':`polygon(0 ${100-entry.from}%,100% ${100-entry.width}%,100% 100%,0 100%)`,'--token-color':entry.color} as React.CSSProperties} title={entry.title} onClick={()=>data.jumpContext(entry)}/>;}):null}
            {data.turns.map((turn,index)=>{const spans=turn.messages.flatMap(id=>{const span=at(id);return span?[span]:[];});if(!spans.length)return null;const start=Math.min(...spans.map(span=>span[0])),end=Math.max(...spans.map(span=>span[1]));return <button type="button" className="reader-navigator-turn" key={turn.messages[0]??index} style={position(start,end)} title={turn.card?undefined:`Turn ${index+1} · ${turn.label}`} aria-label={`Turn ${index+1} · ${turn.label}`} onPointerEnter={turn.card?event=>turnInspector.show(event.currentTarget,<TurnCard card={turn.card!}/>,event.clientX):undefined} onPointerLeave={turnInspector.leave} onClick={()=>{turnInspector.close();const id=turn.messages.find(id=>at(id));if(id)data.jumpMessage(id);}}><span>{index+1}</span></button>;})}
          </div>
        </div>:null}
        <div className="reader-navigator-window-track" aria-hidden="true"><span className="reader-navigator-window" hidden/><i className="reader-navigator-boundary" hidden/><i className="reader-navigator-boundary" hidden/></div>
      </div>
      {/* Shown over the transcript while the context bar is hovered. */}
      {hasContext?<small id={contextNote} className="reader-navigator-context-note">Height = context window in use (largest observed context when unknown) · context-colored line = compaction · blue line and number = turn</small>:null}
      {!clock.timed?<small className="reader-navigator-note">Message order · timestamps unavailable</small>:null}
    </>:<span className="reader-navigator-window" hidden/>}
  </aside>{turnInspector.portal}{createPortal(<div role="group" aria-label="Timeline key"><TimelineLegend embedded data-view-field="timeline_visibility" messages={ribbonMessages} origins={[]} taxonomy={data.taxonomy} tools={tools} error={clock.marks.some(mark=>mark.error)} unknownTools={unknownTools} events={timelineEvents} human={humanKeys} visibility={visibility} onSelect={(section,values,action)=>{setVisibility(previous=>updateTimelineVisibility(previous,section,values,action));panel.dispatchEvent(new Event('pharos:reader-key-edit'));}}/></div>,keyHost)}</>;
}

declare global{interface Window{pharosReaderNavigator?:{mount:(panel:HTMLElement,data:ReaderNavigatorData)=>()=>void}}}
export function installReaderNavigator(){
  window.pharosReaderNavigator={mount(panel,data){
    const element=document.createElement('div'),keyHost=document.createElement('div');element.className='reader-navigator-host';keyHost.className='transcript-timeline-key';panel.querySelector('.turn-list')!.before(element);panel.querySelector('.transcript-content-key')!.before(keyHost);const root=createRoot(element);root.render(<ReaderNavigator panel={panel} data={data} keyHost={keyHost}/>);return()=>{root.unmount();element.remove();keyHost.remove();};
  }};
  window.dispatchEvent(new Event('pharos:reader-navigator-ready'));
}
