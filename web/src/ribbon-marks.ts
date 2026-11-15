// Placement and hit testing for the small marks drawn in a ribbon: tool-action
// dots on the session timeline and agent-event shapes on the timeline and the
// conversation fingerprint. DOM-free; everything is in the ribbon's own
// pixels (x right, y down).
//
// A mark is a dot of radius r, optionally with a tail to x1 (a tool call's
// duration), and a list of candidate centres in preference order: the lanes
// of a row, centre out, for tool dots and untargeted events; points along its
// arc for an event with a target. placeMarks walks the marks left to right
// and gives each the first candidate that touches nothing already placed.
// When every candidate touches something (a dense or zoomed-out stretch), it
// takes the candidate with the least overlap, the earliest on ties, and is
// flagged. Placement depends only on marks within reach, so an uncrowded
// stretch places the same way at every zoom.
export type MarkCandidate={x:number;y:number};
export type MarkInput={id:string;x:number;x1?:number;r:number;candidates:MarkCandidate[]};
export type PlacedMark={id:string;x:number;x1:number;y:number;r:number;overlap:boolean};
export type PlaceOptions={gap?:number;occupied?:PlacedMark[]};

/** How far apart two marks are, edge to edge (negative when they overlap), tails included. */
export function markGap(a:Pick<PlacedMark,'x'|'x1'|'y'|'r'>,b:Pick<PlacedMark,'x'|'x1'|'y'|'r'>){
 const dx=Math.max(0,a.x-b.x1,b.x-a.x1);return Math.hypot(dx,a.y-b.y)-a.r-b.r;
}
const CELL=24;
export function placeMarks(marks:MarkInput[],{gap=1,occupied=[]}:PlaceOptions={}):PlacedMark[]{
 const grid=new Map<number,PlacedMark[]>(),cells=(x0:number,x1:number)=>{const out:number[]=[];for(let c=Math.floor(x0/CELL);c<=Math.floor(x1/CELL);c++)out.push(c);return out;};
 let reach=0;
 const add=(m:PlacedMark)=>{reach=Math.max(reach,m.r);for(const c of cells(m.x,m.x1)){const list=grid.get(c);if(list)list.push(m);else grid.set(c,[m]);}};
 occupied.forEach(add);
 const cost=(m:Omit<PlacedMark,'overlap'>)=>{const seen=new Set<PlacedMark>();let total=0;for(const c of cells(m.x-m.r-reach-gap,m.x1+m.r+reach+gap))for(const other of grid.get(c)??[]){if(seen.has(other))continue;seen.add(other);const d=markGap(m,other);if(d<gap-1e-9)total+=gap-d;}return total;};
 const ordered=[...marks].sort((a,b)=>a.x-b.x||(a.id<b.id?-1:a.id>b.id?1:0)),placed:PlacedMark[]=[];
 for(const mark of ordered){
  const tail=Math.max(0,(mark.x1??mark.x)-mark.x),options=mark.candidates.length?mark.candidates:[{x:mark.x,y:0}];
  let best:PlacedMark|null=null,bestCost=Infinity;
  for(const c of options){const m={id:mark.id,x:c.x,x1:c.x+tail,y:c.y,r:mark.r},k=cost(m);if(k<bestCost-1e-9){best={...m,overlap:k>0};bestCost=k;if(k===0)break;}}
  placed.push(best!);add(best!);
 }
 return placed;
}
/** Lane centres for marks of radius r in a row of this height, centre first, then alternately above and below. An odd count, so a lone mark sits on the axis. */
export function laneCandidates(height:number,r:number,gap=1,edge=.5){
 const pitch=2*r+gap,axis=height/2,lanes=[axis];
 for(let k=1;axis-k*pitch>=r-edge&&axis+k*pitch<=height-r+edge;k++)lanes.push(axis-k*pitch,axis+k*pitch);
 return lanes;
}
/** The candidates for a mark that stays at x and may move between lanes. */
export const laneMark=(id:string,x:number,r:number,lanes:number[],x1=x):MarkInput=>({id,x,x1,r,candidates:lanes.map(y=>({x,y}))});

export type HitTarget={x:number;x1?:number;y:number;r:number};
/** The target nearest the pointer, if the pointer is within slack pixels of its edge. */
export function nearestMark<T extends HitTarget>(targets:T[],x:number,y:number,slack=4):T|null{
 let best:T|null=null,distance=Infinity;
 for(const t of targets){const d=markGap({x,x1:x,y,r:0},{x:t.x,x1:t.x1??t.x,y:t.y,r:t.r});if(d<=slack&&d<distance-1e-9){best=t;distance=d;}}
 return best;
}
/** Placed marks that overlap one another (for tests and QC). */
export const overlapping=(placed:PlacedMark[],gap=0)=>placed.flatMap((a,i)=>placed.slice(i+1).filter(b=>markGap(a,b)<gap-1e-9).map(b=>[a.id,b.id] as const));
