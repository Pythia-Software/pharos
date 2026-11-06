import test from 'node:test';
import assert from 'node:assert/strict';
import {laneCandidates,laneMark,markGap,nearestMark,overlapping,placeMarks} from '../../../src/ribbon-marks';
import {layoutEventArcs} from '../../../src/event-link-layout';
import {ribbonToolPlacement,sessionRibbonTree} from '../../../src/label-session-ribbon-model';

const lanes=laneCandidates(32,3,.5);
const dots=(list,scale=1)=>list.map(([id,t])=>laneMark(id,t*scale,3,lanes));
let seed=11;const rand=()=>(seed=seed*16807%2147483647)/2147483647;

test('lanes run centre out and stay inside the row',()=>{
 assert.deepEqual(lanes,[16,9.5,22.5,3,29]);
 assert.deepEqual(laneCandidates(18,3,.5),[9,2.5,15.5]);
 assert.deepEqual(laneCandidates(6,3,.5),[3]);
});

test('marks with room never overlap: stacks spread across lanes and lone marks keep the axis',()=>{
 const list=[];for(let c=0;c<30;c++)for(let k=0;k<=c%5;k++)list.push(['c'+c+'-'+k,20+c*32+k*.5]);
 const placed=placeMarks(dots(list),{gap:.5});
 assert.deepEqual(overlapping(placed,.5),[]);
 assert.ok(placed.every(m=>!m.overlap));
 for(const m of placed.filter(m=>m.id.endsWith('-0')&&m.id.match(/^c(\d+)/)[1]%5===0))assert.equal(m.y,16,`${m.id} alone stays on the axis`);
 for(const m of placed)assert.equal(m.x,list.find(([id])=>id===m.id)[1],'lanes move a dot up or down, never in time');
});

test('a tail (a call still running) keeps later dots out of its lane',()=>{
 const placed=placeMarks([laneMark('long',0,3,lanes,400),laneMark('later',200,3,lanes)],{gap:.5});
 assert.notEqual(placed.find(m=>m.id==='later').y,placed.find(m=>m.id==='long').y);
 assert.deepEqual(overlapping(placed,.5),[]);
});

test('without room the fallback is deterministic: least overlap, earliest lane on ties',()=>{
 const list=Array.from({length:12},(_,i)=>['m'+String(i).padStart(2,'0'),100]);
 const placed=placeMarks(dots(list),{gap:.5});
 const shuffled=placeMarks(dots([...list].sort(()=>rand()-.5)),{gap:.5});
 assert.deepEqual(shuffled,placed,'input order does not matter');
 assert.deepEqual(placed.slice(0,5).map(m=>m.y),lanes,'the first five take the free lanes');
 assert.ok(placed.slice(0,5).every(m=>!m.overlap)&&placed.slice(5).every(m=>m.overlap),'the rest are flagged');
 for(const m of placed)assert.ok(lanes.includes(m.y),'a fallback still takes one of its candidates');
 // Every lane is full; the overflow piles evenly rather than all on the axis.
 const counts=lanes.map(y=>placed.filter(m=>m.y===y).length);
 assert.ok(Math.max(...counts)-Math.min(...counts)<=1,`even piles ${counts}`);
});

test('placement is stable across zoom: stacks place the same at every scale and zooming in never adds overlap',()=>{
 const stacks=[];for(let c=0;c<12;c++)for(let k=0;k<1+c%4;k++)stacks.push(['s'+c+'-'+k,c*9]);
 const at=scale=>new Map(placeMarks(dots(stacks,scale),{gap:.5}).map(m=>[m.id,m.y]));
 for(const scale of [4,8,16])assert.deepEqual(at(scale),at(4),`${scale}x places each stack as 4x does`);
 const random=Array.from({length:300},(_,i)=>['r'+i,rand()*100]);
 let previous=Infinity;
 for(const scale of [1,2,4,8,16,32]){const n=placeMarks(dots(random,scale),{gap:.5}).filter(m=>m.overlap).length;assert.ok(n<=previous,`${scale}x: ${n} overlaps after ${previous}`);previous=n;}
 assert.equal(previous,0,'enough zoom separates everything');
});

test('events place first and tool dots take the lanes they leave free',()=>{
 const events=[{id:'a',x:120,targetKey:'p',targetX:40},{id:'b',x:300,targetKey:null,targetX:null},{id:'c',x:310,targetKey:'p',targetX:40}];
 const {markers}=layoutEventArcs(events,{width:600,height:32,marker:3.2});
 const marks=[{start:2.4,end:2.4,end_known:true,name:'Read',category:'read',count:1,error:false,tool_id:'t1'},{start:6,end:6.3,end_known:true,name:'Bash',category:'run',count:1,error:false,tool_id:'t2'},{start:6.1,end:6.1,end_known:true,name:'Grep',category:'read',count:1,error:false,tool_id:'t3'}];
 const items=sessionRibbonTree([],marks,[],[],[0,12]).items,tools=[...ribbonToolPlacement(items,at=>at*50,32,markers).values()];
 assert.deepEqual(overlapping([...markers,...tools],.5),[],'no tool dot touches an event marker');
 assert.ok(tools.every(m=>!m.overlap));
 assert.deepEqual(overlapping(markers,1),[]);
});

test('the shared hit test finds the nearest mark within a generous slack',()=>{
 const targets=[{id:'a',x:10,y:16,r:3},{id:'b',x:18,y:16,r:3},{id:'tail',x:40,x1:90,y:9.5,r:3}];
 assert.equal(nearestMark(targets,11,16).id,'a');
 assert.equal(nearestMark(targets,15,19).id,'b');
 assert.equal(nearestMark(targets,70,12).id,'tail','a duration is part of its dot');
 assert.equal(nearestMark(targets,10,22.5)?.id,'a','four pixels of slack past the edge');
 assert.equal(nearestMark(targets,10,24.5),null);
 assert.ok(markGap({x:0,x1:0,y:0,r:3},{x:6,x1:6,y:0,r:3})===0);
});
