// Shared production message-label and conversation data.
import {type LabelMessage} from './conversation-labels';
export type TimeRange=[number,number];
type SessionTreeRow={id:string;outline:string;depth:number;parent_id?:string};
export type TimelineSessionBranch={parentDepth?:number;rails:number[];children:boolean};
export function timelineSessionBranches(rows:SessionTreeRow[]){
  const ids=new Map(rows.map(row=>[row.id,row])),outlines=new Map(rows.map(row=>[row.outline,row]));
  const parents=new Map<string,SessionTreeRow>(),siblings=new Map<string,SessionTreeRow[]>();
  for(const row of rows){
    const parent=ids.get(row.parent_id??'')??outlines.get(row.outline.split('.').slice(0,-1).join('.'));
    if(!parent||parent.id===row.id||parent.depth>=row.depth)continue;
    parents.set(row.id,parent);siblings.set(parent.id,[...(siblings.get(parent.id)??[]),row]);
  }
  const remaining=new Set([...siblings.values()].flatMap(children=>children.slice(0,-1).map(row=>row.id)));
  return new Map(rows.map(row=>{
    const rails:number[]=[];let child=row,parent=parents.get(row.id);
    while(parent){if(remaining.has(child.id))rails.push(parent.depth);child=parent;parent=parents.get(parent.id);}
    return [row.id,{parentDepth:parents.get(row.id)?.depth,rails,children:siblings.has(row.id)} satisfies TimelineSessionBranch];
  }));
}
export const eventTime=(m:LabelMessage):number|null=>{
  const at=m.at?Date.parse(m.at):NaN;return Number.isFinite(at)?at:null;
};
