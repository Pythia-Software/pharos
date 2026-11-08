import React from 'react';
import {compareStates,functionStates,labelFamilies,shortLabel,stateColor,type LabelMessage,type Taxonomy} from './conversation-labels';
import {authorshipSegments,callerSources} from './timeline-caller-model';
import {labelIntervals,type ActivityMark} from './label-session-ribbon-model';
import type {TimeRange} from './timeline-label-model';
import {EventGlyph,EventLinkLegend} from './event-link-overlay';
import {ChartLegendChip} from './chart-legend-chip';
import {ChartTooltip} from './chart-tooltip';
import {ViewDisclosure} from './view-disclosure';
import {useViewEntry} from './view-state';
import {sliceGroupState,type LegendAction} from './chart-selection';
import {timelineSelection,timelineSectionVisible,timelineGroupCounts,type TimelineVisibility,type TimelineVisibilitySection,type TimelineVisibilityKey} from './timeline-visibility';
import {EVENT_GROUPS,eventGroup,type AgentEventLink} from './event-link-layout';

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

export function TimelineLegend({messages,origins,taxonomy,tools,error,events=[],visibility,onSelect,previewOnly=false,onOpenTimeline}:{previewOnly?:boolean;onOpenTimeline?:()=>void;messages:LabelMessage[];origins:string[];taxonomy:Taxonomy;tools:Record<string,string>;error:boolean;events?:AgentEventLink[];visibility:TimelineVisibility;onSelect:(section:TimelineVisibilityKey,values:string[],action:LegendAction)=>void}){
  const [fullExpanded]=useViewEntry('disclosures','timeline-key',true,!previewOnly);
  const expanded=!previewOnly&&fullExpanded;
  const previewHint='Open the full Timeline to change visibility.';
  const modes=timelineModeGroups(messages,taxonomy),uncategorized=messages.some(message=>!functionStates(message,'label').length);
  const sources=callerSources.filter(source=>origins.includes(source.id));
  function chip(section:TimelineVisibilitySection,id:string,label:string,swatch?:React.ReactNode,values=[id],category=false,description?:string){
    if(!expanded&&!swatch)return null;
    const selection=timelineSelection(visibility,section),state=sliceGroupState(selection,values);
    if(!timelineSectionVisible(visibility,section))Object.assign(state,{active:false,mixed:false,dimmed:true,soleSelection:false});
    return <ChartLegendChip key={id} readOnly={previewOnly} onActivate={onOpenTimeline} iconOnly={!expanded} description={previewOnly?[description,previewHint].filter(Boolean).join("\n"):description} item={{key:id,label,color:stateColor(id)}} swatch={swatch} {...state} {...timelineGroupCounts(visibility,section,values)} groupSize={category?values.length:undefined} selectionActive={hasHidden} onAction={action=>onSelect(section,values,action)}/>;
  }
  const modeKeys=(family:typeof modes[number])=>[...(family.direct?[family.id]:[]),...family.labels];
  function heading(id:TimelineVisibilitySection,title:string,values:string[],swatch:React.ReactNode=null,description?:string){
    const selection=timelineSelection(visibility,'sections'),counts=timelineGroupCounts(visibility,id,values),state=sliceGroupState(selection,[id]);
    Object.assign(state,{mixed:counts.visibleCount>0&&counts.hiddenCount>0,dimmed:counts.visibleCount===0,active:state.active&&counts.hiddenCount===0});
    return <ChartLegendChip item={{key:id,label:title,color:''}} readOnly={previewOnly} onActivate={onOpenTimeline} iconOnly={!expanded} description={previewOnly?[description,previewHint].filter(Boolean).join("\n"):description} swatch={swatch} {...state} {...counts} groupSize={values.length} selectionActive={hasHidden} onAction={action=>onSelect('sections',[id],action)}/>;
  }
  function section(id:TimelineVisibilitySection,title:string,values:string[],children:React.ReactNode){
    return <div className="lt-legend-section" role="group" aria-label={title}>
      {expanded?heading(id,title,values):null}
      <div className="lt-legend-items">{children}</div>
    </div>;
  }
  const eventKeys=[...new Set(events.map(event=>eventGroup(event.group).key))];
  const domains={modes:[...modes.flatMap(modeKeys),...(uncategorized?['?']:[])],events:eventKeys,tools:[...Object.keys(tools),...(error?['error']:[])],origins:sources.map(source=>source.id)};
  const hasHidden=Object.entries(domains).some(([section,values])=>timelineGroupCounts(visibility,section as TimelineVisibilitySection,values).hiddenCount>0);
  const content=<>
    {modes.length||uncategorized?section('modes','Message modes',[...modes.flatMap(modeKeys),...(uncategorized?['?']:[])],<div className="lt-mode-groups">{['P','C'].map(root=>{const families=modes.filter(family=>family.id===root||family.id.startsWith(root+'.'));return families.length?<div className="lt-ontology-role" role="group" aria-label={root==='P'?'Sender':'Receiver'} key={root}>
      {chip('modes',root,root==='P'?'Sender':'Receiver',null,families.flatMap(modeKeys),true)}
      <div className="lt-ontology-families">{families.map(family=><span className="lt-mode-group" role="group" aria-label={family.name} key={family.id}>
        {family.id!==root||family.direct?chip('modes',family.id,family.name,family.direct?<i className="lt-label-swatch" style={{background:stateColor(family.id)}}/>:null,modeKeys(family),family.labels.length>0):null}
        {family.labels.map(id=>chip('modes',id,shortLabel(id,taxonomy),<i className="lt-label-swatch" style={{background:stateColor(id)}}/>))}
      </span>)}</div></div>:null;})}{uncategorized?<span className="lt-ontology-role lt-mode-label" role="group" aria-label="Coverage">{expanded?<strong>Coverage</strong>:null}{chip('modes','?','Uncategorized',<i className="lt-label-swatch" style={{background:stateColor('?')}}/>)}</span>:null}</div>):null}
    {/* The rows' order: message modes, agent events, tool actions. */}
    {events.length?section('events','Agent events',eventKeys,<EventLinkLegend events={events} heading={null} renderHint={expanded?undefined:(icon,label)=>heading('events','Agent events',eventKeys,icon,label)} renderGroup={group=>chip('events',group.key,group.short,<EventGlyph group={group.name}/>)}/>):null}
    {Object.keys(tools).length||error?section('tools','Tool actions',[...Object.keys(tools),...(error?['error']:[])],<div className="lt-legend-line">{Object.entries(tools).map(([id,name])=>chip('tools',id,name,<i className="lt-tool-swatch" style={{color:`var(--activity-${id})`}}/>))}{error?chip('tools','error','Error',<i className="lt-tool-swatch" style={{color:'var(--activity-error)'}}/>):null}</div>):null}
    {sources.length?section('origins','Caller text',sources.map(source=>source.id),<div className="lt-legend-line">{sources.map(source=><span key={source.id} title={expanded?source.hint:undefined}>{chip('origins',source.id,source.id==='typed'?'Human typed':source.name,<i className={`lt-source-swatch is-${source.pattern}`}/>,[source.id],false,source.hint)}</span>)}</div>):null}
  </>;
  return <ChartTooltip><div className={`lt-timeline-legend${expanded?'':' is-compact'}${previewOnly?' is-readonly':''}`} data-view-field="timeline_visibility">
    {previewOnly?<button type="button" className="lt-key-open" aria-label="Edit key in full Timeline" data-chart-tooltip={previewHint} onClick={onOpenTimeline}>Key</button>:<ViewDisclosure viewKey="timeline-key" className="lt-key-disclosure" defaultOpen summary="Key">{expanded?content:null}</ViewDisclosure>}
    {!expanded?content:null}
  </div></ChartTooltip>;
}
