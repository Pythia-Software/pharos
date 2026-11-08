import React from 'react';
import {compareStates,functionStates,labelFamilies,shortLabel,stateColor,type LabelMessage,type Taxonomy} from './conversation-labels';
import {authorshipSegments,callerSources} from './timeline-caller-model';
import {labelIntervals,type ActivityMark} from './label-session-ribbon-model';
import type {TimeRange} from './timeline-label-model';
import {EventGlyph,EventLinkLegend} from './event-link-overlay';
import {ChartLegendChip} from './chart-legend-chip';
import {sliceGroupState,type LegendAction} from './chart-selection';
import {timelineSelection,type TimelineVisibility,type TimelineVisibilitySection} from './timeline-visibility';
import {EVENT_GROUPS,type AgentEventLink} from './event-link-layout';

/** Compact, read-only key for a single summary ribbon. Shares the timeline's
 * family palette, event shapes and tool swatches without its filter menus. */
export function TimelineKey({messages,taxonomy,marks,events}:{messages:LabelMessage[];taxonomy:Taxonomy;marks:ActivityMark[];events:AgentEventLink[]}){
  const families=timelineModeGroups(messages,taxonomy),tools=[...new Set(marks.map(mark=>mark.category))];
  return <div className="reader-navigator-key" aria-label="Timeline key">
    {families.map(family=><span key={family.id}><i style={{background:stateColor(family.id)}}/>{family.name}</span>)}
    {messages.some(message=>!functionStates(message,'label').length)?<span><i style={{background:stateColor('?')}}/>Uncategorized</span>:null}
    {EVENT_GROUPS.filter(group=>events.some(event=>event.group===group.name)).map(group=><span key={group.key}><EventGlyph group={group.name}/>{group.short}</span>)}
    {tools.map(id=><span key={id}><i className="lt-tool-swatch" style={{color:`var(--activity-${id})`}}/>{({read:'Read',write:'Write',run:'Run',git:'Git',network:'Network',delegate:'Delegation',wait:'Wait',poll:'Poll',other:'Other'} as Record<string,string>)[id]??id}</span>)}
    {marks.some(mark=>mark.error)?<span><i className="lt-tool-swatch" style={{color:'var(--activity-error)'}}/>Error</span>:null}
  </div>;
}

/** Use the same elapsed attribution and text composition as the drawn ribbons. */
export function timelineLegendWindow(streams:{id?:string;messages:LabelMessage[];marks:ActivityMark[];neighbors?:LabelMessage[];caller?:boolean}[],range:TimeRange){
  const messages=new Map<string,LabelMessage>(),origins=new Set<string>(),tools=new Set<string>(),populated=new Set<string>();let error=false;
  const overlaps=(start:number,end:number)=>start<=range[1]&&end>=range[0];
  for(const stream of streams){
    for(const {message,start,end} of labelIntervals(stream.messages,stream.marks,30*60*1000,stream.neighbors??stream.messages)){
      if(!overlaps(start,end))continue;
      if(stream.id)populated.add(stream.id);
      messages.set(JSON.stringify([message.session_id,message.id]),message);
      if(stream.caller)for(const part of authorshipSegments(message))if(overlaps(start+(end-start)*part.start,start+(end-start)*part.end))origins.add(part.category);
    }
    if(!stream.caller)for(const mark of stream.marks)if(overlaps(mark.start,mark.end_known?Math.max(mark.start,mark.end):mark.start)){if(stream.id)populated.add(stream.id);if(mark.error)error=true;else tools.add(mark.category);}
  }
  return {messages:[...messages.values()],origins:[...origins],tools:[...tools],error,populated:[...populated]};
}

/** Keep only present specific colors, ordered beside their parent mode and role. */
export function timelineModeGroups(messages:LabelMessage[],taxonomy:Taxonomy=[]){
  const labels=[...new Set(messages.flatMap(message=>functionStates(message,'label')))].sort(compareStates);
  return ['P','C'].flatMap(root=>{
    const known=labelFamilies.filter(family=>family.id.startsWith(root+'.')).map(family=>({id:family.id,name:family.name}));
    const extra=[...new Set(labels.filter(id=>id.startsWith(root+'.')).map(id=>id.split('.').slice(0,2).join('.')))].filter(id=>!known.some(family=>family.id===id)).sort(compareStates).map(id=>({id,name:shortLabel(id,taxonomy)}));
    return [...(labels.includes(root)?[{id:root,name:shortLabel(root,taxonomy)}]:[]),...known,...extra].map(family=>({...family,direct:labels.includes(family.id),labels:labels.filter(id=>id.startsWith(family.id+'.')&&family.id!==root)})).filter(family=>family.direct||family.labels.length);
  });
}

export function TimelineLegend({messages,origins,taxonomy,tools,error,events=[],visibility,onSelect}:{messages:LabelMessage[];origins:string[];taxonomy:Taxonomy;tools:Record<string,string>;error:boolean;events?:AgentEventLink[];visibility:TimelineVisibility;onSelect:(section:TimelineVisibilitySection,values:string[],action:LegendAction)=>void}){
  const modes=timelineModeGroups(messages,taxonomy),uncategorized=messages.some(message=>!functionStates(message,'label').length);
  const sources=callerSources.filter(source=>origins.includes(source.id));
  function chip(section:TimelineVisibilitySection,id:string,label:string,swatch?:React.ReactNode,values=[id],category=false){
    const selection=timelineSelection(visibility,section);
    return <ChartLegendChip key={id} item={{key:id,label,color:stateColor(id)}} swatch={swatch} {...sliceGroupState(selection,values)} groupSize={category?values.length:undefined} selectionActive={selection.values.length>0} onAction={action=>onSelect(section,values,action)}/>;
  }
  const modeKeys=(family:typeof modes[number])=>[...(family.direct?[family.id]:[]),...family.labels];
  return <div className="lt-timeline-legend" data-view-field="timeline_visibility">
    {modes.length||uncategorized?<div className="lt-legend-line lt-mode-key"><strong>Message modes</strong><div className="lt-mode-groups">{['P','C'].map(root=>{const families=modes.filter(family=>family.id===root||family.id.startsWith(root+'.'));return families.length?<div className="lt-ontology-role" role="group" aria-label={root==='P'?'Sender':'Receiver'} key={root}>
      {chip('modes',root,root==='P'?'Sender':'Receiver',null,families.flatMap(modeKeys),true)}
      <div className="lt-ontology-families">{families.map(family=><span className="lt-mode-group" role="group" aria-label={family.name} key={family.id}>
        {family.id!==root||family.direct?chip('modes',family.id,family.name,family.direct?<i className="lt-label-swatch" style={{background:stateColor(family.id)}}/>:null,modeKeys(family),family.labels.length>0):null}
        {family.labels.map(id=>chip('modes',id,shortLabel(id,taxonomy),<i className="lt-label-swatch" style={{background:stateColor(id)}}/>))}
      </span>)}</div></div>:null;})}{uncategorized?<span className="lt-ontology-role lt-mode-label" role="group" aria-label="Coverage"><strong>Coverage</strong>{chip('modes','?','Uncategorized',<i className="lt-label-swatch" style={{background:stateColor('?')}}/>)}</span>:null}</div></div>:null}
    {/* The rows' order: message modes, agent events, tool actions. */}
    <EventLinkLegend events={events} renderGroup={group=>chip('events',group.key,group.short,<EventGlyph group={group.name}/>)}/>
    {Object.keys(tools).length||error?<div className="lt-legend-line"><strong>Tool actions</strong>{Object.entries(tools).map(([id,name])=>chip('tools',id,name,<i className="lt-tool-swatch" style={{color:`var(--activity-${id})`}}/>))}{error?chip('tools','error','Error',<i className="lt-tool-swatch" style={{color:'var(--activity-error)'}}/>):null}</div>:null}
    {sources.length?<div className="lt-legend-line"><strong>Caller text</strong>{sources.map(source=><span key={source.id} title={source.hint}>{chip('origins',source.id,source.id==='typed'?'Human typed':source.name,<i className={`lt-source-swatch is-${source.pattern}`}/>)}</span>)}</div>:null}
  </div>;
}
