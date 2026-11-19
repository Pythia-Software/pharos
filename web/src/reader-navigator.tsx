import React,{useCallback,useEffect,useId,useMemo,useRef,useState,useSyncExternalStore} from 'react';
import {createPortal} from 'react-dom';
import {createRoot} from 'react-dom/client';
import {sessionRibbonTree,agentEventClock} from './label-session-ribbon-model';
import type {RibbonReveal} from './label-session-ribbon';
import {useRibbonInspector} from './label-ribbon-inspector';
import type {IdleGap} from './timeline-idle-model';
import {TimelineLegend,timelineKeyContent,timelineLegendCounts,timelineLegendDomains,timelineLegendWindow,useTimelineVisibility} from './timeline-legend';
import {timelineSectionVisible,timelineValueVisible,timelineWaitsVisible,visibleTimelineTree,visibleWaitMarks} from './timeline-visibility';
import {TimelineRow,timelineEventLayer,type TimelineLane,type TimelineSurface} from './timeline-rows';
import {useViewState} from './view-state';
import {voiceClock,type ReaderVoice} from './reader-navigator-model';
import {availableReaderNavigators,readerNavigatorClock,readerNavigatorScale,nearestReaderMessage,contextProfileLayout,contextProfileHit,contextProfileOutline,type ContextShape,type ReaderContext,type ReaderNavigatorData} from './reader-navigator-model';
import {trackReaderViewport} from './reader-navigator-layout';
import {humanEventDimensions,humanEventGroups,humanEventLayout} from './human-events-model';
import './reader-navigator.css';

const chevron=<svg className="reader-section-chevron" viewBox="0 0 12 12" aria-hidden="true"><path d="m4 2 4 4-4 4"/></svg>;
/** How the reader frames the shared timeline rows (timeline-rows.tsx): a label
 * and a track, whose click reveals a message in the transcript below. The
 * session's own row is the navigator's top row, named `title` and holding
 * `top` (its keyboard control) over the ribbon; it folds out into the rows
 * under it, which start at the margin. */
function readerSurface(sessionID:string,title:string,top:React.ReactNode):TimelineSurface{
  const indent=(level:number)=>Math.min(Math.max(0,level-1),6)*10;
  return {
    interaction:'reveal',
    viewKey:row=>`reader-timeline:${sessionID}:${row}`,
    classes:{node:root=>root?'reader-navigator-timeline-disclosure':'reader-section-node',leaf:root=>root?'reader-navigator-row':'reader-section-leaf',human:'reader-human-events',dimension:'reader-section-leaf reader-human-dimension',humanTrack:'reader-section-track reader-human-track'},
    labels:true,
    label:({root,level,expands})=>root&&!expands?{className:'reader-navigator-label'}:{className:'reader-section-label',style:{paddingLeft:indent(level)}},
    rootLabel:(_node,{count,expands})=>expands?<><strong>{title}</strong><small>{count}</small></>:title,
    caret:expands=>expands?chevron:null,
    track:(ribbon,root)=>root?<div className="reader-navigator-track reader-navigator-timeline" role="group" aria-label={`${title}. Click to reveal a message.`}>{ribbon}{top}</div>:<span className="reader-section-track">{ribbon}</span>,
  };
}
/** The hover card of a turn on the Turns row: what the turn's header says. */
function TurnCard({card}:{card:NonNullable<ReaderNavigatorData['turns'][number]['card']>}){
  return <div className="event-link-cards"><div className="event-link-card reader-turn-card">
    <div className="event-link-card-head"><strong>{card.title}</strong>{card.when?<small>{card.when}</small>:null}</div>
    <blockquote>{card.prompt}</blockquote>
    <small>{[...card.stats,card.tokens].filter(Boolean).join(' · ')}</small>
  </div></div>;
}

/** The tape of a voice conversation: each line over its message on the session's axis, your lines above and the replies below. */
function ReaderVoiceRow({voice,at,position}:{voice:ReaderVoice;at:(id:string)=>[number,number]|undefined;position:(start:number,end:number)=>React.CSSProperties}){
  const state=useSyncExternalStore(voice.subscribe,voice.snapshot),plan=state.plan,spans=plan.spans.filter(span=>state.speech||span.line.recorded);
  const read=spans.length-voice.recorded,sounding=new Set(state.playing?state.active:[]);
  const status=!state.speech?'recordings only':state.pending?`reading ${state.measured}/${read}`:read?`${read} read aloud`:'';
  return <div className="reader-navigator-row is-voice">
    <span className="reader-navigator-label reader-voice-control"><button type="button" className="reader-voice-play" aria-pressed={state.playing} onClick={voice.toggle} title={state.speech?'Plays your recordings; the system voice reads the lines the export kept no recording of. Lines play one after another.':'Plays your recordings. Reading the other lines aloud is not available on this computer.'}>{state.playing?'❚❚ Pause':'▶ Play'}</button><small>{voiceClock(plan.total)}{status?' · '+status:''}</small></span>
    <div className="reader-navigator-track reader-navigator-voice" role="group" aria-label="Voice conversation. Choose a line to play from it.">
      {spans.map(span=>{const line=span.line,range=at(line.id);if(!range)return null;return <button key={line.id} type="button" className={`reader-voice-line is-${line.role}${line.recorded?' is-recorded':''}${line.duration?'':' is-estimated'}${sounding.has(line.id)?' is-sounding':''}${state.current===line.id?' is-current':''}`} style={position(range[0],range[1])} title={`${line.role==='user'?'You':'Reply'} · ${line.recorded?'recording':'read aloud'} · ${line.duration?'':'about '}${voiceClock(span.length)}`} aria-label={`Play from line ${span.index+1}`} onClick={()=>voice.playFrom(line.id)}/>;})}
    </div>
  </div>;
}
type DrawnContext={entry:ReaderContext;start:number;end:number};
/** Every context sample of the session in one drawing (contextProfileLayout). */
function ContextProfileDrawing({layout,width,cursor}:{layout:ReturnType<typeof contextProfileLayout>;width:number;cursor:ContextShape|null}){
  return <svg className="reader-navigator-context-profile" viewBox={`0 0 ${width} 100`} preserveAspectRatio="none" aria-hidden="true">
    {layout.layers.map((area,index)=><path key={index} d={area.d} fill={area.color}/>)}
    {cursor?<path className="reader-navigator-context-cursor" d={contextProfileOutline(cursor,width)} vectorEffect="non-scaling-stroke"/>:null}
  </svg>;
}
const ContextProfile=React.memo(ContextProfileDrawing);
/** The Turns row and, behind it, the context in use over the session. Context
 * samples are one drawing with one pointer and keyboard model rather than an
 * element each: a long session has thousands. Above every profile, the turn
 * underneath takes the pointer; turn edges and numbers stay on top. */
function ReaderTurnsRow({data,byID,scale,contexts,trackPx,contextNote}:{data:ReaderNavigatorData;byID:Map<string,[number,number]>;scale:ReturnType<typeof readerNavigatorScale>;contexts:(ReaderContext&{end:number})[]|null;trackPx:number;contextNote:string}){
  const inspector=useRibbonInspector(),hovered=useRef<string|null>(null),keyboardAt=useRef({index:0,focused:false}),keyboardControl=useRef<HTMLButtonElement>(null),selectionID=useId();
  const [cursor,setCursor]=useState(-1),[keyboardIndex,setKeyboardIndex]=useState<number|null>(null);
  const position=(start:number,end:number)=>({left:`min(${scale.fraction(start)*100}%,calc(100% - 3px))`,width:`${Math.max(0,scale.fraction(end)-scale.fraction(start))*100}%`});
  const drawn=useMemo<DrawnContext[]>(()=>(contexts??[]).flatMap(entry=>{const time=byID.get(entry.message.id);return time?[{entry,start:time[0],end:entry.end}]:[];}),[contexts,byID]);
  const layout=useMemo(()=>drawn.length?contextProfileLayout(drawn,time=>scale.fraction(time),trackPx):null,[drawn,scale,trackPx]);
  const card=(index:number)=><div className="event-link-cards"><div className="event-link-card reader-turn-card"><div className="event-link-card-head"><strong>{drawn[index].entry.title}</strong></div></div></div>;
  const hitAt=(track:Element,x:number,y:number)=>{if(!layout)return -1;const rect=track.querySelector('.reader-navigator-context-profile')!.getBoundingClientRect();if(!rect.width||!rect.height)return -1;return contextProfileHit(layout.shapes,(x-rect.left)/rect.width*trackPx,(rect.bottom-y)/rect.height*100,trackPx);};
  // A pseudo-element's pointer target is its button. Keep its painted edge,
  // as well as the number, above the context profiles underneath the turn.
  const turnForeground=(target:EventTarget|null,x:number)=>{
    if(!(target instanceof Element))return null;
    const turn=target.closest<HTMLElement>('.reader-navigator-turn');if(!turn)return null;
    if(target.closest('.reader-navigator-turn>span'))return turn;
    const left=turn.getBoundingClientRect().left,edge=parseFloat(getComputedStyle(turn,'::before').borderLeftWidth);
    return x>=left&&x<left+edge?turn:null;
  };
  const keyboard=(target:EventTarget|null)=>target instanceof Element&&target.classList.contains('reader-navigator-context-keyboard');
  const choose=(element:HTMLElement,index:number)=>{hovered.current=null;keyboardAt.current={index,focused:true};setKeyboardIndex(index);setCursor(index);const rect=element.getBoundingClientRect(),shape=layout!.shapes[index];inspector.show(element,card(index),rect.left+(shape.x0+shape.x1)/2/trackPx*rect.width);};
  const leave=()=>{hovered.current=null;if(keyboardAt.current.focused&&keyboardControl.current)choose(keyboardControl.current,keyboardAt.current.index);else{setCursor(-1);inspector.leave();}};
  const reveal=(index:number)=>{inspector.close();data.jumpContext(drawn[index].entry);};
  return <div className={`reader-navigator-row${layout?'':' is-turns'}`}>
    <span className="reader-navigator-label">{layout?'Turns · context':'Turns'}</span>
    <div className={`reader-navigator-track reader-navigator-turns${layout?' reader-navigator-context':''}`} role="group" aria-label={layout?'Conversation turns and context growth':'Conversation turns'} aria-describedby={layout?contextNote:undefined}
      onPointerMove={event=>{
        if(keyboard(event.target))return;
        const index=turnForeground(event.target,event.clientX)?-1:hitAt(event.currentTarget,event.clientX,event.clientY);
        const turn=index<0&&event.target instanceof Element?event.target.closest<HTMLElement>('.reader-navigator-turn'):null,key=index>=0?`context:${index}`:turn?`turn:${turn.dataset.turn}`:null;
        if(key===hovered.current)return;
        hovered.current=key;setCursor(index);
        const turnCard=turn?data.turns[Number(turn.dataset.turn)]?.card:undefined;
        if(index>=0)inspector.show(event.currentTarget,card(index),event.clientX);
        else if(turn&&turnCard)inspector.show(turn,<TurnCard card={turnCard}/>,event.clientX);
        else leave();
      }}
      onPointerLeave={leave}
      onClickCapture={event=>{if(keyboard(event.target)||turnForeground(event.target,event.clientX))return;const index=hitAt(event.currentTarget,event.clientX,event.clientY);if(index<0)return;event.preventDefault();event.stopPropagation();reveal(index);}}>
      {layout?<ContextProfile layout={layout} width={trackPx} cursor={cursor>=0?layout.shapes[cursor]??null:null}/>:null}
      {layout?<button ref={keyboardControl} type="button" className="reader-navigator-context-keyboard" aria-label="Browse context samples" aria-describedby={selectionID} title="Left / right: move through context samples · Home / End: first / last · Enter: reveal the sample's message"
        onFocus={event=>choose(event.currentTarget,Math.min(keyboardAt.current.index,drawn.length-1))}
        onBlur={()=>{keyboardAt.current.focused=false;setKeyboardIndex(null);if(!hovered.current){setCursor(-1);inspector.leave();}}}
        onKeyDown={event=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(event.key))return;event.preventDefault();const last=drawn.length-1,at=keyboardAt.current.index;choose(event.currentTarget,event.key==='Home'?0:event.key==='End'?last:Math.max(0,Math.min(last,at+(event.key==='ArrowLeft'?-1:1))));}}
        onClick={()=>reveal(Math.min(keyboardAt.current.index,drawn.length-1))}/>:null}
      {layout?<span id={selectionID} className="reader-navigator-context-selection" role="status" aria-live="polite" aria-atomic="true">{keyboardIndex===null?'':`${keyboardIndex+1} of ${drawn.length} · ${drawn[keyboardIndex].entry.title}`}</span>:null}
      {data.turns.map((turn,index)=>{const spans=turn.messages.flatMap(id=>{const span=byID.get(id);return span?[span]:[];});if(!spans.length)return null;const start=Math.min(...spans.map(span=>span[0])),end=Math.max(...spans.map(span=>span[1]));return <button type="button" className="reader-navigator-turn" key={turn.messages[0]??index} data-turn={index} style={position(start,end)} title={turn.card?undefined:`Turn ${index+1} · ${turn.label}`} aria-label={`Turn ${index+1} · ${turn.label}`} onClick={()=>{inspector.close();const id=turn.messages.find(id=>byID.has(id));if(id)data.jumpMessage(id);}}><span>{index+1}</span></button>;})}
    </div>
    {inspector.portal}
  </div>;
}
const MemoReaderTurnsRow=React.memo(ReaderTurnsRow);
function ReaderNavigator({panel,data,keyHost}:{panel:HTMLElement;data:ReaderNavigatorData;keyHost:HTMLElement}){
  const keyEdited=useCallback(()=>{panel.dispatchEvent(new Event('pharos:reader-key-edit'));},[panel]);
  const [visibility,selectKey]=useTimelineVisibility(true,keyEdited);
  const [skipIdle]=useViewState<boolean>('timeline_idle',true);
  const [skipWaits]=useViewState<boolean>('timeline_waits',false);
  const [singleSession,setSingleSession]=useState(()=>panel.parentElement?.dataset.readerSingleSession==='true');
  useEffect(()=>{const reader=panel.parentElement;if(!reader)return;const update=()=>setSingleSession(reader.dataset.readerSingleSession==='true');reader.addEventListener('pharos:reader-sessions',update);update();return()=>reader.removeEventListener('pharos:reader-sessions',update);},[panel]);
  type GapPanel=HTMLElement&{pharosReaderIdleGaps?:IdleGap[];pharosReaderWaitGaps?:IdleGap[]};
  const [workspaceGaps,setWorkspaceGaps]=useState<IdleGap[]|undefined>(()=>(panel as GapPanel).pharosReaderIdleGaps);
  const [waitGaps,setWaitGaps]=useState<IdleGap[]|undefined>(()=>(panel as GapPanel).pharosReaderWaitGaps);
  useEffect(()=>{const update=()=>{setWorkspaceGaps((panel as GapPanel).pharosReaderIdleGaps);setWaitGaps((panel as GapPanel).pharosReaderWaitGaps);};panel.addEventListener('pharos:reader-idle',update);update();return()=>panel.removeEventListener('pharos:reader-idle',update);},[panel]);
  const host=useRef<HTMLElement>(null);
  const clock=useMemo(()=>readerNavigatorClock(data.messages,data.marks),[data]);
  const baseScale=useMemo(()=>readerNavigatorScale(clock,data.callerIDs,skipIdle,workspaceGaps,skipWaits,waitGaps),[clock,data.callerIDs,skipIdle,workspaceGaps,skipWaits,waitGaps]);
  const [trackPx,setTrackPx]=useState(640);
  // The Timeline key's Human events chips choose the dimensions drawn here; the Highlights key's Engagement chips choose the underlines.
  const humanKeys=useMemo(()=>humanEventDimensions((data.humanEvents??[]).filter(event=>clock.byID.has(event.message_id))).map(({id,name,className})=>({id,name,className})),[data,clock]);
  const humanGroups=useMemo(()=>humanEventGroups(data.humanEvents??[],id=>clock.byID.get(id)?.[0],dimension=>timelineSectionVisible(visibility,'human')&&timelineValueVisible(visibility,'human',dimension)),[data,clock,visibility]);
  const humanLayout=useMemo(()=>humanEventLayout(baseScale,humanGroups,trackPx),[baseScale,humanGroups,trackPx]),scale=humanLayout.scale;
  const ribbonMessages=useMemo(()=>[...new Map(clock.messages.filter(message=>['message','reasoning','thinking'].includes(message.kind)).map(message=>[message.id,message])).values()],[clock]);
  const waitsShown=timelineWaitsVisible(visibility),marks=useMemo(()=>visibleWaitMarks(clock.marks,visibility),[clock,waitsShown]);
  const tree=useMemo(()=>sessionRibbonTree(ribbonMessages,marks,[],data.taxonomy,clock.range,true,false,clock.messages,marks,data.events),[data,clock,ribbonMessages,marks]);
  const visibleTree=useMemo(()=>tree?visibleTimelineTree(tree,visibility):null,[tree,visibility]);
  const contexts=useMemo(()=>{const nextByStream=new Map<string,number>();return data.contexts.map(entry=>({...entry,end:0})).reverse().map(entry=>{const start=clock.byID.get(entry.message.id)?.[0]??clock.range[0],end=nextByStream.get(entry.sample.stream)??clock.range[1];nextByStream.set(entry.sample.stream,start);return {...entry,end:Math.max(start,end)};}).reverse();},[data,clock]);
  const eventClock=useMemo(()=>agentEventClock(clock.messages,clock.marks),[clock]);
  const timelineEvents=useMemo(()=>data.events.filter(event=>eventClock.at(event)!==null),[data,eventClock]);
  const eventLayer=useMemo(()=>timelineEventLayer({events:timelineEvents,visibility,times:eventClock,scale,range:clock.range}),[timelineEvents,eventClock,scale,clock,visibility]);
  // The Key lists what the ribbons draw. Without timestamps no call has a known end, which says nothing about the calls.
  const keyContent=useMemo(()=>{const drawn=timelineLegendWindow([{messages:ribbonMessages,marks:clock.marks,neighbors:clock.messages}],clock.range);return timelineKeyContent({...drawn,unknownTools:clock.timed?drawn.unknownTools:[]},{taxonomy:data.taxonomy,events:timelineEvents,human:humanKeys});},[ribbonMessages,clock,data.taxonomy,timelineEvents,humanKeys]);
  const timelineVisible=!!visibleTree&&timelineLegendCounts(visibility,timelineLegendDomains(keyContent)).visible>0;
  const at=(id:string)=>clock.byID.get(id);
  const position=(start:number,end:number)=>({left:`min(${scale.fraction(start)*100}%,calc(100% - 3px))`,width:`${Math.max(0,scale.fraction(end)-scale.fraction(start))*100}%`});
  // A click goes to the message of the tool dot or event marker under it, and otherwise to the message nearest that time.
  const reveal=({message,fraction}:RibbonReveal)=>{const id=message??nearestReaderMessage(clock.byID,scale.time(Math.max(0,Math.min(1,fraction))));if(id)data.jumpMessage(id);};
  const available=availableReaderNavigators(data,clock.byID),hasContext=available.includes('context'),hasTurns=available.includes('turns'),hasVoice=!!data.voice,hasBars=timelineVisible||hasContext||hasTurns||hasVoice,contextNote=useId();
  const keyboard=<button type="button" className="reader-navigator-keyboard" aria-label="Browse session timeline" title="Left / right: move through the session · Home / End: first / last message" onFocus={event=>{const band=host.current?.querySelector<HTMLElement>('.reader-navigator-window');event.currentTarget.dataset.position=band?.dataset.position||'0';}} onClick={event=>{event.stopPropagation();reveal({fraction:Number(event.currentTarget.dataset.position??0)});}} onKeyDown={event=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(event.key))return;event.preventDefault();const button=event.currentTarget,previous=Number(button.dataset.position??0),next=event.key==='Home'?0:event.key==='End'?1:Math.max(0,Math.min(1,previous+(event.key==='ArrowLeft'?-.02:.02)));button.dataset.position=String(next);reveal({fraction:next});}}/>;
  // The reader zooms to this session's span, so a workspace timeline above it would draw the same thing: one row, named for the session when the workspace has several.
  const lane:TimelineLane={surface:readerSurface(data.sessionID,singleSession?'Timeline':'Session Timeline',keyboard),visibility,range:clock.range,scale,events:eventLayer,showUnknownTail:clock.timed,onReveal:reveal,
    human:humanGroups.length?{groups:humanGroups,layout:{slots:humanLayout.slots,trackPx},jump:event=>data.jumpHumanEvent?data.jumpHumanEvent(event):data.jumpMessage(event.message_id),revealAt:fraction=>reveal({fraction})}:undefined};
  useEffect(()=>{if(host.current&&hasBars)return trackReaderViewport(panel,host.current,data,clock.byID,scale);},[panel,data,clock,scale,hasBars]);
  useEffect(()=>{const track=host.current?.querySelector<HTMLElement>('.reader-navigator-window-track');if(!track||typeof ResizeObserver==='undefined')return;const measure=()=>{const width=Math.round(track.clientWidth/8)*8;if(width>0)setTrackPx(width);};const observer=new ResizeObserver(measure);observer.observe(track);measure();return()=>observer.disconnect();},[hasBars]);
  return <><aside ref={host} className="reader-navigator" aria-label="Session navigator" data-range={clock.range.join(',')}>
    {hasBars?<>
      <div className="reader-navigator-bars">
        {timelineVisible?<TimelineRow node={visibleTree!} lane={lane}/>:null}
        {hasContext||hasTurns?<MemoReaderTurnsRow data={data} byID={clock.byID} scale={scale} contexts={hasContext?contexts:null} trackPx={trackPx} contextNote={contextNote}/>:null}
        {data.voice?<ReaderVoiceRow voice={data.voice} at={at} position={position}/>:null}
        <div className="reader-navigator-window-track" aria-hidden="true"><span className="reader-navigator-window" hidden/><i className="reader-navigator-boundary" hidden/><i className="reader-navigator-boundary" hidden/></div>
      </div>
      {/* Shown over the transcript while the context bar is hovered. */}
      {hasContext?<small id={contextNote} className="reader-navigator-context-note">Height = context window in use (largest observed context when unknown) · context-colored line = compaction · blue line and number = turn</small>:null}
      {!clock.timed?<small className="reader-navigator-note">Message order · timestamps unavailable</small>:null}
    </>:<span className="reader-navigator-window" hidden/>}
  </aside>{createPortal(<div role="group" aria-label="Timeline key"><TimelineLegend surface="reader" data-view-field="timeline_visibility" content={keyContent} visibility={visibility} onSelect={selectKey}/></div>,keyHost)}</>;
}

declare global{interface Window{pharosReaderNavigator?:{mount:(panel:HTMLElement,data:ReaderNavigatorData)=>()=>void}}}
export function installReaderNavigator(){
  window.pharosReaderNavigator={mount(panel,data){
    const element=document.createElement('div'),keyHost=document.createElement('div');element.className='reader-navigator-host';keyHost.className='transcript-timeline-key';panel.querySelector('.turn-list')!.before(element);panel.querySelector('.transcript-content-key')!.before(keyHost);const root=createRoot(element);root.render(<ReaderNavigator panel={panel} data={data} keyHost={keyHost}/>);return()=>{root.unmount();element.remove();keyHost.remove();};
  }};
  window.dispatchEvent(new Event('pharos:reader-navigator-ready'));
}
