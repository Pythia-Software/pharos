// A session's lane on the Timeline: the shared timeline rows (timeline-rows.tsx)
// on the window's clock, framed by the session's identity and branch lines.
// A mark's card pins on a click and links to the conversation, since the
// transcript is on another page.
import React,{useMemo} from 'react';
import {Icon} from './icons';
import {SessionIdentity,SessionBranches,sessionBranchX as branchX} from './session-identity';
import {ObservationCard,type EventLayer,type RibbonBrushHandlers,type RibbonSourceHref} from './label-session-ribbon';
import {agentEventClock,sessionRibbonTree,type ActivityMark,type RibbonObservation} from './label-session-ribbon-model';
import type {LabelMessage,Taxonomy} from './conversation-labels';
import type {TimeRange,TimelineSessionBranch} from './timeline-label-model';
import type {KindCounts} from './event-merge';
import type {AgentEventLink} from './event-link-layout';
import {timeScale,type TimeScale} from './timeline-idle-model';
import {timelineSectionVisible,timelineValueVisible,timelineWaitsVisible,visibleTimelineTree,visibleWaitMarks,type TimelineVisibility} from './timeline-visibility';
import {humanEventGroups,type HumanEvent,type HumanEventGroup} from './human-events-model';
import {TimelineRow,timelineEventLayer,type TimelineLane,type TimelineSurface} from './timeline-rows';

/** eventCounts: events per kind across the workspace, the population a merged mark's rarest kind comes from (event-merge.ts). */
type Props={visibility?:TimelineVisibility;scale?:TimeScale;human?:HumanEvent[];humanHref?:(event:HumanEvent)=>string|null;events?:AgentEventLink[];eventCounts?:KindCounts;eventHref?:EventLayer['href'];branch?:TimelineSessionBranch;openSession?:()=>void;current?:boolean;sourceHref?:RibbonSourceHref;collapsed?:boolean;hideEvents?:boolean;caller?:boolean;boundaryMessages?:LabelMessage[];boundaryMarks?:ActivityMark[];sessionID:string;depth:number;name:string;metadata:string;notes?:React.ReactNode[];messages:LabelMessage[];marks:ActivityMark[];inferred:{start:number;end:number}[];taxonomy:Taxonomy;range:TimeRange;detail:boolean;brushHandlers:RibbonBrushHandlers;preview:TimeRange|null;foldControl:React.ReactNode;select:(item:RibbonObservation,anchor:HTMLButtonElement)=>void;restore:(item:RibbonObservation,anchor:HTMLButtonElement)=>void};

/** A lane's Human events inside the window, as the Key leaves them. */
function laneHumanGroups(props:Pick<Props,'human'|'visibility'|'boundaryMessages'|'messages'|'range'>):HumanEventGroup[]{
 if(!props.human?.length)return [];
 const visibility=props.visibility??{},times=new Map((props.boundaryMessages??props.messages).flatMap(message=>{const at=message.at?Date.parse(message.at):NaN;return Number.isFinite(at)?[[message.id,at] as [string,number]]:[];}));
 return humanEventGroups(props.human,id=>{const at=times.get(id);return at===undefined||at<props.range[0]||at>props.range[1]?undefined:at;},dimension=>timelineSectionVisible(visibility,'human')&&timelineValueVisible(visibility,'human',dimension));
}

/** How the Timeline frames a lane's rows: identity, label and ribbon columns; a row that does not fold out is a button pinning everything on it. `collapsed` (the sidebar preview) leaves a picture. */
function laneSurface(props:Props):TimelineSurface{
 return {
  interaction:props.collapsed?'none':'inspect',
  viewKey:row=>`delegation-labels:${props.sessionID}:${row}`,
  classes:{node:root=>root?'lt-session-node lt-session-root':'lt-session-node',leaf:()=>'lt-session-leaf',human:'lt-human-events',dimension:'lt-session-row lt-human-dimension',humanTrack:'reader-human-track lt-human-track'},
  labels:!props.hideEvents,
  label:({root,level,count})=>({className:`lt-session-row-label${root?' is-root':''}`,style:{paddingLeft:root?8:8+Math.min(level-1,8)*9},'aria-label':root?`${count} events`:undefined}),
  rootLabel:(_,{count})=><strong className="lt-event-count">{count}</strong>,
  caret:expands=>expands?<Icon name="chevron-right" className="lt-session-caret"/>:<span className="lt-session-caret" aria-hidden="true">·</span>,
  lead:root=>root?<SessionIdentity name={props.name} metadata={props.metadata} notes={props.notes} depth={props.depth} branch={props.branch} folded={!!props.foldControl} current={props.current} openSession={props.openSession}/>:<span className="lt-session-gutter" aria-hidden="true"><SessionBranches depth={props.depth} branch={props.branch} root={false}/></span>,
  leaf:({node,level,empty,content,inspector})=>{
   if(props.collapsed)return <div className="lt-session-row lt-session-preview">{content}</div>;
   if(empty)return <div className={`lt-session-row lt-session-empty ${level===0?'lt-session-empty-root':''}`}>{content}</div>;
   return <div className="lt-session-leaf"><button type="button" data-ribbon-event={node.items[0]?.id} data-view-field="delegation_event" className="lt-session-row" ref={anchor=>{if(anchor)props.restore(node.items[0],anchor);}} style={{'--row-depth':level} as React.CSSProperties} onClick={event=>{if(props.sourceHref)inspector.pin(event.currentTarget,<ObservationCard items={node.items} sourceHref={props.sourceHref} eventHref={props.eventHref} close={inspector.close}/>,undefined,event.detail===0);else props.select(node.items[0],event.currentTarget);}} onFocus={event=>inspector.show(event.currentTarget,<ObservationCard items={node.items}/>)} onBlur={inspector.leave}>{content}</button></div>;
  },
 };
}

export function RecursiveSessionRibbon(props:Props){
 const waitsShown=timelineWaitsVisible(props.visibility??{});
 // Hidden machine waits leave each launch, before windowing (timeline-visibility.ts).
 const marks=useMemo(()=>visibleWaitMarks(props.marks,props.visibility??{}),[props.marks,waitsShown]);
 const boundaryMarks=useMemo(()=>props.boundaryMarks&&visibleWaitMarks(props.boundaryMarks,props.visibility??{}),[props.boundaryMarks,waitsShown]);
 const fullTree=useMemo(()=>sessionRibbonTree(props.messages,marks,props.inferred,props.taxonomy,props.range,props.detail,props.caller,props.boundaryMessages,boundaryMarks,props.events),[props.messages,marks,props.inferred,props.taxonomy,props.range,props.detail,props.caller,props.boundaryMessages,boundaryMarks,props.events]);
 const tree=useMemo(()=>visibleTimelineTree(fullTree,props.visibility??{},props.caller),[fullTree,props.visibility,props.caller]);
 const untimed=props.messages.filter(m=>!m.at||!Number.isFinite(Date.parse(m.at))).length;
 const scale=useMemo(()=>props.scale??timeScale(props.range),[props.scale,props.range[0],props.range[1]]);
 // The same clock the tree's event rows use: each event at its sentence's share of its message.
 const events=useMemo(()=>props.events?.length?timelineEventLayer({events:props.events,visibility:props.visibility,times:agentEventClock(props.messages,props.boundaryMarks??props.marks,props.boundaryMessages??props.messages),scale,range:props.range,href:props.eventHref}):undefined,
  [props.visibility,props.events,props.messages,props.boundaryMessages,props.boundaryMarks,props.marks,props.range[0],props.range[1],scale,props.eventHref]);
 const humanGroups=useMemo(()=>laneHumanGroups(props),[props.human,props.boundaryMessages,props.messages,props.range[0],props.range[1],props.visibility]);
 const lane:TimelineLane={surface:laneSurface(props),visibility:props.visibility,range:props.range,scale,events,eventCounts:props.eventCounts,human:humanGroups.length?{groups:humanGroups,href:props.humanHref}:undefined,caller:props.caller&&timelineSectionVisible(props.visibility??{},'origins'),sourceHref:props.sourceHref,brushHandlers:props.brushHandlers,preview:props.preview};
 return <div className="lt-session-tree">{props.foldControl?<div className="lt-session-fold-anchor"><div className="lt-session-fold" style={{left:branchX(props.depth)-8}}>{props.foldControl}</div></div>:null}<TimelineRow node={tree} lane={lane}/>{untimed?<small className="lt-session-untimed">{untimed} messages without timestamps cannot be placed on this clock.</small>:null}</div>;
}
