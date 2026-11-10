import type {ReadinessModel} from './data-readiness-model';

type ReadinessDial = {kind:'check'|'unknown'|'missing'} | {kind:'lag';fraction:number;path:string|null};

// The badge measures observed input lag, never job progress, publication age,
// or a severity bucket. Keep a full 12-hour dial full for longer delays.
export function readinessDial({kind,lagMs}:Pick<ReadinessModel,'kind'|'lagMs'>):ReadinessDial {
  if(kind==='current')return {kind:'check'};
  if(kind==='missing')return {kind:'missing'};
  if(kind!=='behind'||lagMs===null||!Number.isFinite(lagMs)||lagMs<0)return {kind:'unknown'};
  const fraction=Math.min(1,lagMs/(12*60*60_000));
  if(fraction===0||fraction===1)return {kind:'lag',fraction,path:null};
  const angle=fraction*2*Math.PI;
  const x=Number((16+5*Math.sin(angle)).toFixed(4));
  const y=Number((16-5*Math.cos(angle)).toFixed(4));
  return {kind:'lag',fraction,path:`M16 16L16 11A5 5 0 ${fraction>.5?1:0} 1 ${x} ${y}Z`};
}
