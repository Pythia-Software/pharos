import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readerGeometry,readerVisibleWindow,readerNavigatorClock,nearestReaderMessage} from '../../../src/reader-navigator-model';
import {reader,event,human,render} from '../helpers/transcript.mjs';
import {plain} from '../helpers/classic-script.mjs';
import {sessionRibbonTree} from '../../../src/label-session-ribbon-model';

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

test('navigator tool marks use the transcript failure policy',()=>{
  for(const [callFields,resultFields,error] of [
    [{},{exit_code:1},true], [{},{content:'Process exited with code 1'},true],
    [{},{content:'Exit code: 2'},true], [{is_error:true},{},true],
    [{},{is_error:true},true], [{},{exit_code:0},false],
  ]){
    const {events,graph}=render([human(),
      event('call','tool_call',{name:'Bash',input:{command:'check'},...callFields},1,{call_id:'c'}),
      event('result','tool_result',resultFields,2,{call_id:'c'}),
    ]);
    const [mark]=reader.readerNavigatorMarks({...graph,messages:events});
    assert.equal(mark.error,error,JSON.stringify([callFields,resultFields]));
    assert.equal(mark.end_known,true);
  }
});

test('untimed tools and delegations keep ordinal marks with unknown durations',()=>{
  const {events,graph}=render([human(),
    event('call','tool_call',{name:'Bash',input:{command:'check'}},1,{call_id:'c'}),
    event('result','tool_result',{exit_code:1},2,{call_id:'c'}),
    event('delegate','delegation',{name:'Task',input:{description:'Review'}},3,{call_id:'d'}),
    event('done','delegation_result',{content:'Reviewed'},4,{call_id:'d'}),
    event('reply','message','Finished',5),
  ].map((message,index)=>({...message,id:message.native_id,created_at:index%2?null:'invalid'})));
  const messages=plain(events).map(message=>({...message,at:message.created_at,labels:[]}));
  const marks=plain(reader.readerNavigatorMarks({...graph,messages:events}));
  assert.equal(marks.length,2);
  assert.ok(marks.every(mark=>mark.start===null&&mark.end===null&&!mark.end_known));
  const clock=readerNavigatorClock(messages,marks);
  assert.equal(clock.timed,false);
  assert.deepEqual(clock.marks.map(mark=>[mark.start,mark.end,mark.end_known,mark.error]),[[1,1,false,true],[3,3,false,false]]);
  for(const mark of clock.marks)assert.equal(clock.byID.get(mark.message_id)[0],mark.start);
  const ribbon=sessionRibbonTree(clock.messages,clock.marks,[],{},clock.range);
  assert.equal(ribbon.items.filter(item=>item.mark).length,2,'both calls reach the shared ribbon');
  const mixed=readerNavigatorClock(messages.map((message,index)=>index===0?{...message,at:'2026-10-01T00:00:00Z'}:message),marks);
  assert.equal(mixed.timed,true);
  assert.equal(mixed.marks.length,0,'missing times never become ordinal positions on a real clock');
});
