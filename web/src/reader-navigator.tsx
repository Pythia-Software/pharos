import React,{useEffect,useId,useMemo,useRef} from 'react';
import {createPortal} from 'react-dom';
import {createRoot} from 'react-dom/client';
import {SessionRibbon} from './label-session-ribbon';
import {sessionRibbonTree,agentEventClock,type RibbonNode} from './label-session-ribbon-model';
import {useRibbonInspector} from './label-ribbon-inspector';
import {timeScale} from './timeline-idle-model';
import {mergeAgentEvents} from './event-merge';
import {TimelineLegend,timelineToolCategories} from './timeline-legend';
import {timelineEventVisible,updateTimelineVisibility,visibleTimelineTree,type TimelineVisibility} from './timeline-visibility';
import {useViewEntry,useViewState} from './view-state';
import {ViewDisclosure} from './view-disclosure';
import {availableReaderNavigators,readerNavigatorClock,nearestReaderMessage,type ReaderNavigatorData} from './reader-navigator-model';
import {trackReaderViewport} from './reader-navigator-layout';
import './reader-navigator.css';

const choices=['timeline','context','turns'] as const;
type Choice=typeof choices[number];
const choiceNames:Record<Choice,string>={timeline:'Timeline',context:'Context',turns:'Turns'};
type ReaderRibbonProps={node:RibbonNode;sessionID:string;range:ReturnType<typeof readerNavigatorClock>['range'];scale:ReturnType<typeof timeScale>;eventLayer:NonNullable<React.ComponentProps<typeof SessionRibbon>['eventLayer']>;visibility:TimelineVisibility;jump:(fraction:number)=>void;timed:boolean;depth?:number};
function ReaderRibbonSection({node,sessionID,range,scale,eventLayer,visibility,jump,timed,depth=0}:ReaderRibbonProps){
  const viewKey=`reader-timeline:${sessionID}:${node.id}`,[open]=useViewEntry('disclosures',viewKey,false);
  const inspector=useRibbonInspector({interactive:false});
  const ownEvents=new Set(node.items.flatMap(item=>item.event?[item.event.id]:[]));
  const layer=node.id==='session'?{...eventLayer,events:mergeAgentEvents(eventLayer.events)}:node.id==='events'||node.id.startsWith('events:')?{...eventLayer,events:eventLayer.events.filter(event=>ownEvents.has(event.id))}:undefined;
  const ribbon=<span className="reader-section-track" onClick={event=>{event.preventDefault();event.stopPropagation();const rect=event.currentTarget.getBoundingClientRect();jump((event.clientX-rect.left)/rect.width);}}><SessionRibbon node={node} visibility={visibility} range={range} clock={scale} brushHandlers={{}} preview={null} inspector={inspector} disabled showUnknownTail={timed} eventLayer={layer}/></span>;
  const label=<span className="reader-section-label" style={{paddingLeft:Math.min(depth,6)*10}}>{node.children.length?<span className="reader-section-chevron" aria-hidden="true">›</span>:<span className="reader-section-chevron" aria-hidden="true">·</span>}<span>{node.title}</span></span>;
  if(!node.children.length)return <div className="reader-section-leaf">{label}{ribbon}</div>;
  return <ViewDisclosure viewKey={viewKey} className="reader-section-node" summary={<>{label}{ribbon}</>}>{open?node.children.map(child=><ReaderRibbonSection key={child.id} node={child} sessionID={sessionID} range={range} scale={scale} eventLayer={eventLayer} visibility={visibility} jump={jump} timed={timed} depth={depth+1}/>):null}</ViewDisclosure>;
}

function ReaderNavigator({panel,data,keyHost}:{panel:HTMLElement;data:ReaderNavigatorData;keyHost:HTMLElement}){
  const [shown,setShown]=useViewState<string[]>('reader_navigator',[...choices]);
  const [visibility,setVisibility]=useViewState<TimelineVisibility>('timeline_visibility',{});
  const host=useRef<HTMLElement>(null),inspector=useRibbonInspector({interactive:false});
  const clock=useMemo(()=>readerNavigatorClock(data.messages,data.marks),[data]);
  const scale=useMemo(()=>timeScale(clock.range),[clock]);
  const ribbonMessages=useMemo(()=>[...new Map(clock.messages.filter(message=>['message','reasoning','thinking'].includes(message.kind)).map(message=>[message.id,message])).values()],[clock]);
  const tree=useMemo(()=>shown.includes('timeline')?sessionRibbonTree(ribbonMessages,clock.marks,[],data.taxonomy,clock.range,true,false,clock.messages,clock.marks,data.events):null,[data,clock,ribbonMessages,shown.includes('timeline')]);
  const visibleTree=useMemo(()=>tree?visibleTimelineTree(tree,visibility):null,[tree,visibility]);
  const contexts=useMemo(()=>{const nextByStream=new Map<string,number>();return data.contexts.map(entry=>({...entry,end:0})).reverse().map(entry=>{const start=clock.byID.get(entry.message.id)?.[0]??clock.range[0],end=nextByStream.get(entry.sample.stream)??clock.range[1];nextByStream.set(entry.sample.stream,start);return {...entry,end:Math.max(start,end)};}).reverse();},[data,clock]);
  const eventClock=useMemo(()=>agentEventClock(clock.messages,clock.marks),[clock]);
  const timelineEvents=useMemo(()=>data.events.filter(event=>eventClock.at(event)!==null),[data,eventClock]);
  const eventLayer=useMemo(()=>({events:timelineEvents.filter(event=>timelineEventVisible(visibility,event)),position:(event:ReaderNavigatorData['events'][number])=>{const at=eventClock.at(event);return at===null?null:scale.fraction(at);},targetPosition:(event:ReaderNavigatorData['events'][number])=>{const at=eventClock.targetAt(event);return at===null?null:scale.fraction(at);}}),[timelineEvents,eventClock,scale,visibility]);
  const tools=Object.fromEntries([...new Set(clock.marks.filter(mark=>!mark.error).map(mark=>mark.category))].map(id=>[id,timelineToolCategories[id]??id]));
  const unknownTools=clock.timed?[...new Set(clock.marks.filter(mark=>!mark.end_known).map(mark=>mark.error?'error':mark.category))]:[];
  const at=(id:string)=>clock.byID.get(id);
  const position=(start:number,end:number)=>({left:`min(${scale.fraction(start)*100}%,calc(100% - 3px))`,width:`${Math.max(0,scale.fraction(end)-scale.fraction(start))*100}%`});
  const jump=(fraction:number)=>{const id=nearestReaderMessage(clock.byID,scale.time(Math.max(0,Math.min(1,fraction))));if(id)data.jumpMessage(id);};
  const available=availableReaderNavigators(data,clock.byID),active=available.filter(choice=>shown.includes(choice)),hidden=available.filter(choice=>!shown.includes(choice)),contextNote=useId();
  // Each bar's own label switches it off; a hidden bar waits at the end of
  // the key with its box unchecked, so the switches take no row of their own.
  const choice=(name:Choice)=><label key={name} className="reader-navigator-choice" title={shown.includes(name)?`Hide the ${name} bar`:`Show the ${name} bar`}><input type="checkbox" data-view-field="reader_navigator" checked={shown.includes(name)} onChange={event=>{const checked=event.currentTarget.checked;setShown(previous=>choices.filter(item=>item===name?checked:previous.includes(item)));}}/>{choiceNames[name]}</label>;
  const hiddenChoices=hidden.length?<span className="reader-navigator-hidden" role="group" aria-label="Hidden session navigators">{hidden.map(choice)}</span>:null;
  useEffect(()=>{if(!host.current)return;if(active.length)return trackReaderViewport(panel,host.current,data,clock.byID);const scroller=panel.closest<HTMLElement>('.workspace-reader-content')!,element=host.current,resize=()=>scroller.style.setProperty('--reader-navigator-height',`${element.offsetHeight}px`),observer=new ResizeObserver(resize);resize();observer.observe(element);return()=>{observer.disconnect();scroller.style.removeProperty('--reader-navigator-height');};},[panel,data,clock,active.join(',')]);
  return <><aside ref={host} className="reader-navigator" aria-label="Session navigator" data-range={clock.range.join(',')}>
    {active.length?<>
      <div className="reader-navigator-bars">
        {shown.includes('timeline')?<div className="reader-navigator-row">{choice('timeline')}<div className="reader-navigator-track reader-navigator-timeline" role="group" aria-label="Session timeline. Click to reveal a message." onClick={event=>{const rect=event.currentTarget.getBoundingClientRect();jump((event.clientX-rect.left)/rect.width);}}>
          <SessionRibbon node={visibleTree!} visibility={visibility} range={clock.range} clock={scale} brushHandlers={{}} preview={null} inspector={inspector} disabled showUnknownTail={clock.timed} eventLayer={{...eventLayer,events:mergeAgentEvents(eventLayer.events)}}/>
          <button type="button" className="reader-navigator-keyboard" aria-label="Browse session timeline" title="Left / right: move through the session · Home / End: first / last message" onFocus={event=>{const band=host.current?.querySelector<HTMLElement>('.reader-navigator-window');event.currentTarget.dataset.position=band?.dataset.position||'0';}} onClick={event=>{event.stopPropagation();jump(Number(event.currentTarget.dataset.position??0));}} onKeyDown={event=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(event.key))return;event.preventDefault();const button=event.currentTarget,previous=Number(button.dataset.position??0),next=event.key==='Home'?0:event.key==='End'?1:Math.max(0,Math.min(1,previous+(event.key==='ArrowLeft'?-.02:.02)));button.dataset.position=String(next);jump(next);}}/>
        </div></div>:null}
        {shown.includes('timeline')&&visibleTree?.children.length?<ReaderRibbonSection node={visibleTree} sessionID={data.sessionID} range={clock.range} scale={scale} eventLayer={eventLayer} visibility={visibility} jump={jump} timed={clock.timed}/>:null}
        {active.includes('context')?<div className="reader-navigator-row">{choice('context')}<div className="reader-navigator-track reader-navigator-context" aria-label="Context growth" aria-describedby={contextNote}>{contexts.map(entry=>{const time=at(entry.message.id);if(!time)return null;return <button key={JSON.stringify([entry.message.id,entry.sample.stream])} type="button" className={`token-marker${entry.sample.compact?' compaction-marker':''}`} style={{...position(time[0],entry.end),'--context-profile':entry.sample.compact?'none':`polygon(0 ${100-entry.from}%,100% ${100-entry.width}%,100% 100%,0 100%)`,'--token-color':entry.color} as React.CSSProperties} title={entry.title} onClick={()=>data.jumpContext(entry)}/>;})}</div></div>:null}
        {active.includes('turns')?<div className="reader-navigator-row">{choice('turns')}<div className="reader-navigator-track" aria-label="Conversation turns">{data.turns.map((turn,index)=>{const spans=turn.messages.flatMap(id=>{const span=at(id);return span?[span]:[];});if(!spans.length)return null;const start=Math.min(...spans.map(span=>span[0])),end=Math.max(...spans.map(span=>span[1]));return <button type="button" className="reader-navigator-turn" key={turn.messages[0]??index} style={position(start,end)} title={`Turn ${index+1} · ${turn.label}`} onClick={()=>{const id=turn.messages.find(id=>at(id));if(id)data.jumpMessage(id);}}><span>{index+1}</span></button>;})}</div></div>:null}
        <div className="reader-navigator-window-track" aria-hidden="true"><span className="reader-navigator-window" hidden/><i className="reader-navigator-boundary" hidden/><i className="reader-navigator-boundary" hidden/></div>
      </div>
      {hiddenChoices?<div className="reader-navigator-key">{hiddenChoices}</div>:null}
      {/* Shown over the transcript while the context bar is hovered. */}
      {active.includes('context')?<small id={contextNote} className="reader-navigator-context-note">Height = context window in use (largest observed context when unknown) · vertical line = compaction</small>:null}
      {!clock.timed?<small className="reader-navigator-note">Message order · timestamps unavailable</small>:null}
    </>:<><div className="reader-navigator-key is-bare">{hiddenChoices}</div><span className="reader-navigator-window" hidden/></>}
  </aside>{createPortal(<div role="group" aria-label="Timeline key"><TimelineLegend embedded data-view-field="timeline_visibility" messages={ribbonMessages} origins={[]} taxonomy={data.taxonomy} tools={tools} error={clock.marks.some(mark=>mark.error)} unknownTools={unknownTools} events={timelineEvents} visibility={visibility} onSelect={(section,values,action)=>setVisibility(previous=>updateTimelineVisibility(previous,section,values,action))}/></div>,keyHost)}</>;
}

declare global{interface Window{pharosReaderNavigator?:{mount:(panel:HTMLElement,data:ReaderNavigatorData)=>()=>void}}}
export function installReaderNavigator(){
  window.pharosReaderNavigator={mount(panel,data){
    const element=document.createElement('div'),keyHost=document.createElement('div');element.className='reader-navigator-host';keyHost.className='transcript-timeline-key';panel.querySelector('.turn-list')!.before(element);panel.querySelector('.transcript-key')!.append(keyHost);const root=createRoot(element);root.render(<ReaderNavigator panel={panel} data={data} keyHost={keyHost}/>);return()=>{root.unmount();element.remove();keyHost.remove();};
  }};
  window.dispatchEvent(new Event('pharos:reader-navigator-ready'));
}
