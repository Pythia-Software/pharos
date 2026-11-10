import {updateSliceValues,sliceValueVisible,sliceSelectionActive,sliceGroupCounts,validHiddenSelection,type LegendAction,type SliceSelection} from './chart-selection';
import {eventGroup,type AgentEventLink} from './event-link-layout';
import {ribbonOriginParts,type RibbonNode,type RibbonObservation} from './label-session-ribbon-model';

export const timelineVisibilitySections=['modes','events','tools','origins'] as const;
export type TimelineVisibilitySection=typeof timelineVisibilitySections[number];
export type TimelineVisibilityKey=TimelineVisibilitySection|'sections';
export type TimelineVisibility=Partial<Record<TimelineVisibilityKey,SliceSelection<string>>>;
export const timelineSelection=(visibility:TimelineVisibility,section:TimelineVisibilityKey):SliceSelection<string>=>visibility[section]??{values:[],excluded:false};
export function timelineValueVisible(visibility:TimelineVisibility,section:TimelineVisibilityKey,value:string){
 return sliceValueVisible(timelineSelection(visibility,section),value);
}
export const timelineSectionVisible=(visibility:TimelineVisibility,section:TimelineVisibilitySection)=>timelineValueVisible(visibility,'sections',section);
export function timelineGroupCounts(visibility:TimelineVisibility,section:TimelineVisibilitySection,values:string[]){
 return timelineSectionVisible(visibility,section)?sliceGroupCounts(timelineSelection(visibility,section),values):{visibleCount:0,hiddenCount:new Set(values).size};
}
export function updateTimelineVisibility(visibility:TimelineVisibility,section:TimelineVisibilityKey,values:string[],action:LegendAction):TimelineVisibility{
 if(action==='clear')return {};
 if(action==='show'||action==='hide'||action==='only'){
  const next={...visibility};
  if(section==='sections'){
   if(action!=='hide')for(const value of values)delete next[value as TimelineVisibilitySection];
  }else{
   if(action==='hide'&&!timelineSectionVisible(visibility,section))return visibility;
   if(action==='only')next.sections={values:[section],excluded:false};
   else if(!timelineSectionVisible(visibility,section)){
    const shown=updateSliceValues(timelineSelection(visibility,'sections'),[section],'show');
    if(sliceSelectionActive(shown))next.sections=shown;else delete next.sections;
    next[section]={values:[],excluded:false,allHidden:true};
   }
  }
  const selection=updateSliceValues(timelineSelection(next,section),values,action);
  if(sliceSelectionActive(selection))next[section]=selection;else delete next[section];
  return next;
 }
 const next={...visibility};
 if(section==='sections'&&action==='toggle'){
  // A whole-section action includes all its values, including future values.
  const partial=values.some(value=>timelineSelection(next,value as TimelineVisibilitySection).values.length>0&&timelineSectionVisible(visibility,value as TimelineVisibilitySection));
  for(const value of values)delete next[value as TimelineVisibilitySection];
  if(action==='toggle'&&partial)return next;
 }else if(section!=='sections'&&!timelineSectionVisible(visibility,section)){
  // A label chosen from a hidden section makes that section visible again.
  const shown=updateSliceValues(timelineSelection(visibility,'sections'),[section],'toggle');
  if(shown.values.length)next.sections=shown;else delete next.sections;
 }
 const selection=updateSliceValues(timelineSelection(visibility,section),values,action);
 if(sliceSelectionActive(selection))next[section]=selection;else delete next[section];
 return next;
}
export function validTimelineVisibility(value:unknown):value is TimelineVisibility{
 if(!value||typeof value!=='object'||Array.isArray(value))return false;
 return Object.entries(value).every(([key,selection])=>(key==='sections'||timelineVisibilitySections.includes(key as TimelineVisibilitySection))
  &&selection&&typeof selection==='object'&&!Array.isArray(selection)&&typeof selection.excluded==='boolean'
  &&Array.isArray(selection.values)&&selection.values.length<=1000&&selection.values.every((item:unknown)=>typeof item==='string')&&validHiddenSelection(selection));
}
export const timelineEventVisible=(visibility:TimelineVisibility,event:AgentEventLink)=>timelineSectionVisible(visibility,'events')&&timelineValueVisible(visibility,'events',eventGroup(event.group).key);

/** Filter already attributed spans so hidden observations never change the clock.
 * Caller filters cut out the original byte runs, retaining their exact times.
 * The same tree feeds drawing, counts, keyboard inspection and pointer hits. */
export function visibleTimelineTree(tree:RibbonNode,visibility:TimelineVisibility,caller=false):RibbonNode{
 if(!sliceSelectionActive(timelineSelection(visibility,'sections'))&&!timelineVisibilitySections.some(section=>sliceSelectionActive(timelineSelection(visibility,section))))return tree;
 const originsActive=caller&&sliceSelectionActive(timelineSelection(visibility,'origins'));
 const items=new Map<RibbonObservation,RibbonObservation[]>();
 function visible(item:RibbonObservation):RibbonObservation[]{
  const cached=items.get(item);if(cached)return cached;
  let result:RibbonObservation[]=[item];
  const section:TimelineVisibilitySection=item.event?'events':item.mark||item.inferred?'tools':item.authorship?'origins':'modes';
  if(!timelineSectionVisible(visibility,section)||item.event&&!timelineEventVisible(visibility,item.event)
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
