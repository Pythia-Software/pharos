// Idle gaps on the session timeline, and the compressed clock that skips them.
//
// A gap is a stretch where nothing runs in any row (no message, no model
// request span, no tool call in flight, in any session or subagent) for at
// least IDLE_GAP_MS, and where the caller speaks first after it (within
// RESUME_WINDOW_MS of the first activity, so a harness's automatic "No
// response requested." just before the person's message still counts): a
// person coming back, or a TL1 orchestrator resuming after a review. Agent
// work that is merely slow (a long tool call, a subagent thinking) is never a
// gap, because its activity is busy time.
//
// Harness waits (a background command, a Monitor watch, a scheduled wakeup:
// harness_waits.go) are machine time: busy for the idle rule, so a stretch the
// agent spent waiting on its own job is drawn, not skipped. A timeline may
// also skip them (waitGaps): stretches of at least IDLE_GAP_MS where nothing
// but a wait is in flight. Skipping waits is a choice, off by default, since
// a wait is often the answer to "why did this take so long".
//
// The clock is piecewise linear and strictly increasing: each skipped gap
// keeps a thin sliver (CUT_SHARE of the active time) where the timeline draws
// a bar, so time(fraction(t)) === t everywhere, including inside a gap.
export type Interval={start:number;end:number};
/** A skipped stretch: `idle` (nothing ran), `wait` (only harness waits ran), or `mixed` when both touch. */
export type GapKind='idle'|'wait'|'mixed';
export type IdleGap={start:number;end:number;kind?:GapKind};
export type TimeCut={start:number;end:number;fraction:number;kind?:GapKind};
export type TimeScale={range:[number,number];compressed:boolean;cuts:TimeCut[];
 /** Virtual (compressed) milliseconds across the range. */
 span:number;
 /** Real time → x in [0, 1] of the range. */
 fraction:(at:number)=>number;
 /** x in [0, 1] → real time. */
 time:(fraction:number)=>number};

export const IDLE_GAP_MS=15*60_000;
/** A caller message this soon after the first activity following quiet counts as what ended it. */
export const RESUME_WINDOW_MS=2*60_000;
export const CUT_SHARE=.012;

/** Gaps of at least `threshold` with nothing in flight anywhere, ended by a caller message. */
export function idleGaps({busy,callers,threshold=IDLE_GAP_MS}:{busy:Interval[];callers:number[];threshold?:number}):IdleGap[]{
 const calls=callers.filter(Number.isFinite).sort((a,b)=>a-b);
 const items=[...busy,...calls.map(at=>({start:at,end:at}))].filter(item=>Number.isFinite(item.start)).map(item=>({start:item.start,end:Number.isFinite(item.end)?Math.max(item.start,item.end):item.start})).sort((a,b)=>a.start-b.start);
 const gaps:IdleGap[]=[];let end=-Infinity,c=0;
 for(const item of items){
  if(Number.isFinite(end)&&item.start-end>=threshold){
   while(c<calls.length&&calls[c]<item.start)c++;
   if(c<calls.length&&calls[c]-item.start<=RESUME_WINDOW_MS)gaps.push({start:end,end:item.start});
  }
  end=Math.max(end,item.end);
 }
 return gaps;
}

type Knot={t:number;v:number};
/** The clock for one visible range; with no gaps it is the plain linear clock. */
export function timeScale(range:[number,number],gaps:IdleGap[]=[],share=CUT_SHARE):TimeScale{
 const [r0,r1]=range,length=Math.max(1,r1-r0);
 const clipped=gaps.map(g=>({start:Math.max(r0,g.start),end:Math.min(r1,g.end)})).filter(g=>g.end>g.start).sort((a,b)=>a.start-b.start);
 const active=length-clipped.reduce((sum,g)=>sum+g.end-g.start,0),keep=active*share;
 const cut=active>0?clipped.filter(g=>g.end-g.start>keep):[];
 if(!cut.length)return {range,compressed:false,cuts:[],span:length,fraction:at=>(at-r0)/length,time:f=>r0+f*length};
 const knots:Knot[]=[{t:r0,v:0}];let v=0,t=r0;
 for(const g of cut){v+=g.start-t;knots.push({t:g.start,v});v+=keep;knots.push({t:g.end,v});t=g.end;}
 v+=r1-t;knots.push({t:r1,v});
 const span=v;
 const along=(key:'t'|'v',other:'t'|'v',value:number)=>{
  if(value<=knots[0][key])return knots[0][other]-(knots[0][key]-value);
  for(let i=1;i<knots.length;i++)if(value<=knots[i][key]){const a=knots[i-1],b=knots[i],run=b[key]-a[key];return run?a[other]+(value-a[key])/run*(b[other]-a[other]):a[other];}
  const last=knots[knots.length-1];return last[other]+(value-last[key]);
 };
 return {range,compressed:true,span,
  cuts:cut.map(g=>({...gaps.find(full=>full.start<=g.start&&full.end>=g.end)??g,fraction:(along('t','v',g.start)+keep/2)/span})),
  fraction:at=>along('t','v',at)/span,time:f=>along('v','t',f*span)};
}

type WaitMark={start:number;end:number;end_known?:boolean;wait_kind?:string;wait_end?:number|null};
/** A harness wait's span, launch to its recorded end; null for other calls or an end not recorded. */
export function markWait(mark:WaitMark):Interval|null{
 return mark.wait_kind&&typeof mark.wait_end==='number'&&Number.isFinite(mark.wait_end)&&mark.wait_end>=mark.start?{start:mark.start,end:mark.wait_end}:null;
}
/** The harness waits in one row. */
export const laneWaits=(lane:{marks:WaitMark[]}):Interval[]=>lane.marks.flatMap(mark=>{const wait=markWait(mark);return wait?[wait]:[];});
/** Busy time for one row; a row whose marks or messages were truncated stays
 * busy to its session's end. Harness waits are busy unless `waits` is false
 * (waitGaps takes them separately). */
export function laneBusy(lane:{marks:WaitMark[];model_inferred?:Interval[];messages?:{created_at?:string|null}[];truncated?:boolean;messages_truncated?:boolean},session?:{start:number|null;end:number|null},{waits=true}:{waits?:boolean}={}):Interval[]{
 const busy:Interval[]=lane.marks.map(m=>({start:m.start,end:m.end_known===false?m.start:m.end}));
 if(waits)busy.push(...laneWaits(lane));
 for(const span of lane.model_inferred??[])busy.push(span);
 for(const m of lane.messages??[]){const at=m.created_at?Date.parse(m.created_at):NaN;if(Number.isFinite(at))busy.push({start:at,end:at});}
 if((lane.truncated||lane.messages_truncated)&&session?.end!=null){const last=busy.reduce((at,item)=>Math.max(at,item.end),session.start??-Infinity);busy.push({start:Number.isFinite(last)?last:session.end,end:session.end});}
 return busy;
}

const merged=(spans:Interval[])=>{
 const out:Interval[]=[];
 for(const span of spans.filter(item=>Number.isFinite(item.start)&&Number.isFinite(item.end)).map(item=>({start:item.start,end:Math.max(item.start,item.end)})).sort((a,b)=>a.start-b.start)){
  const last=out.at(-1);
  if(last&&span.start<=last.end)last.end=Math.max(last.end,span.end);else out.push({...span});
 }
 return out;
};
/** Stretches of at least `threshold` where a harness wait is in flight and
 * nothing else is: the waits' union, less every other kind of busy time
 * (busy computed with `waits: false`). */
export function waitGaps({busy,waits,threshold=IDLE_GAP_MS}:{busy:Interval[];waits:Interval[];threshold?:number}):IdleGap[]{
 const other=merged(busy),gaps:IdleGap[]=[];
 let j=0;
 for(const wait of merged(waits)){
  let from=wait.start;
  while(j<other.length&&other[j].end<from)j++;
  for(let k=j;k<other.length&&other[k].start<=wait.end;k++){
   if(other[k].start-from>=threshold)gaps.push({start:from,end:other[k].start,kind:'wait'});
   from=Math.max(from,other[k].end);
  }
  if(wait.end-from>=threshold)gaps.push({start:from,end:wait.end,kind:'wait'});
 }
 return gaps;
}
/** The stretches a timeline skips, from the idle and wait gaps it may skip;
 * stretches that touch become one cut. */
export function chosenGaps({idle,waits}:{idle:IdleGap[];waits:IdleGap[]},{skipIdle,skipWaits}:{skipIdle:boolean;skipWaits:boolean}):IdleGap[]{
 const chosen=[...(skipIdle?idle.map(g=>({...g,kind:g.kind??'idle'})):[]),...(skipWaits?waits.map(g=>({...g,kind:g.kind??'wait'})):[])].sort((a,b)=>a.start-b.start),out:IdleGap[]=[];
 for(const gap of chosen){
  const last=out.at(-1);
  if(last&&gap.start<=last.end){last.end=Math.max(last.end,gap.end);if(last.kind!==gap.kind)last.kind='mixed';}
  else out.push({...gap});
 }
 return out;
}

/** Virtual-time drags: pan and resize move by what the compressed overview shows. */
export const toVirtual=(scale:TimeScale,at:number)=>scale.fraction(at)*scale.span;
export const fromVirtual=(scale:TimeScale,at:number)=>scale.time(at/scale.span);

/** Axis labels around cuts: a cut's resume label sits left of its bar, so it
 * hides ticks under it and a cut too close after an earlier labelled one goes
 * unlabelled (its bar and tooltip remain). Widths are in CSS pixels. */
export function cutAxisLabels(ticks:number[],cuts:{fraction:number}[],width:number,label=86,tick=64){
 const labelled:number[]=[];let last=-Infinity;
 cuts.forEach((cut,i)=>{const x=cut.fraction*width;if(x-label>=last-4&&x>=label*.6){labelled.push(i);last=x;}});
 const hidden=new Set(ticks.flatMap((f,i)=>{const x=f*width;return labelled.some(c=>{const right=cuts[c].fraction*width;return x+tick/2>right-label&&x-tick/2<right+4;})?[i]:[];}));
 return {labelled:new Set(labelled),hiddenTicks:hidden};
}
