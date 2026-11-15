import React,{useRef,useState} from 'react';
import {useViewState} from './view-state';

export const defaultTimelineColumns={execution:175,events:165,time:0};
export const collapsedTimelineEventsWidth=56;
export type TimelineColumn='execution'|'events';
const names={execution:'session',events:'events'};
export function timelineColumnWidth(column:TimelineColumn,value:number){
  return Math.round(Math.max(column==='execution'?120:collapsedTimelineEventsWidth,Math.min(900,Number.isFinite(value)?value:defaultTimelineColumns[column])));
}
export function normalizeTimelineColumns(value:unknown){
  const raw=value&&typeof value==='object'?value as Record<string,unknown>:{};
  // Older links may contain a fixed clock width. The clock now always fills
  // the remaining space; keep the legacy key portable as its automatic value.
  return {execution:timelineColumnWidth('execution',Number(raw.execution??175)),events:timelineColumnWidth('events',Number(raw.events??165)),time:0};
}
/** Collapsing Events is presentation only; keep the expanded widths in URL state. */
export function timelineColumnLayout(value:unknown,eventsCollapsed=false){
  const columns=normalizeTimelineColumns(value);
  return {...columns,events:eventsCollapsed?collapsedTimelineEventsWidth:columns.events};
}
export function useTimelineColumns(active:boolean,{eventsCollapsed=false}:{eventsCollapsed?:boolean}={}){
  const [saved,setSaved]=useViewState('timeline_columns',defaultTimelineColumns,active);
  const [draft,setDraft]=useState<ReturnType<typeof normalizeTimelineColumns>|null>(null);
  const drag=useRef<{pointer:number;left:number;width:number;column:TimelineColumn;initial:ReturnType<typeof normalizeTimelineColumns>;next:ReturnType<typeof normalizeTimelineColumns>}|null>(null);
  const columns=timelineColumnLayout(draft??saved,eventsCollapsed);
  const handle=(column:TimelineColumn)=>column==='events'&&eventsCollapsed?null:<button type="button" role="separator" aria-orientation="vertical" aria-label={`Resize ${names[column]} column`} aria-valuemin={column==='execution'?120:collapsedTimelineEventsWidth} aria-valuemax={900} aria-valuenow={columns[column]} aria-valuetext={`${columns[column]} pixels`} data-view-field="timeline_columns" className="lt-column-resizer" title="Drag to resize · arrow keys adjust · Home or double-click resets"
    onPointerDown={event=>{if(event.button!==0)return;event.stopPropagation();event.preventDefault();event.currentTarget.focus();const initial=normalizeTimelineColumns(saved);drag.current={pointer:event.pointerId,left:event.clientX,width:event.currentTarget.parentElement!.getBoundingClientRect().width,column,initial,next:initial};event.currentTarget.setPointerCapture(event.pointerId);}}
    onPointerMove={event=>{event.stopPropagation();const current=drag.current;if(!current||current.pointer!==event.pointerId)return;current.next={...current.initial,[column]:timelineColumnWidth(column,current.width+event.clientX-current.left)};setDraft(current.next);}}
    onPointerUp={event=>{event.stopPropagation();const current=drag.current;if(!current||current.pointer!==event.pointerId)return;setSaved({...current.initial,[column]:timelineColumnWidth(column,current.width+event.clientX-current.left)});setDraft(null);drag.current=null;if(event.currentTarget.hasPointerCapture(event.pointerId))event.currentTarget.releasePointerCapture(event.pointerId);}}
    onPointerCancel={()=>{drag.current=null;setDraft(null);}}
    onLostPointerCapture={()=>{drag.current=null;setDraft(null);}}
    onDoubleClick={()=>setSaved({...normalizeTimelineColumns(saved),[column]:defaultTimelineColumns[column]})}
    onKeyDown={event=>{event.stopPropagation();if(event.key==='Escape'){drag.current=null;setDraft(null);return;}if(!['ArrowLeft','ArrowRight','Home'].includes(event.key))return;event.preventDefault();const current=normalizeTimelineColumns(saved),width=current[column]||event.currentTarget.parentElement!.getBoundingClientRect().width;setSaved({...current,[column]:event.key==='Home'?defaultTimelineColumns[column]:timelineColumnWidth(column,width+(event.key==='ArrowLeft'?-1:1)*(event.shiftKey?60:20))});}}><i aria-hidden="true"/></button>;
  return {handle,style:{'--lt-execution':`${columns.execution}px`,'--lt-events':`${columns.events}px`,'--lt-time':'minmax(350px,1fr)','--lt-min-width':`${columns.execution+columns.events+350}px`} as React.CSSProperties};
}
