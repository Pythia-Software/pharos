import React,{useEffect,useMemo,useRef} from 'react';
import {createRoot} from 'react-dom/client';
import {SessionRibbon} from './label-session-ribbon';
import {sessionRibbonTree,agentEventClock,activityCategory} from './label-session-ribbon-model';
import {useRibbonInspector} from './label-ribbon-inspector';
import {timeScale} from './timeline-idle-model';
import {mergeAgentEvents} from './event-merge';
import {TimelineKey} from './timeline-legend';
import {useViewState} from './view-state';
import {readerNavigatorClock,nearestReaderMessage,type ReaderNavigatorData} from './reader-navigator-model';
import {trackReaderViewport} from './reader-navigator-layout';
import './reader-navigator.css';

const choices=['timeline','context','turns'] as const;
function ReaderNavigator({panel,data}:{panel:HTMLElement;data:ReaderNavigatorData}){
  const [shown,setShown]=useViewState<string[]>('reader_navigator',[...choices]);
  const host=useRef<HTMLElement>(null),inspector=useRibbonInspector({interactive:false});
  const clock=useMemo(()=>readerNavigatorClock(data.messages,data.marks),[data]);
  const scale=useMemo(()=>timeScale(clock.range),[clock]);
  const ribbonMessages=useMemo(()=>[...new Map(clock.messages.filter(message=>['message','reasoning','thinking'].includes(message.kind)).map(message=>[message.id,message])).values()],[clock]);
  const tree=useMemo(()=>shown.includes('timeline')?sessionRibbonTree(ribbonMessages,clock.marks,[],data.taxonomy,clock.range,true,false,clock.messages,clock.marks,data.events):null,[data,clock,ribbonMessages,shown.includes('timeline')]);
  const contexts=useMemo(()=>{const nextByStream=new Map<string,number>();return data.contexts.map(entry=>({...entry,end:0})).reverse().map(entry=>{const start=clock.byID.get(entry.message.id)?.[0]??clock.range[0],end=nextByStream.get(entry.sample.stream)??clock.range[1];nextByStream.set(entry.sample.stream,start);return {...entry,end:Math.max(start,end)};}).reverse();},[data,clock]);
  const eventLayer=useMemo(()=>{const times=agentEventClock(clock.messages,clock.marks);return{events:mergeAgentEvents(data.events),position:(event:ReaderNavigatorData['events'][number])=>{const at=times.at(event);return at===null?null:scale.fraction(at);},targetPosition:(event:ReaderNavigatorData['events'][number])=>{const at=times.targetAt(event);return at===null?null:scale.fraction(at);}};},[data,clock,scale]);
  const at=(id:string)=>clock.byID.get(id);
  const position=(start:number,end:number)=>({left:`min(${scale.fraction(start)*100}%,calc(100% - 3px))`,width:`${Math.max(0,scale.fraction(end)-scale.fraction(start))*100}%`});
  const jump=(fraction:number)=>{const id=nearestReaderMessage(clock.byID,scale.time(Math.max(0,Math.min(1,fraction))));if(id)data.jumpMessage(id);};
  const active=choices.filter(choice=>shown.includes(choice));
  useEffect(()=>{if(!host.current)return;if(active.length)return trackReaderViewport(panel,host.current,data,clock.byID);const scroller=panel.closest<HTMLElement>('.workspace-reader-content')!,element=host.current,resize=()=>scroller.style.setProperty('--reader-navigator-height',`${element.offsetHeight}px`),observer=new ResizeObserver(resize);resize();observer.observe(element);return()=>{observer.disconnect();scroller.style.removeProperty('--reader-navigator-height');};},[panel,data,clock,active.join(',')]);
  return <aside ref={host} className="reader-navigator" aria-label="Session navigator" data-range={clock.range.join(',')}>
    <div className="reader-navigator-controls" role="group" aria-label="Show session navigators"><strong>Navigate</strong>{choices.map(choice=><label key={choice}><input type="checkbox" data-view-field="reader_navigator" checked={shown.includes(choice)} onChange={event=>{const checked=event.currentTarget.checked;setShown(previous=>choices.filter(item=>item===choice?checked:previous.includes(item)));}}/>{choice[0].toUpperCase()+choice.slice(1)}</label>)}<output className="reader-navigator-position" hidden={!active.length} aria-live="off"/></div>
    {active.length?<>
      <div className="reader-navigator-bars">
        {shown.includes('timeline')?<div className="reader-navigator-row"><span>Timeline</span><div className="reader-navigator-track reader-navigator-timeline" role="group" aria-label="Session timeline. Click to reveal a message." onClick={event=>{const rect=event.currentTarget.getBoundingClientRect();jump((event.clientX-rect.left)/rect.width);}}>
          <SessionRibbon node={tree!} range={clock.range} clock={scale} brushHandlers={{}} preview={null} inspector={inspector} disabled eventLayer={eventLayer}/>
          <button type="button" className="reader-navigator-keyboard" aria-label="Browse session timeline" title="Left / right: move through the session · Home / End: first / last message" onFocus={event=>{const band=host.current?.querySelector<HTMLElement>('.reader-navigator-window');event.currentTarget.dataset.position=band?.dataset.position||'0';}} onClick={event=>{event.stopPropagation();jump(Number(event.currentTarget.dataset.position??0));}} onKeyDown={event=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(event.key))return;event.preventDefault();const button=event.currentTarget,previous=Number(button.dataset.position??0),next=event.key==='Home'?0:event.key==='End'?1:Math.max(0,Math.min(1,previous+(event.key==='ArrowLeft'?-.02:.02)));button.dataset.position=String(next);jump(next);}}/>
        </div></div>:null}
        {shown.includes('context')?<div className="reader-navigator-row"><span>Context</span><div className="reader-navigator-track reader-navigator-context" aria-label="Context growth">{contexts.length?contexts.map((entry,index)=>{const time=at(entry.message.id);if(!time)return null;return <button key={JSON.stringify([entry.message.id,entry.sample.stream])} type="button" className={`token-marker${entry.sample.compact?' compaction-marker':''}`} style={{...position(time[0],entry.end),'--context-profile':entry.sample.compact?'none':`polygon(0 ${100-entry.from}%,100% ${100-entry.width}%,100% 100%,0 100%)`,'--token-color':entry.color} as React.CSSProperties} title={entry.title} onClick={()=>data.jumpContext(entry)}/>;}):<small>Context measurements unavailable</small>}</div></div>:null}
        {shown.includes('turns')?<div className="reader-navigator-row"><span>Turns</span><div className="reader-navigator-track" aria-label="Conversation turns">{data.turns.map((turn,index)=>{const spans=turn.messages.flatMap(id=>{const span=at(id);return span?[span]:[];});if(!spans.length)return null;const start=Math.min(...spans.map(span=>span[0])),end=Math.max(...spans.map(span=>span[1]));return <button type="button" className="reader-navigator-turn" key={turn.messages[0]??index} style={position(start,end)} title={`Turn ${index+1} · ${turn.label}`} onClick={()=>{const id=turn.messages.find(id=>at(id));if(id)data.jumpMessage(id);}}><span>{index+1}</span></button>;})}</div></div>:null}
        <div className="reader-navigator-window-track" aria-hidden="true"><span className="reader-navigator-window" hidden/><i className="reader-navigator-boundary" hidden/><i className="reader-navigator-boundary" hidden/></div>
      </div>
      {shown.includes('timeline')?<TimelineKey messages={ribbonMessages} taxonomy={data.taxonomy} marks={clock.marks} events={data.events}/>:null}
      {shown.includes('context')?<small className="reader-navigator-note">Height = context window in use (largest observed context when unknown) · vertical line = compaction</small>:null}
      {!clock.timed?<small className="reader-navigator-note">Message order · timestamps unavailable</small>:null}
    </>:<span className="reader-navigator-window" hidden/>}
  </aside>;
}

declare global{interface Window{pharosReaderNavigator?:{mount:(panel:HTMLElement,data:ReaderNavigatorData)=>()=>void}}}
export function installReaderNavigator(){
  window.pharosReaderNavigator={mount(panel,data){
    const categories:Record<string,string>={run:'command',write:'edit',delegate:'agent'};
    data={...data,marks:data.marks.map(mark=>({...mark,category:activityCategory({name:mark.name,category:categories[mark.category]??mark.category})}))};
    const element=document.createElement('div');element.className='reader-navigator-host';panel.querySelector('.turn-list')!.before(element);const root=createRoot(element);root.render(<ReaderNavigator panel={panel} data={data}/>);return()=>{root.unmount();element.remove();};
  }};
  window.dispatchEvent(new Event('pharos:reader-navigator-ready'));
}
