import {updateSliceValues,type LegendAction,type SliceSelection} from './chart-selection';
import {eventGroup,type AgentEventLink} from './event-link-layout';
import {ribbonOriginParts,type RibbonNode,type RibbonObservation} from './label-session-ribbon-model';

export const timelineVisibilitySections=['modes','events','tools','origins'] as const;
export type TimelineVisibilitySection=typeof timelineVisibilitySections[number];
export type TimelineVisibility=Partial<Record<TimelineVisibilitySection,SliceSelection<string>>>;
export const timelineSelection=(visibility:TimelineVisibility,section:TimelineVisibilitySection):SliceSelection<string>=>visibility[section]??{values:[],excluded:false};
export function timelineValueVisible(visibility:TimelineVisibility,section:TimelineVisibilitySection,value:string){
 const selection=timelineSelection(visibility,section);
 return !selection.values.length||(selection.excluded?!selection.values.includes(value):selection.values.includes(value));
}
export function updateTimelineVisibility(visibility:TimelineVisibility,section:TimelineVisibilitySection,values:string[],action:LegendAction):TimelineVisibility{
 const selection=updateSliceValues(timelineSelection(visibility,section),values,action),next={...visibility};
 if(selection.values.length)next[section]=selection;else delete next[section];
 return next;
}
export function validTimelineVisibility(value:unknown):value is TimelineVisibility{
 if(!value||typeof value!=='object'||Array.isArray(value))return false;
 return Object.entries(value).every(([key,selection])=>timelineVisibilitySections.includes(key as TimelineVisibilitySection)
  &&selection&&typeof selection==='object'&&!Array.isArray(selection)&&typeof selection.excluded==='boolean'
  &&Array.isArray(selection.values)&&selection.values.length<=1000&&selection.values.every((item:unknown)=>typeof item==='string'));
}
export const timelineEventVisible=(visibility:TimelineVisibility,event:AgentEventLink)=>timelineValueVisible(visibility,'events',eventGroup(event.group).key);

/** Filter already attributed spans so hidden observations never change the clock.
 * Caller filters cut out the original byte runs, retaining their exact times.
 * The same tree feeds drawing, counts, keyboard inspection and pointer hits. */
export function visibleTimelineTree(tree:RibbonNode,visibility:TimelineVisibility,caller=false):RibbonNode{
 if(!timelineVisibilitySections.some(section=>timelineSelection(visibility,section).values.length))return tree;
 const originsActive=caller&&timelineSelection(visibility,'origins').values.length>0;
 const items=new Map<RibbonObservation,RibbonObservation[]>();
 function visible(item:RibbonObservation):RibbonObservation[]{
  const cached=items.get(item);if(cached)return cached;
  let result:RibbonObservation[]=[item];
  if(item.event&&!timelineEventVisible(visibility,item.event)
   ||item.mark&&!timelineValueVisible(visibility,'tools',item.mark.error?'error':item.mark.category)
   ||item.label&&!timelineValueVisible(visibility,'modes',item.label))result=[];
  else if(originsActive&&item.message)result=(item.authorship?[item]:ribbonOriginParts(item)).filter(part=>timelineValueVisible(visibility,'origins',part.authorship!));
  items.set(item,result);return result;
 }
 function visit(node:RibbonNode):RibbonNode{
  const shown=node.items.flatMap(visible);
  return {...node,items:originsActive?[...new Map(shown.map(item=>[item.id,item])).values()]:shown,children:node.children.map(visit).filter(child=>child.items.length>0)};
 }
 return visit(tree);
}
