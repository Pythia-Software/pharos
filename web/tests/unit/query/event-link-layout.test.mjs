import assert from 'node:assert/strict';
import {test} from 'node:test';
import {arcLevels,arcsCross,arcY,layoutEventArcs} from '../../../src/event-link-layout';

const ev=(id,x,targetKey=null,targetX=null)=>({id,x,targetKey,targetX});

test('events sharing a target share one arc, with each marker on it; untargeted events stay on the axis',()=>{
 const {arcs,markers,axis}=layoutEventArcs([ev('a',120,'p1',20),ev('b',300,'p1',20),ev('c',200),ev('d',260,'p2',240)],{width:400,height:44});
 assert.equal(axis,22);
 const p1=arcs.find(a=>a.key==='p1');
 assert.deepEqual([p1.x0,p1.x1,p1.events],[20,300,['a','b']]);
 for(const m of markers.filter(m=>m.arc))assert.ok(Math.abs(m.y-arcY(arcs.find(a=>a.key===m.arc),axis,m.x))<1e-9,`${m.id} sits on its arc`);
 const c=markers.find(m=>m.id==='c');assert.equal(c.y,axis);assert.equal(c.arc,null);
 for(const m of markers.filter(m=>m.arc))assert.ok(Math.abs(m.y-axis)>=6,'found targets are vertically apart from the axis');
});

test('every marker and arc stays inside the bar',()=>{
 const events=Array.from({length:40},(_,i)=>ev('e'+i,10+i*7,'t'+(i%9),i%9*25));
 for(const height of [22,44,66]){
  const {arcs,markers}=layoutEventArcs(events,{width:300,height,marker:3});
  for(const m of markers)assert.ok(m.y-3>=0&&m.y+3<=height&&m.x>=0&&m.x<=300,`marker ${m.id} at ${m.x},${m.y} in ${height}px`);
  for(const a of arcs)for(const [,y] of a.path.slice(1).split('L').map(p=>p.split(' ').map(Number)))assert.ok(y>=0&&y<=height);
 }
});

test('an arc starting inside another is lower, so nested and overlapping arcs never cross',()=>{
 assert.deepEqual(arcLevels([{x0:0,x1:100},{x0:10,x1:40},{x0:50,x1:200},{x0:60,x1:70},{x0:300,x1:310}]),[3,1,2,1,1]);
 const events=[];let seed=7;const rand=()=>(seed=seed*16807%2147483647)/2147483647;
 for(let i=0;i<60;i++){const t=Math.floor(rand()*12),x0=t*40;events.push(ev('e'+i,x0+5+rand()*400,'t'+t,x0));}
 const {arcs,axis}=layoutEventArcs(events,{width:900,height:44});
 for(const a of arcs)for(const b of arcs)if(a!==b)assert.ok(!arcsCross(a,b,axis),`${a.key} crosses ${b.key}`);
 assert.ok(arcs.some(a=>a.side===-1),'crowded rows use both halves');
});

test('stacked events on one arc spread so each stays hoverable',()=>{
 const {markers}=layoutEventArcs([ev('a',200,'p',10),ev('b',200,'p',10),ev('c',200,'p',10)],{width:400,height:44,marker:3});
 const xs=markers.map(m=>m.x).sort((a,b)=>a-b);
 assert.ok(xs[1]-xs[0]>=5.9&&xs[2]-xs[1]>=5.9);
});
