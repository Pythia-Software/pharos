// Agent events drawn inside a ribbon: the fingerprint bar and the session
// timeline's rows. DOM-free; the components only draw what this returns.
//
// Events come from /api/labels/conversations/<id> (agent_events) and the
// timeline's lanes, each with its target (agent_event_links.go): the earlier
// text it points back at, or null.
//
// Layout, in the ribbon's own pixels (x right, y down, axis = the row's
// centre line):
//
//   - Events that point back at the same target message share one arc. The
//     arc leaves the axis at the target (its left end, an anchor tick) and
//     sweeps up and to the right, easing into a plateau; it ends at its last
//     event. Each event's marker sits where its x meets the arc. Events with
//     no target stay on the axis as small hollow marks, so the found and
//     the not-found separate vertically.
//   - An arc that starts inside another arc's span is drawn lower (nearer the
//     axis): level = 1 + the highest level among arcs starting inside it.
//     With every arc the same eased shape scaled by its lift (rise length
//     proportional to lift), an inner arc stays under every outer arc it
//     starts beneath, so arcs never cross. Levels map to lifts between
//     minLift and maxLift; above two levels, alternate levels mirror below
//     the axis so neither half compresses too far.
//   - Markers avoid one another (ribbon-marks.ts): a marker with a target
//     slides along its arc; one without moves between the row's lanes.
//   - Parallelism: the fingerprint's hatched envelopes are centred on the
//     same axis and grow with overlapping branches. Arcs and markers draw
//     above them with no halo or outline: at the density of a zoomed-out
//     timeline, rings around every mark distort more than they separate. On
//     the timeline each session has its own row, so an event is drawn in the
//     row of the session that said it; a target in another row (a subagent
//     pointing at the caller's prompt) ends its arc at the target's x with an
//     off-row anchor, and hovering bands both ends across every row.
import {laneCandidates,laneMark,placeMarks,type MarkInput,type PlacedMark} from './ribbon-marks';
/** position: how far into its message (a byte share in [0, 1]) the quoted text starts. */
export type EventTarget={method:'quote'|'overlap'|'file';message_id?:string;role?:string;kind?:string;start?:number;end?:number;text?:string;strong?:boolean;shared?:string[];score?:number;files?:string[];at?:string|null;position?:number};
/** position: how far into its message (a byte share in [0, 1]) the event's sentence starts. */
export type AgentEventLink={id:string;message_id:string;kind:string;label:string;group:string;probability:number;source:string;sentence:string;target:EventTarget|null;position?:number};
export type EventShape='up'|'down'|'diamond'|'square';
// Event groups in the reader's fixed order (reader-model.js EVENT_GROUPS),
// each with a shape so group never rests on colour alone.
export const EVENT_GROUPS:{name:string;key:string;shape:EventShape;short:string}[]=[
 {name:'Friction',key:'friction',shape:'up',short:'Friction'},
 {name:'Reactions and learning',key:'learning',shape:'diamond',short:'Learning'},
 {name:'Worth a second look',key:'second-look',shape:'square',short:'Second look'},
 {name:'Choices you may not see',key:'choices',shape:'down',short:'Choices'},
];
export const eventGroup=(name:string)=>EVENT_GROUPS.find(g=>g.name===name)??EVENT_GROUPS[0];
export const eventColor=(name:string)=>`var(--event-${eventGroup(name).key})`;
export const linkedTarget=(e:Pick<AgentEventLink,'target'>)=>e.target?.message_id?e.target:null;
/** A quote of the person's words or a multi-word phrase is strong; word overlap and a one-word quote of agent text are weaker. */
export const weakTarget=(t:EventTarget)=>t.method==='overlap'||t.method==='quote'&&!t.strong;

export type LayoutEvent={id:string;x:number;targetKey:string|null;targetX:number|null;weak?:boolean};
export type ArcLayout={key:string;x0:number;x1:number;level:number;side:1|-1;lift:number;rise:number;path:string;events:string[];weak:boolean};
export type MarkerLayout=PlacedMark&{arc:string|null};
export type EventLayout={arcs:ArcLayout[];markers:MarkerLayout[];axis:number};
export type LayoutOptions={width:number;height:number;marker?:number;gap?:number;minLift?:number;maxLift?:number;sweep?:number;minSpan?:number};

const ease=(u:number)=>1-(1-u)*(1-u);
/** y of an arc at x: eased from the axis to its lift over rise pixels, then flat. */
export function arcY(arc:Pick<ArcLayout,'x0'|'lift'|'rise'|'side'>,axis:number,x:number){
 const u=Math.max(0,Math.min(1,(x-arc.x0)/arc.rise));return axis-arc.side*arc.lift*ease(u);
}
/** Nesting levels: an arc starting inside another's span is lower. */
export function arcLevels(spans:{x0:number;x1:number}[]):number[]{
 const order=spans.map((s,i)=>i).sort((a,b)=>spans[b].x0-spans[a].x0||b-a),level=new Array<number>(spans.length).fill(1);
 order.forEach((i,k)=>{let inner=0;for(const j of order.slice(0,k))if(spans[j].x0>=spans[i].x0&&spans[j].x0<=spans[i].x1)inner=Math.max(inner,level[j]);level[i]=inner+1;});
 return level;
}
const round=(n:number)=>Math.round(n*10)/10;
export function layoutEventArcs(events:LayoutEvent[],options:LayoutOptions):EventLayout{
 const {width,height}=options,axis=height/2,marker=options.marker??3.2,gap=options.gap??1;
 const maxLift=options.maxLift??Math.max(4,axis-marker-1.5),minLift=Math.min(maxLift,options.minLift??Math.max(marker*2+1,maxLift*.5));
 const sweep=options.sweep??Math.max(3,Math.min(7,width/90)),minSpan=options.minSpan??marker*3;
 const groups=new Map<string,{key:string;x0:number;events:LayoutEvent[]}>();
 for(const e of events){if(e.targetKey===null||e.targetX===null||!Number.isFinite(e.targetX))continue;const g=groups.get(e.targetKey)??{key:e.targetKey,x0:e.targetX,events:[]};g.events.push(e);groups.set(e.targetKey,g);}
 const list=[...groups.values()].sort((a,b)=>a.x0-b.x0||a.key.localeCompare(b.key));
 const spans=list.map(g=>({x0:g.x0,x1:Math.min(width,Math.max(g.x0+minSpan,...g.events.map(e=>e.x)))}));
 const levels=arcLevels(spans),depth=Math.max(0,...levels),split=depth>2;
 // Per side, levels rank 1..n from the axis out.
 const sideOf=(level:number):1|-1=>split&&level%2===0?-1:1,sideRank=(level:number)=>split?Math.ceil(level/2):level,sideDepth=split?Math.ceil(depth/2):depth;
 const liftOf=(level:number)=>sideDepth<=1?(minLift+maxLift)/2:minLift+(maxLift-minLift)*(sideRank(level)-1)/(sideDepth-1);
 const arcs:ArcLayout[]=list.map((g,i)=>{
  const lift=liftOf(levels[i]),side=sideOf(levels[i]),rise=lift*sweep,{x0,x1}=spans[i],arc={x0,lift,rise,side};
  const points:string[]=[];const steps=10,end=Math.min(x1,x0+rise);
  for(let k=0;k<=steps;k++){const x=x0+(end-x0)*k/steps;points.push(`${round(x)} ${round(arcY(arc,axis,x))}`);}
  if(x1>end)points.push(`${round(x1)} ${round(arcY(arc,axis,x1))}`);
  return {key:g.key,x0,x1,level:levels[i],side,lift,rise,path:'M'+points.join('L'),events:g.events.map(e=>e.id),weak:g.events.every(e=>e.weak)};
 });
 const byKey=new Map(arcs.map(a=>[a.key,a]));
 // Markers avoid one another with the ribbon's shared placement
 // (ribbon-marks.ts): a marker with a target slides along its arc, never off
 // it; one without stays in the row's lanes, the axis first.
 const unlinked=marker*.72,lanes=laneCandidates(height,unlinked,gap),pitch=2*marker+gap;
 const inputs:MarkInput[]=events.map(e=>{
  const arc=e.targetKey!==null?byKey.get(e.targetKey):undefined;
  if(!arc)return laneMark(e.id,e.x,unlinked,lanes);
  const lo=Math.min(width-marker,arc.x0+marker),hi=Math.max(lo,Math.min(width-marker,arc.x1)),xs=[0,1,-1,2,-2,3,-3].map(k=>Math.max(lo,Math.min(hi,e.x+k*pitch)));
  return {id:e.id,x:e.x,r:marker,candidates:[...new Set(xs)].map(x=>({x,y:arcY(arc,axis,x)}))};
 });
 const arcOf=new Map(events.map(e=>[e.id,e.targetKey!==null&&byKey.has(e.targetKey)?e.targetKey:null]));
 const markers:MarkerLayout[]=placeMarks(inputs,{gap}).map(m=>({...m,arc:arcOf.get(m.id)??null}));
 return {arcs,markers,axis};
}
/** Whether two arcs' drawn curves cross (sampled), for tests and QC. */
export function arcsCross(a:ArcLayout,b:ArcLayout,axis:number){
 if(a.side!==b.side)return false;const lo=Math.max(a.x0,b.x0)+.5,hi=Math.min(a.x1,b.x1);if(hi<=lo)return false;
 let sign=0;for(let x=lo;x<=hi;x+=Math.max(.25,(hi-lo)/200)){const d=arcY(a,axis,x)-arcY(b,axis,x);if(Math.abs(d)<1e-6)continue;const s=Math.sign(d);if(sign&&s!==sign)return true;sign=s;}
 return false;
}
/** A glyph path centred on (cx, cy) with radius r. */
export function glyphPath(shape:EventShape,cx:number,cy:number,r:number){
 const p=(pts:[number,number][])=>'M'+pts.map(([x,y])=>`${round(cx+x*r)} ${round(cy+y*r)}`).join('L')+'Z';
 if(shape==='up')return p([[0,-1.1],[1,.75],[-1,.75]]);
 if(shape==='down')return p([[0,1.1],[1,-.75],[-1,-.75]]);
 if(shape==='diamond')return p([[0,-1.15],[1,0],[0,1.15],[-1,0]]);
 return p([[-.8,-.8],[.8,-.8],[.8,.8],[-.8,.8]]);
}
/** Plain words for where an event points. */
export function targetDescription(t:EventTarget|null):string{
 if(!t)return 'No earlier text found for this event.';
 if(t.method==='file')return `Names ${t.files?.join(', ')}; no earlier text to point at.`;
 const where=t.role==='user'?'the prompt':t.kind==='reasoning'||t.kind==='thinking'?'earlier thinking':'an earlier reply';
 return `Points back at ${where}`;
}
export function targetEvidence(t:EventTarget):string{
 if(t.method==='quote')return t.strong?'Exact: the event quotes these words.':'Exact, but one quoted word in agent text: weaker.';
 if(t.method==='overlap')return `Rule: shares ${(t.shared??[]).slice(0,4).join(', ')}${t.score!=null?` (score ${t.score})`:''}.`;
 return '';
}
