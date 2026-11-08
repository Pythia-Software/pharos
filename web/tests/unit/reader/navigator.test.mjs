import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readerGeometry,readerVisibleWindow,readerNavigatorClock,nearestReaderMessage} from '../../../src/reader-navigator-model';

test('viewport bounds interpolate expanded rows and include collapsed groups',()=>{
  const geometry=readerGeometry([{top:100,bottom:200,start:0,end:10},{top:220,bottom:240,start:10,end:100},{top:260,bottom:360,start:100,end:120}]);
  assert.deepEqual(readerVisibleWindow(geometry,150,230),[5,55]);
  assert.deepEqual(readerVisibleWindow(geometry,200,260),[10,100]);
  assert.equal(readerVisibleWindow(geometry,0,99),null);
  assert.equal(readerVisibleWindow(geometry,370,400),null);
  // The same source range after a disclosure changes its rendered height.
  assert.deepEqual(readerVisibleWindow(readerGeometry([{top:100,bottom:300,start:0,end:10}]),150,200),[2.5,5]);
});

test('overlapping subagents and reordered times form a window covering every visible row',()=>{
  const geometry=readerGeometry([{top:0,bottom:500,start:100,end:200},{top:100,bottom:150,start:0,end:20},{top:180,bottom:240,start:50,end:60}]);
  assert.deepEqual(readerVisibleWindow(geometry,120,140),[8,128]);
  assert.deepEqual(readerVisibleWindow(geometry,190,210),[50+10/6,142]);
});

test('scroll lookup skips unrelated history and only examines visible cached spans',()=>{
  let reads=0;
  const geometry=readerGeometry(Array.from({length:20000},(_,i)=>({top:i*10,bottom:i*10+10,start:i,end:i+1})));
  geometry.spans=geometry.spans.map(span=>new Proxy(span,{get(target,key){reads++;return target[key];}}));
  assert.deepEqual(readerVisibleWindow(geometry,190002,190008),[19000.2,19000.8]);
  assert.ok(reads<30,`${reads} reads should be independent of transcript length`);
});

test('clock preserves missing timestamps and explicitly falls back to source order',()=>{
  const message=(id,at)=>({id,at,kind:'message',role:'assistant',labels:[],text:'',segment:'',chunks:0,text_source:'test',truncated:false});
  const mixed=readerNavigatorClock([message('a','2026-10-01T00:00:00Z'),message('missing',null),message('b','2026-10-01T00:00:10Z')],[]);
  assert.ok(mixed.timed);assert.equal(mixed.byID.has('missing'),false);
  const untimed=readerNavigatorClock([message('a',null),message('b',null)],[]);
  assert.equal(untimed.timed,false);assert.deepEqual(untimed.range,[0,1]);
  assert.equal(nearestReaderMessage(untimed.byID,1),'b');
  assert.equal(nearestReaderMessage(new Map([['a',[0,1]],['b',[4,5]]]),4.5),'b');
});
