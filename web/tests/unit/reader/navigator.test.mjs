import {test} from 'node:test';
import assert from 'node:assert/strict';
import {voiceClock,availableReaderNavigators,contextProfileLayout,contextProfileBounds,contextProfileHit,readerGeometry,readerVisibleWindow,readerNavigatorClock,readerNavigatorScale,nearestReaderMessage} from '../../../src/reader-navigator-model';
import {reader,event,human,render} from '../helpers/transcript.mjs';
import {plain} from '../helpers/classic-script.mjs';
import {sessionRibbonTree,activityCategory} from '../../../src/label-session-ribbon-model';

test('navigator layers reflect drawable measurements and meaningful turn boundaries',()=>{
  const byID=new Map([['a',[0,1]],['b',[1,2]]]);
  const data={contexts:[],turns:[{messages:['a']}]};
  assert.deepEqual(availableReaderNavigators(data,byID),['timeline']);
  data.contexts.push({message:{id:'a'},sample:{size:null,compact:true}});
  assert.deepEqual(availableReaderNavigators(data,byID),['timeline'],'compaction alone is not a context measurement');
  data.contexts.push({message:{id:'missing'},sample:{size:100}});
  data.turns.push({messages:['missing']});
  assert.deepEqual(availableReaderNavigators(data,byID),['timeline'],'undrawable data does not offer a bar');
  data.turns.push({messages:['b']});
  assert.deepEqual(availableReaderNavigators(data,byID),['timeline','turns'],'multiple turns remain navigable without context');
  data.contexts.push({message:{id:'b'},sample:{size:0}});
  assert.deepEqual(availableReaderNavigators(data,byID),['timeline','context','turns'],'zero is a known context measurement');
  data.turns=[{messages:['a','b']}];
  assert.deepEqual(availableReaderNavigators(data,byID),['timeline','context'],'context remains available for a single turn');
});

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

test('navigator compresses idle gaps before a caller resumes while retaining activity',()=>{
  const base=Date.parse('2026-10-01T00:00:00Z'),at=minute=>new Date(base+minute*60000).toISOString();
  const message=(id,minute,role='assistant')=>({id,at:at(minute),kind:'message',role,labels:[],text:'',segment:'',chunks:0,text_source:'test',truncated:false});
  const messages=[message('start',0,'user'),message('reply',1),message('assignment',20,'user'),message('resume',40,'user'),message('end',41)];
  const clock=readerNavigatorClock(messages,[]);
  const scale=readerNavigatorScale(clock,['start','resume']);
  assert.equal(scale.cuts.length,1,'an embedded agent assignment is activity but not a caller');
  assert.deepEqual([scale.cuts[0].start,scale.cuts[0].end],[base+20*60000,base+40*60000]);
  const resume=base+40*60000;
  assert.ok(scale.fraction(resume)-scale.fraction(base+20*60000)<.03,'the idle gap takes little space');
  assert.equal(nearestReaderMessage(clock.byID,scale.time(scale.fraction(resume))),'resume');
  assert.equal(readerNavigatorScale(clock,['start','resume'],false).cuts.length,0,'the shared setting can restore linear time');
  const busyClock=readerNavigatorClock(messages,[{start:base+19*60000,end:base+39*60000,end_known:true,category:'delegate',count:1,error:false,name:'Subagent'}]);
  assert.equal(readerNavigatorScale(busyClock,['start','resume']).cuts.length,0,'a running subagent prevents a gap');
  assert.equal(readerNavigatorScale(clock,['start','resume'],true,[]).cuts.length,0,'workspace evidence also protects activity in other sessions');
  // Machine waits are skipped only when chosen, from the Timeline's published wait gaps.
  const waits=[{start:base+2*60000,end:base+19*60000,kind:'wait'}];
  assert.equal(readerNavigatorScale(clock,['start','resume'],false,undefined,false,waits).cuts.length,0,'waits stay drawn by default');
  assert.deepEqual(readerNavigatorScale(clock,['start','resume'],false,undefined,true,waits).cuts.map(cut=>[cut.start,cut.end,cut.kind]),[[base+2*60000,base+19*60000,'wait']]);
  assert.equal(readerNavigatorScale(clock,['start','resume'],true,undefined,true,waits).cuts.length,2,'idle gaps and waits both cut');
  const untimed=readerNavigatorClock([message('start',0),{...message('end',1),at:null}].map(m=>({...m,at:null})),[]);
  assert.equal(readerNavigatorScale(untimed,['start','end']).compressed,false);
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

test('reader marks carry a call\'s recorded machine wait, which counts as activity unless skipped',()=>{
  const ended=new Date(Date.UTC(2026,8,20,12,40,0)).toISOString();
  const {events,graph}=render([human(),
    event('call','tool_call',{name:'Bash',input:{command:'until done; do sleep 30; done',run_in_background:true}},1,{call_id:'c',harness_wait:{kind:'background_command',ended_at:ended,status:'completed',events:0}}),
    event('result','tool_result',{content:'Command running in background with ID: b1'},2,{call_id:'c'}),
    event('late','tool_call',{name:'Bash',input:{command:'sleep 9999',run_in_background:true}},3,{call_id:'d',harness_wait:{kind:'background_command',ended_at:null,status:'stopped',events:0}}),
    event('reply','message','Done',2405),
  ].map(message=>({...message,id:message.native_id})));
  const marks=plain(reader.readerNavigatorMarks({...graph,messages:events}));
  assert.deepEqual(marks.map(mark=>[mark.wait_kind,mark.wait_end,mark.wait_status]),[['background_command',Date.parse(ended),'completed'],['background_command',null,'stopped']]);
  const clock=readerNavigatorClock(plain(events).map(message=>({...message,at:message.created_at,labels:[]})),marks);
  assert.equal(readerNavigatorScale(clock,['human']).cuts.length,0,'the wait is activity');
  const [cut]=readerNavigatorScale(clock,['human'],true,undefined,true).cuts;
  assert.deepEqual([cut.start,cut.end,cut.kind],[Date.UTC(2026,8,20,12,0,3),Date.parse(ended),'wait'],'skipping waits cuts from the last other activity to its end');
});

test('reader tool categories match the timeline for tool calls',()=>{
  for(const [name,input,category] of [
    ['Read',{file_path:'/repo/a'},'read'],['Search',{query:'climate'},'read'],
    ['Write',{file_path:'/repo/a'},'write'],['Bash',{command:'go test'},'run'],
    ['GitStatus',{},'git'],['Browser',{url:'https://example.com'},'network'],
    ['wait_agent',{},'wait'],['write_stdin',{},'poll'],['Task',{},'delegate'],['Custom',{},'other'],
  ]){
    const {events,graph}=render([human(),event('call','tool_call',{name,input},1,{call_id:'c'})]);
    const call=events.find(message=>message.kind==='tool_call'),mark=reader.readerNavigatorMarks({...graph,messages:events})[0];
    assert.equal(reader.readerTimelineCategory(call),category,name);
    assert.equal(mark.category,category,name);
    assert.equal(activityCategory({name,category:({run:'command',write:'edit',delegate:'agent'})[category]??category}),category,name);
  }
});

test('voice lines play one after another, each for its real length once known',()=>{
  const said=(id,duration)=>({id,role:'user',recorded:true,ready:true,duration,estimate:0}),reply=(id,duration,estimate=1000)=>({id,role:'assistant',recorded:false,ready:!!duration,duration,estimate});
  const plan=plain(reader.voiceSchedule([said('u1',2000),reply('a1',800),said('u2',6000),reply('a2',null,4000)]));
  assert.deepEqual(plan.spans.map(span=>[span.line.id,span.start,span.length]),[['u1',0,2000],['a1',2000,800],['u2',2800,6000],['a2',8800,4000]]);
  assert.equal(plan.total,12800);
  assert.equal(voiceClock(14500),'0:15');assert.equal(voiceClock(125000),'2:05');
});

test('context profiles draw as a few areas and resolve the pointer to the topmost profile',()=>{
  const entry=(stream,from,width,extra={})=>({from,width,color:stream==='main'?'#43877e':'#8a6bbf',sample:{stream,...extra}});
  // Thousands of samples on an 800-unit track: one area per stream run, bounded by the track's resolution.
  const dense=Array.from({length:6000},(_,i)=>({entry:entry('main',i/60,(i+1)/60),start:i,end:i+1}));
  const many=contextProfileLayout(dense,time=>time/6000,800);
  assert.equal(many.layers.length,1,'one stream draws as one area');
  assert.ok(many.layers[0].d.split('L').length<=1700,'the area is sampled at half-pixel resolution, not per sample');
  assert.equal(many.shapes.length,6000,'every sample stays addressable');
  const fraction=time=>time/100;
  const layout=contextProfileLayout([
    {entry:entry('main',0,50),start:0,end:50},
    {entry:entry('main',50,90),start:50,end:100},
    {entry:entry('child',2,6),start:40,end:60},
    {entry:entry('main',100,100,{compact:true}),start:70,end:70},
  ],fraction,100);
  assert.deepEqual(layout.layers.map(area=>area.color),['#43877e','#8a6bbf','#43877e'],'streams and compactions keep their source stacking order');
  assert.ok(!layout.layers[0].d.includes(' 0L'),'the compaction does not raise the main profile to full height');
  assert.equal(contextProfileHit(layout.shapes,50,3,100),2,'inside the child profile, the later (upper) profile wins');
  assert.equal(contextProfileHit(layout.shapes,50,10,100),1,'above the small child profile the main profile takes the point');
  assert.equal(contextProfileHit(layout.shapes,25,10,100),0);
  assert.equal(contextProfileHit(layout.shapes,25,40,100),-1,'above every profile nothing is hit, so the turn underneath takes the pointer');
  assert.equal(contextProfileHit(layout.shapes,71,95,100),3,'a compaction spans the track');
  // A sample narrower than a pixel keeps a marker-sized target.
  const narrow=contextProfileLayout([{entry:entry('main',10,20),start:10,end:10.1}],fraction,100);
  assert.equal(contextProfileHit(narrow.shapes,12,5,100),0);
  assert.deepEqual(contextProfileBounds(narrow.shapes[0],100),[10,13],'a narrow sample keeps a visible marker-sized target');

  const interleaved=contextProfileLayout([
    {entry:entry('main',60,60),start:0,end:50},
    {entry:entry('child',40,40),start:20,end:100},
    {entry:entry('main',60,60),start:50,end:100},
    {entry:{...entry('main',100,100,{compact:true}),color:'#fff'},start:75,end:75},
    {entry:entry('main',30,30),start:75,end:100},
  ],fraction,100);
  assert.deepEqual(interleaved.layers.map(layer=>layer.color),['#43877e','#8a6bbf','#43877e','#fff','#43877e'],'returning streams and samples after compaction paint over earlier layers');
  assert.equal(contextProfileHit(interleaved.shapes,30,20,100),1,'the child paints above the earlier main sample');
  assert.equal(contextProfileHit(interleaved.shapes,60,20,100),2,'the later main sample paints above the child');
  assert.equal(contextProfileHit(interleaved.shapes,76,20,100),4,'a later profile can cover the bottom of a compaction');
  assert.equal(contextProfileHit(interleaved.shapes,76,90,100),3,'the uncovered compaction stays reachable');

  const ending=contextProfileLayout([
    {entry:entry('main',80,80),start:0,end:100},
    {entry:entry('main',100,20),start:100,end:100},
  ],fraction,100);
  assert.deepEqual(contextProfileBounds(ending.shapes[1],100),[97,100],'the final instant sample stays inside the drawing');
  assert.equal(contextProfileHit(ending.shapes,99,30,100),1,'the final sample wins within its minimum-width marker');
  assert.equal(contextProfileHit(ending.shapes,99,70,100),0,'above the falling final profile the earlier sample remains reachable');
  assert.equal(contextProfileHit(ending.shapes,101,10,100),-1,'hits outside the drawing are ignored');
  assert.equal(contextProfileHit(ending.shapes,99,-1,100),-1,'the bottom border is outside the drawing');
});
